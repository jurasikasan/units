package speech_test

// Integration tests for the session manager against the contract (C4):
// a fake OpenAI speech door over HTTP, a UDP tap for the RTP out, and an
// in-memory event publisher. Expected values come from C4, not from the
// code under test.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jurasikasan/units/abilities/speaking/internal/events"
	"github.com/jurasikasan/units/abilities/speaking/internal/speech"
	"github.com/jurasikasan/units/abilities/speaking/internal/tts"
)

const sampleRate = 48000

// recognizable is 20 frames (400 ms) of PCM where the i-th sample is i mod 1000.
func recognizable() []int16 {
	out := make([]int16, 960*20)
	for i := range out {
		out[i] = int16(i % 1000)
	}
	return out
}

// wav is a minimal RIFF/WAVE, 48 kHz mono 16-bit.
func wav(samples []int16) []byte {
	n := len(samples) * 2
	out := make([]byte, 44+n)
	copy(out[0:4], "RIFF")
	binary.LittleEndian.PutUint32(out[4:8], uint32(36+n))
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	binary.LittleEndian.PutUint32(out[16:20], 16)
	binary.LittleEndian.PutUint16(out[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(out[22:24], 1) // channels
	binary.LittleEndian.PutUint32(out[24:28], sampleRate)
	binary.LittleEndian.PutUint32(out[28:32], sampleRate*2)
	binary.LittleEndian.PutUint16(out[32:34], 2)
	binary.LittleEndian.PutUint16(out[34:36], 16)
	copy(out[36:40], "data")
	binary.LittleEndian.PutUint32(out[40:44], uint32(n))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(out[44+i*2:], uint16(s))
	}
	return out
}

// fakePub records the events in order, standing in for the Redis publisher.
type fakePub struct {
	mu     sync.Mutex
	events []events.Event
}

func (p *fakePub) Publish(_ context.Context, e events.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, e)
	return nil
}

func (p *fakePub) list() []events.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]events.Event, len(p.events))
	copy(out, p.events)
	return out
}

func (p *fakePub) hasClosed(reason string) bool {
	for _, e := range p.list() {
		if e.Type == events.SessionClosed && e.Data["reason"] == reason {
			return true
		}
	}
	return false
}

// fakeGateway is a double of the OpenAI speech door: /audio/speech returns
// recognizable audio after any programmed delay, and honours client
// cancellation of in-flight requests.
type fakeGateway struct {
	srv    *httptest.Server
	client *tts.Client

	mu           sync.Mutex
	voices       []string
	failContains string // a sentence containing this gets a 500
	slowContains string // a sentence containing this takes slowDur
	slowDur      time.Duration
	cancelled    []string
}

func newFakeGateway(t *testing.T, voices ...string) *fakeGateway {
	t.Helper()
	g := &fakeGateway{voices: voices, slowDur: 3 * time.Second}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /audio/speech", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.mu.Lock()
		fail := g.failContains != "" && strings.Contains(body.Text, g.failContains)
		slow := g.slowContains != "" && strings.Contains(body.Text, g.slowContains)
		g.mu.Unlock()
		if slow {
			select {
			case <-time.After(g.slowDur):
			case <-r.Context().Done():
				g.mu.Lock()
				g.cancelled = append(g.cancelled, body.Text)
				g.mu.Unlock()
				w.WriteHeader(499)
				return
			}
		}
		if fail {
			http.Error(w, "synth exploded", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(wav(recognizable()))
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	g.client = tts.New(g.srv.URL, "")
	return g
}

func (g *fakeGateway) wasCancelled() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.cancelled) > 0
}

// udpTap listens on a loopback UDP port and records the RTP it gets.
type rtpSeen struct {
	at      time.Time
	header  rtp.Header
	payload []byte
}

type udpTap struct {
	conn *net.UDPConn
	ch   chan rtpSeen
}

func startTap(t *testing.T) *udpTap {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	require.NoError(t, err)
	tap := &udpTap{conn: conn, ch: make(chan rtpSeen, 512)}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			p := &rtp.Packet{}
			if err := p.Unmarshal(buf[:n]); err != nil {
				continue
			}
			pl := make([]byte, len(p.Payload))
			copy(pl, p.Payload)
			select {
			case tap.ch <- rtpSeen{at: time.Now(), header: p.Header, payload: pl}:
			default:
			}
		}
	}()
	t.Cleanup(func() { _ = conn.Close() })
	return tap
}

