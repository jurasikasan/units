// Package call places outbound calls and reports what happens to them
// (phone/CONTRACT.md §2.1, §2.4, §3). It executes and reports; it decides
// nothing about what is said or whether to call again.
package call

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/audioin"
	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/events"
	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/rtp"
	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/sdp"
)

// Error codes of CONTRACT.md §2.5 that dial, hangup and answer can return.
const (
	CodeNotAnswered = "not_answered"
	CodeBadTarget   = "bad_target"
)

const (
	CodeCapacity       = "capacity"
	CodeInvalidNumber  = "invalid_number"
	CodeFromNotAllowed = "from_not_allowed"
	CodeUnknownCall    = "unknown_call"
	CodeNotRinging     = "not_ringing"
)

// Error is a refusal a tool returns to its caller; Code is from §2.5.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

// End reasons, the closed list of CONTRACT.md §3.
const (
	RemoteHangup = "remote-hangup"
	LocalHangup  = "local-hangup"
	Cancelled    = "cancelled"
	Busy         = "busy"
	NoAnswer     = "no-answer"
	Declined     = "declined"
	Abandoned    = "abandoned"
	Unattended   = "unattended"
	Failed       = "failed"
)

type Config struct {
	// Trunk is host:port every call is sent to.
	Trunk string
	// AdvertiseHost is the IP the far end reaches us on: Contact, SDP c=.
	AdvertiseHost string
	SIPPort       int
	RTPPortMin    int
	RTPPortMax    int
	MaxCalls      int
	RingDefault   time.Duration
	RingMax       time.Duration
	// Username and Password answer a 401/407 digest challenge.
	Username string
	Password string
	// DefaultFrom is the caller ID when dial gives none; empty leaves it to the trunk.
	DefaultFrom string
	// FromAllow, when not empty, is the only caller IDs dial accepts.
	FromAllow []string
	// StripPlus and Prefix turn E.164 into what the trunk dials.
	StripPlus bool
	Prefix    string
	// AudioInHost is the host part of the audio_in address the answered
	// event reports; AudioInPortMin/Max bound the per-call audio_in port.
	AudioInHost    string
	AudioInPortMin int
	AudioInPortMax int
	// InboundAnswerTimeout is how long a ringing inbound call may wait to be
	// answered before phone declines it with 480 (INBOUND_ANSWER_TIMEOUT,
	// CONTRACT §6.1); zero means the 30s contract default.
	InboundAnswerTimeout time.Duration
	// RTPIdleTimeout ends an answered inbound call that carries no inbound
	// RTP for this long (RTP_IDLE_TIMEOUT, CONTRACT §6.1); zero means the
	// 60s contract default.
	RTPIdleTimeout time.Duration
	// InboundAllow, when not empty, is the only source IPs an inbound INVITE
	// is accepted from; anything else gets 403 (SIP_INBOUND_ALLOW).
	InboundAllow []string
}

type DialRequest struct {
	To          string
	From        string
	RingTimeout time.Duration
	Traceparent string
}

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

var (
	errHangup   = errors.New("hung up by the caller of phone")
	errNoAnswer = errors.New("ring timeout")
)

// Call is one call from dial (or an inbound INVITE) to ended.
type Call struct {
	ID          string
	traceparent string
	media       *rtp.Session
	listener    *rtp.Out                // §2.3 egress; set once in Dial, moved by Listen
	hangup      context.CancelCauseFunc // cancels the ringing phase (outbound)
	hangupCh    chan struct{}           // closed by Hangup once answered or not
	hangupOnce  sync.Once
	answeredAt  time.Time
	mu          sync.Mutex
	answered    bool

	// Inbound (taken) calls. The dialog context is the source of truth:
	// CANCEL cancels it with sip.ErrTransactionCanceled (sipgo has sent 487),
	// the caller's BYE ends it with no cause.
	inbound    bool
	dlg        *sipgo.DialogServerSession
	codec      rtp.PayloadType // chosen at the offer; the 200 OK answers with it
	answerCh   chan struct{}   // closed by Answer, once
	answerOnce sync.Once
}

// Answered reports whether the callee picked up and the call is still up.
func (c *Call) Answered() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.answered
}

// Media is the call's RTP session; its codec and remote are set once answered.
func (c *Call) Media() *rtp.Session { return c.media }

