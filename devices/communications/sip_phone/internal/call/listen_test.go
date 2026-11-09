package call

// listen tests (contract revision 3, §2.3; expected values from the task, not
// from this code): after answer the far end's G.711 arrives at the listener
// byte-for-byte and in order, with the call's payload type, phone's own
// sequence (+1 per packet) and timestamp (+160 per 20 ms packet), one SSRC
// per call; a second listen moves the stream to the new destination and the
// old one gets nothing; to:"" stops it; hangup stops it. Before answered the
// call refuses with not_answered, an unknown id with unknown_call, and a
// target that is not a resolvable host:port with bad_target.

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	pionrtp "github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/events"
	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/farend"
)

// readRTP reads n RTP packets from a loopback listener.
func readRTP(t *testing.T, conn net.PacketConn, n int, timeout time.Duration) []pionrtp.Packet {
	t.Helper()
	var out []pionrtp.Packet
	for i := 0; i < n; i++ {
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(timeout)))
		buf := make([]byte, 1500)
		recv, _, err := conn.ReadFrom(buf)
		require.NoErrorf(t, err, "packet %d of %d must arrive", i+1, n)
		var p pionrtp.Packet
		require.NoError(t, p.Unmarshal(buf[:recv]))
		out = append(out, p)
	}
	return out
}

// noMoreRTP asserts that no RTP packet arrives within the wait.
func noMoreRTP(t *testing.T, conn net.PacketConn, wait time.Duration) {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(wait)))
	_, _, err := conn.ReadFrom(make([]byte, 1500))
	assert.Error(t, err, "expected no more RTP")
}

func listenCode(t *testing.T, m *Manager, callID, to string) string {
	t.Helper()
	err := m.Listen(callID, to)
	var e *Error
	require.True(t, errors.As(err, &e), "%v", err)
	return e.Code
}

func waitAnswered(t *testing.T, rec *recorder, id string) {
	t.Helper()
	for e := range rec.ch {
		if e.Subject == id && e.Type == events.CallAnswered {
			return
		}
	}
	t.Fatal("no answered event")
}

func TestListenStreamsFarEndAudio(t *testing.T) {
	fe := farend.Start(t)
	m, rec := newPhone(t, fe, nil)

	id, err := m.Dial(DialRequest{To: farend.AnswerAndHold})
	require.NoError(t, err)
	waitAnswered(t, rec, id)
	require.True(t, m.Get(id).Answered())

	a, aPort := farend.ListenUDP(t)
	b, bPort := farend.ListenUDP(t)
	require.NoError(t, m.Listen(id, fmt.Sprintf("127.0.0.1:%d", aPort)))

	// A 20 ms G.711 frame: 160 bytes, known content.
	payload := make([]byte, 160)
	for i := range payload {
		payload[i] = byte(i*7 + 13)
	}

	// 50 known G.711 packets from the far end...
	fe.SendRTP(t, 50, payload)
	got := readRTP(t, a, 50, time.Second)

	// ...arrive at the listener byte-for-byte, in order, with the call's
	// payload type and phone's own headers.
	for i, p := range got {
		assert.Equal(t, uint8(8), p.PayloadType, "packet %d: the call carries PCMA per its SDP answer", i)
		assert.Equal(t, payload, p.Payload, "packet %d: the payload bytes must be unchanged", i)
		if i > 0 {
			assert.Equal(t, got[i-1].SequenceNumber+1, p.SequenceNumber, "packet %d: seq must advance by 1", i)
			assert.Equal(t, got[i-1].Timestamp+160, p.Timestamp, "packet %d: timestamp must advance by 160", i)
		}
		assert.Equal(t, got[0].SSRC, p.SSRC, "packet %d: one SSRC per call", i)
	}

	// A second listen moves the stream to the new destination...
	require.NoError(t, m.Listen(id, fmt.Sprintf("127.0.0.1:%d", bPort)))
	fe.SendRTP(t, 20, payload)
	moved := readRTP(t, b, 20, time.Second)
	assert.Equal(t, payload, moved[len(moved)-1].Payload, "the moved stream must still carry the bytes")
	noMoreRTP(t, a, 300*time.Millisecond)

	// ...and an empty to stops it: nothing more is sent anywhere.
	require.NoError(t, m.Listen(id, ""))
	fe.SendRTP(t, 10, payload)
	noMoreRTP(t, b, 300*time.Millisecond)
	noMoreRTP(t, a, 300*time.Millisecond)

	// Sending stops with the call.
	require.NoError(t, m.Listen(id, fmt.Sprintf("127.0.0.1:%d", bPort)))
	m.Hangup(id)
	rec.until(t, id)
	fe.SendRTP(t, 5, payload)
	noMoreRTP(t, b, 300*time.Millisecond)
}

func TestListenRefusals(t *testing.T) {
	fe := farend.Start(t)
	m, rec := newPhone(t, fe, nil)

	assert.Equal(t, CodeUnknownCall, listenCode(t, m, "call-ffffffff", "127.0.0.1:42000"))

	// Ringing, not answered: listen is refused with its code.
	id, err := m.Dial(DialRequest{To: farend.RingForever})
	require.NoError(t, err)
	first := <-rec.ch
	require.Equal(t, events.CallRinging, first.Type)
	assert.Equal(t, CodeNotAnswered, listenCode(t, m, id, "127.0.0.1:42000"))
	m.Hangup(id)
	rec.until(t, id)

	// Answered: only a bad target is refused.
	id, err = m.Dial(DialRequest{To: farend.AnswerAndHold})
	require.NoError(t, err)
	waitAnswered(t, rec, id)
	for _, to := range []string{"x", "127.0.0.1", ":42000", "127.0.0.1:99999"} {
		assert.Equal(t, CodeBadTarget, listenCode(t, m, id, to), "to=%q", to)
	}
	// A bad target leaves the call listening to nothing, still usable.
	_, lnPort := farend.ListenUDP(t)
	require.NoError(t, m.Listen(id, fmt.Sprintf("127.0.0.1:%d", lnPort)))
	m.Hangup(id)
	rec.until(t, id)
}
