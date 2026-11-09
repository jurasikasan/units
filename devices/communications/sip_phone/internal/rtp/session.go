// Package rtp is plain RTP/G.711 media for one call: one UDP socket per call,
// PCMU or PCMA as agreed in SDP, symmetric (latches onto where packets come
// from).
package rtp

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"

	pionrtp "github.com/pion/rtp"
)

const (
	sampleRate8kHz = 8000
	frameMs        = 20
	// SamplesPerFrame is the number of 8kHz samples in one 20ms RTP packet
	// (160 bytes for either G.711 flavour, one byte per sample).
	SamplesPerFrame = sampleRate8kHz * frameMs / 1000
)

// Session owns one UDP socket for one call's RTP media.
type Session struct {
	conn *net.UDPConn
	// payloadType is the codec agreed in the SDP answer. Outbound packets are
	// stamped with it; inbound packets carrying anything else (RFC 2833 DTMF,
	// comfort noise) are dropped rather than fed to the decoder as audio.
	payloadType PayloadType
	// remoteAddr is where we send RTP. Seeded from the SDP offer, then latched
	// to the actual packet source on first inbound RTP (symmetric RTP, RFC 4961)
	// — switches/SBCs commonly send from a different port than they advertise.
	// Read in WritePayload (audioOut goroutine), written in ReadLoop (audioIn
	// goroutine), so it's an atomic pointer.
	remoteAddr   atomic.Pointer[net.UDPAddr]
	latched      atomic.Bool
	lastInbound  atomic.Int64 // UnixNano of the last inbound RTP packet, or the answer time
	sentFirst    atomic.Bool
	unexpectedPT atomic.Bool // log unknown inbound payload types once, not per packet
	ssrc         uint32
	writeMu      sync.Mutex // seq and timestamp: audio_in and the silence fill both write
	seq          uint16
	timestamp    uint32
}

// NewSession opens a UDP socket targeting remoteAddr (parsed from the inbound
// SDP offer's c=/m=audio lines). When portMin/portMax define a range it binds
// the first free port in it — required behind Docker so the port can be
// published and the far switch's RTP actually reaches us. portMin<=0 falls back
// to an OS-assigned ephemeral port (local dev, no NAT in the way).
func NewSession(remoteAddr *net.UDPAddr, portMin, portMax int, payloadType PayloadType) (*Session, error) {
	conn, err := listenInRange(portMin, portMax)
	if err != nil {
		return nil, fmt.Errorf("rtp: failed to open UDP socket: %w", err)
	}
	s := &Session{
		conn:        conn,
		payloadType: payloadType,
		ssrc:        rand.Uint32(), //nolint:gosec
	}
	s.remoteAddr.Store(remoteAddr)
	log.Printf("[rtp] session up: local=:%d  sdp-remote=%v  codec=%s", s.LocalPort(), remoteAddr, payloadType.Name())
	return s, nil
}

// PayloadType returns the codec agreed for this call.
func (s *Session) PayloadType() PayloadType { return s.payloadType }

// Answered sets the codec and the remote address from the SDP answer. As the
// caller, phone opens the socket before it knows either: the port goes into
// the offer. Call it before anything reads or writes media. It also starts
// the inbound-media clock: from here, LastInbound is how long ago the last
// inbound RTP arrived (now, since none has yet).
func (s *Session) Answered(addr *net.UDPAddr, pt PayloadType) {
	s.payloadType = pt
	if addr != nil && addr.IP != nil {
		s.remoteAddr.Store(addr)
	}
	s.lastInbound.Store(time.Now().UnixNano())
}

// Latched reports whether any inbound RTP was ever received. False means the
// far end never sent media — the call never really came up.
func (s *Session) Latched() bool { return s.latched.Load() }