// Traceparent is the trace the call was dialled under, for events about it.
func (c *Call) Traceparent() string { return c.traceparent }

type Manager struct {
	cfg     Config
	dc      *sipgo.DialogClientCache
	dsc     *sipgo.DialogServerCache
	contact sip.ContactHeader
	pub     events.Publisher

	mu         sync.Mutex
	calls      map[string]*Call
	seenIDs    sync.Map // every call_id we ever gave out: answer of an ended call is not_ringing, not unknown_call
	inboundDlg sync.Map // *sipgo.DialogServerSession -> *Call, for re-INVITEs in the dialog
	wg         sync.WaitGroup

	// OnAnswered runs once media is up, before answered is published: what
	// reacts to answered may speak at once. OnEnding runs before ended is
	// published, so whatever it reports about the call comes first.
	OnAnswered func(*Call)
	OnEnding   func(*Call)
}

func NewManager(cfg Config, client *sipgo.Client, pub events.Publisher) *Manager {
	contact := sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: "phone", Host: cfg.AdvertiseHost, Port: cfg.SIPPort}}
	return &Manager{
		cfg:     cfg,
		dc:      sipgo.NewDialogClientCache(client, contact),
		dsc:     sipgo.NewDialogServerCache(client, contact),
		contact: contact,
		pub:     pub,
		calls:   map[string]*Call{},
	}
}

// inboundAnswerTimeout applies the contract default (30s) when unset.
func (m *Manager) inboundAnswerTimeout() time.Duration {
	if t := m.cfg.InboundAnswerTimeout; t > 0 {
		return t
	}
	return 30 * time.Second
}

// rtpIdleTimeout applies the contract default (60s) when unset.
func (m *Manager) rtpIdleTimeout() time.Duration {
	if t := m.cfg.RTPIdleTimeout; t > 0 {
		return t
	}
	return 60 * time.Second
}

// OnBye is the SIP server's BYE handler: the callee hung up.
// OnBye answers a BYE in a dialog phone has in either direction: an inbound
// call the caller is hanging up (dsc) or an outbound one (dc). A dialog
// phone never had gets 481.
func (m *Manager) OnBye(req *sip.Request, tx sip.ServerTransaction) {
	if err := m.dsc.ReadBye(req, tx); err != nil {
		if err := m.dc.ReadBye(req, tx); err != nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
		}
	}
}

// sourceIP is the network source of a request, no port: what
// SIP_INBOUND_ALLOW lists.
func sourceIP(req *sip.Request) string {
	src := req.Source()
	host, _, err := net.SplitHostPort(src)
	if err != nil {
		return src
	}
	return host
}

// viaSourceIP is where a request came from: the Via received= an SBC stamped,
// else the Via host. nil when neither is an IP.
func viaSourceIP(req *sip.Request) net.IP {
	via := req.Via()
	if via == nil {
		return nil
	}
	if received, ok := via.Params.Get("received"); ok {
		if ip := net.ParseIP(received); ip != nil {
			return ip
		}
	}
	return net.ParseIP(via.Host)
}

