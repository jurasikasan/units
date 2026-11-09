package hearing

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	rtp "github.com/pion/rtp"

	"github.com/jurasikasan/units/senses/hearing/internal/g711"
)

// --- fake transcriptions door ---

type sttFake struct {
	*httptest.Server
	mu      sync.Mutex
	fail    bool
	delay   time.Duration
	byLen   bool // answer "len=<WAV bytes>" instead of text
	text    string
	lang    string
	wavLens []int
	asked   []string // the language field of each request
}

func newSTTFake() *sttFake {
	f := &sttFake{text: "hello", lang: "en"}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "bad multipart", http.StatusBadRequest)
			return
		}
		file, hdr, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "no file field", http.StatusBadRequest)
			return
		}
		raw, _ := io.ReadAll(file)
		_ = file.Close()
		f.mu.Lock()
		f.wavLens = append(f.wavLens, len(raw))
		f.asked = append(f.asked, r.FormValue("language"))
		failed := f.fail
		text, lang := f.text, f.lang
		delay, byLen := f.delay, f.byLen
		f.mu.Unlock()
		if byLen {
			text = fmt.Sprintf("len=%d", len(raw))
		}
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		if failed {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_ = hdr
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"text":%q,"language":%q,"language_probability":0.9,"backend":"fake"}`, text, lang)
	}))
	return f
}

// --- fake VAD engine (silero) ---

type vadFake struct {
	*httptest.Server
	mu       sync.Mutex
	bodyLens []int
	deletes  int
	rejected int // non-1024-byte bodies
	probs    []float64
}

func newVADFake() *vadFake {
	f := &vadFake{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			if !strings.HasPrefix(r.URL.Path, "/vad/") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.deletes++
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/vad/") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodyLens = append(f.bodyLens, len(body))
		f.mu.Unlock()
		if len(body) != 1024 {
			// the VAD engine requires exactly 1024-byte bodies
			f.mu.Lock()
			f.rejected++
			f.mu.Unlock()
			http.Error(w, "body must be 1024 bytes", http.StatusBadRequest)
			return
		}
		var sum float64
		for i := 0; i+1 < len(body); i += 2 {
			v := float64(int16(binary.LittleEndian.Uint16(body[i:])))
			sum += v * v
		}
		n := float64(len(body) / 2)
		rms := 0.0
		if n > 0 {
			rms = (sum / n)
		}
		prob := 0.0
		if rms*(rms) > 500*500 { // rms above 500
			prob = 1.0
		}
		f.mu.Lock()
		f.probs = append(f.probs, prob)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"prob":%v}`, prob)
	}))
	return f
}

// --- harness ---

type harness struct {
	t    *testing.T
	svc  *Service
	sink *InMemorySink
	stt  *sttFake
}

