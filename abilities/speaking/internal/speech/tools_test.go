package speech_test

// The open/say/close tools as an MCP client sees them, over the SDK's
// in-memory transport. C0 §5 "Trace": the cause of every event of an organ's
// session is the open call that made the session, so every event of the
// session carries that call's traceparent — the one from the MCP call's
// metadata — and an open without one leaves no ce_traceparent at all.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jurasikasan/units/abilities/speaking/internal/events"
	"github.com/jurasikasan/units/abilities/speaking/internal/speech"
)

// tp1 and tp2 are the traceparents the test drives the opens with (C0 §5).
const (
	tp1 = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tp2 = "00-0af7651916cd43dd8448eb211c80319c-ba185263303d899f-01"
)

func connectTools(t *testing.T, sp *speech.Speech) *mcp.ClientSession {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	_, err := speech.Server(sp).Connect(ctx, st, nil)
	require.NoError(t, err)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, meta mcp.Meta) (map[string]any, string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args, Meta: meta})
	require.NoError(t, err)
	if res.IsError {
		return nil, res.Content[0].(*mcp.TextContent).Text
	}
	b, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(b, &out))
	return out, ""
}

// open → say → close through the MCP tools: every event of the session
// (utterance.spoken and session.closed) carries exactly the traceparent
// given to open in the call's _meta.
func TestOpenWithTraceparent_EventsCarryIt(t *testing.T) {
	g := newFakeGateway(t, "piper_denis")
	pub := &fakePub{}
	sp := newSpeech(t, speech.Config{}, g, pub)
	tap := startTap(t)
	cs := connectTools(t, sp)

	out, errText := callTool(t, cs, "open", map[string]any{"to": tap.addr(), "voice_name": "piper_denis"}, mcp.Meta{"traceparent": tp1})
	require.Empty(t, errText)
	id, _ := out["session_id"].(string)
	require.NotEmpty(t, id)

	out, errText = callTool(t, cs, "speak", map[string]any{"session_id": id, "text": "Привет!"}, nil)
	require.Empty(t, errText)
	require.NotEmpty(t, out)

	waitFor(t, 15*time.Second, "the spoken event", func() bool {
		l := pub.list()
		return len(l) >= 1 && l[0].Type == events.SpeakComplete
	})

	out, errText = callTool(t, cs, "close", map[string]any{"session_id": id}, nil)
	require.Empty(t, errText)
	assert.Equal(t, true, out["ok"])

	waitFor(t, 15*time.Second, "the session.closed event", func() bool {
		l := pub.list()
		return len(l) >= 2 && l[1].Type == events.SessionClosed
	})

	l := pub.list()
	require.Len(t, l, 2, "one spoken then one closed")
	assert.Equal(t, events.SpeakComplete, l[0].Type)
	assert.Equal(t, tp1, l[0].Traceparent, "the trace given to open is on the spoken event")
	assert.Equal(t, events.SessionClosed, l[1].Type)
	assert.Equal(t, tp1, l[1].Traceparent, "the trace given to open is on the closed event")
}

// An open whose call has no _meta traceparent: the session's events have
// no traceparent at all — never a made-up value.
func TestOpenWithoutTraceparent_NoField(t *testing.T) {
	g := newFakeGateway(t, "piper_denis")
	pub := &fakePub{}
	sp := newSpeech(t, speech.Config{}, g, pub)
	tap := startTap(t)
	cs := connectTools(t, sp)

	out, errText := callTool(t, cs, "open", map[string]any{"to": tap.addr(), "voice_name": "piper_denis"}, nil)
	require.Empty(t, errText)
	id, _ := out["session_id"].(string)
	require.NotEmpty(t, id)

	_, errText = callTool(t, cs, "speak", map[string]any{"session_id": id, "text": "Привет!"}, nil)
	require.Empty(t, errText)
	waitFor(t, 15*time.Second, "the spoken event", func() bool {
		l := pub.list()
		return len(l) >= 1 && l[0].Type == events.SpeakComplete
	})
	_, errText = callTool(t, cs, "close", map[string]any{"session_id": id}, nil)
	require.Empty(t, errText)
	waitFor(t, 15*time.Second, "the session.closed event", func() bool {
		l := pub.list()
		return len(l) >= 2 && l[1].Type == events.SessionClosed
	})

	l := pub.list()
	require.Len(t, l, 2)
	assert.Empty(t, l[0].Traceparent, "no traceparent given to open: not on the spoken event")
	assert.Empty(t, l[1].Traceparent, "no traceparent given to open: not on the closed event")
}

