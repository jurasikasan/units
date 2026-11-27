package sight

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