func newHarness(t *testing.T, mutate func(*Config)) *harness {
	t.Helper()
	sink := &InMemorySink{}
	stt := newSTTFake()
	h := &harness{t: t, sink: sink, stt: stt}
	cfg := Config{
		Sink:            sink,
		STTURL:          stt.URL,
		VADKind:         "energy",
		EnergyThreshold: 500,
		Onset:           200 * time.Millisecond,
		MergeWindow:     400 * time.Millisecond,
		MaxUtterance:    25 * time.Second,
		MaxSessions:     4,
		IdleTimeout:     2 * time.Minute,
		AudioInHost:     "127.0.0.1",
		PortMin:         -1, // ephemeral UDP port
		PortMax:         -1,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	h.svc = NewService(cfg)
	t.Cleanup(func() {
		svc := h.svc
		svc.mu.Lock()
		ids := make([]string, 0, len(svc.sessions))
		for id := range svc.sessions {
			ids = append(ids, id)
		}
		svc.mu.Unlock()
		for _, id := range ids {
			_ = svc.Close(id)
		}
		stt.Close()
	})
	return h
}

func (h *harness) open(t *testing.T) (string, string) {
	t.Helper()
	id, addr, err := h.svc.Open(context.Background(), nil, "", "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return id, addr
}

// events returns a snapshot of the events of type typ, in arrival order.
func (h *harness) events(typ string) []Event {
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	var out []Event
	for _, ev := range h.sink.Events {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

// waitType polls the sink until at least n events of type typ arrived
// (or the deadline passes).
func (h *harness) waitType(t *testing.T, typ string, n int, timeout time.Duration) []Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		evs := h.events(typ)
		if len(evs) >= n {
			return evs
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d %q events; sink has %d: %+v", n, typ, len(h.sink.Events), h.sink.Events)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitNoType fails if any NEW event of typ appears within quiet,
// relative to the count present when the call is made (test (a) has a
// zero baseline, i.e. no events at all).
func (h *harness) waitNoType(t *testing.T, typ string, quiet time.Duration) {
	t.Helper()
	base := len(h.events(typ))
	time.Sleep(quiet)
	if evs := h.events(typ); len(evs) != base {
		t.Fatalf("expected no new %q events, got %d new (total %d): %+v", typ, len(evs)-base, len(evs), evs[base:])
	}
}

// --- RTP senders ---

type sender struct {
	t       *testing.T
	conn    *net.UDPConn
	ssrc    uint32
	seq     uint16
	ts      uint32
	firstAt time.Time // when the first audio packet hit the wire
}

func newSender(t *testing.T, audioIn string, ssrc uint32) *sender {
	t.Helper()
	host, portStr, err := net.SplitHostPort(audioIn)
	if err != nil {
		t.Fatalf("bad audio_in %q: %v", audioIn, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("bad audio_in port: %v", err)
	}
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP(host), Port: port})
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &sender{t: t, conn: conn, ssrc: ssrc, seq: 100}
}

func (s *sender) send(pt byte, payload []byte, samples int) {
	s.t.Helper()
	pkt := &rtp.Packet{
		SequenceNumber: s.seq,
		Timestamp:      s.ts,
		SSRC:           s.ssrc,
		PayloadType:    pt,
		Payload:        payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		s.t.Fatalf("marshal rtp: %v", err)
	}
	if s.firstAt.IsZero() {
		s.firstAt = time.Now()
	}
	s.seq++
	s.ts += uint32(samples)
	if _, err := s.conn.Write(raw); err != nil {
		s.t.Fatalf("send udp: %v", err)
	}
}

func (s *sender) tone(pt byte, amp int16, startSec, durSec float64) {
	s.t.Helper()
	rate := 8000.0
	if pt == 96 {
		rate = 48000.0
	}
	total := int(durSec * rate)
	per := int(0.020 * rate) // 20 ms packets
	base := int(startSec * rate)
	for i := 0; i < total; i += per {
		n := per
		if total-i < n {
			n = total - i
		}
		payload := make([]byte, 0, 2*n)
		for j := 0; j < n; j++ {
			idx := base + i + j
			v := int16(float64(amp) * sin2pi(1000*float64(idx)/rate))
			switch pt {
			case 0:
				payload = append(payload, ulawNearest(v))
			case 8:
				payload = append(payload, alawNearest(v))
			case 96:
				payload = append(payload, byte(uint16(v)>>8), byte(uint16(v)))
			}
		}
		s.send(pt, payload, n)
	}
}

func (s *sender) silence(pt byte, startSec, durSec float64) {
	s.t.Helper()
	rate := 8000.0
	if pt == 96 {
		rate = 48000.0
	}
	total := int(durSec * rate)
	per := int(0.020 * rate)
	for i := 0; i < total; i += per {
		n := per
		if total-i < n {
			n = total - i
		}
		switch pt {
		case 0:
			s.send(pt, bytes.Repeat([]byte{0xFF}, n), n) // 0xFF decodes to 0
		case 8:
			s.send(pt, bytes.Repeat([]byte{0x55}, n), n) // 0x55 decodes to -8
		case 96:
			s.send(pt, make([]byte, 2*n), n)
		}
	}
}

// gap advances the sender's RTP timestamp by durSec at the payload
// type's clock without sending a single packet — a sender that
// suppresses silence. C5 §3: the times must follow the RTP clock, so
// the gap must land in the reported times, not disappear from them.
func (s *sender) gap(pt byte, durSec float64) {
	s.t.Helper()
	rate := 8000.0
	if pt == 96 {
		rate = 48000.0
	}
	s.ts += uint32(durSec * rate)
}

var ulawTable = func() [256]int16 {
	var t [256]int16
	for i := range t {
		t[i] = g711.Ulaw(byte(i))
	}
	return t
}()

var alawTable = func() [256]int16 {
	var t [256]int16
	for i := range t {
		t[i] = g711.Alaw(byte(i))
	}
	return t
}()

func ulawNearest(v int16) byte {
	best, bestD := byte(0xFF), int32(1<<30)
	for c := 0; c < 256; c++ {
		d := int32(ulawTable[c] - v)
		if d < 0 {
			d = -d
		}
		if d < bestD {
			bestD, best = d, byte(c)
		}
	}
	return best
}

func alawNearest(v int16) byte {
	best, bestD := byte(0x55), int32(1<<30)
	for c := 0; c < 256; c++ {
		d := int32(alawTable[c] - v)
		if d < 0 {
			d = -d
		}
		if d < bestD {
			bestD, best = d, byte(c)
		}
	}
	return best
}

// --- (a) a 100 ms burst is no speech: no events at all ---

func TestBurstShorterThanOnsetIsSilent(t *testing.T) {
	h := newHarness(t, nil)
	id, addr := h.open(t)
	s := newSender(t, addr, 0xA11CE)
	s.tone(0, 8000, 0, 0.100) // 1 kHz, amplitude 8000, 100 ms
	s.silence(0, 0.100, 0.5)
	// the burst is shorter than HEARING_ONSET (200 ms): no speech events
	h.waitNoType(t, "speechrecog.start-of-input", 700*time.Millisecond)
	h.waitNoType(t, "speechrecog.recognition-complete", 0)
	if err := h.svc.Close(id); err != nil {
		t.Fatalf("close: %v", err)
	}
	closed := h.waitType(t, "speechrecog.session-closed", 1, 5*time.Second)
	if got := closed[0].Data["reason"]; got != "closed" {
		t.Fatalf("reason = %v, want closed", got)
	}
}

// --- (b) a 1 s burst: started, then heard, with sane times ---

func TestOneSecondBurst(t *testing.T) {
	h := newHarness(t, nil)
	id, addr := h.open(t)
	s := newSender(t, addr, 0xA11CE)
	s.tone(0, 8000, 0, 1.0) // 1 kHz, amplitude 8000, 1 s
	s.silence(0, 1.0, 1.0)  // enough silence for the merge window
	// burst start = when the first audio actually entered audio_in
	// (payload encoding before the first send must not count)
	burstStart := s.firstAt

	started := h.waitType(t, "speechrecog.start-of-input", 1, 5*time.Second)
	heard := h.waitType(t, "speechrecog.recognition-complete", 1, 5*time.Second)
	if len(started) != 1 {
		t.Fatalf("started events = %d, want 1", len(started))
	}
	if heard[0].Data["request_id"] != started[0].Data["request_id"] {
		t.Fatalf("heard %v != started %v", heard[0].Data["request_id"], started[0].Data["request_id"])
	}
	if got := heard[0].Data["text"]; got != "hello" {
		t.Fatalf("text = %v, want hello", got)
	}
	if got := heard[0].Data["language"]; got != "en" {
		t.Fatalf("language = %v, want en", got)
	}
	if got := heard[0].Data["completion_cause"]; got != "success" {
		t.Fatalf("completion_cause = %v, want success", got)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, started[0].Data["started_at"].(string))
	if err != nil {
		t.Fatalf("bad started_at: %v", err)
	}
	if d := absDur(startedAt.Sub(burstStart)); d > 40*time.Millisecond {
		t.Fatalf("started_at off by %v (> 40 ms)", d)
	}
	endedAt, err := time.Parse(time.RFC3339Nano, heard[0].Data["ended_at"].(string))
	if err != nil {
		t.Fatalf("bad ended_at: %v", err)
	}
	// ended_at is when the last speech ended (burst end), not the event time
	wantEnd := burstStart.Add(1 * time.Second)
	if d := absDur(endedAt.Sub(wantEnd)); d > 40*time.Millisecond {
		t.Fatalf("ended_at off by %v (> 40 ms)", d)
	}
	if got := heard[0].Data["session_id"]; got != id {
		t.Fatalf("session_id = %v, want %v", got, id)
	}

	if err := h.svc.Close(id); err != nil {
		t.Fatalf("close: %v", err)
	}
	h.waitType(t, "speechrecog.session-closed", 1, 5*time.Second)
	evs := h.sink.Events
	if len(evs) != 3 || evs[0].Type != "speechrecog.start-of-input" ||
		evs[1].Type != "speechrecog.recognition-complete" || evs[2].Type != "speechrecog.session-closed" {
		t.Fatalf("event order = %+v", evs)
	}
}

// --- (c) merge window: 300 ms apart merges, 600 ms apart splits ---

func TestMergeWindow(t *testing.T) {
	t.Run("gap_300ms_merges", func(t *testing.T) {
		h := newHarness(t, nil)
		id, addr := h.open(t)
		s := newSender(t, addr, 0xA11CE)
		s.tone(0, 8000, 0, 1.0)
		s.silence(0, 1.0, 0.3)
		s.tone(0, 8000, 1.3, 1.0)
		s.silence(0, 2.3, 1.0)
		started := h.waitType(t, "speechrecog.start-of-input", 1, 5*time.Second)
		heard := h.waitType(t, "speechrecog.recognition-complete", 1, 5*time.Second)
		h.waitNoType(t, "speechrecog.start-of-input", 300*time.Millisecond)
		h.waitNoType(t, "speechrecog.recognition-complete", 300*time.Millisecond)
		if len(started) != 1 {
			t.Fatalf("started = %d, want 1", len(started))
		}
		if len(heard) != 1 {
			t.Fatalf("heard = %d, want 1", len(heard))
		}
		endedAt, err := time.Parse(time.RFC3339Nano, heard[0].Data["ended_at"].(string))
		if err != nil {
			t.Fatal(err)
		}
		_ = endedAt
		if err := h.svc.Close(id); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("gap_600ms_splits", func(t *testing.T) {
		h := newHarness(t, nil)
		id, addr := h.open(t)
		s := newSender(t, addr, 0xA11CE)
		s.tone(0, 8000, 0, 1.0)
		s.silence(0, 1.0, 0.6)
		s.tone(0, 8000, 1.6, 1.0)
		s.silence(0, 2.6, 1.0)
		h.waitType(t, "speechrecog.start-of-input", 2, 5*time.Second)
		heard := h.waitType(t, "speechrecog.recognition-complete", 2, 5*time.Second)
		h.waitNoType(t, "speechrecog.recognition-complete", 300*time.Millisecond)
		if len(heard) != 2 {
			t.Fatalf("heard = %d, want 2", len(heard))
		}
		ids := map[string]bool{heard[0].Data["request_id"].(string): true}
		if ids[heard[1].Data["request_id"].(string)] {
			t.Fatalf("two heards share a request_id: %+v", heard)
		}
		if err := h.svc.Close(id); err != nil {
			t.Fatal(err)
		}
	})
}

// --- (d) HEARING_MAX_UTTERANCE cuts long speech ---

func TestMaxUtteranceCuts(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxUtterance = 2 * time.Second })
	id, addr := h.open(t)
	s := newSender(t, addr, 0xA11CE)
	s.tone(0, 8000, 0, 5.0)
	s.silence(0, 5.0, 1.0)
	started := h.waitType(t, "speechrecog.start-of-input", 3, 10*time.Second)
	heard := h.waitType(t, "speechrecog.recognition-complete", 3, 10*time.Second)
	if len(started) != 3 {
		t.Fatalf("started = %d, want 3", len(started))
	}
	if len(heard) != 3 {
		t.Fatalf("heard = %d, want 3", len(heard))
	}
	// the two cut at HEARING_MAX_UTTERANCE say so; the last ended in silence
	for i, want := range []string{"success-maxtime", "success-maxtime", "success"} {
		if got := heard[i].Data["completion_cause"]; got != want {
			t.Fatalf("heard[%d] completion_cause = %v, want %s", i, got, want)
		}
	}
	// every heard is preceded by its own started, with the same request_id
	byID := map[string]bool{}
	seenStarted := map[string]bool{}
	for _, ev := range h.sink.Events {
		switch ev.Type {
		case "speechrecog.start-of-input":
			seenStarted[ev.Data["request_id"].(string)] = true
		case "speechrecog.recognition-complete":
			uid := ev.Data["request_id"].(string)
			if !seenStarted[uid] {
				t.Fatalf("heard %q was not preceded by its start-of-input", uid)
			}
			if byID[uid] {
				t.Fatalf("utterance %q heard twice", uid)
			}
			byID[uid] = true
		}
	}
	if err := h.svc.Close(id); err != nil {
		t.Fatal(err)
	}
}

// Speech to the detector, words to nobody: STT answered with no text, so the
// recognition completes with no-match (RFC 6787 §9.4.11, 001).
func TestEmptyTranscriptIsNoMatch(t *testing.T) {
	h := newHarness(t, nil)
	h.stt.mu.Lock()
	h.stt.text = "  "
	h.stt.mu.Unlock()
	id, addr := h.open(t)
	s := newSender(t, addr, 0xA11CE)
	s.tone(0, 8000, 0, 1.0)
	s.silence(0, 1.0, 1.0)
	heard := h.waitType(t, "speechrecog.recognition-complete", 1, 5*time.Second)
	if got := heard[0].Data["completion_cause"]; got != "no-match" {
		t.Fatalf("completion_cause = %v, want no-match", got)
	}
	if err := h.svc.Close(id); err != nil {
		t.Fatal(err)
	}
}

// The session's speech_language reaches STT with every utterance;
// a session opened without one leaves STT to guess.
func TestSessionLanguageReachesSTT(t *testing.T) {
	h := newHarness(t, nil)
	for i, lang := range []string{"ru", ""} {
		id, addr, err := h.svc.Open(context.Background(), nil, lang, "")
		if err != nil {
			t.Fatal(err)
		}
		s := newSender(t, addr, 0xA11CE)
		s.tone(0, 8000, 0, 1.0)
		s.silence(0, 1.0, 1.0)
		h.waitType(t, "speechrecog.recognition-complete", i+1, 5*time.Second)
		if err := h.svc.Close(id); err != nil {
			t.Fatal(err)
		}
		h.stt.mu.Lock()
		asked := h.stt.asked
		h.stt.asked = nil
		h.stt.mu.Unlock()
		if len(asked) == 0 {
			t.Fatalf("language %q: STT was not asked", lang)
		}
		for _, a := range asked {
			if a != lang {
				t.Fatalf("STT got language %q, want %q", a, lang)
			}
		}
	}
}

// --- (e) a failing STT: heard with recognizer-error and empty text ---

func TestSTTFailure(t *testing.T) {
	h := newHarness(t, nil)
	h.stt.mu.Lock()
	h.stt.fail = true
	h.stt.mu.Unlock()
	id, addr := h.open(t)
	s := newSender(t, addr, 0xA11CE)
	s.tone(0, 8000, 0, 1.0)
	s.silence(0, 1.0, 1.0)
	heard := h.waitType(t, "speechrecog.recognition-complete", 1, 5*time.Second)
	if got := heard[0].Data["completion_cause"]; got != "recognizer-error" {
		t.Fatalf("completion_cause = %v, want recognizer-error", got)
	}
	if got := heard[0].Data["text"]; got != "" {
		t.Fatalf("text = %q, want empty", got)
	}
	if err := h.svc.Close(id); err != nil {
		t.Fatal(err)
	}
}

// --- (f) closing mid-utterance: heard, then closed ---

func TestCloseMidUtterance(t *testing.T) {
	h := newHarness(t, nil)
	id, addr := h.open(t)
	s := newSender(t, addr, 0xA11CE)
	s.tone(0, 8000, 0, 1.5)
	h.waitType(t, "speechrecog.start-of-input", 1, 5*time.Second)
	if err := h.svc.Close(id); err != nil {
		t.Fatalf("close: %v", err)
	}
	h.waitType(t, "speechrecog.recognition-complete", 1, 5*time.Second)
	closed := h.waitType(t, "speechrecog.session-closed", 1, 5*time.Second)
	if got := closed[0].Data["reason"]; got != "closed" {
		t.Fatalf("reason = %v, want closed", got)
	}
	evs := h.sink.Events
	foundHeard, foundClosed := -1, -1
	for i, ev := range evs {
		switch ev.Type {
		case "speechrecog.recognition-complete":
			foundHeard = i
		case "speechrecog.session-closed":
			foundClosed = i
		}
	}
	if foundHeard < 0 || foundClosed < 0 || foundHeard > foundClosed {
		t.Fatalf("expected heard before closed, got %+v", evs)
	}
}

// --- (g) the same speech at 8 kHz and 48 kHz gives the same events ---

func TestCodecParity(t *testing.T) {
	type norm struct {
		typ       string
		startedMs int64 // started_at relative to burst start, or 0
		endedMs   int64
		ok        bool
		text      string
	}
	run := func(pt byte) []norm {
		h := newHarness(t, nil)
		id, addr := h.open(t)
		s := newSender(t, addr, 0xA11CE)
		s.tone(pt, 8000, 0, 1.0)
		s.silence(pt, 1.0, 1.0)
		burstStart := s.firstAt
		h.waitType(t, "speechrecog.start-of-input", 1, 5*time.Second)
		h.waitType(t, "speechrecog.recognition-complete", 1, 5*time.Second)
		if err := h.svc.Close(id); err != nil {
			t.Fatal(err)
		}
		h.waitType(t, "speechrecog.session-closed", 1, 5*time.Second)
		var out []norm
		for _, ev := range h.sink.Events {
			n := norm{typ: ev.Type}
			for _, k := range []string{"started_at", "ended_at"} {
				if v, okk := ev.Data[k].(string); okk {
					ts, err := time.Parse(time.RFC3339Nano, v)
					if err != nil {
						t.Fatal(err)
					}
					ms := int64(ts.Sub(burstStart) / time.Millisecond)
					if k == "started_at" {
						n.startedMs = ms
					} else {
						n.endedMs = ms
					}
				}
			}
			if v, okk := ev.Data["completion_cause"].(string); okk {
				n.ok = v != "recognizer-error"
			}
			if v, okk := ev.Data["text"].(string); okk {
				n.text = v
			}
			out = append(out, n)
		}
		return out
	}
	p0 := run(0)
	p8 := run(8)
	p96 := run(96)
	// "Same events" (this task's acceptance criterion; CONTRACT.md has
	// only §1–§6 — C5 §4 lists PT 0/8 and PT 96 as audio_in, C5 §3 the
	// events): identical types, order, count, utterance outcomes
	// (ok, text). Timestamps are wall-clock relative
	// to each run's own burst start and the two runs are separate UDP
	// sessions scheduled independently, so they are compared within a
	// 25 ms scheduling tolerance (a 16 kHz sample is 62.5 µs; any
	// genuine codec difference in boundary windows would show up as
	// ~32 ms = one VAD window, well outside the tolerance).
	const tolMs = 25
	same := func(a, b []norm) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i].typ != b[i].typ || a[i].ok != b[i].ok || a[i].text != b[i].text {
				return false
			}
			if d := a[i].startedMs - b[i].startedMs; d < -tolMs || d > tolMs {
				return false
			}
			if d := a[i].endedMs - b[i].endedMs; d < -tolMs || d > tolMs {
				return false
			}
		}
		return true
	}
	if !same(p0, p8) {
		t.Fatalf("PT 0 and PT 8 give different events:\n%+v\n%+v", p0, p8)
	}
	if !same(p0, p96) {
		t.Fatalf("PT 0 and PT 96 give different events:\n%+v\n%+v", p0, p96)
	}
	// the transcribed audio must be the same too: identical WAV lengths
	h0 := newHarness(t, nil)
	id0, addr0 := h0.open(t)
	// one sender per session: the RTP sequence number must keep
	// increasing (a restart would look like a retransmission burst)
	s0 := newSender(t, addr0, 0xA11CE)
	s0.tone(0, 8000, 0, 1.0)
	s0.silence(0, 1.0, 1.0)
	h0.waitType(t, "speechrecog.recognition-complete", 1, 5*time.Second)
	_ = h0.svc.Close(id0)
	h0.stt.mu.Lock()
	want := h0.stt.wavLens[len(h0.stt.wavLens)-1]
	h0.stt.mu.Unlock()
	for _, pt := range []byte{0, 8, 96} {
		h := newHarness(t, nil)
		id, addr := h.open(t)
		s := newSender(t, addr, 0xA11CE)
		s.tone(pt, 8000, 0, 1.0)
		s.silence(pt, 1.0, 1.0)
		h.waitType(t, "speechrecog.recognition-complete", 1, 5*time.Second)
		_ = h.svc.Close(id)
		h.stt.mu.Lock()
		got := h.stt.wavLens[len(h.stt.wavLens)-1]
		h.stt.mu.Unlock()
		if got != want {
			t.Fatalf("PT %d: transcribed %d bytes, want %d (same audio)", pt, got, want)
		}
	}
}

// --- (h) the first SSRC owns audio_in; a second one is dropped ---

func TestSecondSSRCDropped(t *testing.T) {
	h := newHarness(t, nil)
	id, addr := h.open(t)
	a := newSender(t, addr, 0xA11CE) // the first SSRC: owns the port
	b := newSender(t, addr, 0xBEEF)  // a second SSRC: must be ignored
	a.tone(0, 8000, 0, 1.0)
	b.tone(0, 8000, 0, 1.5) // longer burst; if it leaked in, ended_at would be ~1.5 s
	a.silence(0, 1.0, 1.0)
	burstStart := a.firstAt // when A's audio actually entered audio_in
	heard := h.waitType(t, "speechrecog.recognition-complete", 1, 5*time.Second)
	h.waitNoType(t, "speechrecog.recognition-complete", 400*time.Millisecond)
	endedAt, err := time.Parse(time.RFC3339Nano, heard[0].Data["ended_at"].(string))
	if err != nil {
		t.Fatal(err)
	}
	// the utterance must end with A's 1.0 s burst, not B's 1.5 s one:
	// if the second SSRC had been accepted, ended_at would be >= ~1.5 s
	if endedAt.After(burstStart.Add(1300 * time.Millisecond)) {
		t.Fatalf("ended_at %v suggests the second SSRC was accepted (burst end ~1.5 s)", endedAt)
	}
	if endedAt.Before(burstStart.Add(850 * time.Millisecond)) {
		t.Fatalf("ended_at %v is before A's burst ended (~1.0 s)", endedAt)
	}
	if err := h.svc.Close(id); err != nil {
		t.Fatal(err)
	}
}

// --- (i) an idle session is closed with reason idle ---

func TestIdleTimeout(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.IdleTimeout = 800 * time.Millisecond })
	_, addr := h.open(t)
	s := newSender(t, addr, 0xA11CE)
	s.tone(0, 8000, 0, 0.1) // a short burst: no utterance, just recent activity
	// then no more packets: the session goes idle
	closed := h.waitType(t, "speechrecog.session-closed", 1, 5*time.Second)
	if got := closed[0].Data["reason"]; got != "idle" {
		t.Fatalf("reason = %v, want idle", got)
	}
	if evs := h.events("speechrecog.start-of-input"); len(evs) != 0 {
		t.Fatalf("unexpected started: %+v", evs)
	}
	if evs := h.events("speechrecog.recognition-complete"); len(evs) != 0 {
		t.Fatalf("unexpected heard: %+v", evs)
	}
}

