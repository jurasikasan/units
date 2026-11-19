// Package audio carries a call's sound between OpenAI Realtime's shape and
// the organs': PCM16 little-endian at 24 kHz, base64 in JSON, on the
// WebSocket; L16 (network byte order) at 48 kHz in RTP, payload type 96, 20 ms
// packets, toward hearing and from speaking.
package audio

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"sync"
)

const (
	// Rate is the Realtime schema's PCM16 rate.
	Rate = 24000
	// LineRate is the organs' L16 rate.
	LineRate = 48000
	// Frame is one RTP packet's samples at LineRate: 20 ms.
	Frame = LineRate / 50
	// PayloadType is L16 48 kHz mono, dynamic (RFC 3551).
	PayloadType = 96
)

// Up turns PCM16 little-endian at 24 kHz into samples at 48 kHz: each
// sample, then the midpoint to the next. last is the sample before pcm (the
// previous chunk's last), so chunks join without a click; the new last is
// returned.
func Up(pcm []byte, last int16) ([]int16, int16) {
	n := len(pcm) / 2
	out := make([]int16, 0, 2*n)
	prev := last
	for i := 0; i < n; i++ {
		s := int16(binary.LittleEndian.Uint16(pcm[2*i:]))
		out = append(out, int16((int32(prev)+int32(s))/2), s)
		prev = s
	}
	return out, prev
}

// Down turns L16 (network byte order) at 48 kHz into PCM16 little-endian at
// 24 kHz: each pair averaged, which is also the low-pass a halving needs.
func Down(l16 []byte) []byte {
	n := len(l16) / 4
	out := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		a := int32(int16(binary.BigEndian.Uint16(l16[4*i:])))
		b := int32(int16(binary.BigEndian.Uint16(l16[4*i+2:])))
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16((a+b)/2)))
	}
	return out
}

// Sender sends samples at 48 kHz to one address as RTP, 20 ms a packet, one
// SSRC, its sequence and timestamp counting from random starts. A partial
// packet waits for the rest.
type Sender struct {
	mu      sync.Mutex
	conn    *net.UDPConn
	ssrc    uint32
	seq     uint16
	ts      uint32
	pending []int16
	last    int16
}

// NewSender sends to hostport (UDP).
func NewSender(hostport string) (*Sender, error) {
	addr, err := net.ResolveUDPAddr("udp", hostport)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, err
	}
	var r [10]byte
	if _, err := rand.Read(r[:]); err != nil {
		return nil, err
	}
	return &Sender{
		conn: conn,
		ssrc: binary.BigEndian.Uint32(r[0:]),
		seq:  binary.BigEndian.Uint16(r[4:]),
		ts:   binary.BigEndian.Uint32(r[6:]),
	}, nil
}

// SendPCM24 takes PCM16 little-endian at 24 kHz, as the Realtime schema
// appends it, and sends every whole packet of it.
func (s *Sender) SendPCM24(pcm []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var up []int16
	up, s.last = Up(pcm, s.last)
	s.pending = append(s.pending, up...)
	pkt := make([]byte, 12+2*Frame)
	for len(s.pending) >= Frame {
		pkt[0] = 0x80
		pkt[1] = PayloadType
		binary.BigEndian.PutUint16(pkt[2:], s.seq)
		binary.BigEndian.PutUint32(pkt[4:], s.ts)
		binary.BigEndian.PutUint32(pkt[8:], s.ssrc)
		for i, v := range s.pending[:Frame] {
			binary.BigEndian.PutUint16(pkt[12+2*i:], uint16(v))
		}
		if _, err := s.conn.Write(pkt); err != nil {
			return err
		}
		s.pending = s.pending[Frame:]
		s.seq++
		s.ts += Frame
	}
	return nil
}

// Close stops sending.
func (s *Sender) Close() error { return s.conn.Close() }

// Payload is an RTP packet's L16 payload, or an error when the packet is not
// one: too short, not version 2, not payload type 96. Header extensions,
// CSRCs and padding are skipped.
func Payload(pkt []byte) ([]byte, error) {
	if len(pkt) < 12 || pkt[0]>>6 != 2 {
		return nil, errors.New("not RTP")
	}
	if pkt[1]&0x7f != PayloadType {
		return nil, errors.New("not L16 48 kHz")
	}
	off := 12 + 4*int(pkt[0]&0x0f)
	if pkt[0]&0x10 != 0 {
		if len(pkt) < off+4 {
			return nil, errors.New("short extension")
		}
		off += 4 + 4*int(binary.BigEndian.Uint16(pkt[off+2:]))
	}
	end := len(pkt)
	if pkt[0]&0x20 != 0 && end > off {
		end -= int(pkt[end-1])
	}
	if off > end {
		return nil, errors.New("short packet")
	}
	return pkt[off:end], nil
}
