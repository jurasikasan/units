package speech

// White-box test for the one-socket-per-session rule: the session's UDP
// socket is opened with the session and closed when it ends, so a datagram
// through it afterwards fails.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jurasikasan/units/abilities/speaking/internal/events"
	"github.com/jurasikasan/units/abilities/speaking/internal/tts"
)

type nullPub struct{}

func (nullPub) Publish(context.Context, events.Event) error { return nil }

// fakeVoices is a TTS client nothing is asked of: these tests open and close.
func fakeVoices(t *testing.T, _ ...string) *tts.Client {
	t.Helper()
	return tts.New("http://unused.invalid", "")
}

func TestSessionEnd_ClosesItsSocket(t *testing.T) {
	sp := New(Config{MaxSessions: 1}, fakeVoices(t, "piper_v1"), nullPub{})
	id, err := sp.Open(context.Background(), "127.0.0.1:9", "piper_v1", "", "")
	require.NoError(t, err)

	sp.mu.Lock()
	sess := sp.sessions[id]
	sp.mu.Unlock()
	require.NotNil(t, sess)
	require.NotNil(t, sess.sink)
	sink := sess.sink

	require.NoError(t, sp.Close(id))
	deadline := time.Now().Add(5 * time.Second)
	for !sess.closed() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.True(t, sess.closed(), "the session has ended")

	f := make([]int16, frameSamples)
	assert.Error(t, sink.WriteFrame(f),
		"a datagram through the socket of an ended session fails: the socket is closed")
}