// OnInvite takes an inbound INVITE (CONTRACT §6.1) or answers a re-INVITE in
// a dialog phone already has. A new call gets 100, then 180, and the
// phone.call.incoming event; the 200 OK (with the answer SDP) goes only when
// answer is called, or 480 after INBOUND_ANSWER_TIMEOUT.
func (m *Manager) OnInvite(req *sip.Request, tx sip.ServerTransaction) {
	if m.handleReInvite(req, tx) {
		return
	}
	if allow := m.cfg.InboundAllow; len(allow) > 0 && !slices.Contains(allow, sourceIP(req)) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusForbidden, "Forbidden", nil))
		return
	}
	m.mu.Lock()
	full := m.cfg.MaxCalls > 0 && len(m.calls) >= m.cfg.MaxCalls
	m.mu.Unlock()
	if full {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusServiceUnavailable, "Service Unavailable", nil))
		return
	}
	offer, err := sdp.ParseOffer(string(req.Body()))
	if err != nil {
		log.Printf("[call] inbound INVITE: %v", err)
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, "Bad Request", nil))
		return
	}
	codec, ok := sdp.ChooseCodec(offer.Formats)
	if !ok {
		// §6.1: an offer with neither PCMU nor PCMA gets 488, no events.
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusNotAcceptableHere, "Not Acceptable Here", nil))
		return
	}
	if ip := offer.Addr.IP; ip == nil || ip.IsUnspecified() {
		// c= is 0.0.0.0 (a phone behind carrier NAT) or a hostname, which
		// RFC 4566 §6 allows: media goes to the caller's own address instead,
		// so our first packets reach the carrier's media gateway and latch it.
		if via := viaSourceIP(req); via != nil {
			offer.Addr = &net.UDPAddr{IP: via, Port: offer.Addr.Port}
		}
	}
	dlg, err := m.dsc.ReadInvite(req, tx)
	if err != nil {
		log.Printf("[call] inbound INVITE: %v", err)
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusRequestTimeout, "Request Timeout", nil))
		return
	}

	c := &Call{
		ID:       newCallID("call-in-"),
		inbound:  true,
		dlg:      dlg,
		codec:    codec,
		hangupCh: make(chan struct{}),
		answerCh: make(chan struct{}),
	}
	media, err := rtp.NewSession(offer.Addr, m.cfg.RTPPortMin, m.cfg.RTPPortMax, codec)
	if err != nil {
		_ = dlg.Close()
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusServiceUnavailable, "Service Unavailable", nil))
		return
	}
	c.media = media
	c.listener = media.NewOut()

	m.mu.Lock()
	for m.calls[c.ID] != nil {
		c.ID = newCallID("call-in-")
	}
	m.calls[c.ID] = c
	m.mu.Unlock()
	m.seenIDs.Store(c.ID, struct{}{})
	m.inboundDlg.Store(dlg, c)
	m.wg.Add(1)

	// Provisionals are fire-and-forget; the final 200 SDP comes from
	// runInbound, after answer.
	_ = dlg.Respond(sip.StatusTrying, "Trying", nil)
	_ = dlg.Respond(sip.StatusRinging, "Ringing", nil)

	fromUser := ""
	if fh := req.From(); fh != nil {
		fromUser = fh.Address.User
	}
	m.publish(c, events.CallIncoming, map[string]any{
		"from":       fromUser,
		"to":         req.Recipient.User,
		"offered_at": time.Now().UTC().Format(time.RFC3339Nano),
	})
	// Not in a goroutine: sipgo terminates the INVITE transaction as soon as
	// this handler returns without a final response on it.
	m.runInbound(c, dlg, offer.Addr)
}

// OnAck lets sipgo complete the dialog bookkeeping for the INVITE's ACK.
func (m *Manager) OnAck(req *sip.Request, tx sip.ServerTransaction) {
	if err := m.dsc.ReadAck(req, tx); err != nil {
		// ACK for a dialog we no longer track: nothing left to do with it.
		log.Printf("[call] ACK: %v", err)
	}
}

// OnOptions answers OPTIONS with the methods phone supports.
func (m *Manager) OnOptions(req *sip.Request, tx sip.ServerTransaction) {
	res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil)
	res.AppendHeader(sip.NewHeader("Allow", "INVITE, ACK, BYE, CANCEL, OPTIONS, UPDATE"))
	res.AppendHeader(&m.contact)
	_ = tx.Respond(res)
}

// OnUpdate answers UPDATE with 200, as the contract requires.
func (m *Manager) OnUpdate(req *sip.Request, tx sip.ServerTransaction) {
	res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil)
	res.AppendHeader(&m.contact)
	_ = tx.Respond(res)
}

// handleReInvite answers a re-INVITE in an already-open inbound dialog
// (CONTRACT §6.1): 200 with the same one-codec answer SDP, and the RTP leg
// pointed at the offer's new c=/port. It reports whether the request
// belonged to a known dialog.
func (m *Manager) handleReInvite(req *sip.Request, tx sip.ServerTransaction) bool {
	dlg, err := m.dsc.MatchDialogRequest(req)
	if err != nil {
		return false
	}
	cv, ok := m.inboundDlg.Load(dlg)
	if !ok {
		return false
	}
	c := cv.(*Call)
	if !c.Answered() {
		// A re-INVITE while still ringing: nothing to change, decline it.
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusTemporarilyUnavailable, "Temporarily Unavailable", nil))
		return true
	}
	if offer, err := sdp.ParseOffer(string(req.Body())); err == nil && offer.Addr != nil {
		c.media.SetRemote(offer.Addr)
	}
	res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", sdp.BuildAnswer(m.cfg.AdvertiseHost, c.media.LocalPort(), c.codec))
	res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	res.AppendHeader(&m.contact)
	_ = tx.Respond(res)
	return true
}

