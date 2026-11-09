package call

// Inbound calls (contract revision 4: §2.4, §2.5, §2.6, §3, §5, §6.1). Every
// test drives phone over loopback SIP from farend.Caller, as a trunk would;
// the expected codes, reasons and codecs are the contract's numbered
// behaviours, not this code's.

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	pionrtp "github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/events"
	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/farend"
)

func phoneAddr(m *Manager) string { return fmt.Sprintf("127.0.0.1:%d", m.cfg.SIPPort) }

// incoming waits for the next phone.call.incoming and returns it.
func incoming(t *testing.T, rec *recorder) events.Event {
	t.Helper()
	select {
	case e := <-rec.ch:
		require.Equal(t, events.CallIncoming, e.Type, "first event of an inbound call")
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no phone.call.incoming")
	}
	return events.Event{}
}

// noEvents asserts phone published nothing within the wait.
func noEvents(t *testing.T, rec *recorder, wait time.Duration) {
	t.Helper()
	select {
	case e := <-rec.ch:
		t.Fatalf("expected no events, got %s", e.Type)
	case <-time.After(wait):
	}
}

func answerCode(t *testing.T, err error) string {
	t.Helper()
	var e *Error
	require.True(t, errors.As(err, &e), "%v", err)
	return e.Code
}

// sdpCodecs are the payload types of the m=audio line of an SDP body.
func sdpCodecs(t *testing.T, body []byte) []string {
	t.Helper()
	for _, line := range strings.Split(string(body), "\r\n") {
		if strings.HasPrefix(line, "m=audio ") {
			return strings.Fields(line)[3:]
		}
	}
	t.Fatalf("no m=audio line in %q", body)
	return nil
}

func toTag(r *sip.Response) string {
	tag, _ := r.To().Params.Get("tag")
	return tag
}

func response(t *testing.T, call *farend.Call, code int) *sip.Response {
	t.Helper()
	for _, r := range call.Responses() {
		if int(r.StatusCode) == code {
			return r
		}
	}
	t.Fatalf("no %d among %v", code, call.Codes())
	return nil
}

// Behaviour 1: 100, 180, incoming{from, to, offered_at}; no 200 until answer.
func TestInboundRingsAndReportsIncoming(t *testing.T) {
	m, rec := newPhone(t, farend.Start(t), nil)
	caller := farend.NewCaller(t, phoneAddr(m))

	call := caller.Invite("+380501112233", "380441234567", farend.OfferSDP(caller.MediaPort, "0", "8"))
	e := incoming(t, rec)

	id := e.Data["call_id"].(string)
	assert.True(t, strings.HasPrefix(id, "call-in-"), id)
	assert.Equal(t, id, e.Subject)
	assert.Equal(t, "+380501112233", e.Data["from"])
	assert.Equal(t, "380441234567", e.Data["to"])
	_, err := time.Parse(time.RFC3339Nano, e.Data["offered_at"].(string))
	assert.NoError(t, err, "offered_at is a timestamp")

	assert.Eventually(t, func() bool { return len(call.Codes()) >= 2 }, 2*time.Second, 10*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, []int{100, 180}, call.Codes(), "rings and waits: no final response before answer")
	select {
	case <-call.Done:
		t.Fatal("INVITE ended before answer")
	default:
	}

	m.Hangup(id)
	call.Wait(t)
	rec.until(t, id)
}

