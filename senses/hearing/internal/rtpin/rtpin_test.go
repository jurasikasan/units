package rtpin

import (
	"testing"
	"time"

	rtp "github.com/pion/rtp"
)

func newTestIn() *In {
	return &In{streams: map[uint32]*stream{}, closed: make(chan struct{})}
}

func pcmu(seq uint16, ts uint32) *rtp.Packet {
	payload := make([]byte, 160) // 20 ms of PCMU at 8 kHz
	for i := range payload {
		payload[i] = 0xFF // μ-law silence
	}
	return &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: seq, Timestamp: ts, SSRC: 42}, Payload: payload}
}

// UNIT-116: a session fed across the 16-bit sequence wrap, longer than
// 65,536 packets, keeps flowing in order, every packet's audio out once
// and its time placed by the RTP clock.
func TestKeepsHearingPastTheSequenceWrap(t *testing.T) {
	in := newTestIn()
	const packets = 70000 // more than 65,536, about 23 minutes at 20 ms
	baseSeq := uint16(65500)
	baseTS := uint32(4294960000) // the timestamp wraps too

	order := make([]int, packets)
	for i := range order {
		order[i] = i
	}
	// two packets swapped right across the wrap, and again past 65,536 packets
	wrapAt := int(65536 - uint32(baseSeq))
	order[wrapAt-1], order[wrapAt] = order[wrapAt], order[wrapAt-1]
	order[66000], order[66001] = order[66001], order[66000]

	samples := 0
	var last time.Time
	for _, i := range order {
		f, ok := in.handle(pcmu(baseSeq+uint16(i), baseTS+uint32(i)*160))
		if !ok {
			continue
		}
		if !last.IsZero() && !f.FirstTime.After(last) {
			t.Fatalf("frame at %v is not after the previous one at %v", f.FirstTime, last)
		}
		last = f.FirstTime
		samples += len(f.Pcm)
	}
	if len(in.streams[42].pending) != 0 {
		t.Fatalf("%d packets stuck in the jitter buffer", len(in.streams[42].pending))
	}
	if in.streams[42].nextOffset != packets {
		t.Fatalf("nextOffset = %d, want %d", in.streams[42].nextOffset, packets)
	}
	want := packets * 320 // 8 kHz → 16 kHz
	if samples < want-320 || samples > want {
		t.Fatalf("%d samples out, want about %d", samples, want)
	}
	span := last.Sub(in.streams[42].firstWall)
	if wantSpan := time.Duration(packets-1) * 20 * time.Millisecond; span < wantSpan-40*time.Millisecond || span > wantSpan {
		t.Fatalf("last frame %v after the first, want about %v", span, wantSpan)
	}
}

// A duplicate, or a packet older than the next one expected, is dropped
// rather than held in the buffer.
func TestDropsLateAndDuplicatePackets(t *testing.T) {
	in := newTestIn()
	for i := 0; i < 5; i++ {
		in.handle(pcmu(uint16(65534+i), uint32(i)*160))
	}
	if _, ok := in.handle(pcmu(65535, 160)); ok {
		t.Fatal("a duplicate gave audio")
	}
	if len(in.streams[42].pending) != 0 || in.streams[42].nextOffset != 5 {
		t.Fatalf("pending %d, nextOffset %d; want 0 and 5", len(in.streams[42].pending), in.streams[42].nextOffset)
	}
}

// fakeClock is an arrival clock a test moves by hand.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func l16(seq uint16, ts uint32) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts, SSRC: 42},
		Payload: make([]byte, 960*2)} // 20 ms of L16 at 48 kHz
}

// UNIT-137: at 48 kHz the 32-bit RTP timestamp wraps every ≈24.85 h. A
// session that goes on across it keeps its times increasing and placed by
// the RTP clock, past two wraps.
func TestTimesGoOnPastTheTimestampWrap(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)}
	in := newTestIn()
	in.now = clk.now
	start := clk.t
	const step = 960                    // 20 ms at 48 kHz
	const perHour = 3600 * 48000 / step // packets' worth of RTP time in an hour
	baseTS := uint32(1 << 31)           // the first wrap comes ≈12.4 h in
	var seq uint16
	var last time.Time
	var n int64 // RTP time so far, in packets
	// A minute of speech each hour, nothing sent in between (silence
	// suppressed), for 55 hours: the timestamp wraps twice.
	for h := 0; h < 55; h++ {
		for i := 0; i < 3000; i++ {
			f, ok := in.handle(l16(seq, baseTS+uint32(n*step)))
			if !ok {
				t.Fatalf("hour %d packet %d gave no audio", h, i)
			}
			if !last.IsZero() && !f.FirstTime.After(last) {
				t.Fatalf("hour %d: %v is not after %v", h, f.FirstTime, last)
			}
			if want := start.Add(time.Duration(n) * 20 * time.Millisecond); !f.FirstTime.Equal(want) {
				t.Fatalf("hour %d packet %d at %v, want %v by the RTP clock", h, i, f.FirstTime, want)
			}
			last = f.FirstTime
			seq++
			n++
			clk.t = clk.t.Add(20 * time.Millisecond)
		}
		n += perHour - 3000
		clk.t = clk.t.Add(time.Duration(perHour-3000) * 20 * time.Millisecond)
	}
	if span := last.Sub(start); span < 54*time.Hour {
		t.Fatalf("last frame %v after the first, want past 54 h", span)
	}
}