// LastInbound is the last inbound RTP packet's arrival (any payload type),
// or the answer time if none has come yet. The RTP-idle watchdog compares
// against it.
func (s *Session) LastInbound() time.Time {
	n := s.lastInbound.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// SetRemote repoints outbound RTP, for when a re-INVITE moves the far end's
// media (an SBC re-anchoring mid-call, typically around hold/transfer). The
// symmetric-RTP latch is armed again so the first packet from the new address
// still wins over what the SDP claimed — see ReadLoop.
func (s *Session) SetRemote(addr *net.UDPAddr) {
	if addr == nil || addr.IP == nil {
		return
	}
	s.remoteAddr.Store(addr)
	s.latched.Store(false)
	log.Printf("[rtp] remote moved to %v (re-INVITE)", addr)
}

// listenInRange binds the first free UDP port in [portMin, portMax]; with an
// empty/invalid range it lets the OS pick any ephemeral port.
func listenInRange(portMin, portMax int) (*net.UDPConn, error) {
	if portMin <= 0 || portMax < portMin {
		return net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	}
	for port := portMin; port <= portMax; port++ {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: port})
		if err == nil {
			return conn, nil
		}
	}
	return nil, fmt.Errorf("no free UDP port in range %d-%d", portMin, portMax)
}

// LocalPort returns the OS-assigned local UDP port, used to build the SDP answer.
func (s *Session) LocalPort() int {
	return s.conn.LocalAddr().(*net.UDPAddr).Port
}

// WritePayload sends one G.711 payload (160 bytes for a 20ms frame) as one RTP packet.
func (s *Session) WritePayload(payload []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	pkt := pionrtp.Packet{
		Header: pionrtp.Header{
			Version:        2,
			PayloadType:    uint8(s.payloadType),
			SequenceNumber: s.seq,
			Timestamp:      s.timestamp,
			SSRC:           s.ssrc,
		},
		Payload: payload,
	}
	buf, err := pkt.Marshal()
	if err != nil {
		return fmt.Errorf("rtp: marshal packet: %w", err)
	}
	dst := s.remoteAddr.Load()
	if s.sentFirst.CompareAndSwap(false, true) {
		log.Printf("[rtp] first outbound RTP → %v (local :%d)", dst, s.LocalPort())
	}
	if _, err := s.conn.WriteToUDP(buf, dst); err != nil {
		return fmt.Errorf("rtp: write packet: %w", err)
	}
	s.seq++
	s.timestamp += uint32(SamplesPerFrame)
	return nil
}

// ReadLoop reads RTP packets and forwards each G.711 payload to onPayload until
// ctx is cancelled or the socket errors (which Close triggers).
func (s *Session) ReadLoop(ctx context.Context, onPayload func(payload []byte)) {
	buf := make([]byte, 1500)
	for {
		if ctx.Err() != nil {
			return
		}
		n, src, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[rtp] read error: %v", err)
			return
		}
		s.lastInbound.Store(time.Now().UnixNano())
		// Symmetric RTP: latch outbound onto the address we actually receive
		// from, overriding the SDP offer, on the first inbound packet.
		if s.latched.CompareAndSwap(false, true) {
			sdp := s.remoteAddr.Load()
			log.Printf("[rtp] first inbound RTP from %v (SDP offered %v) — latching replies to %v", src, sdp, src)
			s.remoteAddr.Store(src)
		}
		var pkt pionrtp.Packet
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			log.Printf("[rtp] unmarshal error: %v", err)
			continue
		}
		// Trunks multiplex RFC 2833 DTMF and comfort noise onto the same
		// stream. Feeding those to the G.711 decoder produces noise bursts
		// that trip the VAD, so drop anything that isn't the agreed codec.
		if PayloadType(pkt.PayloadType) != s.payloadType {
			if s.unexpectedPT.CompareAndSwap(false, true) {
				log.Printf("[rtp] dropping non-audio payload type %d (call is %s/%d)",
					pkt.PayloadType, s.payloadType.Name(), s.payloadType)
			}
			continue
		}
		onPayload(pkt.Payload)
	}
}

// Close shuts down the UDP socket, unblocking ReadLoop.
func (s *Session) Close() error {
	return s.conn.Close()
}