// Behaviour 2: answer → 200 with exactly one codec, the first G.711 in the
// offer's order; answered{audio_in}; 180 and 200 share a To tag and carry
// Contact.
func TestAnswerPicksTheOffersFirstG711(t *testing.T) {
	for _, tc := range []struct {
		offer []string
		want  string
	}{
		{[]string{"0", "8"}, "0"},
		{[]string{"8", "0"}, "8"},
		{[]string{"18", "8", "0", "101"}, "8"},
	} {
		t.Run(strings.Join(tc.offer, " "), func(t *testing.T) {
			m, rec := newPhone(t, farend.Start(t), nil)
			caller := farend.NewCaller(t, phoneAddr(m))
			call := caller.Invite("100", "200", farend.OfferSDP(caller.MediaPort, tc.offer...))
			id := incoming(t, rec).Subject

			require.NoError(t, m.Answer(id))
			call.Wait(t)
			require.NoError(t, call.Err)

			ok := response(t, call, 200)
			assert.Equal(t, []string{tc.want}, sdpCodecs(t, ok.Body()), "one codec in the answer")
			ringing := response(t, call, 180)
			assert.NotEmpty(t, toTag(ringing))
			assert.Equal(t, toTag(ringing), toTag(ok), "one To tag on 180 and 200")
			assert.NotNil(t, ringing.Contact(), "Contact on 180")
			assert.NotNil(t, ok.Contact(), "Contact on 200")

			e := <-rec.ch
			require.Equal(t, events.CallAnswered, e.Type)
			_, port, err := net.SplitHostPort(e.Data["audio_in"].(string))
			require.NoError(t, err, "audio_in is host:port")
			assert.NotEqual(t, "0", port)

			m.Hangup(id)
			assert.Equal(t, []string{events.CallEnded}, types(rec.until(t, id)))
		})
	}
}

// Behaviour 3 (§2.4): hangup while ringing → 603, ended{declined, 603}.
func TestHangupOfRingingInboundDeclines(t *testing.T) {
	m, rec := newPhone(t, farend.Start(t), nil)
	caller := farend.NewCaller(t, phoneAddr(m))
	call := caller.Invite("100", "200", farend.OfferSDP(caller.MediaPort, "8"))
	id := incoming(t, rec).Subject

	m.Hangup(id)
	call.Wait(t)
	assert.Equal(t, 603, call.Codes()[len(call.Codes())-1])
	es := rec.until(t, id)
	assert.Equal(t, []string{events.CallEnded}, types(es))
	d := ended(t, es)
	assert.Equal(t, Declined, d["reason"])
	assert.Equal(t, 603, d["sip_status"])
}

// Behaviour 4: CANCEL before answer → 487 to the INVITE, ended{abandoned}.
func TestCallerCancelIsAbandoned(t *testing.T) {
	m, rec := newPhone(t, farend.Start(t), nil)
	caller := farend.NewCaller(t, phoneAddr(m))
	call := caller.Invite("100", "200", farend.OfferSDP(caller.MediaPort, "8"))
	id := incoming(t, rec).Subject

	call.Cancel()
	call.Wait(t)
	assert.Contains(t, call.Codes(), 487)
	d := ended(t, rec.until(t, id))
	assert.Equal(t, Abandoned, d["reason"])
	assert.Equal(t, 0, d["sip_status"])
}

// Behaviour 5: nobody answers within INBOUND_ANSWER_TIMEOUT → 480,
// ended{unattended, 480}.
func TestUnansweredInboundIsUnattended(t *testing.T) {
	m, rec := newPhone(t, farend.Start(t), func(c *Config) { c.InboundAnswerTimeout = 300 * time.Millisecond })
	caller := farend.NewCaller(t, phoneAddr(m))
	call := caller.Invite("100", "200", farend.OfferSDP(caller.MediaPort, "8"))
	id := incoming(t, rec).Subject

	call.Wait(t)
	assert.Equal(t, 480, call.Codes()[len(call.Codes())-1])
	d := ended(t, rec.until(t, id))
	assert.Equal(t, Unattended, d["reason"])
	assert.Equal(t, 480, d["sip_status"])
}

// Behaviour 6 (§5): outbound and inbound share PHONE_MAX_CALLS; over it an
// INVITE gets 503 and no events.
func TestInboundOverTheLimitIs503(t *testing.T) {
	fe := farend.Start(t)
	m, rec := newPhone(t, fe, func(c *Config) { c.MaxCalls = 1 })

	out, err := m.Dial(DialRequest{To: farend.RingForever})
	require.NoError(t, err)
	require.Equal(t, events.CallRinging, (<-rec.ch).Type)

	caller := farend.NewCaller(t, phoneAddr(m))
	call := caller.Invite("100", "200", farend.OfferSDP(caller.MediaPort, "8"))
	call.Wait(t)
	assert.Equal(t, []int{503}, call.Codes(), "an outbound call holds the only line")
	noEvents(t, rec, 200*time.Millisecond)

	m.Hangup(out)
	rec.until(t, out)

	// And the other way round: a ringing inbound call holds the line for dial.
	call = caller.Invite("100", "200", farend.OfferSDP(caller.MediaPort, "8"))
	id := incoming(t, rec).Subject
	_, err = m.Dial(DialRequest{To: farend.RingForever})
	assert.Equal(t, CodeCapacity, answerCode(t, err))
	m.Hangup(id)
	call.Wait(t)
	rec.until(t, id)
}