// --- (j) error codes ---

func TestErrorCodes(t *testing.T) {
	t.Run("unsupported_recognizer", func(t *testing.T) {
		h := newHarness(t, nil)
		_, _, err := h.svc.Open(context.Background(), []string{"speech", "echo"}, "", "")
		if err == nil || !strings.HasPrefix(err.Error(), "unsupported_recognizer") {
			t.Fatalf("err = %v, want prefix unsupported_recognizer", err)
		}
		_, _, err = h.svc.Open(context.Background(), []string{"noise"}, "", "")
		if err == nil || !strings.HasPrefix(err.Error(), "unsupported_recognizer") {
			t.Fatalf("err = %v, want prefix unsupported_recognizer", err)
		}
	})
	t.Run("capacity", func(t *testing.T) {
		h := newHarness(t, func(c *Config) { c.MaxSessions = 1 })
		id1, _ := h.open(t)
		_, _, err := h.svc.Open(context.Background(), nil, "", "")
		if err == nil || !strings.HasPrefix(err.Error(), "capacity") {
			t.Fatalf("err = %v, want prefix capacity", err)
		}
		if err := h.svc.Close(id1); err != nil {
			t.Fatal(err)
		}
		id2, _ := h.open(t)
		if id1 == id2 {
			t.Fatalf("session ids are not unique")
		}
		if err := h.svc.Close(id2); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unknown_session", func(t *testing.T) {
		h := newHarness(t, nil)
		err := h.svc.Close("hr-00000000")
		if err == nil || !strings.HasPrefix(err.Error(), "unknown_session") {
			t.Fatalf("err = %v, want prefix unknown_session", err)
		}
	})
	t.Run("close_twice", func(t *testing.T) {
		h := newHarness(t, nil)
		id, _ := h.open(t)
		if err := h.svc.Close(id); err != nil {
			t.Fatal(err)
		}
		h.waitType(t, "speechrecog.session-closed", 1, 5*time.Second)
		if err := h.svc.Close(id); err != nil {
			t.Fatalf("second close must be ok, got %v", err)
		}
		time.Sleep(300 * time.Millisecond)
		if evs := h.events("speechrecog.session-closed"); len(evs) != 1 {
			t.Fatalf("closed events = %d, want 1 (no second event)", len(evs))
		}
	})
}

