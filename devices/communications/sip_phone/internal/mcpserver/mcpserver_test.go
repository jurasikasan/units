package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/call"
	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/device"
	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/events"
	"github.com/jurasikasan/units/devices/communications/sip_phone/internal/farend"
)

// The tools as an MCP client sees them, over the SDK's in-memory transport,
// shapes from CONTRACT.md §2.

type pub struct{ ch chan events.Event }

func (p pub) Publish(_ context.Context, e events.Event) error { p.ch <- e; return nil }

func (p pub) waitFor(t *testing.T, typ string) events.Event {
	t.Helper()
	for {
		select {
		case e := <-p.ch:
			if e.Type == typ {
				return e
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no %s", typ)
		}
	}
}

func connect(t *testing.T) (*mcp.ClientSession, pub) {
	t.Helper()
	fe := farend.Start(t)
	conn, port := farend.ListenUDP(t)
	ua, err := sipgo.NewUA()
	require.NoError(t, err)
	srv, err := sipgo.NewServer(ua)
	require.NoError(t, err)
	cli, err := sipgo.NewClient(ua, sipgo.WithClientHostname("127.0.0.1"), sipgo.WithClientPort(port))
	require.NoError(t, err)
	p := pub{ch: make(chan events.Event, 64)}
	m := call.NewManager(call.Config{
		Trunk: fe.Addr, AdvertiseHost: "127.0.0.1", SIPPort: port,
		MaxCalls: 2, RingDefault: 3 * time.Second, StripPlus: true,
		AudioInHost: "127.0.0.1", AudioInPortMin: 43000, AudioInPortMax: 43199,
	}, cli, p)
	srv.OnBye(m.OnBye)
	go srv.ServeUDP(conn) //nolint:errcheck

	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	_, err = New(device.New(m), "test").Connect(ctx, st, nil)
	require.NoError(t, err)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close(); m.Wait(); _ = ua.Close(); _ = conn.Close() })
	return cs, p
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

func TestToolsEndToEnd(t *testing.T) {
	cs, p := connect(t)
	tp := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

	out, errText := callTool(t, cs, "dial", map[string]any{"to": farend.AnswerAndHold, "ring_timeout_s": 30}, mcp.Meta{"traceparent": tp})
	require.Empty(t, errText)
	callID, _ := out["call_id"].(string)
	require.True(t, strings.HasPrefix(callID, "call-"), "%v", out)

	answered := p.waitFor(t, events.CallAnswered)
	assert.Equal(t, tp, answered.Traceparent, "the trace given to dial is on the call's events")
	assert.Regexp(t, `^127\.0\.0\.1:43[01]\d\d$`, answered.Data["audio_in"], "answered carries the audio_in socket")

	out, _ = callTool(t, cs, "hangup", map[string]any{"call_id": callID}, nil)
	assert.Equal(t, map[string]any{"ok": true}, out)
	assert.Equal(t, call.LocalHangup, p.waitFor(t, events.CallEnded).Data["reason"])

	out, _ = callTool(t, cs, "hangup", map[string]any{"call_id": callID}, nil)
	assert.Equal(t, map[string]any{"ok": true}, out, "hanging up an ended call is not an error")
}

func TestRefusalsAreToolErrorsWithTheirCode(t *testing.T) {
	cs, p := connect(t)

	_, errText := callTool(t, cs, "dial", map[string]any{"to": "0441234567"}, nil)
	assert.True(t, strings.HasPrefix(errText, "invalid_number:"), errText)

	// Two calls up is the configured maximum; the third is refused with its code.
	out, _ := callTool(t, cs, "dial", map[string]any{"to": farend.RingForever}, nil)
	id1, _ := out["call_id"].(string)
	out, _ = callTool(t, cs, "dial", map[string]any{"to": farend.RingForever}, nil)
	id2, _ := out["call_id"].(string)
	p.waitFor(t, events.CallRinging)
	_, errText = callTool(t, cs, "dial", map[string]any{"to": farend.RingForever}, nil)
	assert.True(t, strings.HasPrefix(errText, "capacity:"), errText)

	callTool(t, cs, "hangup", map[string]any{"call_id": id1}, nil)
	callTool(t, cs, "hangup", map[string]any{"call_id": id2}, nil)
	assert.Equal(t, call.Cancelled, p.waitFor(t, events.CallEnded).Data["reason"])
	assert.Equal(t, call.Cancelled, p.waitFor(t, events.CallEnded).Data["reason"])
}
