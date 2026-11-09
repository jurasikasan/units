package farend

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/require"
)

// OfferSDP is a caller's offer: m=audio on mediaPort, the payload types in
// the order given (the caller's order of preference).
func OfferSDP(mediaPort int, pts ...string) []byte {
	return OfferSDPHost("127.0.0.1", mediaPort, pts...)
}

// OfferSDPHost is OfferSDP with c= set to host, which need not be an IP.
func OfferSDPHost(host string, mediaPort int, pts ...string) []byte {
	m := "m=audio " + strconv.Itoa(mediaPort) + " RTP/AVP"
	for _, pt := range pts {
		m += " " + pt
	}
	return []byte("v=0\r\no=- 0 0 IN IP4 " + host + "\r\ns=-\r\nc=IN IP4 " + host + "\r\nt=0 0\r\n" + m + "\r\n")
}

// Caller is the far end placing a call to phone, as a trunk does for an
// inbound call. Each Call records the responses to its INVITEs as they come
// off the wire; the caller counts the BYEs phone sends it.
type Caller struct {
	t     *testing.T
	ua    *sipgo.UserAgent
	cli   *sipgo.Client
	dc    *sipgo.DialogClientCache
	phone string

	// Media is the caller's RTP socket; its port goes into the offer.
	Media     *net.UDPConn
	MediaPort int

	mu    sync.Mutex
	calls map[string]*Call // by Call-ID
	// GotBye receives once per BYE phone sends into the caller's dialog.
	GotBye chan struct{}
}

// NewCaller starts a caller on loopback that calls phone at phoneAddr
// (host:port of phone's SIP server).
func NewCaller(t *testing.T, phoneAddr string) *Caller {
	t.Helper()
	conn, port := ListenUDP(t)
	media, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)

	ua, err := sipgo.NewUA(sipgo.WithUserAgent("trunk-caller"))
	require.NoError(t, err)
	srv, err := sipgo.NewServer(ua)
	require.NoError(t, err)
	cli, err := sipgo.NewClient(ua, sipgo.WithClientHostname("127.0.0.1"), sipgo.WithClientPort(port))
	require.NoError(t, err)
	c := &Caller{
		t: t, ua: ua, cli: cli, phone: phoneAddr,
		dc:        sipgo.NewDialogClientCache(cli, sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: "trunk", Host: "127.0.0.1", Port: port}}),
		Media:     media,
		MediaPort: media.LocalAddr().(*net.UDPAddr).Port,
		GotBye:    make(chan struct{}, 4),
		calls:     map[string]*Call{},
	}
	// Responses are recorded off the transport, in arrival order: sipgo's
	// client transaction passes up whatever response it holds when its
	// reader gets to it, so a 180 overtaken by the 200 never reaches
	// WaitAnswer.
	ua.TransportLayer().OnMessage(func(msg sip.Message) {
		r, ok := msg.(*sip.Response)
		if !ok || r.CSeq() == nil || r.CSeq().MethodName != sip.INVITE || r.CallID() == nil {
			return
		}
		c.mu.Lock()
		call := c.calls[r.CallID().Value()]
		c.mu.Unlock()
		if call != nil {
			call.record(r)
		}
	})
	srv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		if err := c.dc.ReadBye(req, tx); err != nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
			return
		}
		c.GotBye <- struct{}{}
	})
	go srv.ServeUDP(conn) //nolint:errcheck
	t.Cleanup(func() { _ = ua.Close(); _ = conn.Close(); _ = media.Close() })
	return c
}

// Call is one INVITE the caller placed.
type Call struct {
	c      *Caller
	Sess   *sipgo.DialogClientSession
	cancel context.CancelFunc
	// Done is closed when the INVITE got its final response (or was given up);
	// Err is then what WaitAnswer returned.
	Done chan struct{}
	Err  error

	mu        sync.Mutex
	responses []*sip.Response
}

// Invite calls phone: from is the From user part, to the Request-URI user
// part, offer the SDP. It returns at once; the INVITE's fate comes in Done.
func (c *Caller) Invite(from, to string, offer []byte) *Call {
	c.t.Helper()
	host, port, err := net.SplitHostPort(c.phone)
	require.NoError(c.t, err)
	recipient := sip.Uri{Scheme: "sip", User: to, Host: host, Port: atoi(port)}
	fromHdr := &sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: from, Host: "127.0.0.1"}, Params: sip.NewParams()}
	fromHdr.Params.Add("tag", sip.GenerateTagN(16))

	callID := sip.CallIDHeader(sip.GenerateTagN(16))
	call := &Call{c: c, Done: make(chan struct{})}
	c.mu.Lock()
	c.calls[string(callID)] = call
	c.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	call.cancel = cancel
	sess, err := c.dc.Invite(ctx, recipient, offer, sip.NewHeader("Content-Type", "application/sdp"), fromHdr, &callID)
	require.NoError(c.t, err)
	call.Sess = sess
	go func() {
		defer close(call.Done)
		call.Err = sess.WaitAnswer(ctx, sipgo.AnswerOptions{})
		if call.Err == nil {
			_ = sess.Ack(context.Background())
		}
	}()
	return call
}

// record keeps one response per status code: retransmissions are not new.
func (call *Call) record(r *sip.Response) {
	call.mu.Lock()
	defer call.mu.Unlock()
	for _, have := range call.responses {
		if have.StatusCode == r.StatusCode {
			return
		}
	}
	call.responses = append(call.responses, r)
}

// Responses are the responses phone sent to this call's INVITE, in the
// order they arrived.
func (call *Call) Responses() []*sip.Response {
	call.mu.Lock()
	defer call.mu.Unlock()
	return append([]*sip.Response(nil), call.responses...)
}

// Codes are the status codes of Responses, in order.
func (call *Call) Codes() []int {
	var out []int
	for _, r := range call.Responses() {
		out = append(out, int(r.StatusCode))
	}
	return out
}

// Wait blocks until the INVITE has its final response.
func (call *Call) Wait(t *testing.T) {
	t.Helper()
	select {
	case <-call.Done:
	case <-time.After(10 * time.Second):
		t.Fatal("INVITE got no final response")
	}
}

// Cancel gives up the call before it is answered: sipgo sends CANCEL.
func (call *Call) Cancel() { call.cancel() }

// Bye hangs up an answered call.
func (call *Call) Bye(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, call.Sess.Bye(ctx))
}

// ReInvite sends a re-INVITE in the answered dialog with a new offer and
// returns phone's response.
func (call *Call) ReInvite(t *testing.T, offer []byte) *sip.Response {
	t.Helper()
	req := sip.NewRequest(sip.INVITE, call.Sess.InviteResponse.Contact().Address)
	req.SetBody(offer)
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := call.Sess.Do(ctx, req)
	require.NoError(t, err)
	if res.IsSuccess() {
		ack := sip.NewRequest(sip.ACK, req.Recipient)
		ack.AppendHeader(sip.HeaderClone(req.CSeq()))
		ack.CSeq().MethodName = sip.ACK
		_ = call.Sess.WriteRequest(ack)
	}
	return res
}

// Request sends a request outside any dialog (OPTIONS, UPDATE) and returns
// phone's response.
func (c *Caller) Request(t *testing.T, method sip.RequestMethod) *sip.Response {
	t.Helper()
	host, port, err := net.SplitHostPort(c.phone)
	require.NoError(t, err)
	req := sip.NewRequest(method, sip.Uri{Scheme: "sip", User: "phone", Host: host, Port: atoi(port)})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := c.cli.Do(ctx, req)
	require.NoError(t, err)
	return res
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
