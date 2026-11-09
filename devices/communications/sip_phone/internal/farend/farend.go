// Package farend is a SIP far end for phone's tests: an in-process sipgo UA
// on loopback whose behaviour is picked by the number dialled. check.sh needs
// no docker, so sipp is for the end-to-end run only.
package farend

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
	pionrtp "github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

const (
	AnswerThenBye = "+380000000001"
	Busy          = "+380000000002"
	RingForever   = "+380000000003"
	Decline       = "+380000000004"
	Auth          = "+380000000005"
	AnswerAndHold = "+380000000006"
	NotFound      = "+380000000007"

	User     = "trunkuser"
	Password = "trunkpass"
)

// AnswerSDP is what the far end answers with: PCMA on loopback, media at port.
func AnswerSDP(port int) string {
	return "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio " +
		strconv.Itoa(port) + " RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\n"
}

// DefaultAnswerMediaPort is where the far end puts phone's media unless a test
// listening for it names a port of its own. phone sends silence there on every
// answered call, so a test reading media must not share it with tests in other
// packages running at the same time.
const DefaultAnswerMediaPort = 40000

// AnswerPayloadType is the G.711 the far end answers with and sends in SendRTP.
const AnswerPayloadType = uint8(8) // PCMA

type FarEnd struct {
	Addr      string
	Invites   atomic.Int32
	Cancelled atomic.Bool
	AuthOK    atomic.Bool
	GotBye    chan struct{}
	// Media is the media address of the last offer seen: where the far end's
	// half of the call's audio (SendRTP) goes.
	Media atomic.Pointer[net.UDPAddr]
	// AnswerMediaPort is the media port the far end answers with; 0 is
	// DefaultAnswerMediaPort.
	AnswerMediaPort atomic.Int32
}

func (fe *FarEnd) answerSDP() []byte {
	port := int(fe.AnswerMediaPort.Load())
	if port == 0 {
		port = DefaultAnswerMediaPort
	}
	return []byte(AnswerSDP(port))
}

// ListenUDP opens a loopback UDP socket on a free port.
func ListenUDP(t *testing.T) (net.PacketConn, int) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	return conn, conn.LocalAddr().(*net.UDPAddr).Port
}

// Start runs a far end; the number dialled picks its script.
func Start(t *testing.T) *FarEnd {
	t.Helper()
	conn, port := ListenUDP(t)
	fe := &FarEnd{Addr: conn.LocalAddr().String(), GotBye: make(chan struct{}, 1)}

	ua, err := sipgo.NewUA(sipgo.WithUserAgent("far-end"))
	require.NoError(t, err)
	srv, err := sipgo.NewServer(ua)
	require.NoError(t, err)
	cli, err := sipgo.NewClient(ua, sipgo.WithClientHostname("127.0.0.1"), sipgo.WithClientPort(port))
	require.NoError(t, err)
	dsc := sipgo.NewDialogServerCache(cli, sip.ContactHeader{Address: sip.Uri{Scheme: "sip", Host: "127.0.0.1", Port: port}})
	chal := &digest.Challenge{Realm: "trunk", Nonce: "4b1d", Algorithm: "MD5"}

	srv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) { _ = dsc.ReadAck(req, tx) })
	srv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = dsc.ReadBye(req, tx)
		fe.GotBye <- struct{}{}
	})
	srv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		fe.Invites.Add(1)
		tx.OnCancel(func(*sip.Request) { fe.Cancelled.Store(true) })

		// The real trunk refuses an unlabelled body the same way.
		if ct := req.ContentType(); ct == nil || ct.Value() != "application/sdp" {
			_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusUnsupportedMediaType, "Unsupported Media Type", nil))
			return
		}

		if req.Recipient.User == Auth[1:] {
			h := req.GetHeader("Proxy-Authorization")
			if h == nil {
				res := sip.NewResponseFromRequest(req, sip.StatusProxyAuthRequired, "Proxy Authentication Required", nil)
				res.AppendHeader(sip.NewHeader("Proxy-Authenticate", chal.String()))
				_ = tx.Respond(res)
				return
			}
			creds, err := digest.ParseCredentials(h.Value())
			want, err2 := digest.Digest(chal, digest.Options{Method: "INVITE", URI: req.Recipient.Addr(), Username: User, Password: Password})
			if err != nil || err2 != nil || creds.Response != want.Response {
				_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusForbidden, "Forbidden", nil))
				return
			}
			fe.AuthOK.Store(true)
		}

		dlg, err := dsc.ReadInvite(req, tx)
		if err != nil {
			return
		}
		defer dlg.Close() //nolint:errcheck
		_ = dlg.Respond(sip.StatusRinging, "Ringing", nil)
		// A trunk sends the call's audio to the address in our offer; record
		// it so SendRTP can aim the far end's RTP there.
		if port := offerPort(req.Body()); port > 0 {
			fe.Media.Store(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
		}

		// A real far end rings for a while; a final response hard on the heels
		// of 180 can overtake WaitAnswer starting to read, and ringing is not
		// guaranteed by the contract anyway.
		time.Sleep(50 * time.Millisecond)

		switch "+" + req.Recipient.User {
		case Busy:
			_ = dlg.Respond(sip.StatusBusyHere, "Busy Here", nil)
		case Decline:
			_ = dlg.Respond(sip.StatusGlobalDecline, "Decline", nil)
		case NotFound:
			_ = dlg.Respond(sip.StatusNotFound, "Not Found", nil)
		case RingForever:
			<-tx.Done()
		case AnswerThenBye, Auth:
			_ = dlg.RespondSDP(fe.answerSDP())
			time.Sleep(150 * time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = dlg.Bye(ctx)
			cancel()
		case AnswerAndHold:
			_ = dlg.RespondSDP(fe.answerSDP())
			<-dlg.Context().Done()
		}
	})

	go srv.ServeUDP(conn) //nolint:errcheck
	t.Cleanup(func() { _ = ua.Close(); _ = conn.Close() })
	return fe
}

// SendRTP sends n 20 ms G.711 packets with the given payload toward the media
// address of the last offer — the far end's half of the call's audio, as a
// trunk would send it, in the payload type the far end answers with.
func (fe *FarEnd) SendRTP(t *testing.T, n int, payload []byte) {
	t.Helper()
	dst := fe.Media.Load()
	require.NotNil(t, dst, "the far end must have seen an offer before it can send media")
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	for i := 0; i < n; i++ {
		pkt := pionrtp.Packet{
			Header: pionrtp.Header{
				Version: 2, PayloadType: AnswerPayloadType,
				SequenceNumber: uint16(i), Timestamp: uint32(i) * 160, SSRC: 0xF00D5EED,
			},
			Payload: payload,
		}
		buf, err := pkt.Marshal()
		require.NoError(t, err)
		_, err = conn.WriteToUDP(buf, dst)
		require.NoError(t, err)
	}
}

// offerPort is the port of the m=audio line of an SDP body, 0 if it has none.
func offerPort(body []byte) int {
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("m=audio ")) {
			continue
		}
		f := bytes.Fields(line)
		if len(f) < 2 {
			return 0
		}
		p, err := strconv.Atoi(string(f[1]))
		if err != nil {
			return 0
		}
		return p
	}
	return 0
}
