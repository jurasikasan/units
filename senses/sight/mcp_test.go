package sight

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectMCP(t *testing.T, svc *Service) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()
	ss, err := NewMCPServer(svc, "test").Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		_ = cs.Close()
		_ = ss.Wait()
	})
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, params *mcp.CallToolParams) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), params)
	if err != nil {
		t.Fatalf("CallTool %s: %v", params.Name, err)
	}
	return res
}

func toolText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// open → {"session_id", "video_in", "sight_in"}; close → {"ok": true}; errors are tool
// errors whose text starts with the code; the open call's traceparent is on
// every event of the session.
func TestMCPOpenClose(t *testing.T) {
	h := newHarness(t, nil)
	cs := connectMCP(t, h.svc)
	const tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

	res := callTool(t, cs, &mcp.CallToolParams{Name: "open", Arguments: map[string]any{"language": "en"},
		Meta: mcp.Meta{"traceparent": tp}})
	if res.IsError {
		t.Fatalf("open: %s", toolText(res))
	}
	var out openOut
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil || !strings.HasPrefix(out.SessionID, "sg-") ||
		out.SightIn != "http://sight.test/in/"+out.SessionID || !strings.HasPrefix(out.VideoIn, "127.0.0.1:") {
		t.Fatalf("open: %s (%v)", b, err)
	}
	h.send(t, out.SessionID, []map[string]any{{"n": "read", "t": at(0), "vs": "EXIT"}})

	res = callTool(t, cs, &mcp.CallToolParams{Name: "close", Arguments: map[string]any{"session_id": out.SessionID}})
	if res.IsError || !strings.Contains(toolText(res), `"ok":true`) {
		t.Fatalf("close: %s", toolText(res))
	}
	evs := h.sink.Events()
	if types(evs) != TypeRecognitionComplete+" "+TypeSessionClosed {
		t.Fatalf("events: %s", types(evs))
	}
	for _, e := range evs {
		if e.Traceparent != tp {
			t.Errorf("%s: traceparent %q", e.Type, e.Traceparent)
		}
	}

	for args, code := range map[string]string{
		`{"recognize":["faces"]}`:   "unsupported_recognizer",
		`{"change_threshold":1.5}`:  "bad_argument",
		`{"describe_every_ms":-10}`: "bad_argument",
	} {
		var a map[string]any
		_ = json.Unmarshal([]byte(args), &a)
		res := callTool(t, cs, &mcp.CallToolParams{Name: "open", Arguments: a})
		if !res.IsError || !strings.HasPrefix(toolText(res), code) {
			t.Errorf("open %s: %s", args, toolText(res))
		}
	}
	res = callTool(t, cs, &mcp.CallToolParams{Name: "close", Arguments: map[string]any{"session_id": "sg-00000000"}})
	if !res.IsError || !strings.HasPrefix(toolText(res), "unknown_session") {
		t.Errorf("close unknown: %s", toolText(res))
	}
}

// UNIT-131 for sight: open's idle_timeout_ms is the session's own; 0 never
// idles out; absent keeps the deployment's; below zero is bad_argument.
func TestMCPOpenIdleTimeout(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.IdleTimeout = 100 * time.Millisecond })
	cs := connectMCP(t, h.svc)
	open := func(args map[string]any) string {
		t.Helper()
		res := callTool(t, cs, &mcp.CallToolParams{Name: "open", Arguments: args})
		if res.IsError {
			t.Fatalf("open %v: %s", args, toolText(res))
		}
		return res.StructuredContent.(map[string]any)["session_id"].(string)
	}
	never := open(map[string]any{"idle_timeout_ms": 0})
	deployments := open(map[string]any{})
	own := open(map[string]any{"idle_timeout_ms": 400})
	closed := func(id string) string {
		for _, e := range h.sink.Events() {
			if e.Type == TypeSessionClosed && e.Subject == id {
				return data(e)["reason"].(string)
			}
		}
		return ""
	}
	time.Sleep(250 * time.Millisecond)
	if closed(deployments) != "idle" || closed(own) != "" {
		t.Fatalf("after 250 ms: the deployment's %q (want idle), its own 400 ms %q (want open)", closed(deployments), closed(own))
	}
	time.Sleep(450 * time.Millisecond)
	if closed(own) != "idle" || closed(never) != "" {
		t.Fatalf("after 700 ms: its own %q (want idle), never %q (want open)", closed(own), closed(never))
	}
	// still looking: words sent to it are seen
	h.send(t, never, []map[string]any{{"n": "read", "t": at(0), "vs": "EXIT"}})
	var mine []Event
	for _, e := range h.close(t, never) {
		if e.Subject == never {
			mine = append(mine, e)
		}
	}
	if types(mine) != TypeRecognitionComplete+" "+TypeSessionClosed || data(mine[1])["reason"] != "closed" {
		t.Fatalf("the eye that stayed: %s", types(mine))
	}
	res := callTool(t, cs, &mcp.CallToolParams{Name: "open", Arguments: map[string]any{"idle_timeout_ms": -1}})
	if !res.IsError || !strings.HasPrefix(toolText(res), "bad_argument") {
		t.Fatalf("idle_timeout_ms -1: %s", toolText(res))
	}
}