// --- (k) silero VAD client sends exactly 1024-byte bodies ---

func TestSileroVADClient(t *testing.T) {
	vf := newVADFake()
	h := newHarness(t, func(c *Config) {
		c.VADKind = "silero"
		c.VADURL = vf.URL
	})
	id, addr := h.open(t)
	s := newSender(t, addr, 0xA11CE)
	s.tone(0, 8000, 0, 1.0)
	s.silence(0, 1.0, 1.0)
	h.waitType(t, "speechrecog.start-of-input", 1, 5*time.Second)
	heard := h.waitType(t, "speechrecog.recognition-complete", 1, 5*time.Second)
	if got := heard[0].Data["completion_cause"]; got != "success" {
		t.Fatalf("completion_cause = %v, want success", got)
	}
	if err := h.svc.Close(id); err != nil {
		t.Fatal(err)
	}
	vf.mu.Lock()
	defer vf.mu.Unlock()
	if len(vf.bodyLens) == 0 {
		t.Fatal("the VAD engine got no requests")
	}
	for i, n := range vf.bodyLens {
		if n != 1024 {
			t.Fatalf("request %d: body is %d bytes, want exactly 1024", i, n)
		}
	}
	if vf.rejected != 0 {
		t.Fatalf("the VAD engine rejected %d non-1024-byte bodies", vf.rejected)
	}
	if vf.deletes == 0 {
		t.Fatal("DELETE /vad/{id} was never sent on close")
	}
}

