package rtpin

import (
	"testing"
	"time"

	rtp "github.com/pion/rtp"
)

func newTestIn() *In {
	return &In{pending: map[int]*pend{}, lastSrcCount: 160, closed: make(chan struct{})}
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
	if len(in.pending) != 0 {
		t.Fatalf("%d packets stuck in the jitter buffer", len(in.pending))
	}
	if in.nextOffset != packets {
		t.Fatalf("nextOffset = %d, want %d", in.nextOffset, packets)
	}
	want := packets * 320 // 8 kHz → 16 kHz
	if samples < want-320 || samples > want {
		t.Fatalf("%d samples out, want about %d", samples, want)
	}
	span := last.Sub(in.firstWall)
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
	if len(in.pending) != 0 || in.nextOffset != 5 {
		t.Fatalf("pending %d, nextOffset %d; want 0 and 5", len(in.pending), in.nextOffset)
	}
}