// Two sessions opened under two different traceparents: each session's
// events carry its own, and no event carries the other one's.
func TestTwoSessions_EachKeepsItsOwnTraceparent(t *testing.T) {
	g := newFakeGateway(t, "piper_denis")
	pub := &fakePub{}
	sp := newSpeech(t, speech.Config{}, g, pub)
	tap := startTap(t)
	cs := connectTools(t, sp)

	opens := []struct {
		tp   string
		text string
	}{
		{tp1, "первая"},
		{tp2, "вторая"},
	}
	ids := make([]string, len(opens))
	for i, o := range opens {
		out, errText := callTool(t, cs, "open", map[string]any{"to": tap.addr(), "voice_name": "piper_denis"}, mcp.Meta{"traceparent": o.tp})
		require.Empty(t, errText)
		ids[i], _ = out["session_id"].(string)
		require.NotEmpty(t, ids[i])
		_, errText = callTool(t, cs, "speak", map[string]any{"session_id": ids[i], "text": o.text}, nil)
		require.Empty(t, errText)
	}

	waitFor(t, 15*time.Second, "both spoken events", func() bool {
		n := 0
		for _, e := range pub.list() {
			if e.Type == events.SpeakComplete {
				n++
			}
		}
		return n == 2
	})
	for _, id := range ids {
		out, errText := callTool(t, cs, "close", map[string]any{"session_id": id}, nil)
		require.Empty(t, errText)
		assert.Equal(t, true, out["ok"])
	}
	waitFor(t, 15*time.Second, "both session.closed events", func() bool {
		n := 0
		for _, e := range pub.list() {
			if e.Type == events.SessionClosed {
				n++
			}
		}
		return n == 2
	})

	want := map[string]string{ids[0]: tp1, ids[1]: tp2}
	l := pub.list()
	require.Len(t, l, 4)
	for _, e := range l {
		tp, ok := want[e.Subject]
		require.True(t, ok, "an event for an unknown session: %v", e)
		assert.Equal(t, tp, e.Traceparent,
			"session %s under %s: event %s carries its own traceparent", e.Subject, tp, e.Type)
	}
}

// UNIT-4, through the tool: a speak whose synthesis never answers fails within
// its own synthesis_timeout_ms — cause error, spoken up to the stalled piece,
// the engine's request cancelled — and the phrase after it is spoken whole.
func TestSpeak_SynthesisTimeoutFailsThePhrase(t *testing.T) {
	g := newFakeGateway(t, "piper_denis")
	g.slowContains = "stall"
	g.slowDur = time.Minute // an engine that has hung
	pub := &fakePub{}
	// the session's default is far longer than the test: the request's own wins
	sp := newSpeech(t, speech.Config{SynthesisTimeout: time.Minute}, g, pub)
	tap := startTap(t)
	cs := connectTools(t, sp)

	out, errText := callTool(t, cs, "open", map[string]any{"to": tap.addr(), "voice_name": "piper_denis"}, nil)
	require.Empty(t, errText)
	id := out["session_id"].(string)

	start := time.Now()
	const text1 = "Минутку. stall stall stall."
	out, errText = callTool(t, cs, "speak", map[string]any{"session_id": id, "text": text1, "synthesis_timeout_ms": 400}, nil)
	require.Empty(t, errText)
	u1 := out["request_id"].(string)
	out, errText = callTool(t, cs, "speak", map[string]any{"session_id": id, "text": "Продолжаем."}, nil)
	require.Empty(t, errText)
	u2 := out["request_id"].(string)

	waitFor(t, 10*time.Second, "the first speak-complete", func() bool { return len(pub.list()) >= 1 })
	failedAfter := time.Since(start)
	l := pub.list()
	assert.Equal(t, u1, l[0].Data["request_id"])
	assert.Equal(t, "error", l[0].Data["completion_cause"])
	assert.Equal(t, "Минутку.", l[0].Data["spoken"])
	// "Минутку." plays ~0.1 s of audio, then 0.4 s of waiting: nowhere near the
	// session's minute or the engine's.
	assert.Less(t, failedAfter, 3*time.Second)
	waitFor(t, 5*time.Second, "the stalled synthesis cancelled", g.wasCancelled)

	waitFor(t, 10*time.Second, "the second speak-complete", func() bool { return len(pub.list()) >= 2 })
	l = pub.list()
	assert.Equal(t, u2, l[1].Data["request_id"])
	assert.Equal(t, "normal", l[1].Data["completion_cause"])
	assert.Equal(t, "Продолжаем.", l[1].Data["spoken"])
}

func TestSpeak_SynthesisTimeoutMustBePositive(t *testing.T) {
	g := newFakeGateway(t, "piper_denis")
	sp := newSpeech(t, speech.Config{}, g, &fakePub{})
	tap := startTap(t)
	cs := connectTools(t, sp)

	out, errText := callTool(t, cs, "open", map[string]any{"to": tap.addr(), "voice_name": "piper_denis"}, nil)
	require.Empty(t, errText)
	for _, ms := range []int{0, -5} {
		_, errText = callTool(t, cs, "speak", map[string]any{"session_id": out["session_id"], "text": "x", "synthesis_timeout_ms": ms}, nil)
		assert.True(t, strings.HasPrefix(errText, "bad_argument:"), "%d: %q", ms, errText)
	}
}
