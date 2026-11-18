package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A turn is what the scripted model says when asked: words, or tool calls,
// or both.
type turn struct {
	words string
	calls []scriptedCall
	usage string // the usage chunk's usage, as JSON; none when empty
	model string // x-litellm-model-name, when set
}

// scriptedCall is a tool call as it streams: an id and a name, then the
// arguments in the fragments given.
type scriptedCall struct {
	id, name string
	args     []string
}

// scriptedModel answers each request with the next turn (the last one
// again when they run out) and keeps every request's body.
type scriptedModel struct {
	mu    sync.Mutex
	turns []turn
	asked []map[string]any
}

func (f *scriptedModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	n := len(f.asked)
	f.asked = append(f.asked, body)
	f.mu.Unlock()
	t := f.turns[min(n, len(f.turns)-1)]

	w.Header().Set("Content-Type", "text/event-stream")
	if t.model != "" {
		w.Header().Set("X-Litellm-Model-Name", t.model)
	}
	chunk := func(delta map[string]any) {
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	}
	if t.words != "" {
		chunk(map[string]any{"content": t.words})
	}
	for i, c := range t.calls {
		chunk(map[string]any{"tool_calls": []any{map[string]any{
			"index": i, "id": c.id, "type": "function", "function": map[string]any{"name": c.name, "arguments": ""}}}})
		for _, a := range c.args {
			chunk(map[string]any{"tool_calls": []any{map[string]any{"index": i, "function": map[string]any{"arguments": a}}}})
		}
	}
	if t.usage != "" {
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":%s}\n\n", t.usage)
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

// toolServer is an MCP server, stateless as MCP 2026-07-28 has it, with an
// adder, a tool that fails, and a slow one.
func toolServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "calc", Version: "test"}, nil)
	type ab struct {
		A float64 `json:"a"`
		B float64 `json:"b"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "add", Description: "a + b"},
		func(_ context.Context, _ *mcp.CallToolRequest, in ab) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprint(in.A + in.B)}}}, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "fail", Description: "always fails"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return nil, nil, fmt.Errorf("out of order")
		})
	mcp.AddTool(s, &mcp.Tool{Name: "slow", Description: "answers after a while"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			time.Sleep(300 * time.Millisecond)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil, nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func serveWithTools(t *testing.T, f *scriptedModel, servers []Server, maxSteps int) *httptest.Server {
	t.Helper()
	model := httptest.NewServer(f)
	t.Cleanup(model.Close)
	a := New(model.URL, "m", "k", "")
	a.UseTools(servers, maxSteps)
	mind := httptest.NewServer(a)
	t.Cleanup(mind.Close)
	return mind
}

// toolEvent is an AG-UI tool event as a client reads it.
type toolEvent struct {
	Type         string `json:"type"`
	ToolCallID   string `json:"toolCallId"`
	ToolCallName string `json:"toolCallName"`
	Delta        string `json:"delta"`
	Content      string `json:"content"`
	Message      string `json:"message"`
}

func runEvents(t *testing.T, mind *httptest.Server, question string) []toolEvent {
	t.Helper()
	resp := post(t, context.Background(), mind.URL, "alice", `{"messages":[{"id":"1","role":"user","content":"`+question+`"}]}`)
	defer func() { _ = resp.Body.Close() }()
	var out []toolEvent
	for _, line := range strings.Split(readAll(t, resp), "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			var e toolEvent
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				t.Fatalf("not an event: %s", data)
			}
			out = append(out, e)
		}
	}
	return out
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			return b.String()
		}
	}
}

func types_(evs []toolEvent) string {
	var s []string
	for _, e := range evs {
		s = append(s, e.Type)
	}
	return strings.Join(s, " ")
}

// The id a provider gave a call, odd characters and all, goes back to it as
// it was: Gemini keeps its thought signature in there.
const odd = "call_1__thought__EpwBCpkB/x+y=="

func TestToolLoopCallsAndAnswers(t *testing.T) {
	f := &scriptedModel{turns: []turn{
		{calls: []scriptedCall{{id: odd, name: "calc__add", args: []string{`{"a": 2,`, ` "b": 3}`}}}},
		{words: "It is 5."},
	}}
	evs := runEvents(t, serveWithTools(t, f, []Server{{Name: "calc", URL: toolServer(t).URL}}, 0), "2+3?")

	want := "RUN_STARTED TOOL_CALL_START TOOL_CALL_ARGS TOOL_CALL_ARGS TOOL_CALL_END TOOL_CALL_RESULT " +
		"TEXT_MESSAGE_START TEXT_MESSAGE_CONTENT TEXT_MESSAGE_END RUN_FINISHED"
	if got := types_(evs); got != want {
		t.Fatalf("events\n got %s\nwant %s", got, want)
	}
	if evs[1].ToolCallID != odd || evs[1].ToolCallName != "calc__add" {
		t.Errorf("start %+v", evs[1])
	}
	if evs[2].Delta+evs[3].Delta != `{"a": 2, "b": 3}` {
		t.Errorf("args %q %q", evs[2].Delta, evs[3].Delta)
	}
	if evs[5].ToolCallID != odd || evs[5].Content != "5" {
		t.Errorf("result %+v", evs[5])
	}

	// The first request offered the tool with its schema.
	tools, _ := json.Marshal(f.asked[0]["tools"])
	if !strings.Contains(string(tools), `"name":"calc__add"`) || !strings.Contains(string(tools), `"a"`) {
		t.Errorf("tools offered: %s", tools)
	}
	// The second carried the call back with its id untouched, and the result.
	if got := callsAndResults(f.asked[1]); got != "call "+odd+` calc__add {"a": 2, "b": 3}; result `+odd+" 5" {
		t.Errorf("second request: %s", got)
	}
}

// callsAndResults is what a request says of tool calls and their results,
// in order: "call <id> <name> <args>" and "result <id> <content>".
func callsAndResults(req map[string]any) string {
	var out []string
	msgs, _ := req["messages"].([]any)
	for _, m := range msgs {
		m := m.(map[string]any)
		if calls, ok := m["tool_calls"].([]any); ok {
			for _, c := range calls {
				c := c.(map[string]any)
				fn := c["function"].(map[string]any)
				out = append(out, fmt.Sprintf("call %s %s %s", c["id"], fn["name"], fn["arguments"]))
			}
		}
		if m["role"] == "tool" {
			out = append(out, fmt.Sprintf("result %s %s", m["tool_call_id"], m["content"]))
		}
	}
	return strings.Join(out, "; ")
}

func TestParallelCallsAreMadeAtOnce(t *testing.T) {
	f := &scriptedModel{turns: []turn{
		{calls: []scriptedCall{{id: "c1", name: "calc__slow", args: []string{"{}"}}, {id: "c2", name: "calc__slow", args: []string{"{}"}},
			{id: "c3", name: "calc__add", args: []string{`{"a":1,"b":1}`}}}},
		{words: "ok"},
	}}
	mind := serveWithTools(t, f, []Server{{Name: "calc", URL: toolServer(t).URL}}, 0)
	start := time.Now()
	evs := runEvents(t, mind, "go")
	if d := time.Since(start); d > 550*time.Millisecond {
		t.Errorf("two slow calls took %v: not at once", d)
	}
	var results []string
	for _, e := range evs {
		if e.Type == "TOOL_CALL_RESULT" {
			results = append(results, e.ToolCallID+"="+e.Content)
		}
	}
	if got := strings.Join(results, " "); got != "c1=done c2=done c3=2" {
		t.Errorf("results in the order of the calls: %s", got)
	}
	msgs, _ := json.Marshal(f.asked[1]["messages"])
	if strings.Count(string(msgs), `"role":"tool"`) != 3 {
		t.Errorf("three results back to the model: %s", msgs)
	}
}

func TestAFailingToolIsAnAnswerToo(t *testing.T) {
	f := &scriptedModel{turns: []turn{
		{calls: []scriptedCall{{id: "c1", name: "calc__fail", args: []string{"{}"}}, {id: "c2", name: "nobody__nothing", args: []string{"{}"}}}},
		{words: "sorry"},
	}}
	evs := runEvents(t, serveWithTools(t, f, []Server{{Name: "calc", URL: toolServer(t).URL}}, 0), "go")
	if got := types_(evs); !strings.HasSuffix(got, "RUN_FINISHED") {
		t.Fatalf("events %s", got)
	}
	msgs, _ := json.Marshal(f.asked[1]["messages"])
	for _, s := range []string{"out of order", `there is no tool \"nobody__nothing\"`} {
		if !strings.Contains(string(msgs), s) {
			t.Errorf("the model hears %q: %s", s, msgs)
		}
	}
}

