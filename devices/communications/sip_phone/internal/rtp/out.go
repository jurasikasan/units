package rtp

import (
	"log"
	"math/rand"
	"net"
	"sync"

	pionrtp "github.com/pion/rtp"
)

// Out is one call's far-end audio egress (CONTRACT.md revision 3, §2.3): it
// re-sends the G.711 the call receives, as it arrives and as the trunk sent
// it — no transcoding — with the call's own sequence, timestamp and SSRC. One
// destination at a time; SetDest moves it, nil stops it.
type Out struct {
	s    *Session
	ssrc uint32
	mu   sync.Mutex
	dst  *net.UDPAddr // nil: stopped
	seq  uint16
	ts   uint32
}

// NewOut gives the session a listener egress with its own SSRC — the one
// SSRC per call the contract asks for.
func (s *Session) NewOut() *Out {
	return &Out{s: s, ssrc: rand.Uint32()} //nolint:gosec
}

// SetDest moves the egress to addr; a nil addr stops it.
func (o *Out) SetDest(addr *net.UDPAddr) {
	o.mu.Lock()
	o.dst = addr
	o.mu.Unlock()
}

// Send emits one received G.711 payload (20 ms, 160 samples) as one RTP
// packet to the current destination; a no-op while stopped. Send errors are
// logged, not returned: one dead listener must not take the call down.
func (o *Out) Send(pt PayloadType, payload []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	dst := o.dst
	if dst == nil {
		return
	}
	pkt := pionrtp.Packet{
		Header: pionrtp.Header{
			Version:        2,
			PayloadType:    uint8(pt),
			SequenceNumber: o.seq,
			Timestamp:      o.ts,
			SSRC:           o.ssrc,
		},
		Payload: payload,
	}
	buf, err := pkt.Marshal()
	if err != nil {
		log.Printf("[rtp] listen: marshal: %v", err)
		return
	}
	if _, err := o.s.conn.WriteToUDP(buf, dst); err != nil {
		log.Printf("[rtp] listen: write to %v: %v", dst, err)
	}
	o.seq++
	o.ts += uint32(SamplesPerFrame)
}