func (tap *udpTap) addr() string { return tap.conn.LocalAddr().String() }

// take blocks until n packets are in or the timeout expires.
func (tap *udpTap) take(t *testing.T, n int, timeout time.Duration) []rtpSeen {
	t.Helper()
	out := make([]rtpSeen, 0, n)
	deadline := time.After(timeout)
	for len(out) < n {
		select {
		case p := <-tap.ch:
			out = append(out, p)
		case <-deadline:
			require.Failf(t, "timeout", "wanted %d rtp packets, got %d", n, len(out))
		}
	}
	return out
}

// quiesce drains packets until none arrive within d.
func (tap *udpTap) quiesce(t *testing.T, d time.Duration) []rtpSeen {
	t.Helper()
	var out []rtpSeen
	for {
		select {
		case p := <-tap.ch:
			out = append(out, p)
			continue
		case <-time.After(d):
			return out
		}
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.Truef(t, cond(), "timed out after %s waiting for %s", timeout, what)
}

func newSpeech(t *testing.T, cfg speech.Config, g *fakeGateway, pub *fakePub) *speech.Speech {
	t.Helper()
	if cfg.MaxSessions == 0 {
		cfg.MaxSessions = 4
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 2 * time.Minute
	}
	return speech.New(cfg, g.client, pub)
}

// spokenAtWordBoundary asserts the C4 §3 property of "spoken": a prefix of the
// text, cut at a word boundary.
func spokenAtWordBoundary(t *testing.T, spoken, text string) {
	t.Helper()
	require.NotEmpty(t, spoken, "something was spoken")
	require.True(t, strings.HasPrefix(text, spoken), "spoken %q is a prefix of %q", spoken, text)
	if len(spoken) < len(text) {
		require.Equal(t, byte(' '), text[len(spoken)], "spoken %q stops at a word boundary of %q", spoken, text)
	}
}

func TestOpenSayClose_RTPAndEvents(t *testing.T) {
	g := newFakeGateway(t, "piper_denis")
	pub := &fakePub{}
	sp := newSpeech(t, speech.Config{}, g, pub)
	tap := startTap(t)
	ctx := context.Background()

	id, err := sp.Open(ctx, tap.addr(), "piper_denis", "", "")
	require.NoError(t, err)
	assert.Regexp(t, `^spk-[0-9a-f]{8}$`, id, "session id shape (C4 §2.1)")

	uid, err := sp.Speak(id, "Привет!")
	require.NoError(t, err)
	assert.Regexp(t, `^utt-[0-9a-f]{8}$`, uid, "utterance id shape (C4 §2.2)")

	// 20 ms of 48 kHz audio per packet: 960 samples = 1920 bytes (C4 §4).
	pkts := tap.take(t, 20, 15*time.Second)
	ssrc := pkts[0].header.SSRC
	steps960 := 0
	for i, p := range pkts {
		assert.Equal(t, 1920, len(p.payload), "packet %d: 960 samples of 16 bits", i)
		assert.Equal(t, uint8(96), p.header.PayloadType, "packet %d: payload type", i)
		assert.Equal(t, ssrc, p.header.SSRC, "packet %d: one SSRC for the whole session", i)
		if i == 0 {
			assert.Equal(t, uint32(0), p.header.Timestamp, "first packet starts the clock")
			want := make([]byte, 1920)
			for s := 0; s < 960; s++ {
				binary.BigEndian.PutUint16(want[s*2:], uint16(int16(s%1000)))
			}
			assert.Equal(t, want, p.payload, "first frame is the first 960 samples, big-endian")
			continue
		}
		assert.Equal(t, uint16(pkts[i-1].header.SequenceNumber+1), p.header.SequenceNumber,
			"packet %d: sequence advances by 1", i)
		delta := int(p.header.Timestamp) - int(pkts[i-1].header.Timestamp)
		assert.Contains(t, []int{960, 1920}, delta, "packet %d: ts step is a whole number of 20 ms frames", i)
		if delta == 960 {
			steps960++
		}
		gap := p.at.Sub(pkts[i-1].at)
		assert.GreaterOrEqual(t, gap, 10*time.Millisecond, "packet %d: not faster than 20 ms apart", i)
		assert.LessOrEqual(t, gap, 120*time.Millisecond, "packet %d: at the pace of speech, ~20 ms", i)
	}
	assert.GreaterOrEqual(t, steps960, 15, "most of the 19 steps are exactly +960 (20 ms), got %d", steps960)

	waitFor(t, 5*time.Second, "the spoken event", func() bool {
		l := pub.list()
		return len(l) >= 1 && l[0].Type == events.SpeakComplete
	})
	l := pub.list()
	require.NotEmpty(t, l)
	assert.Equal(t, events.SpeakComplete, l[0].Type)
	assert.Equal(t, id, l[0].Subject, "ce_subject is the session id")
	assert.Equal(t, id, l[0].Data["session_id"])
	assert.Equal(t, uid, l[0].Data["request_id"])
	assert.Equal(t, "Привет!", l[0].Data["spoken"], "the whole text was sent")
	assert.Equal(t, "normal", l[0].Data["completion_cause"])

	require.NoError(t, sp.Close(id))
	waitFor(t, 5*time.Second, "the session.closed event", func() bool {
		l := pub.list()
		return len(l) >= 2 && l[1].Type == events.SessionClosed
	})
	l = pub.list()
	require.GreaterOrEqual(t, len(l), 2)
	assert.Equal(t, events.SessionClosed, l[1].Type, "closed follows spoken")
	assert.Equal(t, id, l[1].Data["session_id"])
	assert.Equal(t, "closed", l[1].Data["reason"])
	assert.Len(t, l, 2, "no second closed event")

	// Closing a closed session is ok:true with no second event (C4 §2.3).
	require.NoError(t, sp.Close(id))
	assert.Len(t, pub.list(), 2)
	// The session is gone: say on it is unknown_session.
	_, err = sp.Speak(id, "ещё")
	assert.ErrorContains(t, err, "unknown_session")
}

func TestTwoSays_SpokenInSayOrder(t *testing.T) {
	g := newFakeGateway(t, "piper_alena")
	pub := &fakePub{}
	sp := newSpeech(t, speech.Config{}, g, pub)
	tap := startTap(t)
	ctx := context.Background()

	id, err := sp.Open(ctx, tap.addr(), "piper_alena", "", "")
	require.NoError(t, err)
	u1, err := sp.Speak(id, "Первая.")
	require.NoError(t, err)
	u2, err := sp.Speak(id, "Вторая.")
	require.NoError(t, err)

	waitFor(t, 20*time.Second, "two spoken events", func() bool {
		n := 0
		for _, e := range pub.list() {
			if e.Type == events.SpeakComplete {
				n++
			}
		}
		return n == 2
	})
	l := pub.list()
	require.GreaterOrEqual(t, len(l), 2)
	assert.Equal(t, u1, l[0].Data["request_id"], "spoken in the order they were said")
	assert.Equal(t, u2, l[1].Data["request_id"])
	assert.Equal(t, "normal", l[0].Data["completion_cause"])
	assert.Equal(t, "normal", l[1].Data["completion_cause"])
	assert.Equal(t, "Первая.", l[0].Data["spoken"])
	assert.Equal(t, "Вторая.", l[1].Data["spoken"])

	require.NoError(t, sp.Close(id))
}

func TestGap_TimestampKeepsAdvancing(t *testing.T) {
	g := newFakeGateway(t, "piper_denis")
	g.slowContains = "second"
	g.slowDur = time.Second // the gap: synthesis takes ~1 s
	pub := &fakePub{}
	sp := newSpeech(t, speech.Config{}, g, pub)
	tap := startTap(t)
	ctx := context.Background()

	id, err := sp.Open(ctx, tap.addr(), "piper_denis", "", "")
	require.NoError(t, err)
	_, err = sp.Speak(id, "one")
	require.NoError(t, err)
	waitFor(t, 15*time.Second, "the first spoken", func() bool {
		l := pub.list()
		return len(l) >= 1 && l[0].Type == events.SpeakComplete
	})
	u1 := tap.quiesce(t, 150*time.Millisecond)
	require.NotEmpty(t, u1)
	lastTS := u1[len(u1)-1].header.Timestamp
	lastAt := u1[len(u1)-1].at

	_, err = sp.Speak(id, "second")
	require.NoError(t, err)
	next := tap.take(t, 1, 15*time.Second)[0]

	wall := next.at.Sub(lastAt)
	assert.GreaterOrEqual(t, wall, 900*time.Millisecond, "the gap is dominated by the ~1 s synthesis")
	jump := int(next.header.Timestamp) - int(lastTS)
	want := int(wall/(20*time.Millisecond)) * 960
	assert.InDelta(t, float64(want), float64(jump), 960,
		"the timestamp jumps by the time that passed at 48 kHz, ± a frame (got %d, wanted ~%d for %s)", jump, want, wall)
	assert.Equal(t, uint16(u1[len(u1)-1].header.SequenceNumber+1), next.header.SequenceNumber, "sequence does not skip")
	require.NoError(t, sp.Close(id))
}

func TestCloseMidUtterance_CutsDropsAndCancels(t *testing.T) {
	g := newFakeGateway(t, "piper_denis")
	g.slowContains = "slow" // the second sentence takes 3 s, so it is in flight at close
	pub := &fakePub{}
	sp := newSpeech(t, speech.Config{}, g, pub)
	tap := startTap(t)
	ctx := context.Background()

	const text1 = "Добрый день. slow slow slow slow"
	id, err := sp.Open(ctx, tap.addr(), "piper_denis", "", "")
	require.NoError(t, err)
	u1, err := sp.Speak(id, text1)
	require.NoError(t, err)
	u2, err := sp.Speak(id, "queued utterance")
	require.NoError(t, err)

	// let a few frames of u1 go out, then cut while u1's next sentence is
	// still being synthesised and u2 is only queued.
	time.Sleep(150 * time.Millisecond)
	require.NoError(t, sp.Close(id))

	waitFor(t, 5*time.Second, "all three events", func() bool {
		n := 0
		for _, e := range pub.list() {
			if e.Type == events.SpeakComplete || e.Type == events.SessionClosed {
				n++
			}
		}
		return n >= 3
	})
	l := pub.list()
	require.GreaterOrEqual(t, len(l), 3)

	// u1: cut mid-utterance — complete, not full, spoken is a word-boundary prefix.
	assert.Equal(t, events.SpeakComplete, l[0].Type)
	assert.Equal(t, u1, l[0].Data["request_id"])
	assert.Equal(t, "cancelled", l[0].Data["completion_cause"])
	spokenAtWordBoundary(t, l[0].Data["spoken"].(string), text1)

	// u2: dropped before it began — complete with nothing spoken, not full.
	assert.Equal(t, events.SpeakComplete, l[1].Type)
	assert.Equal(t, u2, l[1].Data["request_id"])
	assert.Equal(t, "", l[1].Data["spoken"], "empty for an utterance dropped before it began")
	assert.Equal(t, "cancelled", l[1].Data["completion_cause"])

	// then session.closed, reason closed, and it is the last event.
	assert.Equal(t, events.SessionClosed, l[2].Type)
	assert.Equal(t, "closed", l[2].Data["reason"])
	assert.Equal(t, id, l[2].Data["session_id"])
	assert.Len(t, l, 3, "closed is the last and only closed event")

	// the in-flight speech request was abandoned (C4 §2.3).
	waitFor(t, 3*time.Second, "the fake gateway to see the cancellation", g.wasCancelled)
}

// BARGE-IN-OCCURRED cuts as stop does; the requests it cut say barge-in.
func TestBargeInOccurred_CauseIsBargeIn(t *testing.T) {
	g := newFakeGateway(t, "piper_denis")
	g.slowContains = "slow"
	pub := &fakePub{}
	sp := newSpeech(t, speech.Config{}, g, pub)
	tap := startTap(t)

	id, err := sp.Open(context.Background(), tap.addr(), "piper_denis", "", "")
	require.NoError(t, err)
	_, err = sp.Speak(id, "Добрый день. slow slow slow slow")
	require.NoError(t, err)
	_, err = sp.Speak(id, "queued utterance")
	require.NoError(t, err)

	time.Sleep(150 * time.Millisecond)
	require.NoError(t, sp.BargeInOccurred(id))
	l := pub.list()
	require.Len(t, l, 2)
	assert.Equal(t, "barge-in", l[0].Data["completion_cause"])
	assert.Equal(t, "barge-in", l[1].Data["completion_cause"])
}

// stop cuts what is playing and drops the queue like close, but the session
// stays: no session-closed, and the next speak goes out on the same RTP stream.
func TestStopMidUtterance_SessionGoesOn(t *testing.T) {
	g := newFakeGateway(t, "piper_denis")
	g.slowContains = "slow"
	pub := &fakePub{}
	sp := newSpeech(t, speech.Config{}, g, pub)
	tap := startTap(t)
	ctx := context.Background()

	const text1 = "Добрый день. slow slow slow slow"
	id, err := sp.Open(ctx, tap.addr(), "piper_denis", "", "")
	require.NoError(t, err)
	u1, err := sp.Speak(id, text1)
	require.NoError(t, err)
	u2, err := sp.Speak(id, "queued utterance")
	require.NoError(t, err)

	time.Sleep(150 * time.Millisecond)
	require.NoError(t, sp.Stop(id))
	before := tap.quiesce(t, 150*time.Millisecond)
	require.NotEmpty(t, before)

	l := pub.list()
	require.Len(t, l, 2, "stop returns once both requests have their speak-complete")
	assert.Equal(t, u1, l[0].Data["request_id"])
	assert.Equal(t, "cancelled", l[0].Data["completion_cause"])
	spokenAtWordBoundary(t, l[0].Data["spoken"].(string), text1)
	assert.Equal(t, u2, l[1].Data["request_id"])
	assert.Equal(t, "", l[1].Data["spoken"])
	waitFor(t, 3*time.Second, "the in-flight synthesis to be abandoned", g.wasCancelled)

	u3, err := sp.Speak(id, "one")
	require.NoError(t, err)
	after := tap.take(t, 1, 15*time.Second)[0]
	waitFor(t, 15*time.Second, "u3 spoken", func() bool { return len(pub.list()) >= 3 })
	l = pub.list()
	assert.Equal(t, u3, l[2].Data["request_id"])
	assert.Equal(t, "normal", l[2].Data["completion_cause"])
	for _, e := range l {
		assert.NotEqual(t, events.SessionClosed, e.Type, "stop keeps the session")
	}
	last := before[len(before)-1].header
	assert.Equal(t, last.SSRC, after.header.SSRC, "one stream for the session's whole life")
	assert.Equal(t, uint16(last.SequenceNumber+1), after.header.SequenceNumber, "sequence does not skip")

	require.NoError(t, sp.Stop(id), "hush on a quiet session is ok")
	require.NoError(t, sp.Close(id))
	assert.ErrorContains(t, sp.Stop(id), "unknown_session")
	assert.ErrorContains(t, sp.Stop("spk-nope"), "unknown_session")
}

func TestTTSError_EndsUtteranceSessionGoesOn(t *testing.T) {
	g := newFakeGateway(t, "piper_denis")
	g.failContains = "broken" // the second sentence of u1 gets a 500
	pub := &fakePub{}
	sp := newSpeech(t, speech.Config{}, g, pub)
	tap := startTap(t)
	ctx := context.Background()

	const text1 = "Раз. broken два."
	id, err := sp.Open(ctx, tap.addr(), "piper_denis", "", "")
	require.NoError(t, err)
	u1, err := sp.Speak(id, text1)
	require.NoError(t, err)
	u2, err := sp.Speak(id, "Продолжаем.")
	require.NoError(t, err)

	waitFor(t, 20*time.Second, "two spoken events", func() bool {
		n := 0
		for _, e := range pub.list() {
			if e.Type == events.SpeakComplete {
				n++
			}
		}
		return n == 2
	})
	l := pub.list()
	require.GreaterOrEqual(t, len(l), 2)

	// u1 ends at the failed piece: complete, not full, spoken up to it.
	assert.Equal(t, u1, l[0].Data["request_id"])
	assert.Equal(t, "error", l[0].Data["completion_cause"])
	spokenAtWordBoundary(t, l[0].Data["spoken"].(string), text1)

	// the session goes on with the next utterance, which plays in full.
	assert.Equal(t, u2, l[1].Data["request_id"])
	assert.Equal(t, "normal", l[1].Data["completion_cause"])
	assert.Equal(t, "Продолжаем.", l[1].Data["spoken"])

	require.NoError(t, sp.Close(id))
	waitFor(t, 5*time.Second, "session.closed", func() bool { return pub.hasClosed("closed") })
}

func TestErrorCodes(t *testing.T) {
	g := newFakeGateway(t, "piper_denis")
	pub := &fakePub{}
	tap := startTap(t)
	ctx := context.Background()
	addr := tap.addr()

	sp := newSpeech(t, speech.Config{}, g, pub)

	// unsupported_codec: the only codec this step sends is L16.
	_, err := sp.Open(ctx, addr, "piper_denis", "opus", "")
	assert.ErrorContains(t, err, "unsupported_codec")
	l16, err := sp.Open(ctx, addr, "piper_denis", "L16", "")
	require.NoError(t, err, "L16 is the supported codec")
	require.NoError(t, sp.Close(l16))

	// bad_target: to is not host:port, or the host does not resolve.
	_, err = sp.Open(ctx, "nohost:43012", "piper_denis", "", "")
	assert.ErrorContains(t, err, "bad_target")
	_, err = sp.Open(ctx, "x:y", "piper_denis", "", "")
	assert.ErrorContains(t, err, "bad_target")

	// unknown_voice: no default, no substitute.
	_, err = sp.Open(ctx, addr, "nobody", "", "")
	assert.ErrorContains(t, err, "unknown_voice")

	// capacity: at the limit open fails at once; a closed session frees its slot.
	sp2 := newSpeech(t, speech.Config{MaxSessions: 2}, g, pub)
	a, err := sp2.Open(ctx, addr, "piper_denis", "", "")
	require.NoError(t, err)
	b, err := sp2.Open(ctx, addr, "piper_denis", "", "")
	require.NoError(t, err)
	_, err = sp2.Open(ctx, addr, "piper_denis", "", "")
	assert.ErrorContains(t, err, "capacity")
	require.NoError(t, sp2.Close(a))
	c, err := sp2.Open(ctx, addr, "piper_denis", "", "")
	require.NoError(t, err, "the freed slot is usable again")
	require.NoError(t, sp2.Close(b))
	require.NoError(t, sp2.Close(c))

	// unknown_session: no such session, or it has closed.
	_, err = sp2.Speak("spk-00000000", "привет")
	assert.ErrorContains(t, err, "unknown_session")
	assert.ErrorContains(t, sp2.Close("spk-00000000"), "unknown_session")
	_, err = sp2.Speak(a, "позже")
	assert.ErrorContains(t, err, "unknown_session", "a closed session is gone")
}

func TestIdleTimeout_EndsTheSession(t *testing.T) {
	g := newFakeGateway(t, "piper_denis")
	ctx := context.Background()
	tap := startTap(t)

	// Nothing is queued or playing: after the idle timeout the session ends
	// by itself with reason idle, and the slot is freed.
	pub := &fakePub{}
	sp := newSpeech(t, speech.Config{IdleTimeout: 400 * time.Millisecond}, g, pub)
	id, err := sp.Open(ctx, tap.addr(), "piper_denis", "", "")
	require.NoError(t, err)
	waitFor(t, 10*time.Second, "session.closed with reason idle", func() bool { return pub.hasClosed("idle") })
	_, err = sp.Speak(id, "позже")
	assert.ErrorContains(t, err, "unknown_session")
	again, err := sp.Open(ctx, tap.addr(), "piper_denis", "", "")
	require.NoError(t, err, "the slot is free again")
	require.NoError(t, sp.Close(again))

	// A playing session is not idle: speech longer than the timeout keeps it
	// open, then it ends on its own once it is quiet.
	pub2 := &fakePub{}
	sp2 := newSpeech(t, speech.Config{IdleTimeout: 300 * time.Millisecond}, g, pub2)
	id2, err := sp2.Open(ctx, tap.addr(), "piper_denis", "", "")
	require.NoError(t, err)
	_, err = sp2.Speak(id2, "один два три четыре пять шесть семь восемь девять десять одиннадцать двенадцать")
	require.NoError(t, err)
	waitFor(t, 10*time.Second, "the long utterance spoken in full", func() bool {
		for _, e := range pub2.list() {
			if e.Type == events.SpeakComplete && e.Data["completion_cause"] == "normal" {
				return true
			}
		}
		return false
	})
	assert.False(t, pub2.hasClosed("idle"), "no idle close while speech was playing")
	waitFor(t, 10*time.Second, "session.closed with reason idle after the speech", func() bool { return pub2.hasClosed("idle") })
}

// slowTTS is a SynthClient that never synthesises: opens alone are under test.
// Its delay once widened the window between an open taking its slot and its
// session existing (the voice lookup); open no longer asks the engine,
// so the window is the target resolution alone.
type slowTTS struct {
	delay  time.Duration
	voices map[string]bool
}

func (t *slowTTS) Synth(context.Context, string, string) ([]int16, error) {
	return nil, errors.New("slowTTS: no synthesis in this test")
}

// 50 open/close cycles at a limit of 4: every open fits, and afterwards the
// manager holds nothing — the old ids are unknown_session and 4 fresh
// sessions fit again.
func TestOpenCloseCycling_FreesSlotsAndForgetsSessions(t *testing.T) {
	g := newFakeGateway(t, "piper_denis")
	pub := &fakePub{}
	sp := newSpeech(t, speech.Config{MaxSessions: 4}, g, pub)
	tap := startTap(t)
	ctx := context.Background()

	const n = 50
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		id, err := sp.Open(ctx, tap.addr(), "piper_denis", "", "")
		require.NoError(t, err, "open %d of %d", i+1, n)
		ids[i] = id
		require.NoError(t, sp.Close(id), "close %d of %d", i+1, n)
	}

	assert.Equal(t, 0, sp.Len(), "an ended session leaves nothing in the manager")
	for i, id := range ids {
		_, err := sp.Speak(id, "позже")
		assert.ErrorContains(t, err, "unknown_session", "id %d of %d", i+1, n)
	}

	for i := 0; i < 4; i++ {
		id, err := sp.Open(ctx, tap.addr(), "piper_denis", "", "")
		require.NoError(t, err, "fresh session %d fits the now-empty limit", i+1)
		require.NoError(t, sp.Close(id))
	}
}

