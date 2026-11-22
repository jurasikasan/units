package fetch

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
)

// Which addresses fetch may reach: the public internet only. Anything that
// leads into the machine, its networks or the stack around it is refused:
// loopback, private, link-local (the cloud's metadata at 169.254.169.254),
// multicast, unspecified, and the ranges that are no public host or carry
// one inside them (IPv4 in IPv6, NAT64, 6to4, Teredo).
var notPublic = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",       // this network
		"100.64.0.0/10",   // shared address space (carrier NAT)
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"240.0.0.0/4",     // reserved, and the broadcast
		"::/96",           // IPv4-compatible (deprecated): an IPv4 inside
		"64:ff9b::/96",    // NAT64: an IPv4 inside
		"64:ff9b:1::/48",  // local NAT64
		"100::/64",        // discard
		"2001::/32",       // Teredo: an IPv4 inside
		"2001:db8::/32",   // documentation
		"2002::/16",       // 6to4: an IPv4 inside
		"fec0::/10",       // site-local (deprecated)
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// Public says whether ip is a public unicast address fetch may reach. An
// IPv4-mapped IPv6 address (::ffff:10.0.0.1) is judged as the IPv4 it is.
func Public(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// Refused is a url fetch will not reach, and why, in words.
type Refused struct{ Why string }

func (r *Refused) Error() string { return r.Why }

// checkURL refuses what is no http(s) URL to a named host.
func checkURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return &Refused{fmt.Sprintf("only http and https are fetched, not %q", u.Scheme)}
	}
	if u.Hostname() == "" {
		return &Refused{"the url names no host"}
	}
	if u.User != nil {
		return &Refused{"a url with credentials in it is not fetched"}
	}
	return nil
}

// checkHost resolves host and refuses it when any of its addresses is not
// allowed. It gives a clear answer early, on the first url and on every
// redirect; the dial below is what holds, since a name may resolve
// differently a moment later.
func (c *Client) checkHost(ctx context.Context, host string) error {
	ips, err := c.resolve(ctx, host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		if !c.allow(ip) {
			return refusedAddr(host, ip)
		}
	}
	return nil
}

// resolve is host's addresses: itself when it is one.
func (c *Client) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	ips, err := c.lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("%s did not resolve: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%s did not resolve", host)
	}
	return ips, nil
}

func refusedAddr(host string, ip netip.Addr) error {
	ip = ip.Unmap()
	if strings.Trim(host, "[]") == ip.String() {
		return &Refused{fmt.Sprintf("%s is not a public address", ip)}
	}
	return &Refused{fmt.Sprintf("%s resolves to %s, not a public address", host, ip)}
}

// dial is the transport's only way out, and the last word: it resolves the
// name itself, refuses it when any of its addresses is not allowed, and
// connects to an address it checked, never to the name. A name that
// resolved to a public address for checkHost and to a private one now (DNS
// rebinding), a redirect, a retry, a keep-alive dial: all of them pass
// here. The socket's Control checks the address once more, as the kernel
// is given it.
func (c *Client) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := c.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if !c.allow(ip) {
			return nil, refusedAddr(host, ip)
		}
	}
	d := net.Dialer{Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return &Refused{fmt.Sprintf("an address that is no ip:port: %q", address)}
		}
		if !c.allow(ap.Addr()) {
			return refusedAddr(ap.Addr().String(), ap.Addr())
		}
		return nil
	}}
	var last error
	for _, ip := range ips {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}