// Get returns a call that has not ended, or nil.
func (m *Manager) Get(id string) *Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[id]
}

// Wait blocks until every call has ended; for shutdown and tests.
func (m *Manager) Wait() { m.wg.Wait() }

// Dial starts a call and returns before anyone answers.
func (m *Manager) Dial(req DialRequest) (string, error) {
	if !e164.MatchString(req.To) {
		return "", &Error{CodeInvalidNumber, fmt.Sprintf("to %q is not E.164", req.To)}
	}
	from := req.From
	if from == "" {
		from = m.cfg.DefaultFrom
	}
	if len(m.cfg.FromAllow) > 0 && !slices.Contains(m.cfg.FromAllow, from) {
		return "", &Error{CodeFromNotAllowed, fmt.Sprintf("from %q is not on the allowlist", from)}
	}
	ring := req.RingTimeout
	if ring <= 0 {
		ring = m.cfg.RingDefault
	}
	if m.cfg.RingMax > 0 && ring > m.cfg.RingMax {
		ring = m.cfg.RingMax
	}

	m.mu.Lock()
	if m.cfg.MaxCalls > 0 && len(m.calls) >= m.cfg.MaxCalls {
		m.mu.Unlock()
		return "", &Error{CodeCapacity, fmt.Sprintf("%d calls already up", len(m.calls))}
	}
	c := &Call{ID: newCallID("call-"), traceparent: req.Traceparent, hangupCh: make(chan struct{})}
	for m.calls[c.ID] != nil {
		c.ID = newCallID("call-")
	}
	m.calls[c.ID] = c
	m.mu.Unlock()
	m.seenIDs.Store(c.ID, struct{}{})

	media, err := rtp.NewSession(nil, m.cfg.RTPPortMin, m.cfg.RTPPortMax, rtp.PayloadTypePCMA)
	if err != nil {
		m.forget(c.ID)
		return "", &Error{CodeCapacity, "no free RTP port: " + err.Error()}
	}
	c.media = media
	c.listener = media.NewOut()

	ringCtx, hangup := context.WithCancelCause(context.Background())
	c.hangup = hangup
	m.wg.Add(1)
	go m.run(c, ringCtx, ring, m.uri(req.To), from)
	return c.ID, nil
}

// Listen moves the call's far-end audio to to (host:port, UDP) — §2.3: the
// G.711 the call carries as it arrives, no transcoding. One destination per
// call: a second Listen with a different to moves the stream there. to:""
// stops it. Refusals: unknown_call, not_answered (before answered),
// bad_target (to is not a resolvable host:port).
func (m *Manager) Listen(id, to string) error {
	c := m.Get(id)
	if c == nil {
		return &Error{CodeUnknownCall, fmt.Sprintf("no call %s", id)}
	}
	if !c.Answered() {
		return &Error{CodeNotAnswered, fmt.Sprintf("call %s has not answered yet", id)}
	}
	if to == "" {
		c.listener.SetDest(nil)
		return nil
	}
	dst, err := resolveTarget(to)
	if err != nil {
		return &Error{CodeBadTarget, fmt.Sprintf("to %q: %v", to, err)}
	}
	c.listener.SetDest(dst)
	return nil
}

// resolveTarget is a listen target: host:port, resolved, with a usable port.
func resolveTarget(to string) (*net.UDPAddr, error) {
	host, portStr, err := net.SplitHostPort(to)
	if err != nil {
		return nil, errors.New("want host:port")
	}
	if host == "" {
		return nil, errors.New("missing host")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("bad port %q", portStr)
	}
	ips, err := net.LookupHost(host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("does not resolve")
	}
	return &net.UDPAddr{IP: net.ParseIP(ips[0]), Port: port}, nil
}

// Hangup ends a call: CANCEL while it rings, BYE once answered. A call that
// has already ended, or never existed, is not an error.
func (m *Manager) Hangup(id string) {
	c := m.Get(id)
	if c == nil {
		return
	}
	c.hangupOnce.Do(func() {
		if c.hangup != nil { // inbound calls ring on a dialog, not a context
			c.hangup(errHangup)
		}
		close(c.hangupCh)
	})
}