// --- (h) times follow the RTP timestamps (C5 §3), not the sample count ---

// The expected values come from the task (C5 §3): a 2 s gap where no
// packets are sent but the RTP timestamp advances by 2 s puts the
// second utterance 3 s after the first. Times placed by emitted sample
// count (an earlier version) put it only ~1 s after, so this fails there.
func TestRTPTimeWithPacketGap(t *testing.T) {
	for _, tc := range []struct {
		name string
		pt   byte
	}{
		{"pcmu_8k", 0},
		{"l16_48k", 96},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			id, addr := h.open(t)
			s := newSender(t, addr, 0xA11CE)
			s.tone(tc.pt, 8000, 0, 1.0)   // 1 s of speech
			s.gap(tc.pt, 2.0)             // 2 s pass: RTP timestamp advances, no packets
			s.tone(tc.pt, 8000, 3.0, 1.0) // 1 s of speech
			s.silence(tc.pt, 4.0, 0.8)    // silence so the utterances close (RTP time continues)
			started := h.waitType(t, "speechrecog.start-of-input", 2, 10*time.Second)
			h.waitType(t, "speechrecog.recognition-complete", 2, 10*time.Second)
			if err := h.svc.Close(id); err != nil {
				t.Fatal(err)
			}
			if len(started) != 2 {
				t.Fatalf("started events = %d, want 2", len(started))
			}
			startedAt := func(ev Event) time.Time {
				v, ok := ev.Data["started_at"].(string)
				if !ok {
					t.Fatalf("no started_at in %s: %v", ev.Type, ev.Data)
				}
				ts, err := time.Parse(time.RFC3339Nano, v)
				if err != nil {
					t.Fatalf("bad started_at %q: %v", v, err)
				}
				return ts
			}
			delta := startedAt(started[1]).Sub(startedAt(started[0]))
			// Tolerance ±60 ms: the task's stated allowance, the
			// detector's onset granularity — each started_at may land up
			// to one VAD window (32 ms) after its utterance's true
			// onset, independently for the two utterances (≤64 ms), plus
			// the resampler's alignment of 16 kHz samples to packet
			// boundaries (≤16 ms). 60 ms covers the onset-window part
			// with margin and is far below the 2 s under test.
			const tol = 60 * time.Millisecond
			if d := absDur(delta - 3*time.Second); d > tol {
				t.Fatalf("second utterance started %v after the first, want 3s ± %v (RTP time of the gap)", delta, tol)
			}
		})
	}
}

