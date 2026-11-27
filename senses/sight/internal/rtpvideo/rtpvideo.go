// Package rtpvideo receives RTP video (H.264, RFC 6184) on one UDP port and
// its RTCP on the next, and turns it into access units placed on the
// stream's own timeline.
//
// Rules (CONTRACT §4.1), hearing's where hearing has one:
//   - one port pair per session: RTP on an even port, RTCP on the one above
//     it (RFC 3550 §11), or multiplexed on the RTP port (RFC 5761);
//   - the first packet with a dynamic payload type (96–127) and a non-zero
//     SSRC owns the port; packets of any other SSRC or payload type are
//     dropped, and so are sender reports of another SSRC;
//   - a sequence-ordered jitter buffer releases packets in order; a gap that
//     stays missing for JitterWindow is a loss, and the access unit it
//     breaks is marked Broken;
//   - times come from the RTP timestamps at 90 kHz: on the wall clock of the
//     sender's latest RTCP sender report when one has come, else on the
//     arrival of the first packet, read once.
package rtpvideo

import (
	"encoding/binary"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	rtp "github.com/pion/rtp"
)

// JitterWindow is how long a missing sequence number is waited for before
// it counts as lost.
const JitterWindow = 100 * time.Millisecond

// ClockRate is H.264's RTP clock (RFC 6184 §8.2.1).
const ClockRate = 90000

// Clock names what a time was placed by.
const (
	ClockRTCP        = "rtcp-sr"      // the sender's RTCP sender report: its wall clock
	ClockFirstPacket = "first-packet" // the arrival of the stream's first packet
)

// In is one session's video input: an RTP socket and an RTCP socket.
type In struct {
	rtp  *net.UDPConn
	rtcp *net.UDPConn
	out  chan AccessUnit

	closeOnce sync.Once
	wg        sync.WaitGroup

	mu         sync.Mutex
	owner      bool
	ssrc       uint32
	pt         uint8
	highSeq    int64 // the highest extended sequence number seen
	nextSeq    int64 // the next one to release
	pending    map[int64]pend
	refTS      uint32 // the RTP timestamp extTS was last moved forward by
	extTS      int64
	firstWall  time.Time // the wall clock at the owner's first packet, read once
	firstTS    int64
	reports    map[uint32]report // the latest sender report of each SSRC
	lastPacket time.Time
	asm        assembler
}

type pend struct {
	pkt     *rtp.Packet
	ts      int64
	arrived time.Time
}

// report is an RTCP sender report: the sender's wall clock at one RTP
// timestamp (RFC 3550 §6.4.1).
type report struct {
	wall time.Time
	ts   uint32
}

// Listen takes the RTP port and the RTCP port above it on host (all
// interfaces when empty). port 0 lets the kernel pick an RTP port, and the
// one above it is tried; Listen fails when that one is taken.
func Listen(host string, port int) (*In, error) {
	rtpConn, err := listen(host, port)
	if err != nil {
		return nil, err
	}
	p := rtpConn.LocalAddr().(*net.UDPAddr).Port
	rtcpConn, err := listen(host, p+1)
	if err != nil {
		_ = rtpConn.Close()
		return nil, err
	}
	// A burst of a key frame is many packets at once.
	_ = rtpConn.SetReadBuffer(4 << 20)
	in := &In{
		rtp:     rtpConn,
		rtcp:    rtcpConn,
		out:     make(chan AccessUnit, 64),
		pending: map[int64]pend{},
		reports: map[uint32]report{},
	}
	in.wg.Add(2)
	go in.readRTP()
	go in.readRTCP()
	go func() {
		in.wg.Wait()
		in.mu.Lock()
		aus := in.drain()
		in.mu.Unlock()
		for _, au := range aus {
			in.out <- au
		}
		close(in.out)
	}()
	return in, nil
}

func listen(host string, port int) (*net.UDPConn, error) {
	addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	return net.ListenUDP("udp", addr)
}

// Port is the RTP port as bound; RTCP is on the one above it.
func (in *In) Port() int { return in.rtp.LocalAddr().(*net.UDPAddr).Port }

// Out is the stream of access units in sequence order. It is closed after
// Stop, once what was buffered has been released.
func (in *In) Out() <-chan AccessUnit { return in.out }

// LastPacket is the wall-clock time of the last packet the owner sent; zero
// before the first.
func (in *In) LastPacket() time.Time {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.lastPacket
}

// Stop closes both sockets; Out closes once what was buffered is out.
func (in *In) Stop() {
	in.closeOnce.Do(func() {
		_ = in.rtp.Close()
		_ = in.rtcp.Close()
	})
}

// At places an access unit's timestamp on the wall clock: by the owner's
// latest sender report when one has come, else by the first packet's
// arrival. It says which it was.
func (in *In) At(ts int64) (time.Time, string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if r, ok := in.reports[in.ssrc]; ok && in.owner {
		// The report's timestamp is near the stream's present: placed
		// against the latest one seen, a signed 32-bit difference is exact.
		srTS := in.extTS + int64(int32(r.ts-in.refTS))
		return r.wall.Add(ticks(ts - srTS)), ClockRTCP
	}
	return in.firstWall.Add(ticks(ts - in.firstTS)), ClockFirstPacket
}

func ticks(n int64) time.Duration { return time.Duration(n * int64(time.Second) / ClockRate) }

