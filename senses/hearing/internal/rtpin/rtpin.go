// Package rtpin receives RTP audio on one UDP port and turns it into
// time-placed streams of 16 kHz mono samples, one per sender.
//
// Rules implemented (C5 section 4):
//   - one port per session, several senders on it: each SSRC is a stream
//     of its own, with its own payload type, jitter buffer and time
//     placement, up to a cap of streams at once; a packet of a new SSRC
//     over the cap, and a packet of a known SSRC with another payload
//     type than its first, are dropped;
//   - PT 0 (PCMU) and PT 8 (PCMA) at 8 kHz are decoded per ITU-T G.711
//     and linearly interpolated to 16 kHz; PT 96 (L16 48 kHz mono,
//     network byte order) is averaged every 3 to 16 kHz;
//   - a sequence-ordered jitter buffer of 60 ms releases packets in
//     order; a sequence gap that stays missing for the buffer window is
//     filled with silence (one lost packet is one silence fill);
//   - sequence numbers are extended past the 16-bit wrap, so a stream
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

// Frame is a burst of 16 kHz samples of one stream, with the RTP-placed
// time of its first sample.
type Frame struct {
	Source    uint32 // the stream's SSRC
	Pcm       []int16
	FirstTime time.Time
}

// In is one session's audio input socket.
type In struct {
	conn *net.UDPConn
	out  chan Frame

	closeOnce sync.Once
	closed    chan struct{}

	// maxSources caps the streams at once; 0 is no cap.
	maxSources int
	// now is the arrival clock; nil is time.Now. Tests set it.
	now func() time.Time

	mu         sync.Mutex
	streams    map[uint32]*stream
	lastPacket time.Time // of any stream
}

// stream is one sender's audio on the port: one SSRC.
type stream struct {
	ssrc         uint32
	pt           byte
	rate         int // the payload type's clock: 8000 (PT 0/8) or 48000 (PT 96)
	baseSeq      uint16
	nextOffset   int
	pending      map[int]*pend
	resampler    resample.Resampler
	outAcc       []int16
	outAccStart  time.Time
	haveAccStart bool
	firstTS      int64     // the extended RTP timestamp of the first accepted packet
	srcTotal     int64     // source samples placed so far (for lost-packet times)
	firstWall    time.Time // the wall clock, read once, at the stream's first packet
	extTS        int64     // the newest extended RTP timestamp seen
	refWall      time.Time // when the packet that set extTS arrived
	lastPacket   time.Time
	lastSrcCount int
}

type pend struct {
	payload []byte
	ts      int64 // the packet's extended RTP timestamp
	t       time.Time
}