// Behaviour 7: SIP_INBOUND_ALLOW set and the source not on it → 403, no
// events; on it → the call rings.
func TestInboundAllowlist(t *testing.T) {
	m, rec := newPhone(t, farend.Start(t), func(c *Config) { c.InboundAllow = []string{"192.0.2.10"} })
	caller := farend.NewCaller(t, phoneAddr(m))
	call := caller.Invite("100", "200", farend.OfferSDP(caller.MediaPort, "8"))
	call.Wait(t)
	assert.Equal(t, []int{403}, call.Codes())
	noEvents(t, rec, 200*time.Millisecond)

	m2, rec2 := newPhone(t, farend.Start(t), func(c *Config) { c.InboundAllow = []string{"192.0.2.10", "127.0.0.1"} })
	caller2 := farend.NewCaller(t, phoneAddr(m2))
	call2 := caller2.Invite("100", "200", farend.OfferSDP(caller2.MediaPort, "8"))
	id := incoming(t, rec2).Subject
	m2.Hangup(id)
	call2.Wait(t)
	rec2.until(t, id)
}

// Behaviour 8: an offer with neither PCMU nor PCMA → 488, no events.
func TestOfferWithoutG711Is488(t *testing.T) {
	m, rec := newPhone(t, farend.Start(t), nil)
	caller := farend.NewCaller(t, phoneAddr(m))
	call := caller.Invite("100", "200", farend.OfferSDP(caller.MediaPort, "18", "101"))
	call.Wait(t)
	assert.Equal(t, []int{488}, call.Codes())
	noEvents(t, rec, 200*time.Millisecond)
}

// answeredInbound places a call to phone, answers it and returns its id
// with the answered event's audio_in.
func answeredInbound(t *testing.T, m *Manager, rec *recorder, caller *farend.Caller, offer ...string) (*farend.Call, string, string) {
	t.Helper()
	call := caller.Invite("100", "200", farend.OfferSDP(caller.MediaPort, offer...))
	id := incoming(t, rec).Subject
	require.NoError(t, m.Answer(id))
	call.Wait(t)
	require.NoError(t, call.Err)
	e := <-rec.ch
	require.Equal(t, events.CallAnswered, e.Type)
	return call, id, e.Data["audio_in"].(string)
}

// Behaviour 9: the caller's BYE → ended{remote-hangup}; our hangup → BYE to
// the caller, ended{local-hangup}.
func TestAnsweredInboundHangups(t *testing.T) {
	m, rec := newPhone(t, farend.Start(t), nil)
	caller := farend.NewCaller(t, phoneAddr(m))

	call, id, _ := answeredInbound(t, m, rec, caller, "8")
	call.Bye(t)
	assert.Equal(t, RemoteHangup, ended(t, rec.until(t, id))["reason"])

	_, id, _ = answeredInbound(t, m, rec, caller, "8")
	m.Hangup(id)
	assert.Equal(t, LocalHangup, ended(t, rec.until(t, id))["reason"])
	select {
	case <-caller.GotBye:
	case <-time.After(3 * time.Second):
		t.Fatal("the caller got no BYE")
	}
}

// Behaviour 10 (§2.5): answer of an outbound, answered or ended call →
// not_ringing; of an id phone never gave → unknown_call.
func TestAnswerRefusals(t *testing.T) {
	fe := farend.Start(t)
	m, rec := newPhone(t, fe, nil)

	out, err := m.Dial(DialRequest{To: farend.RingForever})
	require.NoError(t, err)
	assert.Equal(t, CodeNotRinging, answerCode(t, m.Answer(out)), "outbound")
	m.Hangup(out)
	rec.until(t, out)

	caller := farend.NewCaller(t, phoneAddr(m))
	_, id, _ := answeredInbound(t, m, rec, caller, "8")
	assert.Equal(t, CodeNotRinging, answerCode(t, m.Answer(id)), "already answered")
	m.Hangup(id)
	rec.until(t, id)
	assert.Equal(t, CodeNotRinging, answerCode(t, m.Answer(id)), "ended")

	assert.Equal(t, CodeUnknownCall, answerCode(t, m.Answer("call-in-00000000")))
}

