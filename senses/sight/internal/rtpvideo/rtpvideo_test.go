package rtpvideo

import (
	"bytes"
	"encoding/binary"
	"net"
	"strconv"
	"testing"
	"time"

	rtp "github.com/pion/rtp"
)

var (
	sps = []byte{0x67, 0x42, 0xc0, 0x1e, 0xaa}
	pps = []byte{0x68, 0xce, 0x3c, 0x80}
	idr = append([]byte{0x65}, bytes.Repeat([]byte{0x11}, 30)...)
	pfr = []byte{0x41, 0x9a, 0x22}
)

func annexB(nals ...[]byte) []byte {
	var b []byte
	for _, n := range nals {
		b = append(append(b, startCode...), n...)
	}
	return b
}

func stapA(nals ...[]byte) []byte {
	b := []byte{0x18} // F=0, NRI=0, type 24
	for _, n := range nals {
		b = append(b, byte(len(n)>>8), byte(len(n)))
		b = append(b, n...)
	}
	return b
}

// fuA cuts a NAL unit into n FU-A fragments.
func fuA(nal []byte, n int) [][]byte {
	body := nal[1:]
	size := (len(body) + n - 1) / n
	var out [][]byte
	for i := 0; i < n; i++ {
		part := body[i*size : min((i+1)*size, len(body))]
		fu := nal[0]&0x1f | map[bool]byte{true: 0x80}[i == 0] | map[bool]byte{true: 0x40}[i == n-1]
		out = append(out, append([]byte{nal[0]&0xe0 | nalFUA, fu}, part...))
	}
	return out
}

func TestAssemblerSTAPAndFUA(t *testing.T) {
	var a assembler
	var aus []AccessUnit
	frags := fuA(idr, 3)
	aus = append(aus, a.push(stapA(sps, pps), 1000, false)...)
	for i, f := range frags {
		aus = append(aus, a.push(f, 1000, i == len(frags)-1)...)
	}
	aus = append(aus, a.push(pfr, 4000, true)...)
	if len(aus) != 2 {
		t.Fatalf("%d access units", len(aus))
	}
	if !aus[0].Key || aus[0].Broken || aus[0].TS != 1000 || !bytes.Equal(aus[0].Data, annexB(sps, pps, idr)) {
		t.Errorf("key unit: %+v", aus[0])
	}
	if aus[1].Key || aus[1].Broken || !bytes.Equal(aus[1].Data, annexB(pfr)) {
		t.Errorf("p unit: %+v", aus[1])
	}

	// An IDR whose SPS/PPS came in a unit of their own gets them in front.
	aus = a.push(idr, 7000, true)
	if len(aus) != 1 || !aus[0].Key || !bytes.Equal(aus[0].Data, annexB(sps, pps, idr)) {
		t.Errorf("idr alone: %+v", aus)
	}
}

func TestAssemblerLoss(t *testing.T) {
	var a assembler
	frags := fuA(idr, 3)
	a.push(stapA(sps, pps), 1000, false)
	a.push(frags[0], 1000, false)
	a.lost() // frags[1]
	aus := a.push(frags[2], 1000, true)
	if len(aus) != 1 || !aus[0].Broken || aus[0].Key {
		t.Fatalf("a fragment lost: %+v", aus)
	}
	// A whole picture lost between two: the one after is broken too.
	a.lost()
	if aus := a.push(pfr, 4000, true); len(aus) != 1 || !aus[0].Broken {
		t.Errorf("after a lost picture: %+v", aus)
	}
	if aus := a.push(pfr, 7000, true); len(aus) != 1 || aus[0].Broken {
		t.Errorf("the next one is whole: %+v", aus)
	}
	// Interleaved mode is not taken.
	if aus := a.push([]byte{0x1d, 0x80, 1}, 9000, true); len(aus) != 1 || !aus[0].Broken {
		t.Errorf("FU-B: %+v", aus)
	}
}

type sender struct {
	t    *testing.T
	conn net.Conn
	ssrc uint32
	pt   uint8
}

func dial(t *testing.T, port int, ssrc uint32) *sender {
	t.Helper()
	c, err := net.Dial("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &sender{t: t, conn: c, ssrc: ssrc, pt: 96}
}

func (s *sender) send(seq uint16, ts uint32, marker bool, payload []byte) {
	s.t.Helper()
	b, err := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: s.pt, SequenceNumber: seq, Timestamp: ts,
		SSRC: s.ssrc, Marker: marker}, Payload: payload}).Marshal()
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.conn.Write(b); err != nil {
		s.t.Fatal(err)
	}
}

// SR builds an RTCP sender report (RFC 3550 §6.4.1): wall at ts.
func SR(ssrc uint32, wall time.Time, ts uint32) []byte {
	b := make([]byte, 28)
	b[0], b[1] = 0x80, 200
	binary.BigEndian.PutUint16(b[2:], 6)
	binary.BigEndian.PutUint32(b[4:], ssrc)
	sec := uint64(wall.Unix() + ntpEpoch)
	frac := uint64(wall.Nanosecond()) << 32 / uint64(time.Second)
	binary.BigEndian.PutUint64(b[8:], sec<<32|frac)
	binary.BigEndian.PutUint32(b[16:], ts)
	return b
}

