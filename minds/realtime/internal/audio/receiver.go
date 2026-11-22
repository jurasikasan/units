package audio

import (
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"sync"
)

// Ports hands out UDP ports from a range, one per call.
type Ports struct {
	mu       sync.Mutex
	min, max int
	used     map[int]bool
}

// NewPorts is the range min..max, both included.
func NewPorts(min, max int) *Ports { return &Ports{min: min, max: max, used: map[int]bool{}} }

// Listen opens the first free port of the range on all addresses.
func (p *Ports) Listen() (*Receiver, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for port := p.min; port <= p.max; port++ {
		if p.used[port] {
			continue
		}
		conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
		if err != nil {
			continue
		}
		p.used[port] = true
		return &Receiver{conn: conn, Port: port, free: func() { p.release(port) }}, nil
	}
	return nil, errors.New("no free port in the audio range")
}

func (p *Ports) release(port int) {
	p.mu.Lock()
	delete(p.used, port)
	p.mu.Unlock()
}

// Receiver takes RTP L16 at 48 kHz on one port. The first SSRC owns it;
// packets from others are dropped (the organs' rule).
type Receiver struct {
	conn *net.UDPConn
	Port int
	free func()
	once sync.Once
}

// Address is host:Port, the host being the name senders reach us by.
func (r *Receiver) Address(host string) string { return net.JoinHostPort(host, strconv.Itoa(r.Port)) }

// Run reads until Close, handing each packet's payload on as PCM16
// little-endian at 24 kHz, with the packet's RTP timestamp.
func (r *Receiver) Run(got func(pcm24 []byte, ts uint32)) {
	buf := make([]byte, 1<<16) // a 20 ms L16 packet is 1932 bytes, past an Ethernet MTU
	var owner uint32
	owned := false
	for {
		n, _, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		payload, err := Payload(buf[:n])
		if err != nil {
			continue
		}
		ssrc := binary.BigEndian.Uint32(buf[8:])
		if !owned {
			owner, owned = ssrc, true
		} else if ssrc != owner {
			continue
		}
		got(Down(payload), binary.BigEndian.Uint32(buf[4:]))
	}
}

// Close stops reading and frees the port.
func (r *Receiver) Close() {
	r.once.Do(func() {
		_ = r.conn.Close()
		r.free()
	})
}