// Behaviour 11: OPTIONS → 200 with Allow; UPDATE → 200; a re-INVITE with a
// new address → 200 with the same codec, and phone's RTP goes there.
func TestOptionsUpdateAndReInvite(t *testing.T) {
	m, rec := newPhone(t, farend.Start(t), nil)
	caller := farend.NewCaller(t, phoneAddr(m))

	res := caller.Request(t, sip.OPTIONS)
	assert.Equal(t, 200, int(res.StatusCode))
	require.NotNil(t, res.GetHeader("Allow"))
	assert.Contains(t, res.GetHeader("Allow").Value(), "INVITE")
	assert.Equal(t, 200, int(caller.Request(t, sip.UPDATE).StatusCode))

	call, id, audioIn := answeredInbound(t, m, rec, caller, "0", "8")
	moved, movedPort := farend.ListenUDP(t)
	res = call.ReInvite(t, farend.OfferSDP(movedPort, "8", "0"))
	require.Equal(t, 200, int(res.StatusCode))
	assert.Equal(t, []string{"0"}, sdpCodecs(t, res.Body()), "the re-INVITE keeps the call's codec")

	_, port, err := net.SplitHostPort(audioIn)
	require.NoError(t, err)
	sendL16(t, "127.0.0.1:"+port, 5)
	pkts := readRTP(t, moved, 1, 2*time.Second)
	assert.Equal(t, uint8(0), pkts[0].PayloadType, "PCMU, the call's codec, at the new address")

	m.Hangup(id)
	rec.until(t, id)
}

// sendL16 sends n 20 ms packets of 48 kHz L16 into a call's audio_in, as
// speech does (§2.2).
func sendL16(t *testing.T, to string, n int) {
	t.Helper()
	dst, err := net.ResolveUDPAddr("udp", to)
	require.NoError(t, err)
	conn, err := net.DialUDP("udp", nil, dst)
	require.NoError(t, err)
	defer conn.Close() //nolint:errcheck
	for i := 0; i < n; i++ {
		pkt := pionrtp.Packet{
			Header:  pionrtp.Header{Version: 2, PayloadType: 96, SequenceNumber: uint16(i), Timestamp: uint32(i) * 960, SSRC: 0x5BEEC4},
			Payload: make([]byte, 960*2),
		}
		buf, err := pkt.Marshal()
		require.NoError(t, err)
		_, err = conn.Write(buf)
		require.NoError(t, err)
	}
}

// sendG711 sends one PCMU packet from the caller's media socket to phone's
// media port, as a trunk sends the caller's voice.
func sendG711(t *testing.T, caller *farend.Caller, to *net.UDPAddr, seq uint16, payload []byte) {
	t.Helper()
	pkt := pionrtp.Packet{
		Header:  pionrtp.Header{Version: 2, PayloadType: 0, SequenceNumber: seq, Timestamp: uint32(seq) * 160, SSRC: 0xCA11E4},
		Payload: payload,
	}
	buf, err := pkt.Marshal()
	require.NoError(t, err)
	_, err = caller.Media.WriteToUDP(buf, to)
	require.NoError(t, err)
}

// phoneMedia is phone's RTP address from the 200 OK's SDP.
func phoneMedia(t *testing.T, call *farend.Call) *net.UDPAddr {
	t.Helper()
	for _, line := range strings.Split(string(response(t, call, 200).Body()), "\r\n") {
		if strings.HasPrefix(line, "m=audio ") {
			port := strings.Fields(line)[1]
			addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:"+port)
			require.NoError(t, err)
			return addr
		}
	}
	t.Fatal("no m=audio in the 200 OK")
	return nil
}

