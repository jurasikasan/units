package call

import (
	"fmt"
	"net"
	"time"

	"github.com/emiago/sipgo"
)

// NewClient is the SIP client that sends phone's requests through conn, the
// socket phone listens on: an INVITE, ACK, CANCEL or BYE leaves from the port
// that its Via and Contact name, so a trunk that answers to Via (no rport) or
// a firewall that admits only that port still reaches us. conn must be served
// by a sipgo server on the same UA (Server.ServeUDP), which puts it in the
// transport's pool under its local address; the client picks it from there.
//
// The returned port is the one to advertise (Config.SIPPort).
func NewClient(ua *sipgo.UserAgent, conn net.PacketConn, advertiseHost string) (*sipgo.Client, int, error) {
	local := conn.LocalAddr().(*net.UDPAddr)
	cli, err := sipgo.NewClient(ua,
		sipgo.WithClientHostname(advertiseHost),
		sipgo.WithClientPort(local.Port),
		sipgo.WithClientConnectionAddr(local.String()),
	)
	return cli, local.Port, err
}

// Serve serves conn on srv and returns once the socket is in the transport's
// pool, so a request sent right after goes through it rather than trying to
// bind a second socket to the same port. The channel carries ServeUDP's end.
func Serve(srv *sipgo.Server, conn net.PacketConn) (<-chan error, error) {
	done := make(chan error, 1)
	go func() { done <- srv.ServeUDP(conn) }()
	addr := conn.LocalAddr().String()
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(time.Millisecond) {
		if c, _ := srv.TransportLayer().GetConnection("udp", addr); c != nil {
			return done, nil
		}
		select {
		case err := <-done:
			return nil, fmt.Errorf("serving %s ended before it began: %v", addr, err)
		default:
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("serving %s: socket not in the transport after 2s", addr)
		}
	}
}
