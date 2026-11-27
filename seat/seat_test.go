package seat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The seat is tested at its walls: a caller posting a chat request as the
// brain does, a page over HTTP and SSE, Claude over MCP.

func chatRequest(seat string, extra map[string]any) map[string]any {
	req := map[string]any{"model": seat, "messages": []any{
		map[string]any{"role": "system", "content": "be a mind"},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": `[10:00:00.000 hearing] "hello"`}}},
	}, "tools": []any{
		map[string]any{"type": "function", "function": map[string]any{"name": "speak_aloud", "description": "Say something aloud.",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}}},
		map[string]any{"type": "function", "function": map[string]any{"name": "skip_turn", "parameters": map[string]any{"type": "object"}}},
	}, "tool_choice": "required"}
	for k, v := range extra {
		req[k] = v
	}
	return req
}

type reply struct {
	code int
	body map[string]any
}

// ask posts a chat request in the background, as the brain does, and gives
// the door's answer on the channel.
func ask(t *testing.T, ctx context.Context, base string, req map[string]any) <-chan reply {
	t.Helper()
	out := make(chan reply, 1)
	raw, _ := json.Marshal(req)
	go func() {
		r, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			out <- reply{code: -1}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		out <- reply{resp.StatusCode, body}
	}()
	return out
}

func post(t *testing.T, url string, body any) int {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func pending(t *testing.T, base, seat string) []Turn {
	t.Helper()
	resp, err := http.Get(base + "/seats/" + seat + "/turns")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out []Turn
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type sse struct{ kind, data string }

func watch(t *testing.T, url string) <-chan sse {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("watch: %v", err)
	}
	out := make(chan sse, 16)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		var ev sse
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.kind = line[7:]
			case strings.HasPrefix(line, "data: "):
				ev.data = line[6:]
			case line == "" && ev.kind != "":
				out <- ev
				ev = sse{}
			}
		}
	}()
	return out
}

func next(t *testing.T, ch <-chan sse) sse {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
		return sse{}
	}
}