func (in *In) readRTP() {
	defer in.wg.Done()
	buf := make([]byte, 65536)
	for {
		n, err := in.rtp.Read(buf)
		if err != nil {
			return
		}
		b := buf[:n]
		// RFC 5761 §4: RTCP multiplexed on the RTP port has a packet type
		// of 192–223 where RTP has its marker and payload type.
		if len(b) > 1 && b[1] >= 192 && b[1] <= 223 {
			in.rtcpPacket(b)
			continue
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(b); err != nil {
			continue
		}
		// Unmarshal keeps slices of buf; the packet may wait in the buffer.
		pkt.Payload = append([]byte(nil), pkt.Payload...)
		in.mu.Lock()
		aus := in.handle(pkt, time.Now())
		in.mu.Unlock()
		// The reader of Out reads until it closes, so this does not hang.
		for _, au := range aus {
			in.out <- au
		}
	}
}

func (in *In) readRTCP() {
	defer in.wg.Done()
	buf := make([]byte, 65536)
	for {
		n, err := in.rtcp.Read(buf)
		if err != nil {
			return
		}
		in.rtcpPacket(buf[:n])
	}
}

// rtcpPacket takes the sender reports of a compound RTCP packet.
func (in *In) rtcpPacket(b []byte) {
	for _, r := range senderReports(b) {
		in.mu.Lock()
		in.reports[r.ssrc] = r.report
		in.mu.Unlock()
	}
}

type ssrcReport struct {
	ssrc uint32
	report
}

// ntpEpoch is 1900-01-01 in Unix seconds' terms: NTP time starts there.
const ntpEpoch = 2208988800

// senderReports reads the SRs (PT 200) of a compound RTCP packet
// (RFC 3550 §6.4.1); anything else in it is skipped.
func senderReports(b []byte) []ssrcReport {
	var out []ssrcReport
	for len(b) >= 4 {
		if b[0]>>6 != 2 {
			return out
		}
		size := (int(binary.BigEndian.Uint16(b[2:4])) + 1) * 4
		if size > len(b) {
			return out
		}
		if b[1] == 200 && size >= 28 {
			ntp := binary.BigEndian.Uint64(b[8:16])
			sec, frac := int64(ntp>>32)-ntpEpoch, ntp&0xffffffff
			out = append(out, ssrcReport{
				ssrc: binary.BigEndian.Uint32(b[4:8]),
				report: report{
					wall: time.Unix(sec, int64(frac*uint64(time.Second)>>32)),
					ts:   binary.BigEndian.Uint32(b[16:20]),
				},
			})
		}
		b = b[size:]
	}
	return out
}

// handle takes one RTP packet arrived at now and returns the access units it
// finished. Called with mu held.
func (in *In) handle(pkt *rtp.Packet, now time.Time) []AccessUnit {
	if !in.owner {
		if pkt.SSRC == 0 || pkt.PayloadType < 96 || pkt.PayloadType > 127 {
			return nil
		}
		in.owner, in.ssrc, in.pt = true, pkt.SSRC, pkt.PayloadType
		in.highSeq, in.nextSeq = int64(pkt.SequenceNumber), int64(pkt.SequenceNumber)
		in.refTS, in.extTS = pkt.Timestamp, int64(pkt.Timestamp)
		// The only wall-clock reading of the stream's time without a
		// sender report: from here on, the RTP timestamps place it.
		in.firstWall, in.firstTS = now, int64(pkt.Timestamp)
	} else if pkt.SSRC != in.ssrc || pkt.PayloadType != in.pt {
		return nil // another sender, or another codec, is not this stream
	}
	in.lastPacket = now

	seq := in.highSeq + int64(int16(pkt.SequenceNumber-uint16(in.highSeq)))
	if seq > in.highSeq {
		in.highSeq = seq
	}
	if seq < in.nextSeq {
		return nil // late past its window, or a duplicate
	}
	if _, dup := in.pending[seq]; dup {
		return nil
	}
	ts := in.extTS + int64(int32(pkt.Timestamp-in.refTS))
	if ts > in.extTS {
		in.refTS, in.extTS = pkt.Timestamp, ts
	}
	in.pending[seq] = pend{pkt: pkt, ts: ts, arrived: now}

	aus := in.release()
	// A gap still open after the window is a loss: go on from what came after it.
	for len(in.pending) > 0 {
		low := in.lowest()
		if now.Sub(in.pending[low].arrived) < JitterWindow {
			break
		}
		in.asm.lost()
		in.nextSeq = low
		aus = append(aus, in.release()...)
	}
	return aus
}

// release hands the in-order packets to the assembler.
func (in *In) release() []AccessUnit {
	var aus []AccessUnit
	for {
		p, ok := in.pending[in.nextSeq]
		if !ok {
			return aus
		}
		delete(in.pending, in.nextSeq)
		in.nextSeq++
		aus = append(aus, in.asm.push(p.pkt.Payload, p.ts, p.pkt.Marker)...)
	}
}

func (in *In) lowest() int64 {
	keys := make([]int64, 0, len(in.pending))
	for k := range in.pending {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys[0]
}

// drain releases everything still buffered, gaps counted as losses, and the
// unit being built. Called with mu held, after the sockets are closed.
func (in *In) drain() []AccessUnit {
	var aus []AccessUnit
	for len(in.pending) > 0 {
		if _, ok := in.pending[in.nextSeq]; !ok {
			in.asm.lost()
			in.nextSeq = in.lowest()
		}
		aus = append(aus, in.release()...)
	}
	if in.asm.have {
		aus = append(aus, in.asm.flush())
	}
	return aus
}
