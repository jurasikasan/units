// Package sdp builds phone's offer and reads the far end's answer. phone is the
// caller (UAC); the shapes mirror a UAS's.
package sdp

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/rtp"
)

// Offered is the G.711 set phone offers, in its own order of preference. The
// answer picks; EU trunks usually take PCMA.
var Offered = []rtp.PayloadType{rtp.PayloadTypePCMA, rtp.PayloadTypePCMU}

// Accepted is the G.711 set phone will answer with on an inbound call. The
// codec chosen is the first of these in the OFFER's own order (CONTRACT §6.1),
// so a caller that prefers PCMU gets PCMU back.
var Accepted = []rtp.PayloadType{rtp.PayloadTypePCMA, rtp.PayloadTypePCMU}

// BuildOffer returns the SDP offer for an outbound INVITE. Lines are CRLF:
// RFC 4566 §5 mandates it and carrier SBCs are the parsers that don't forgive
// a bare LF.
func BuildOffer(host string, port int) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "v=0\r\no=- 0 0 IN IP4 %s\r\ns=-\r\nc=IN IP4 %s\r\nt=0 0\r\n", host, host)
	fmt.Fprintf(&b, "m=audio %d RTP/AVP", port)
	for _, pt := range Offered {
		fmt.Fprintf(&b, " %d", pt)
	}
	b.WriteString("\r\n")
	for _, pt := range Offered {
		fmt.Fprintf(&b, "a=rtpmap:%d %s/8000\r\n", pt, pt.Name())
	}
	b.WriteString("a=ptime:20\r\na=sendrecv\r\n")
	return []byte(b.String())
}

// Offer is what phone needs from an inbound INVITE: where the caller will send
// media and the payload types it offered, in the offer's own order.
type Offer struct {
	Addr    *net.UDPAddr
	Formats []rtp.PayloadType
}

// ParseOffer reads the caller's SDP offer. It is the mirror of ParseAnswer:
// the m=audio line's formats are the caller's, in its own order of preference.
func ParseOffer(body string) (Offer, error) {
	var out Offer
	var ip string
	var port int

	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "c=IN IP4 "):
			ip = strings.TrimSpace(strings.TrimPrefix(line, "c=IN IP4 "))
		case strings.HasPrefix(line, "m=audio "):
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			p, err := strconv.Atoi(fields[1])
			if err != nil {
				continue
			}
			port = p
			if len(fields) > 3 {
				for _, f := range fields[3:] {
					n, err := strconv.Atoi(f)
					if err != nil || n < 0 || n > 127 {
						continue
					}
					out.Formats = append(out.Formats, rtp.PayloadType(n))
				}
			}
		}
	}
	if ip == "" || port == 0 {
		return out, fmt.Errorf("sdp: missing c=/m=audio line in offer")
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		parsed = net.IPv4zero
	}
	out.Addr = &net.UDPAddr{IP: parsed, Port: port}
	return out, nil
}

// ChooseCodec picks the codec for an inbound answer from the offered formats:
// the first G.711 in the offer's own order (CONTRACT §6.1). ok is false when
// the offer has neither PCMA nor PCMU, which is a 488.
func ChooseCodec(offered []rtp.PayloadType) (rtp.PayloadType, bool) {
	for _, pt := range offered {
		for _, a := range Accepted {
			if pt == a {
				return pt, true
			}
		}
	}
	return 0, false
}

// BuildAnswer returns the SDP in the inbound 200 OK: exactly one codec — the
// one ChooseCodec picked from the offer — and phone's media address. CRLF
// lines, as in BuildOffer.
func BuildAnswer(host string, port int, codec rtp.PayloadType) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "v=0\r\no=- 0 0 IN IP4 %s\r\ns=-\r\nc=IN IP4 %s\r\nt=0 0\r\n", host, host)
	fmt.Fprintf(&b, "m=audio %d RTP/AVP %d\r\n", port, codec)
	fmt.Fprintf(&b, "a=rtpmap:%d %s/8000\r\n", codec, codec.Name())
	b.WriteString("a=ptime:20\r\na=sendrecv\r\n")
	return []byte(b.String())
}

// Answer is what phone needs from the 200 OK: where to send media and the
// codec the far end chose.
type Answer struct {
	Addr  *net.UDPAddr
	Codec rtp.PayloadType
}

// ParseAnswer reads the far end's SDP answer. The codec is the first format in
// its m=audio line that phone offered; an answer with none of them is an error
// (RFC 3264 §6: the answer is a subset of the offer).
func ParseAnswer(body string) (Answer, error) {
	var out Answer
	var ip string
	var port int
	var formats []rtp.PayloadType

	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "c=IN IP4 "):
			ip = strings.TrimSpace(strings.TrimPrefix(line, "c=IN IP4 "))
		case strings.HasPrefix(line, "m=audio "):
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			p, err := strconv.Atoi(fields[1])
			if err != nil {
				continue
			}
			port = p
			if len(fields) > 3 {
				for _, f := range fields[3:] {
					n, err := strconv.Atoi(f)
					if err != nil || n < 0 || n > 127 {
						continue
					}
					formats = append(formats, rtp.PayloadType(n))
				}
			}
		}
	}
	if ip == "" || port == 0 {
		return out, fmt.Errorf("sdp: missing c=/m=audio line in answer")
	}
	// A hostname is legal in c= (RFC 4566 §6). The unspecified address stands in
	// for it; symmetric RTP then latches onto where packets actually come from.
	parsed := net.ParseIP(ip)
	if parsed == nil {
		parsed = net.IPv4zero
	}
	out.Addr = &net.UDPAddr{IP: parsed, Port: port}

	for _, pt := range formats {
		for _, o := range Offered {
			if pt == o {
				out.Codec = pt
				return out, nil
			}
		}
	}
	return out, fmt.Errorf("sdp: answer has no G.711 format phone offered (got %v)", formats)
}
