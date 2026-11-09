package hearing

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectMCP wires a client session to the hearing MCP server in memory.
func connectMCP(t *testing.T, svc *Service) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()
	ss, err := NewMCPServer(svc).Connect(ctx, t1, nil)
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

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

func toolText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// open → {"session_id", "audio_in"}; close → {"ok": true};
// errors are MCP tool errors whose text starts with the code.
func TestMCPOpenClose(t *testing.T) {
	h := newHarness(t, nil)
	cs := connectMCP(t, h.svc)

	res := callTool(t, cs, "open", map[string]any{"recognize": []string{"speech"}})
	if res.IsError {
		t.Fatalf("open is an error: %q", toolText(t, res))
	}
	sc, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("no structured content: %#v", res.StructuredContent)
	}
	sid, _ := sc["session_id"].(string)
	if !strings.HasPrefix(sid, "hr-") {
		t.Fatalf("session_id = %q, want hr- prefix", sid)
	}
	audioIn, _ := sc["audio_in"].(string)
	if !strings.Contains(audioIn, ":") {
		t.Fatalf("audio_in = %q, want a host:port string", audioIn)
	}

	// close of a session that never existed
	res = callTool(t, cs, "close", map[string]any{"session_id": "hr-00000000"})
	if !res.IsError || !strings.HasPrefix(toolText(t, res), "unknown_session") {
		t.Fatalf("want isError with unknown_session prefix, got isError=%v text=%q", res.IsError, toolText(t, res))
	}

	// unrecognized recognizer
	res = callTool(t, cs, "open", map[string]any{"recognize": []string{"speech", "echo"}})
	if !res.IsError || !strings.HasPrefix(toolText(t, res), "unsupported_recognizer") {
		t.Fatalf("want isError with unsupported_recognizer prefix, got isError=%v text=%q", res.IsError, toolText(t, res))
	}

	// close → {"ok": true}
	res = callTool(t, cs, "close", map[string]any{"session_id": sid})
	if res.IsError {
		t.Fatalf("close is an error: %q", toolText(t, res))
	}
	sc, ok = res.StructuredContent.(map[string]any)
	if !ok || sc["ok"] != true {
		t.Fatalf("close out = %#v, want {ok: true}", res.StructuredContent)
	}
}

func TestMCPCapacity(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxSessions = 1 })
	cs := connectMCP(t, h.svc)
	res := callTool(t, cs, "open", nil)
	if res.IsError {
		t.Fatalf("first open is an error: %q", toolText(t, res))
	}
	res = callTool(t, cs, "open", nil)
	if !res.IsError || !strings.HasPrefix(toolText(t, res), "capacity") {
		t.Fatalf("want isError with capacity prefix, got isError=%v text=%q", res.IsError, toolText(t, res))
	}
}

// The HTTP surface: GET /health and the stateless streamable /mcp
// endpoint on the same mux.
func TestHTTPEndpoints(t *testing.T) {
	h := newHarness(t, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return NewMCPServer(h.svc) },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("GET /health = %d %s, want 200 {\"status\":\"ok\"}", resp.StatusCode, body)
	}

	do := func(id int, method, body string) (int, string) {
		req, err := http.NewRequest("POST", ts.URL+"/mcp", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		return r.StatusCode, string(raw)
	}

	code, out := do(1, "POST", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`)
	if code != http.StatusOK || !strings.Contains(out, "serverInfo") {
		t.Fatalf("initialize = %d %s", code, out)
	}
	code, out = do(2, "POST", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"open","arguments":{"recognize":["speech"]}}}`)
	if code != http.StatusOK || !strings.Contains(out, "session_id") || !strings.Contains(out, "audio_in") {
		t.Fatalf("tools/call open = %d %s", code, out)
	}
}