// New listens on host:port for RTP. port 0 lets the kernel pick a
// free port. maxSources caps the senders heard at once; 0 is no cap.
func New(host string, port, maxSources int) (*In, error) {
	addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}
	in := &In{
		conn:       conn,
		out:        make(chan Frame, 64),
		closed:     make(chan struct{}),
		maxSources: maxSources,
		streams:    map[uint32]*stream{},
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

func (in *In) clock() time.Time {
	if in.now != nil {
		return in.now()
	}
	return time.Now()
}

// LastPacket is the wall-clock time of the last accepted packet of any
// stream.
func (in *In) LastPacket() time.Time {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.lastPacket
}

// Sources is the streams heard now, each with the wall-clock time of its
// last accepted packet.
func (in *In) Sources() map[uint32]time.Time {
	in.mu.Lock()
	defer in.mu.Unlock()
	out := make(map[uint32]time.Time, len(in.streams))
	for ssrc, st := range in.streams {
		out[ssrc] = st.lastPacket
	}
	return out
}

// DropIdle forgets the streams with no packet since before, and says which:
// their places are free for new senders. A forgotten SSRC that sends again
// is a new stream.
func (in *In) DropIdle(before time.Time) []uint32 {
	in.mu.Lock()
	defer in.mu.Unlock()
	var gone []uint32
	for ssrc, st := range in.streams {
		if st.lastPacket.Before(before) {
			delete(in.streams, ssrc)
			gone = append(gone, ssrc)
		}
	}
	return gone
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

	now := in.clock()
	st, known := in.streams[pkt.SSRC]
	if !known {
		if pkt.SSRC == 0 {
			return Frame{}, false
		}
		if in.maxSources > 0 && len(in.streams) >= in.maxSources {
			return Frame{}, false // the ear is full: a new sender waits for a place
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
		// C5 §3: times are placed by the RTP timestamps. The wall clock is
		// read exactly once, here, at the stream's first packet; from then
		// on packet times derive from pkt.Timestamp, arrival only choosing
		// the wrap cycle (extend).
		st = &stream{
			ssrc:         pkt.SSRC,
			pt:           pkt.PayloadType,
			rate:         rate,
			baseSeq:      pkt.SequenceNumber,
			pending:      map[int]*pend{},
			firstWall:    now,
			firstTS:      int64(pkt.Timestamp),
			extTS:        int64(pkt.Timestamp),
			refWall:      now,
			lastSrcCount: 160,
		}
		st.resampler.SetRate(rate)
		in.streams[pkt.SSRC] = st
	} else if pkt.PayloadType != st.pt {
		return Frame{}, false // one sender changing codec mid-stream is not followed
	}

	// The offset is extended past the 16-bit wrap (RFC 3550 A.1): it is
	// read as the signed distance from the next expected sequence number,
	// so a stream longer than 65,536 packets keeps its order.
	expected := st.baseSeq + uint16(st.nextOffset)
	offset := st.nextOffset + int(int16(pkt.SequenceNumber-expected))
	if offset < st.nextOffset {
		return Frame{}, false // late past its window, or a duplicate
	}

	ts := st.extend(pkt.Timestamp, now)
	if ts > st.extTS {
		st.extTS, st.refWall = ts, now
	}
	st.lastPacket = now
	in.lastPacket = now

	if offset == st.nextOffset {
		st.pushPayload(pkt.Payload, ts)
		st.nextOffset++
		st.drainPending()
	} else if _, exists := st.pending[offset]; !exists {
		st.pending[offset] = &pend{payload: pkt.Payload, ts: ts, t: now}
		st.flushStale(now)
	}

	if len(st.outAcc) == 0 {
		return Frame{}, false
	}
	frame := Frame{Source: st.ssrc, Pcm: st.outAcc, FirstTime: st.outAccStart}
	st.outAcc = nil
	st.haveAccStart = false
	return frame, true
}

// extend places a 32-bit RTP timestamp on the stream's 64-bit timeline.
// Of the timestamps that read as ts modulo 2^32, it takes the one nearest
// to where the RTP clock should be by now: the newest timestamp seen, moved
// on by the wall time since its packet arrived. So a reordered packet lands
// just behind, a long session goes on past the wrap, and a silence longer
// than half a wrap (about 12.4 h at 48 kHz) still reads forward. Arrival
// only chooses the cycle; the time comes from ts itself (C5 §3).
func (st *stream) extend(ts uint32, now time.Time) int64 {
	// in microseconds, so a silence of years does not overflow the product
	expect := st.extTS + int64(now.Sub(st.refWall)/time.Microsecond)*int64(st.rate)/1e6
	return expect + int64(int32(ts-uint32(expect)))
}

func (st *stream) drainPending() {
	for {
		p, ok := st.pending[st.nextOffset]
		if !ok {
			return
		}
		delete(st.pending, st.nextOffset)
		st.pushPayload(p.payload, p.ts)
		st.nextOffset++
	}
}

func (st *stream) flushStale(now time.Time) {
	minOff := -1
	var minP *pend
	for off, p := range st.pending {
		if minOff == -1 || off < minOff {
			minOff = off
			minP = p
		}
	}
	if minOff < 0 || now.Sub(minP.t) < JitterWindow {
		return
	}
	for st.nextOffset < minOff {
		st.pushSilence()
		st.nextOffset++
	}
	delete(st.pending, minOff)
	st.pushPayload(minP.payload, minP.ts)
	st.nextOffset++
}

func (st *stream) pushPayload(payload []byte, ts int64) {
	var src []int16
	switch st.pt {
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
	st.lastSrcCount = len(src)
	st.pushSamples(src, ts)
}

// pushSilence fills one lost packet with silence (size of a normal
// packet for this codec). The lost packet's own RTP timestamp is
// unknown, so it is estimated as the sender's clock continuing from
// the last known stream position.
func (st *stream) pushSilence() {
	src := make([]int16, st.lastSrcCount)
	st.pushSamples(src, st.firstTS+st.srcTotal)
}

func (st *stream) pushSamples(src []int16, ts int64) {
	if !st.haveAccStart {
		// C5 §3: "placed by the RTP timestamps of the packets". The
		// time of this sample is firstWall + (pkt.Timestamp -
		// firstTimestamp)/rate, where rate is the payload type's clock
		// (PT 0/8: 8000, PT 96: 48000), on timestamps extended past the
		// 32-bit wrap (extend), so a session longer than a wrap keeps its
		// times increasing. Packet gaps (a sender suppressing silence) land
		// where the RTP clock says; arrival only chooses the wrap cycle.
		st.outAccStart = st.firstWall.Add(ticks(ts-st.firstTS, st.rate))
		st.haveAccStart = true
	}
	out := st.resampler.Push(src)
	st.srcTotal += int64(len(src))
	if len(out) == 0 {
		return
	}
	st.outAcc = append(st.outAcc, out...)
}

// ticks is n samples of a rate-Hz clock as a duration, whole seconds apart
// so that a session of days does not overflow the nanoseconds.
func ticks(n int64, rate int) time.Duration {
	r := int64(rate)
	return time.Duration(n/r)*time.Second + time.Duration(n%r)*time.Second/time.Duration(r)
}
