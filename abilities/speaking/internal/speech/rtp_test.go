package speech

import (
	"net"
	"testing"
	"time"

	"github.com/pion/rtp"
)

// UNIT-130: the RTP timestamp advances by the silence between phrases,
// however long it is: RFC 3550 §5.1, the timestamp reflects the sampling
// instant. A listener places perception time by it.
func TestTimestampCarriesTheSilenceBetweenPhrases(t *testing.T) {
	ln, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	clock := time.Date(2026, 11, 27, 21, 1, 25, 0, time.UTC)
	sink := newRTPSink(7, ln.LocalAddr().(*net.UDPAddr))
	defer sink.Close()
	sink.now = func() time.Time { return clock }

	read := func() uint32 {
		t.Helper()
		buf := make([]byte, 4096)
		_ = ln.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := ln.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		var p rtp.Packet
		if err := p.Unmarshal(buf[:n]); err != nil {
			t.Fatal(err)
		}
		return p.Timestamp
	}
	frame := make([]int16, frameSamples)

	for _, gap := range []time.Duration{30 * time.Second, 95 * time.Second, 25 * time.Hour} {
		if err := sink.WriteFrame(frame); err != nil {
			t.Fatal(err)
		}
		before := read()
		clock = clock.Add(gap)
		if err := sink.WriteFrame(frame); err != nil {
			t.Fatal(err)
		}
		after := read()
		got := time.Duration(after-before) * time.Second / sampleRate // modulo 2^32, as a listener reads it
		want := gap % (time.Duration(1<<32) * time.Second / sampleRate)
		if d := got - want; d < -20*time.Millisecond || d > 20*time.Millisecond {
			t.Errorf("a %v silence moved the timestamp by %v, want %v", gap, got, want)
		}
		clock = clock.Add(20 * time.Millisecond)
	}
}