// 20 concurrent opens at a limit of 4: exactly 4
// succeed, the other 16 fail with capacity.
func TestConcurrentOpen_ExactlyTheLimitWin(t *testing.T) {
	tts := &slowTTS{delay: 100 * time.Millisecond, voices: map[string]bool{"piper_denis": true}}
	pub := &fakePub{}
	sp := speech.New(speech.Config{MaxSessions: 4}, tts, pub)
	tap := startTap(t)
	ctx := context.Background()

	const opens = 20
	var wg sync.WaitGroup
	res := make(chan error, opens)
	for i := 0; i < opens; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := sp.Open(ctx, tap.addr(), "piper_denis", "", "")
			res <- err
		}()
	}
	wg.Wait()
	close(res)

	ok, capc := 0, 0
	for err := range res {
		switch {
		case err == nil:
			ok++
		case strings.Contains(err.Error(), "capacity"):
			capc++
		default:
			t.Fatalf("unexpected open error: %v", err)
		}
	}
	assert.Equal(t, 4, ok, "exactly the limit of concurrent opens succeeds")
	assert.Equal(t, opens-4, capc, "the rest fail with capacity")
}

// an open that fails after taking its slot (unknown_voice) gives the slot
// back: with 3 of 4 taken, a failing open, then a valid one — the valid one
// succeeds.
func TestFailedOpen_GivesItsSlotBack(t *testing.T) {
	g := newFakeGateway(t, "piper_denis")
	pub := &fakePub{}
	sp := newSpeech(t, speech.Config{MaxSessions: 4}, g, pub)
	tap := startTap(t)
	ctx := context.Background()

	opened := make([]string, 3)
	for i := 0; i < 3; i++ {
		id, err := sp.Open(ctx, tap.addr(), "piper_denis", "", "")
		require.NoError(t, err, "open %d of 3", i+1)
		opened[i] = id
	}

	_, err := sp.Open(ctx, tap.addr(), "nobody", "", "")
	assert.ErrorContains(t, err, "unknown_voice")

	id, err := sp.Open(ctx, tap.addr(), "piper_denis", "", "")
	require.NoError(t, err, "the slot the failed open took is back")
	require.NoError(t, sp.Close(id))
	for _, id := range opened {
		require.NoError(t, sp.Close(id))
	}
}

// codec is exactly "L16": "l16" and "L16 " are unsupported; empty and
// absent mean L16.
func TestCodecStrictlyL16(t *testing.T) {
	g := newFakeGateway(t, "piper_denis")
	pub := &fakePub{}
	sp := newSpeech(t, speech.Config{}, g, pub)
	tap := startTap(t)
	ctx := context.Background()

	for _, bad := range []string{"l16", "L16 ", " L16", "opus"} {
		_, err := sp.Open(ctx, tap.addr(), "piper_denis", bad, "")
		assert.ErrorContains(t, err, "unsupported_codec", "codec %q", bad)
	}
	for _, good := range []string{"L16", ""} {
		id, err := sp.Open(ctx, tap.addr(), "piper_denis", good, "")
		require.NoError(t, err, "codec %q", good)
		require.NoError(t, sp.Close(id))
	}
}