func TestAServerThatDoesNotAnswerIsLeftOut(t *testing.T) {
	f := &scriptedModel{turns: []turn{{words: "hi"}}}
	evs := runEvents(t, serveWithTools(t, f, []Server{{Name: "gone", URL: "http://127.0.0.1:1/mcp"}}, 0), "hi")
	if got := types_(evs); !strings.HasSuffix(got, "RUN_FINISHED") {
		t.Fatalf("events %s", got)
	}
	if _, offered := f.asked[0]["tools"]; offered {
		t.Errorf("no tools to offer, yet: %v", f.asked[0]["tools"])
	}
}

func TestStepsRunOut(t *testing.T) {
	f := &scriptedModel{turns: []turn{{calls: []scriptedCall{{id: "c", name: "calc__add", args: []string{`{"a":1,"b":1}`}}}}}}
	evs := runEvents(t, serveWithTools(t, f, []Server{{Name: "calc", URL: toolServer(t).URL}}, 3), "loop")
	last := evs[len(evs)-1]
	if last.Type != "RUN_ERROR" || !strings.Contains(last.Message, "3 steps") {
		t.Fatalf("last event %+v", last)
	}
	if len(f.asked) != 3 {
		t.Errorf("asked %d times, want 3", len(f.asked))
	}
}

func TestHistoryCarriesCallsAndResults(t *testing.T) {
	f := &scriptedModel{turns: []turn{{words: "ok"}}}
	mind := serveWithTools(t, f, nil, 0)
	body := `{"messages":[
		{"id":"1","role":"user","content":"2+3?"},
		{"id":"2","role":"assistant","toolCalls":[{"id":"` + odd + `","type":"function","function":{"name":"calc__add","arguments":"{\"a\":2,\"b\":3}"}}]},
		{"id":"3","role":"tool","toolCallId":"` + odd + `","content":"5"},
		{"id":"4","role":"tool","toolCallId":"stray","content":"lost"},
		{"id":"5","role":"user","content":"and again?"}]}`
	resp := post(t, context.Background(), mind.URL, "alice", body)
	_ = readAll(t, resp)
	_ = resp.Body.Close()
	if got := callsAndResults(f.asked[0]); got != "call "+odd+` calc__add {"a":2,"b":3}; result `+odd+" 5" {
		t.Errorf("the call and its result are in the prompt, a result with no call is not: %s", got)
	}
}

func TestParseServers(t *testing.T) {
	got, err := ParseServers(" calc=http://a:1/mcp , web=http://b/mcp,")
	if err != nil || fmt.Sprint(got) != "[{calc http://a:1/mcp} {web http://b/mcp}]" {
		t.Errorf("%v %v", got, err)
	}
	if _, err := ParseServers("calc"); err == nil {
		t.Error("no url is an error")
	}
}
