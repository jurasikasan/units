// Package audioin is phone's inbound audio socket, one per answered call
// (phone/CONTRACT.md §2.2). It accepts L16 48 kHz mono RTP (payload type 96,
// big-endian int16, RFC 3551) and hands each payload to its handler. It is
// box-internal: senders inside the box reach it at AUDIO_IN_HOST:port; it is
// not published in unit.yaml.
package audioin

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"

	pionrtp "github.com/pion/rtp"
)

// L16PayloadType is the payload type audio_in accepts (L16 linear PCM,
// RFC 3551 dynamic range). Anything else is dropped.
const L16PayloadType = 96

// Listener is one audio_in socket.
type Listener struct {
	conn *net.UDPConn
}

// New binds the first free UDP port in [portMin, portMax]; with an
// empty/invalid range it lets the OS pick an ephemeral port (tests).
func New(portMin, portMax int) (*Listener, error) {
	conn, err := listenInRange(portMin, portMax)
	if err != nil {
		return nil, fmt.Errorf("audioin: %w", err)
	}
	return &Listener{conn: conn}, nil
}

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

// Port is the local UDP port audio_in listens on.
func (l *Listener) Port() int { return l.conn.LocalAddr().(*net.UDPAddr).Port }

// Read delivers each incoming RTP payload (payload type, SSRC, payload) to
// onPkt, as it comes, until ctx is cancelled or the socket closes.
// Deciding what to forward is the handler's: one sender owns the call and
// only payload type 96 is audio, so both are dropped here.
func (l *Listener) Read(ctx context.Context, onPkt func(pt uint8, ssrc uint32, payload []byte)) {
	// An audio_in frame is a 12-byte RTP header plus 960 int16 samples
	// (1920 bytes) = 1932 bytes; the buffer must fit the whole datagram or
	// ReadFromUDP silently truncates it.
	buf := make([]byte, 2048)
	for {
		if ctx.Err() != nil {
			return
		}
		n, _, err := l.conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("[audioin] read error: %v", err)
			return
		}
		var pkt pionrtp.Packet
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			log.Printf("[audioin] unmarshal error: %v", err)
			continue
		}
		if ctx.Err() != nil {
			return
		}
		onPkt(pkt.PayloadType, pkt.SSRC, pkt.Payload)
	}
}

// Close shuts the socket down, unblocking Read. Packets sent afterwards are
// refused by the OS (connection refused) or lost.
func (l *Listener) Close() error { return l.conn.Close() }

// L16ToSamples decodes one L16 payload: big-endian int16 (RFC 3551), one
// sample per two bytes.
func L16ToSamples(payload []byte) []int16 {
	out := make([]int16, len(payload)/2)
	for i := range out {
		out[i] = int16(uint16(payload[i*2])<<8 | uint16(payload[i*2+1]))
	}
	return out
}
