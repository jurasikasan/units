package call

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/events"
	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/farend"
)

// Expected events and end reasons come from phone/CONTRACT.md §3.

type recorder struct {
	mu  sync.Mutex
	all []events.Event
	ch  chan events.Event
}

func (r *recorder) Publish(_ context.Context, e events.Event) error {
	r.mu.Lock()
	r.all = append(r.all, e)
	r.mu.Unlock()
	r.ch <- e
	return nil
}

// until reads events of one call until its ended, and returns them in order.
func (r *recorder) until(t *testing.T, callID string) []events.Event {
	t.Helper()
	var got []events.Event
	deadline := time.After(10 * time.Second)
	for {
		select {
		case e := <-r.ch:
			if e.Subject != callID {
				continue
			}
			got = append(got, e)
			if e.Type == events.CallEnded {
				return got
			}
		case <-deadline:
			t.Fatalf("no %s for %s; got %v", events.CallEnded, callID, types(got))
		}
	}
}

func types(es []events.Event) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Type
	}
	return out
}

func newPhone(t *testing.T, fe *farend.FarEnd, mod func(*Config)) (*Manager, *recorder) {
	t.Helper()
	conn, port := farend.ListenUDP(t)
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("phone-test"))
	require.NoError(t, err)
	srv, err := sipgo.NewServer(ua)
	require.NoError(t, err)
	cli, err := sipgo.NewClient(ua, sipgo.WithClientHostname("127.0.0.1"), sipgo.WithClientPort(port))
	require.NoError(t, err)

	cfg := Config{
		Trunk: fe.Addr, AdvertiseHost: "127.0.0.1", SIPPort: port,
		MaxCalls: 4, RingDefault: 3 * time.Second, RingMax: 5 * time.Second,
		Username: farend.User, Password: farend.Password, StripPlus: true,
	}
	if mod != nil {
		mod(&cfg)
	}
	rec := &recorder{ch: make(chan events.Event, 64)}
	m := NewManager(cfg, cli, rec)
	srv.OnBye(m.OnBye)
	srv.OnInvite(m.OnInvite)
	srv.OnAck(m.OnAck)
	srv.OnOptions(m.OnOptions)
	srv.OnUpdate(m.OnUpdate)
	go srv.ServeUDP(conn) //nolint:errcheck
	t.Cleanup(func() {
		m.Wait()
		_ = ua.Close()
		_ = conn.Close()
	})
	return m, rec
}

func ended(t *testing.T, es []events.Event) map[string]any {
	t.Helper()
	require.NotEmpty(t, es)
	last := es[len(es)-1]
	require.Equal(t, events.CallEnded, last.Type)
	return last.Data
}