// UNIT-137: a silence longer than half a wrap (≈12.4 h at 48 kHz) reads
// forward: the next packet's time is after the silence, not before it, even
// when the timestamp wraps inside the silence.
func TestASilenceLongerThanHalfAWrapReadsForward(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)}
	in := newTestIn()
	in.now = clk.now
	start := clk.t
	hours := func(h time.Duration) uint32 { return uint32(int64(h/time.Second) * 48000) }
	if _, ok := in.handle(l16(1, 1000)); !ok {
		t.Fatal("first packet gave no audio")
	}
	// a word ten hours in, then a silence of twenty: 30 h in all, past the
	// wrap at ≈24.85 h, and the silence longer than half a wrap
	clk.t = start.Add(10 * time.Hour)
	if f, ok := in.handle(l16(2, 1000+hours(10*time.Hour))); !ok || !f.FirstTime.Equal(start.Add(10*time.Hour)) {
		t.Fatalf("the word ten hours in: %v %v", f.FirstTime, ok)
	}
	silence := 20 * time.Hour
	clk.t = clk.t.Add(silence)
	ts := 1000 + hours(30*time.Hour) // modulo 2^32: it wrapped
	f, ok := in.handle(l16(3, ts))
	if !ok {
		t.Fatal("the packet after the silence gave no audio")
	}
	if want := start.Add(30 * time.Hour); !f.FirstTime.Equal(want) {
		t.Fatalf("after the silence at %v, want %v", f.FirstTime, want)
	}
	// The time is the RTP clock's, not arrival's: the next packet, a second
	// late on the way, is 20 ms on.
	clk.t = clk.t.Add(time.Second)
	f, ok = in.handle(l16(4, ts+960))
	if !ok {
		t.Fatal("the next packet gave no audio")
	}
	if want := start.Add(30*time.Hour + 20*time.Millisecond); !f.FirstTime.Equal(want) {
		t.Fatalf("next packet at %v, want %v", f.FirstTime, want)
	}
}

func from(p *rtp.Packet, ssrc uint32) *rtp.Packet { p.SSRC = ssrc; return p }

// UNIT-121: several senders on one port are streams of their own: each its
// own sequence, codec and RTP clock, each frame saying whose it is.
func TestSendersAreStreamsOfTheirOwn(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)}
	in := newTestIn()
	in.now = clk.now
	start := clk.t
	// A on PCMU from sequence 10, B on L16 from sequence 60000, interleaved
	for i := 0; i < 50; i++ {
		fa, ok := in.handle(from(pcmu(uint16(10+i), 5000+uint32(i)*160), 0xA))
		if !ok || fa.Source != 0xA || !fa.FirstTime.Equal(start.Add(time.Duration(i)*20*time.Millisecond)) {
			t.Fatalf("A packet %d: %+v %v", i, fa.FirstTime, ok)
		}
		fb, ok := in.handle(from(l16(uint16(60000+i), 900000+uint32(i)*960), 0xB))
		if !ok || fb.Source != 0xB || !fb.FirstTime.Equal(start.Add(time.Duration(i)*20*time.Millisecond)) {
			t.Fatalf("B packet %d: %+v %v", i, fb.FirstTime, ok)
		}
		if len(fa.Pcm) < 319 || len(fb.Pcm) != 320 { // the interpolation holds back one sample at first
			t.Fatalf("packet %d: %d and %d samples, want about 320 each", i, len(fa.Pcm), len(fb.Pcm))
		}
		clk.t = clk.t.Add(20 * time.Millisecond)
	}
	// a sender changing its codec mid-stream is not followed
	if _, ok := in.handle(from(l16(60, 5000+50*160), 0xA)); ok {
		t.Fatal("A's packet on another payload type gave audio")
	}
}

// UNIT-121: over the cap, a new sender is not heard until a place frees;
// a sender silent for long leaves, and its place goes to the next.
func TestSendersAreCappedAndIdleOnesLeave(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)}
	in := newTestIn()
	in.now = clk.now
	in.maxSources = 2
	in.handle(from(pcmu(1, 0), 0xA))
	clk.t = clk.t.Add(time.Minute)
	in.handle(from(pcmu(1, 0), 0xB))
	if _, ok := in.handle(from(pcmu(1, 0), 0xC)); ok {
		t.Fatal("a third sender was heard over a cap of two")
	}
	gone := in.DropIdle(clk.t.Add(-30 * time.Second))
	if len(gone) != 1 || gone[0] != 0xA {
		t.Fatalf("dropped %x, want only a", gone)
	}
	if _, ok := in.handle(from(pcmu(1, 0), 0xC)); !ok {
		t.Fatal("the third sender was not heard once a place freed")
	}
	if got := in.Sources(); len(got) != 2 || !got[0xB].Equal(clk.t) || !got[0xC].Equal(clk.t) {
		t.Fatalf("sources %v, want b and c", got)
	}
}