// --- (i) an unpaced burst is timed by its RTP timestamps ---

// 2 s of packets with consecutive timestamps, sent back to back with
// no pacing: the utterance's duration is the 2 s of the RTP clock, not
// the few milliseconds the send took.
func TestUnpacedBurstDuration(t *testing.T) {
	h := newHarness(t, nil)
	id, addr := h.open(t)
	s := newSender(t, addr, 0xA11CE)
	s.tone(0, 8000, 0, 2.0) // one burst of 100 back-to-back packets
	s.silence(0, 2.0, 0.8)
	h.waitType(t, "speechrecog.start-of-input", 1, 5*time.Second)
	heard := h.waitType(t, "speechrecog.recognition-complete", 1, 10*time.Second)
	if err := h.svc.Close(id); err != nil {
		t.Fatal(err)
	}
	parse := func(k string) time.Time {
		v, ok := heard[0].Data[k].(string)
		if !ok {
			t.Fatalf("no %s in heard: %v", k, heard[0].Data)
		}
		ts, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			t.Fatalf("bad %s %q: %v", k, v, err)
		}
		return ts
	}
	dur := parse("ended_at").Sub(parse("started_at"))
	// Tolerance: the utterance boundaries sit on VAD windows (32 ms),
	// so either end may be off by up to one window plus the
	// resampler's sample alignment. 60 ms covers it.
	const tol = 60 * time.Millisecond
	if d := absDur(dur - 2*time.Second); d > tol {
		t.Fatalf("burst duration = %v, want 2s ± %v (RTP time of the burst)", dur, tol)
	}
}