// Answer takes a ringing inbound call (CONTRACT §2.6, §6.1): phone replies 200
// OK with its own one-codec SDP. unknown_call if phone never gave that id;
// not_ringing if the call is outbound, already answered, or already ended.
func (m *Manager) Answer(id string) error {
	c := m.Get(id)
	if c == nil {
		if _, seen := m.seenIDs.Load(id); seen {
			return &Error{CodeNotRinging, fmt.Sprintf("call %s is not ringing", id)}
		}
		return &Error{CodeUnknownCall, fmt.Sprintf("no call %s", id)}
	}
	if !c.inbound || c.Answered() {
		return &Error{CodeNotRinging, fmt.Sprintf("call %s is not ringing", id)}
	}
	c.answerOnce.Do(func() { close(c.answerCh) })
	return nil
}

func (m *Manager) uri(e164 string) sip.Uri {
	user := e164
	if m.cfg.StripPlus {
		user = strings.TrimPrefix(user, "+")
	}
	user = m.cfg.Prefix + user
	var u sip.Uri
	_ = sip.ParseUri("sip:"+user+"@"+m.cfg.Trunk, &u)
	return u
}

func (m *Manager) run(c *Call, ringCtx context.Context, ring time.Duration, to sip.Uri, from string) {
	defer m.wg.Done()
	defer c.media.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeoutCause(ringCtx, ring, errNoAnswer)
	defer cancel()

	// sipgo sends the body as given; a trunk refuses it unlabelled with 415.
	headers := []sip.Header{sip.NewHeader("Content-Type", "application/sdp")}
	if from != "" {
		fromHdr := &sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: from, Host: m.cfg.AdvertiseHost}, Params: sip.NewParams()}
		fromHdr.Params.Add("tag", sip.GenerateTagN(16))
		headers = append(headers, fromHdr)
	}
	offer := sdp.BuildOffer(m.cfg.AdvertiseHost, c.media.LocalPort())

	sess, err := m.dc.Invite(ctx, to, offer, headers...)
	if err != nil {
		m.end(c, Failed, 0, fmt.Errorf("invite: %w", err))
		return
	}
	defer sess.Close() //nolint:errcheck

	var ringing sync.Once
	err = sess.WaitAnswer(ctx, sipgo.AnswerOptions{
		Username: m.cfg.Username,
		Password: m.cfg.Password,
		OnResponse: func(r *sip.Response) error {
			if r.StatusCode == sip.StatusRinging || r.StatusCode == sip.StatusSessionInProgress {
				ringing.Do(func() { m.publish(c, events.CallRinging, nil) })
			}
			return nil
		},
	})
	if err != nil {
		reason, status := notAnswered(err, context.Cause(ctx))
		m.end(c, reason, status, err)
		return
	}

	if err := sess.Ack(context.Background()); err != nil {
		log.Printf("[call %s] ACK: %v", c.ID, err)
	}
	ans, err := sdp.ParseAnswer(string(sess.InviteResponse.Body()))
	if err != nil {
		// Answered, but there is no media phone can speak into: hang up
		// rather than hold a silent line.
		_ = sess.Bye(context.Background())
		m.end(c, Failed, 0, err)
		return
	}
	c.media.Answered(ans.Addr, ans.Codec)

	upCtx, stopUp := context.WithCancel(context.Background())
	defer stopUp()
	ln, err := m.up(upCtx, c)
	if err != nil {
		// Answered, but there is no inbound audio pipe: hang up rather than
		// hold a one-way line.
		_ = sess.Bye(context.Background())
		m.end(c, Failed, 0, err)
		return
	}
	defer ln.Close() //nolint:errcheck

	select {
	case <-c.hangupCh:
		byeCtx, cancelBye := context.WithTimeout(context.Background(), 5*time.Second)
		if err := sess.Bye(byeCtx); err != nil {
			log.Printf("[call %s] BYE: %v", c.ID, err)
		}
		cancelBye()
		m.end(c, LocalHangup, 0, nil)
	case <-sess.Context().Done():
		m.end(c, RemoteHangup, 0, nil)
	}
}