func receive(t *testing.T, in *In, n int) []AccessUnit {
	t.Helper()
	var aus []AccessUnit
	deadline := time.After(2 * time.Second)
	for len(aus) < n {
		select {
		case au, ok := <-in.Out():
			if !ok {
				return aus
			}
			aus = append(aus, au)
		case <-deadline:
			t.Fatalf("%d of %d access units", len(aus), n)
		}
	}
	return aus
}

// The first SSRC owns the port; packets out of order are put back in it;
// sequence numbers and timestamps wrap; times come from the RTP clock,
// anchored at the first packet, then at the sender's report.
func TestInOwnsOrdersAndPlaces(t *testing.T) {
	in, err := Listen("127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Stop()
	owner, other := dial(t, in.Port(), 0xabc), dial(t, in.Port(), 0xdef)

	ts0 := uint32(0xffffffff - 4500) // wraps 50 ms in
	before := time.Now()
	owner.send(65534, ts0, true, stapA(sps, pps, idr))
	time.Sleep(20 * time.Millisecond)
	other.send(100, 5, true, idr)          // not the owner
	owner.send(0, ts0+18000, true, pfr)    // seq 0 before 65535: out of order, across the wrap
	owner.send(65535, ts0+9000, true, pfr) // 100 ms
	aus := receive(t, in, 3)
	after := time.Now()

	if !aus[0].Key || aus[1].Key || aus[2].Key {
		t.Errorf("keys: %v %v %v", aus[0].Key, aus[1].Key, aus[2].Key)
	}
	if aus[1].TS-aus[0].TS != 9000 || aus[2].TS-aus[0].TS != 18000 {
		t.Errorf("timestamps %d %d %d", aus[0].TS, aus[1].TS, aus[2].TS)
	}
	at0, clock := in.At(aus[0].TS)
	if clock != ClockFirstPacket || at0.Before(before) || at0.After(after) {
		t.Errorf("first at %s by %s (sent between %s and %s)", at0, clock, before, after)
	}
	if at2, _ := in.At(aus[2].TS); at2.Sub(at0) != 200*time.Millisecond {
		t.Errorf("the third is %s after the first, not by the RTP clock", at2.Sub(at0))
	}

	// The other sender's report is not this stream's; the owner's is.
	rtcp := dial(t, in.Port()+1, 0)
	wall := time.Date(2026, 11, 27, 21, 0, 0, 0, time.UTC)
	_, _ = rtcp.conn.Write(SR(0xdef, wall.Add(time.Hour), 0))
	_, _ = rtcp.conn.Write(SR(0xabc, wall, ts0+9000))
	deadline := time.Now().Add(time.Second)
	for {
		at, clock := in.At(aus[2].TS)
		if clock == ClockRTCP {
			if want := wall.Add(100 * time.Millisecond); !at.Equal(want) {
				t.Errorf("by the report: %s, want %s", at, want)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sender report was not taken")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if lp := in.LastPacket(); lp.Before(before) {
		t.Errorf("last packet %s", lp)
	}
}

// A gap that stays open past the window is a loss: the unit after it is
// broken, and what is buffered comes out on Stop.
func TestInLossAndStop(t *testing.T) {
	in, err := Listen("127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	s := dial(t, in.Port(), 7)
	s.send(10, 0, true, stapA(sps, pps, idr))
	// 11 is lost.
	s.send(12, 6000, true, pfr)
	time.Sleep(JitterWindow + 20*time.Millisecond)
	s.send(13, 9000, true, pfr)
	aus := receive(t, in, 3)
	if aus[0].Broken || !aus[1].Broken || aus[1].TS != 6000 || aus[2].Broken {
		t.Errorf("loss: %+v", aus)
	}
	s.send(15, 15000, true, pfr) // waits for 14
	time.Sleep(20 * time.Millisecond)
	in.Stop()
	rest := receive(t, in, 1)
	if len(rest) != 1 || !rest[0].Broken || rest[0].TS != 15000 {
		t.Errorf("on stop: %+v", rest)
	}
	if _, ok := <-in.Out(); ok {
		t.Error("Out is not closed")
	}
}

// RTCP multiplexed on the RTP port (RFC 5761) is taken as RTCP.
func TestRTCPMux(t *testing.T) {
	in, err := Listen("127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Stop()
	s := dial(t, in.Port(), 9)
	s.send(1, 90000, true, stapA(sps, pps, idr))
	receive(t, in, 1)
	wall := time.Date(2026, 11, 27, 21, 0, 0, 0, time.UTC)
	_, _ = s.conn.Write(SR(9, wall, 0))
	deadline := time.Now().Add(time.Second)
	for {
		if at, clock := in.At(90000); clock == ClockRTCP {
			if !at.Equal(wall.Add(time.Second)) {
				t.Errorf("at %s", at)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a muxed sender report was not taken")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
