// Package rtpin receives RTP audio on one UDP port and turns it into a
// time-placed stream of 16 kHz mono samples.
//
// Rules implemented (C5 section 4):
//   - one port per session; the first SSRC seen owns audio_in, packets
//     from any other SSRC are dropped;
//   - PT 0 (PCMU) and PT 8 (PCMA) at 8 kHz are decoded per ITU-T G.711
//     and linearly interpolated to 16 kHz; PT 96 (L16 48 kHz mono,
//     network byte order) is averaged every 3 to 16 kHz;
//   - a sequence-ordered jitter buffer of 60 ms releases packets in
//     order; a sequence gap that stays missing for the buffer window is
//     filled with silence (one lost packet is one silence fill);
//   - sequence numbers are extended past the 16-bit wrap, so a session
//     goes on past 65,536 packets; a packet older than the next expected
//     one (late past the window, or a duplicate) is dropped;
//   - RTP timestamps are extended past the 32-bit wrap (about 24.85 h at
//     48 kHz), the wrap cycle chosen so the RTP time since the previous
//     packet agrees with the arrival clock to within half a wrap; times
//     still come from the timestamps, arrival only picks the cycle.
package rtpin

import (
	"encoding/binary"
	"net"
	"strconv"
	"sync"
	"time"

	rtp "github.com/pion/rtp"

	"github.com/jurasikasan/units/senses/hearing/internal/g711"
	"github.com/jurasikasan/units/senses/hearing/internal/resample"
)

// JitterWindow is how long a missing sequence number is buffered before
// it is filled with silence.
const JitterWindow = 60 * time.Millisecond

// Frame is a burst of 16 kHz samples with the RTP-placed time of its
// first sample.
type Frame struct {
	Pcm       []int16
	FirstTime time.Time
}

// In is one session's audio input socket.
type In struct {
	conn *net.UDPConn
	out  chan Frame

	closeOnce sync.Once
	closed    chan struct{}

	mu           sync.Mutex
	haveOwner    bool
	ownerSSRC    uint32
	ownerPT      byte
	baseSeq      uint16
	nextOffset   int
	pending      map[int]*pend
	resampler    resample.Resampler
	outAcc       []int16
	outAccStart  time.Time
	haveAccStart bool
	rate         int       // the payload type's clock: 8000 (PT 0/8) or 48000 (PT 96)
	firstTS      int64     // the extended RTP timestamp of the first accepted packet
	srcTotal     int64     // source samples placed so far (for lost-packet times)
	firstWall    time.Time // the wall clock, read once, at the first accepted packet
	extTS        int64     // the newest extended RTP timestamp seen
	refWall      time.Time // when the packet that set extTS arrived
	lastPacket   time.Time
	lastSrcCount int

	// now is the arrival clock; nil is time.Now. Tests set it.
	now func() time.Time
}

type pend struct {
	payload []byte
	ts      int64 // the packet's extended RTP timestamp
	t       time.Time
}

func (in *In) clock() time.Time {
	if in.now != nil {
		return in.now()
	}
	return time.Now()
}

// extend places a 32-bit RTP timestamp on the session's 64-bit timeline.
// Of the timestamps that read as ts modulo 2^32, it takes the one nearest
// to where the RTP clock should be by now: the newest timestamp seen, moved
// on by the wall time since its packet arrived. So a reordered packet lands just
// behind, a long session goes on past the wrap, and a silence longer than
// half a wrap (about 12.4 h at 48 kHz) still reads forward. Arrival only
// chooses the cycle; the time comes from ts itself (C5 §3).
func (in *In) extend(ts uint32, now time.Time) int64 {
	// in microseconds, so a silence of years does not overflow the product
	expect := in.extTS + int64(now.Sub(in.refWall)/time.Microsecond)*int64(in.rate)/1e6
	return expect + int64(int32(ts-uint32(expect)))
}

// New listens on host:port for RTP. port 0 lets the kernel pick a
// free port.
func New(host string, port int) (*In, error) {
	addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}
	in := &In{
		conn:         conn,
		out:          make(chan Frame, 64),
		closed:       make(chan struct{}),
		pending:      map[int]*pend{},
		lastSrcCount: 160,
	}
	go in.readLoop()
	return in, nil
}

// Out is the stream of decoded frames. It is closed when Stop is called.
func (in *In) Out() <-chan Frame { return in.out }

// AddrString is "host:port" as bound.
func (in *In) AddrString() string {
	a := in.conn.LocalAddr().(*net.UDPAddr)
	return net.JoinHostPort(in.hostPart(a.IP), strconv.Itoa(a.Port))
}

func (in *In) hostPart(ip net.IP) string {
	if ip.IsUnspecified() {
		return "0.0.0.0"
	}
	return ip.String()
}

// LastPacket is the wall-clock time of the last accepted packet.
func (in *In) LastPacket() time.Time {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.lastPacket
}

// Stop closes the socket and the frame stream.
func (in *In) Stop() {
	in.closeOnce.Do(func() {
		close(in.closed)
		_ = in.conn.Close()
	})
}

func (in *In) readLoop() {
	defer close(in.out)
	buf := make([]byte, 65536)
	for {
		n, _, err := in.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		select {
		case <-in.closed:
			return
		default:
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			continue
		}
		select {
		case <-in.closed:
			return
		default:
		}
		frame, have := in.handle(pkt)
		if have {
			select {
			case in.out <- frame:
			case <-in.closed:
				return
			}
		}
	}
}