// Behaviour 12: an answered call ends failed, with a BYE, when no inbound RTP
// comes for RTP_IDLE_TIMEOUT — and not while it keeps coming.
func TestInboundRTPIdleEndsTheCall(t *testing.T) {
	m, rec := newPhone(t, farend.Start(t), func(c *Config) { c.RTPIdleTimeout = 400 * time.Millisecond })
	caller := farend.NewCaller(t, phoneAddr(m))
	call, id, _ := answeredInbound(t, m, rec, caller, "0")
	media := phoneMedia(t, call)

	for i := 0; i < 20; i++ { // 1 s of voice, well past the idle timeout
		sendG711(t, caller, media, uint16(i), make([]byte, 160))
		time.Sleep(50 * time.Millisecond)
	}
	noEvents(t, rec, 0)
	require.NotNil(t, m.Get(id), "a call carrying RTP stays up")

	d := ended(t, rec.until(t, id))
	assert.Equal(t, Failed, d["reason"])
	select {
	case <-caller.GotBye:
	case <-time.After(3 * time.Second):
		t.Fatal("the caller got no BYE")
	}
}

// Behaviour 13 (§3): every incoming ends in exactly one ended, its last
// event — even when both sides hang up at once and hangup comes again after.
func TestInboundEndsExactlyOnce(t *testing.T) {
	m, rec := newPhone(t, farend.Start(t), nil)
	caller := farend.NewCaller(t, phoneAddr(m))

	call := caller.Invite("100", "200", farend.OfferSDP(caller.MediaPort, "8"))
	first := incoming(t, rec)
	require.NoError(t, m.Answer(first.Subject))
	call.Wait(t)

	byeDone := make(chan struct{})
	go func() { defer close(byeDone); _ = call.Sess.Bye(t.Context()) }()
	m.Hangup(first.Subject)
	<-byeDone
	m.Hangup(first.Subject)

	// Whether answered goes out depends on which end phone sees first; the
	// call was picked up either way, so it is never abandoned.
	es := append([]events.Event{first}, rec.until(t, first.Subject)...)
	assert.Contains(t, [][]string{
		{events.CallIncoming, events.CallAnswered, events.CallEnded},
		{events.CallIncoming, events.CallEnded},
	}, types(es))
	assert.Contains(t, []any{RemoteHangup, LocalHangup}, ended(t, es)["reason"])
	noEvents(t, rec, 300*time.Millisecond)
}

// Behaviour 14 (§2.3): listen works on an answered inbound call — the
// caller's G.711 as it arrives, in the call's payload type.
func TestListenOnInboundCall(t *testing.T) {
	m, rec := newPhone(t, farend.Start(t), nil)
	caller := farend.NewCaller(t, phoneAddr(m))
	call, id, _ := answeredInbound(t, m, rec, caller, "0", "8")

	ear, earPort := farend.ListenUDP(t)
	require.NoError(t, m.Listen(id, fmt.Sprintf("127.0.0.1:%d", earPort)))
	media := phoneMedia(t, call)
	voice := make([]byte, 160)
	for i := range voice {
		voice[i] = byte(i)
	}
	for i := 0; i < 3; i++ {
		sendG711(t, caller, media, uint16(i), voice)
	}
	pkts := readRTP(t, ear, 3, 2*time.Second)
	for _, p := range pkts {
		assert.Equal(t, uint8(0), p.PayloadType, "PCMU, as the call carries it")
		assert.Equal(t, voice, p.Payload, "the caller's audio, byte for byte")
	}

	m.Hangup(id)
	rec.until(t, id)
}

// A c= phone cannot send to — 0.0.0.0 behind carrier NAT, or a hostname —
// is replaced by the caller's own address: phone's RTP reaches the caller
// before the caller has sent any (the old SIP server's behaviour, paid for at
// a carrier SBC).
func TestUnusableConnectionAddressFallsBackToTheCaller(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "sbc.example.net"} {
		t.Run(host, func(t *testing.T) {
			m, rec := newPhone(t, farend.Start(t), nil)
			caller := farend.NewCaller(t, phoneAddr(m))
			_ = caller.Invite("100", "200", farend.OfferSDPHost(host, caller.MediaPort, "8"))
			id := incoming(t, rec).Subject
			require.NoError(t, m.Answer(id))
			e := <-rec.ch
			require.Equal(t, events.CallAnswered, e.Type)
			_, port, err := net.SplitHostPort(e.Data["audio_in"].(string))
			require.NoError(t, err)

			sendL16(t, "127.0.0.1:"+port, 3)
			pkts := readRTP(t, caller.Media, 1, 2*time.Second)
			assert.Equal(t, uint8(8), pkts[0].PayloadType)

			m.Hangup(id)
			rec.until(t, id)
		})
	}
}