// up brings an answered call's media up, the same for both directions:
// the far end's audio mirrored to the listen destination (§2.3), audio_in
// opened and forwarded down the RTP leg (§2.2), then answered published.
// Both stop with ctx; the caller closes the returned audio_in once the call
// ends.
func (m *Manager) up(ctx context.Context, c *Call) (*audioin.Listener, error) {
	ln, err := audioin.New(m.cfg.AudioInPortMin, m.cfg.AudioInPortMax)
	if err != nil {
		return nil, err
	}
	go c.media.ReadLoop(ctx, func(payload []byte) {
		c.listener.Send(c.media.PayloadType(), payload)
	})

	// One sender owns the call: the first SSRC seen on audio_in becomes its
	// source; everything else (other senders, non-L16) is dropped.
	var owner atomic.Uint32
	var lastIn atomic.Int64 // UnixNano of the last frame forwarded from audio_in
	lastIn.Store(time.Now().UnixNano())
	go keepAlive(ctx, c.media, &lastIn)
	go ln.Read(ctx, func(pt uint8, ssrc uint32, payload []byte) {
		if pt != audioin.L16PayloadType {
			return
		}
		if !owner.CompareAndSwap(0, ssrc) && owner.Load() != ssrc {
			return
		}
		samples := audioin.L16ToSamples(payload)
		if len(samples) < 6 {
			return
		}
		// 48 kHz L16 to 8 kHz by averaging each run of 6, then the call's
		// G.711, out on the leg with its own sequence/timestamp/SSRC.
		if err := c.media.WritePayload(c.media.PayloadType().EncodePCM(rtp.DownsampleAvg6x(samples))); err != nil {
			log.Printf("[call %s] forward audio_in: %v", c.ID, err)
		}
		lastIn.Store(time.Now().UnixNano())
	})

	c.mu.Lock()
	c.answered = true
	c.answeredAt = time.Now()
	c.mu.Unlock()
	if m.OnAnswered != nil {
		m.OnAnswered(c)
	}
	m.publish(c, events.CallAnswered, map[string]any{
		"answered_at": c.answeredAt.UTC().Format(time.RFC3339Nano),
		"audio_in":    fmt.Sprintf("%s:%d", m.cfg.AudioInHost, ln.Port()),
	})
	return ln, nil
}

// silenceAfter is how long audio_in may stay quiet before keepAlive fills the
// call: three frames, past a paced sender's jitter, so fill never lands between
// frames of speech, and far inside the seconds after which a trunk drops a call
// whose RTP stopped.
const silenceAfter = 60 * time.Millisecond

// keepAlive holds the line (§2.2): while nothing arrives at audio_in, the call
// carries G.711 silence at one packet per 20 ms, on the leg's own stream.
func keepAlive(ctx context.Context, media *rtp.Session, lastIn *atomic.Int64) {
	silence := media.PayloadType().EncodePCM(make([]int16, rtp.SamplesPerFrame))
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if time.Since(time.Unix(0, lastIn.Load())) >= silenceAfter {
			_ = media.WritePayload(silence)
		}
	}
}