func (in *In) handle(pkt *rtp.Packet) (Frame, bool) {
	in.mu.Lock()
	defer in.mu.Unlock()

	if !in.haveOwner {
		if pkt.SSRC == 0 {
			return Frame{}, false
		}
		var rate int
		switch pkt.PayloadType {
		case 0, 8:
			rate = 8000
		case 96:
			rate = 48000
		default:
			return Frame{}, false
		}
		in.haveOwner = true
		in.ownerSSRC = pkt.SSRC
		in.ownerPT = pkt.PayloadType
		in.rate = rate
		// C5 §3: times are placed by the RTP timestamps. The wall clock is
		// read exactly once, here, at the owner's first packet; from then on
		// packet times derive from pkt.Timestamp and arrival plays no part.
		now := in.clock()
		in.firstWall = now
		in.firstTS = int64(pkt.Timestamp)
		in.extTS, in.refWall = int64(pkt.Timestamp), now
		in.baseSeq = pkt.SequenceNumber
		in.resampler.SetRate(rate)
	} else if pkt.SSRC != in.ownerSSRC || pkt.PayloadType != in.ownerPT {
		return Frame{}, false // a second speaker / another codec is not us
	}

	// The offset is extended past the 16-bit wrap (RFC 3550 A.1): it is
	// read as the signed distance from the next expected sequence number,
	// so a session longer than 65,536 packets keeps its order.
	expected := in.baseSeq + uint16(in.nextOffset)
	offset := in.nextOffset + int(int16(pkt.SequenceNumber-expected))
	if offset < in.nextOffset {
		return Frame{}, false // late past its window, or a duplicate
	}

	now := in.clock()
	ts := in.extend(pkt.Timestamp, now)
	if ts > in.extTS {
		in.extTS, in.refWall = ts, now
	}
	in.lastPacket = now

	if offset == in.nextOffset {
		in.pushPayload(pkt.Payload, ts)
		in.nextOffset++
		in.drainPending()
	} else if _, exists := in.pending[offset]; !exists {
		in.pending[offset] = &pend{payload: pkt.Payload, ts: ts, t: now}
		in.flushStale()
	}

	if len(in.outAcc) == 0 {
		return Frame{}, false
	}
	frame := Frame{Pcm: in.outAcc, FirstTime: in.outAccStart}
	in.outAcc = nil
	in.haveAccStart = false
	return frame, true
}

func (in *In) drainPending() {
	for {
		p, ok := in.pending[in.nextOffset]
		if !ok {
			return
		}
		delete(in.pending, in.nextOffset)
		in.pushPayload(p.payload, p.ts)
		in.nextOffset++
	}
}

func (in *In) flushStale() {
	minOff := -1
	var minP *pend
	for off, p := range in.pending {
		if minOff == -1 || off < minOff {
			minOff = off
			minP = p
		}
	}
	if minOff < 0 || in.clock().Sub(minP.t) < JitterWindow {
		return
	}
	for in.nextOffset < minOff {
		in.pushSilence()
		in.nextOffset++
	}
	delete(in.pending, minOff)
	in.pushPayload(minP.payload, minP.ts)
	in.nextOffset++
}

func (in *In) pushPayload(payload []byte, ts int64) {
	var src []int16
	switch in.ownerPT {
	case 0:
		src = make([]int16, len(payload))
		for i, b := range payload {
			src[i] = g711.Ulaw(b)
		}
	case 8:
		src = make([]int16, len(payload))
		for i, b := range payload {
			src[i] = g711.Alaw(b)
		}
	case 96:
		src = make([]int16, len(payload)/2)
		for i := range src {
			src[i] = int16(binary.BigEndian.Uint16(payload[2*i:]))
		}
	}
	in.lastSrcCount = len(src)
	in.pushSamples(src, ts)
}

// pushSilence fills one lost packet with silence (size of a normal
// packet for this codec). The lost packet's own RTP timestamp is
// unknown, so it is estimated as the sender's clock continuing from
// the last known stream position.
func (in *In) pushSilence() {
	src := make([]int16, in.lastSrcCount)
	in.pushSamples(src, in.firstTS+in.srcTotal)
}

func (in *In) pushSamples(src []int16, ts int64) {
	if !in.haveAccStart {
		// C5 §3: "placed by the RTP timestamps of the packets". The
		// time of this sample is firstWall + (pkt.Timestamp -
		// firstTimestamp)/rate, where rate is the payload type's clock
		// (PT 0/8: 8000, PT 96: 48000), on timestamps extended past the
		// 32-bit wrap (extend), so a session longer than a wrap keeps its
		// times increasing. Packet gaps (a sender suppressing silence) land
		// where the RTP clock says; arrival only chooses the wrap cycle.
		diff := ts - in.firstTS
		in.outAccStart = in.firstWall.Add(ticks(diff, in.rate))
		in.haveAccStart = true
	}
	out := in.resampler.Push(src)
	in.srcTotal += int64(len(src))
	if len(out) == 0 {
		return
	}
	in.outAcc = append(in.outAcc, out...)
}

// ticks is n samples of a rate-Hz clock as a duration, whole seconds apart
// so that a session of days does not overflow the nanoseconds.
func ticks(n int64, rate int) time.Duration {
	r := int64(rate)
	return time.Duration(n/r)*time.Second + time.Duration(n%r)*time.Second/time.Duration(r)
}