// --- (j) a timestamp wrap past 2^32 keeps the times increasing ---

func TestRTPTimestampWrap(t *testing.T) {
	h := newHarness(t, nil)
	id, addr := h.open(t)
	s := newSender(t, addr, 0xA11CE)
	// 8 kHz: the next 8000 timestamp ticks cross 2^32 mid-utterance.
	s.ts = 0xFFFFFFFF - 3200
	s.tone(0, 8000, 0, 1.0)
	s.silence(0, 1.0, 0.8)
	started := h.waitType(t, "speechrecog.start-of-input", 1, 5*time.Second)
	heard := h.waitType(t, "speechrecog.recognition-complete", 1, 10*time.Second)
	if err := h.svc.Close(id); err != nil {
		t.Fatal(err)
	}
	parse := func(k string) time.Time {
		for _, ev := range []Event{started[0], heard[0]} {
			if v, ok := ev.Data[k].(string); ok {
				ts, err := time.Parse(time.RFC3339Nano, v)
				if err != nil {
					t.Fatalf("bad %s %q: %v", k, v, err)
				}
				return ts
			}
		}
		t.Fatalf("no %s in started/heard", k)
		return time.Time{}
	}
	startAt, endAt := parse("started_at"), parse("ended_at")
	if !endAt.After(startAt) {
		t.Fatalf("times went backwards across the 2^32 wrap: started_at=%v ended_at=%v", startAt, endAt)
	}
	const tol = 60 * time.Millisecond
	if d := absDur(endAt.Sub(startAt) - time.Second); d > tol {
		t.Fatalf("duration across the wrap = %v, want 1s ± %v", endAt.Sub(startAt), tol)
	}
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func sin2pi(x float64) float64 {
	return math.Sin(2 * math.Pi * x)
}

// The session outlives the call that opened it: an MCP tool call's context is
// cancelled when open returns, and a session must keep listening, recognising
// and reporting until close or idle (C5 §2). Found live in the parrot probe:
// every STT request and event publish failed with "context canceled".
func TestSessionOutlivesOpenCallContext(t *testing.T) {
	h := newHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	_, addr, err := h.svc.Open(ctx, nil, "", "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cancel() // what the MCP server does once open has answered
	s := newSender(t, addr, 0xC0FFEE)
	s.tone(0, 8000, 0, 1.0)
	s.silence(0, 1.0, 1.0)
	heard := h.waitType(t, "speechrecog.recognition-complete", 1, 5*time.Second)
	if heard[0].Data["completion_cause"] != "success" || heard[0].Data["text"] != "hello" {
		t.Fatalf("heard = %v, want success, text hello", heard[0].Data)
	}
}

// A pause shorter than HEARING_MERGE_WINDOW does not end an utterance (C5 §3,
// Merge) — before the onset as well as after it. Found in the parrot probe:
// one quiet 32 ms window between "The" and "quick" (or one window the VAD
// missed) threw the candidate away with its audio, and the first words never
// reached STT.
func TestPauseBeforeOnsetKeepsTheStart(t *testing.T) {
	h := newHarness(t, nil)
	_, addr := h.open(t)
	s := newSender(t, addr, 0xBEEF)
	s.tone(0, 8000, 0, 0.120)   // "The": shorter than the onset
	s.silence(0, 0.120, 0.064)  // a gap of two windows
	s.tone(0, 8000, 0.184, 1.0) // "quick brown fox…"
	s.silence(0, 1.184, 1.0)
	burstStart := s.firstAt

	started := h.waitType(t, "speechrecog.start-of-input", 1, 5*time.Second)
	heard := h.waitType(t, "speechrecog.recognition-complete", 1, 5*time.Second)
	startedAt, err := time.Parse(time.RFC3339Nano, started[0].Data["started_at"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if d := absDur(startedAt.Sub(burstStart)); d > 40*time.Millisecond {
		t.Fatalf("started_at %v after the first burst, want within 40 ms (the first word was dropped)", startedAt.Sub(burstStart))
	}
	if heard[0].Data["started_at"] != started[0].Data["started_at"] {
		t.Fatalf("heard started_at %v != started %v", heard[0].Data["started_at"], started[0].Data["started_at"])
	}
	// What went to STT covers both bursts and the gap: 1.184 s of 16 kHz
	// int16 is 37 888 bytes of samples, plus a WAV header.
	h.stt.mu.Lock()
	got := h.stt.wavLens[len(h.stt.wavLens)-1]
	h.stt.mu.Unlock()
	if want := int(1.184*16000*2) - 2*512*2; got < want {
		t.Fatalf("STT got %d bytes, want at least %d (both bursts)", got, want)
	}
}

// --- transcription runs during the merge window, not after it ---

func TestHeardDoesNotWaitMergeWindowThenSTT(t *testing.T) {
	const merge, sttDelay = time.Second, 800 * time.Millisecond
	h := newHarness(t, func(c *Config) { c.MergeWindow = merge })
	h.stt.delay = sttDelay
	id, addr := h.open(t)
	s := newSender(t, addr, 0xA11CE)
	s.tone(0, 8000, 0, 1.0)
	speechEnd := time.Now()
	arrived := make(chan time.Duration, 1)
	go func() {
		for time.Since(speechEnd) < 5*time.Second {
			if len(h.events("speechrecog.recognition-complete")) > 0 {
				arrived <- time.Since(speechEnd)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		close(arrived)
	}()
	// The pause in real time: the windows after the speech are what start
	// the guess and close the utterance.
	for i := 0; i < 125; i++ {
		s.send(0, bytes.Repeat([]byte{0xFF}, 160), 160)
		time.Sleep(20 * time.Millisecond)
	}
	got, ok := <-arrived
	if !ok {
		t.Fatal("no utterance.heard")
	}
	// Serial would be merge + sttDelay = 1.8 s after the speech ended;
	// with the transcription started in the pause it is about the merge
	// window itself.
	if got > 1500*time.Millisecond {
		t.Fatalf("heard %v after the speech ended; the transcription waited for the merge window", got)
	}
	_ = h.svc.Close(id)
}

func TestSpeechAfterThePauseVoidsTheGuess(t *testing.T) {
	h := newHarness(t, nil)
	h.stt.byLen = true
	h.stt.delay = 100 * time.Millisecond
	id, addr := h.open(t)
	s := newSender(t, addr, 0xA11CE)
	// 300 ms is past the pause that starts a guess and inside the merge window.
	s.tone(0, 8000, 0, 1.0)
	s.silence(0, 1.0, 0.3)
	s.tone(0, 8000, 1.3, 1.0)
	s.silence(0, 2.3, 1.0)
	heard := h.waitType(t, "speechrecog.recognition-complete", 1, 5*time.Second)
	h.waitNoType(t, "speechrecog.recognition-complete", 300*time.Millisecond)
	var n int
	if _, err := fmt.Sscanf(heard[0].Data["text"].(string), "len=%d", &n); err != nil {
		t.Fatalf("text %q: %v", heard[0].Data["text"], err)
	}
	// 2 s of speech at 16 kHz, 16 bit: the guess of the first second alone is 32000.
	if n < 64000 {
		t.Fatalf("heard the transcription of %d WAV bytes, want the whole 2 s utterance", n)
	}
	_ = h.svc.Close(id)
}