// runInbound drives a taken call inside its INVITE handler: while it rings
// it waits for answer (200 OK), hangup (603), the caller giving up (CANCEL,
// answered 487 by sipgo) or the answer timeout (480); once answered it
// carries the audio both ways and ends on either hangup or RTP silence
// (CONTRACT §6.1). Each final response is written before the handler can
// return, and a final non-2xx waits for its ACK.
func (m *Manager) runInbound(c *Call, dlg *sipgo.DialogServerSession, remote *net.UDPAddr) {
	defer m.wg.Done()
	defer c.media.Close() //nolint:errcheck
	defer dlg.Close()     //nolint:errcheck

	timeout := time.NewTimer(m.inboundAnswerTimeout())
	defer timeout.Stop()
	select {
	case <-c.hangupCh:
		m.end(c, Declined, sip.StatusGlobalDecline, nil)
		_ = dlg.Respond(sip.StatusGlobalDecline, "Decline", nil)
		return
	case <-dlg.Context().Done():
		m.end(c, Abandoned, 0, nil)
		return
	case <-timeout.C:
		m.end(c, Unattended, sip.StatusTemporarilyUnavailable, nil)
		_ = dlg.Respond(sip.StatusTemporarilyUnavailable, "Temporarily Unavailable", nil)
		return
	case <-c.answerCh:
	}

	// 200 OK with the one-codec SDP; sipgo retransmits it until the ACK.
	c.media.Answered(remote, c.codec)
	if err := dlg.RespondSDP(sdp.BuildAnswer(m.cfg.AdvertiseHost, c.media.LocalPort(), c.codec)); err != nil {
		// The dialog can end while the 200 waits for its ACK: a CANCEL that
		// won the race with answer, or the caller's BYE right after its ACK,
		// which sipgo may see before the ACK. Only the first is abandoned.
		if errors.Is(context.Cause(dlg.Context()), sip.ErrTransactionCanceled) {
			m.end(c, Abandoned, 0, nil)
			return
		}
		if dlg.Context().Err() != nil {
			m.end(c, RemoteHangup, 0, nil)
			return
		}
		m.byeInbound(c, dlg)
		m.end(c, Failed, 0, fmt.Errorf("200 OK: %w", err))
		return
	}

	upCtx, stopUp := context.WithCancel(context.Background())
	defer stopUp()
	ln, err := m.up(upCtx, c)
	if err != nil {
		m.byeInbound(c, dlg)
		m.end(c, Failed, 0, err)
		return
	}
	defer ln.Close() //nolint:errcheck

	// The call is up. End it on either hangup, or on the caller's silence:
	// an answered inbound call that carries no inbound RTP for
	// RTP_IDLE_TIMEOUT ends failed (§6.1).
	idle := m.rtpIdleTimeout()
	ticker := time.NewTicker(max(idle/10, 50*time.Millisecond))
	defer ticker.Stop()
	for {
		select {
		case <-c.hangupCh:
			m.byeInbound(c, dlg)
			m.end(c, LocalHangup, 0, nil)
			return
		case <-dlg.Context().Done():
			// The caller's BYE: OnBye answered it 200 and ended the dialog.
			m.end(c, RemoteHangup, 0, nil)
			return
		case <-ticker.C:
			if time.Since(c.media.LastInbound()) >= idle {
				m.byeInbound(c, dlg)
				m.end(c, Failed, 0, fmt.Errorf("no inbound RTP for %s", idle))
				return
			}
		}
	}
}

func (m *Manager) byeInbound(c *Call, dlg *sipgo.DialogServerSession) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := dlg.Bye(ctx); err != nil {
		log.Printf("[call %s] BYE: %v", c.ID, err)
	}
}

// notAnswered maps how WaitAnswer failed onto an end reason.
func notAnswered(err, cause error) (string, int) {
	var dr *sipgo.ErrDialogResponse
	if errors.As(err, &dr) {
		switch code := int(dr.Res.StatusCode); code {
		case sip.StatusBusyHere, sip.StatusGlobalBusyEverywhere:
			return Busy, code
		case sip.StatusGlobalDecline:
			return Declined, code
		default:
			return Failed, code
		}
	}
	switch {
	case errors.Is(cause, errHangup):
		return Cancelled, 0
	case errors.Is(cause, errNoAnswer):
		return NoAnswer, 0
	}
	return Failed, 0
}

func (m *Manager) end(c *Call, reason string, status int, err error) {
	if err != nil {
		log.Printf("[call %s] ended %s (%d): %v", c.ID, reason, status, err)
	}
	var dur int64
	c.mu.Lock()
	if c.answered {
		dur = time.Since(c.answeredAt).Milliseconds()
	}
	c.answered = false
	c.mu.Unlock()
	if m.OnEnding != nil {
		m.OnEnding(c)
	}
	if c.dlg != nil {
		m.inboundDlg.CompareAndDelete(c.dlg, c)
	}
	m.forget(c.ID)
	m.publish(c, events.CallEnded, map[string]any{"reason": reason, "sip_status": status, "duration_ms": dur})
}

func (m *Manager) forget(id string) {
	m.mu.Lock()
	delete(m.calls, id)
	m.mu.Unlock()
}

func (m *Manager) publish(c *Call, typ string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["call_id"] = c.ID
	e := events.Event{Type: typ, Subject: c.ID, Data: data, Traceparent: c.traceparent, Time: time.Now()}
	if err := m.pub.Publish(context.Background(), e); err != nil {
		log.Printf("[call %s] publish %s: %v", c.ID, typ, err)
	}
}

// newCallID is an id phone makes up: outbound "call-" plus 8 hex, inbound
// "call-in-" plus 8 hex (CONTRACT §3, §6.1).
func newCallID(prefix string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