func TestRemoteHangupAfterAnswer(t *testing.T) {
	fe := farend.Start(t)
	m, rec := newPhone(t, fe, nil)

	id, err := m.Dial(DialRequest{To: farend.AnswerThenBye, Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"})
	require.NoError(t, err)
	es := rec.until(t, id)

	assert.Equal(t, []string{events.CallRinging, events.CallAnswered, events.CallEnded}, types(es))
	d := ended(t, es)
	assert.Equal(t, RemoteHangup, d["reason"])
	assert.Equal(t, 0, d["sip_status"])
	assert.Greater(t, d["duration_ms"], int64(0))
	for _, e := range es {
		assert.Equal(t, id, e.Data["call_id"])
		assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", e.Traceparent)
	}
	assert.Nil(t, m.Get(id), "an ended call is forgotten")
}

func TestBusy(t *testing.T) {
	fe := farend.Start(t)
	m, rec := newPhone(t, fe, nil)
	id, err := m.Dial(DialRequest{To: farend.Busy})
	require.NoError(t, err)
	es := rec.until(t, id)

	assert.Equal(t, []string{events.CallRinging, events.CallEnded}, types(es))
	d := ended(t, es)
	assert.Equal(t, Busy, d["reason"])
	assert.Equal(t, 486, d["sip_status"])
}

func TestDeclined(t *testing.T) {
	fe := farend.Start(t)
	m, rec := newPhone(t, fe, nil)
	id, err := m.Dial(DialRequest{To: farend.Decline})
	require.NoError(t, err)
	d := ended(t, rec.until(t, id))
	assert.Equal(t, Declined, d["reason"])
	assert.Equal(t, 603, d["sip_status"])
}

func TestOtherFinalResponseIsFailedWithItsCode(t *testing.T) {
	fe := farend.Start(t)
	m, rec := newPhone(t, fe, nil)
	id, err := m.Dial(DialRequest{To: farend.NotFound})
	require.NoError(t, err)
	d := ended(t, rec.until(t, id))
	assert.Equal(t, Failed, d["reason"])
	assert.Equal(t, 404, d["sip_status"])
}

func TestNoAnswerSendsCancel(t *testing.T) {
	fe := farend.Start(t)
	m, rec := newPhone(t, fe, nil)
	id, err := m.Dial(DialRequest{To: farend.RingForever, RingTimeout: 300 * time.Millisecond})
	require.NoError(t, err)
	es := rec.until(t, id)

	assert.Equal(t, []string{events.CallRinging, events.CallEnded}, types(es))
	assert.Equal(t, NoAnswer, ended(t, es)["reason"])
	assert.Eventually(t, fe.Cancelled.Load, 2*time.Second, 20*time.Millisecond, "far end got CANCEL")
}

func TestHangupWhileRingingIsCancelled(t *testing.T) {
	fe := farend.Start(t)
	m, rec := newPhone(t, fe, nil)
	id, err := m.Dial(DialRequest{To: farend.RingForever})
	require.NoError(t, err)

	first := <-rec.ch
	require.Equal(t, events.CallRinging, first.Type)
	m.Hangup(id)
	es := rec.until(t, id)

	assert.Equal(t, Cancelled, ended(t, es)["reason"])
	assert.Eventually(t, fe.Cancelled.Load, 2*time.Second, 20*time.Millisecond, "far end got CANCEL")
}

func TestLocalHangupAfterAnswerSendsBye(t *testing.T) {
	fe := farend.Start(t)
	m, rec := newPhone(t, fe, nil)
	id, err := m.Dial(DialRequest{To: farend.AnswerAndHold})
	require.NoError(t, err)

	for e := range rec.ch {
		if e.Type == events.CallAnswered {
			break
		}
	}
	require.NotNil(t, m.Get(id))
	assert.True(t, m.Get(id).Answered())
	m.Hangup(id)
	es := rec.until(t, id)

	assert.Equal(t, LocalHangup, ended(t, es)["reason"])
	select {
	case <-fe.GotBye:
	case <-time.After(2 * time.Second):
		t.Fatal("far end got no BYE")
	}
	m.Hangup(id) // a second hangup of an ended call is not an error and ends nothing
}

func TestDigestChallengeIsAnswered(t *testing.T) {
	fe := farend.Start(t)
	m, rec := newPhone(t, fe, nil)
	id, err := m.Dial(DialRequest{To: farend.Auth})
	require.NoError(t, err)
	es := rec.until(t, id)

	assert.Equal(t, int32(2), fe.Invites.Load(), "INVITE repeated with credentials")
	assert.True(t, fe.AuthOK.Load())
	assert.Equal(t, []string{events.CallRinging, events.CallAnswered, events.CallEnded}, types(es))
	assert.Equal(t, RemoteHangup, ended(t, es)["reason"])
}

func TestDialRefusals(t *testing.T) {
	fe := farend.Start(t)
	m, rec := newPhone(t, fe, func(c *Config) {
		c.MaxCalls = 1
		c.FromAllow = []string{"+380501112233"}
	})

	code := func(err error) string {
		var e *Error
		require.True(t, errors.As(err, &e), "%v", err)
		return e.Code
	}

	_, err := m.Dial(DialRequest{To: "0441234567", From: "+380501112233"})
	assert.Equal(t, CodeInvalidNumber, code(err))
	_, err = m.Dial(DialRequest{To: farend.RingForever, From: "SHOP"})
	assert.Equal(t, CodeFromNotAllowed, code(err))

	id, err := m.Dial(DialRequest{To: farend.RingForever, From: "+380501112233"})
	require.NoError(t, err)
	_, err = m.Dial(DialRequest{To: farend.RingForever, From: "+380501112233"})
	assert.Equal(t, CodeCapacity, code(err))

	m.Hangup(id)
	rec.until(t, id)
	_, err = m.Dial(DialRequest{To: farend.Busy, From: "+380501112233"})
	assert.NoError(t, err, "a line is free again once the call ended")
}

// A line with no trunk only takes calls: dial still returns a call_id, and
// the call ends failed with no SIP status — "no route" (CONTRACT.md §3).
func TestDialWithoutTrunkFails(t *testing.T) {
	m, rec := newPhone(t, farend.Start(t), func(c *Config) { c.Trunk = "" })
	id, err := m.Dial(DialRequest{To: farend.AnswerThenBye})
	require.NoError(t, err)
	d := ended(t, rec.until(t, id))
	assert.Equal(t, Failed, d["reason"])
	assert.Equal(t, 0, d["sip_status"])
}