// A request held as a turn, seen whole by the page, answered once; the
// caller gets a model's completion with the one tool call.
func TestATurnAnsweredOverHTTP(t *testing.T) {
	s := New(time.Minute)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close) // after the watcher below is let go
	events := watch(t, srv.URL+"/seats/b/events")

	got := ask(t, context.Background(), srv.URL, chatRequest("b", nil))
	e := next(t, events)
	var turn Turn
	if e.kind != "turn" || json.Unmarshal([]byte(e.data), &turn) != nil || turn.Seat != "b" ||
		!strings.Contains(string(turn.Request), `"tool_choice":"required"`) {
		t.Fatalf("turn event: %+v", e)
	}
	if p := pending(t, srv.URL, "b"); len(p) != 1 || p[0].ID != turn.ID {
		t.Fatalf("pending: %+v", p)
	}
	answer := srv.URL + "/seats/b/turns/" + turn.ID + "/answer"
	for what, a := range map[string]any{
		"no tool call where one is required": map[string]any{"text": "hi"},
		"a tool not offered":                 map[string]any{"tool_call": map[string]any{"name": "fly"}},
		"arguments not an object":            map[string]any{"tool_call": map[string]any{"name": "skip_turn", "arguments": []int{1}}},
		"an unknown field":                   map[string]any{"txt": "hi"},
	} {
		if code := post(t, answer, a); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", what, code)
		}
	}
	if code := post(t, srv.URL+"/seats/a/turns/"+turn.ID+"/answer", map[string]any{"text": "x"}); code != http.StatusNotFound {
		t.Fatalf("another seat's turn: %d", code)
	}
	ok := map[string]any{"text": "Someone greets me.", "tool_call": map[string]any{"name": "speak_aloud", "arguments": map[string]any{"text": "hello"}}}
	if code := post(t, answer, ok); code != http.StatusOK {
		t.Fatalf("answer: %d", code)
	}
	if code := post(t, answer, ok); code != http.StatusConflict {
		t.Fatalf("a second answer: %d", code)
	}

	r := <-got
	choice := r.body["choices"].([]any)[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	call := msg["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if r.code != http.StatusOK || choice["finish_reason"] != "tool_calls" || msg["content"] != "Someone greets me." ||
		call["name"] != "speak_aloud" || call["arguments"] != `{"text":"hello"}` || r.body["model"] != "b" {
		t.Fatalf("completion: %d %v", r.code, r.body)
	}
	if e := next(t, events); e.kind != "gone" || !strings.Contains(e.data, `"reason":"answered"`) {
		t.Fatalf("gone: %+v", e)
	}
	if len(pending(t, srv.URL, "b")) != 0 {
		t.Fatal("still pending")
	}
}

// A caller that gives up withdraws its turn; one held past Hold is let go
// with a timeout.
func TestATurnWithdrawnOrExpired(t *testing.T) {
	s := New(200 * time.Millisecond)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ask(t, ctx, srv.URL, chatRequest("b", nil))
	eventually(t, "held", func() bool { return len(pending(t, srv.URL, "b")) == 1 })
	id := pending(t, srv.URL, "b")[0].ID
	cancel()
	eventually(t, "withdrawn", func() bool { return s.Counts.Withdrawn.Load() == 1 })
	if code := post(t, srv.URL+"/seats/b/turns/"+id+"/answer", map[string]any{"tool_call": map[string]any{"name": "skip_turn"}}); code != http.StatusNotFound {
		t.Fatalf("answer to a withdrawn turn: %d", code)
	}

	r := <-ask(t, context.Background(), srv.URL, chatRequest("b", nil))
	if r.code != http.StatusGatewayTimeout || s.Counts.Expired.Load() != 1 {
		t.Fatalf("expired: %d %v", r.code, r.body)
	}
}

func TestTheDoorRefuses(t *testing.T) {
	srv := httptest.NewServer(New(time.Minute).Handler())
	defer srv.Close()
	for what, req := range map[string]map[string]any{
		"a model that is no seat's name": chatRequest("Bad Seat", nil),
		"a stream":                       chatRequest("b", map[string]any{"stream": true}),
	} {
		if r := <-ask(t, context.Background(), srv.URL, req); r.code != http.StatusBadRequest || r.body["error"] == nil {
			t.Fatalf("%s: %d %v", what, r.code, r.body)
		}
	}
}

// Claude sits in the seat over MCP: await_turn gives the turn in words with
// its pictures beside, answer answers it.
func TestATurnAnsweredOverMCP(t *testing.T) {
	s := New(time.Minute)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "claude", Version: "0"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()

	// Nobody asked yet: an empty turn after the wait.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "await_turn", Arguments: map[string]any{"seat": "b", "wait_seconds": 1}})
	if err != nil || res.IsError || res.StructuredContent.(map[string]any)["turn_id"] != "" {
		t.Fatalf("no turn: %v %v", res, err)
	}

	png := []byte("\x89PNG\r\n\x1a\nfake")
	req := chatRequest("b", nil)
	msgs := req["messages"].([]any)
	msgs[1].(map[string]any)["content"] = append(msgs[1].(map[string]any)["content"].([]any),
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)}})
	req["messages"] = append(msgs,
		map[string]any{"role": "assistant", "content": "I greet.", "tool_calls": []any{map[string]any{"id": "call_1", "type": "function",
			"function": map[string]any{"name": "speak_aloud", "arguments": `{"text":"hi"}`}}}},
		map[string]any{"role": "tool", "tool_call_id": "call_1", "content": `{"listeners":1}`})
	got := ask(t, context.Background(), srv.URL, req)

	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "await_turn", Arguments: map[string]any{"seat": "b"}})
	if err != nil || res.IsError {
		t.Fatalf("await_turn: %v %v", res, err)
	}
	out := res.StructuredContent.(map[string]any)
	text := res.Content[0].(*mcp.TextContent).Text
	img, isImg := res.Content[1].(*mcp.ImageContent)
	if out["turn_id"] == "" || out["tool_choice"] != "required" || out["images"] != float64(1) || !isImg ||
		!bytes.Equal(img.Data, png) || img.MIMEType != "image/png" {
		t.Fatalf("turn: %v %v", out, res.Content)
	}
	for _, want := range []string{"--- system ---\nbe a mind", `[10:00:00.000 hearing] "hello"`, "(picture 1)",
		`(called speak_aloud {"text":"hi"}, id call_1)`, "--- tool (call_1) ---", "speak_aloud: Say something aloud.", "tool_choice: required"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the turn in words lacks %q:\n%s", want, text)
		}
	}

	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "answer", Arguments: map[string]any{
		"seat": "b", "turn_id": out["turn_id"], "text": "They answered.", "tool": "skip_turn"}})
	if err != nil || res.IsError {
		t.Fatalf("answer: %v %v", res, err)
	}
	r := <-got
	call := r.body["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if r.code != http.StatusOK || call["name"] != "skip_turn" || call["arguments"] != "{}" {
		t.Fatalf("completion: %v", r.body)
	}
	res, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "answer", Arguments: map[string]any{
		"seat": "b", "turn_id": out["turn_id"], "tool": "skip_turn"}})
	if !res.IsError || !strings.HasPrefix(res.Content[0].(*mcp.TextContent).Text, "answered") {
		t.Fatalf("a second answer over MCP: %v", res.Content)
	}
}
