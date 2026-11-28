package brain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jurasikasan/units/shared/events"
)

// The body is a fake built with the MCP SDK, declaring the tools a body
// declares and recording what it was asked to do.
type fakeBody struct {
	mu    sync.Mutex
	calls []map[string]any
}

func (f *fakeBody) server(t *testing.T) *httptest.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "body", Version: "0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "speak_aloud", Description: "Say something aloud."},
		func(_ context.Context, req *mcp.CallToolRequest, in struct {
			Text string `json:"text" jsonschema:"the words to say"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			f.record("speak_aloud", map[string]any{"text": in.Text}, req)
			return nil, map[string]any{"spoken": in.Text, "completion_cause": "normal", "listeners": 1}, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "skip_turn", Description: "Do nothing this turn."},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, map[string]any, error) {
			f.record("skip_turn", nil, req)
			return nil, map[string]any{"skipped": true}, nil
		})
	s := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	t.Cleanup(s.Close)
	return s
}

func (f *fakeBody) record(name string, args map[string]any, req *mcp.CallToolRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tp, _ := req.Params.Meta["traceparent"].(string)
	f.calls = append(f.calls, map[string]any{"name": name, "args": args, "traceparent": tp})
}

func (f *fakeBody) got() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.calls...)
}

func toolCall(id, name, args string) map[string]any {
	return map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": args}}
}

func bodyRig(t *testing.T, body *fakeBody, answer func(k int) map[string]any) *rig {
	return newRigWith(t, nil, func(r *rig) {
		r.model.answer = answer
		r.mind.Body = &Body{URL: body.server(t).URL}
	})
}

// One thought, one act: the body's tools are offered with tool_choice
// required; only the first call is done, the rest counted; what the body
// answered goes to the log and memory, never back to the model within the
// thought; the next thought recalls the act as a call and its result.
func TestOneActPerThought(t *testing.T) {
	body := &fakeBody{}
	r := bodyRig(t, body, func(k int) map[string]any {
		if k == 1 {
			return map[string]any{"role": "assistant", "content": "Someone is there. I greet them.",
				"tool_calls": []any{toolCall("call_1", "speak_aloud", `{"text":"hello"}`), toolCall("call_2", "skip_turn", `{}`)}}
		}
		return map[string]any{"role": "assistant", "content": "They answered."}
	})
	now := time.Now().UTC().Truncate(time.Millisecond)
	r.hear("utt-1", "is anyone there?", now.Add(-time.Second), now.Add(-500*time.Millisecond))

	eventually(t, "an act", func() bool { return len(r.log.lines("act")) == 1 })
	time.Sleep(150 * time.Millisecond)
	reqs := r.model.got()
	if len(reqs) != 1 {
		t.Fatalf("the model was asked %d times for one thought", len(reqs))
	}
	req := reqs[0]
	if req["tool_choice"] != "required" {
		t.Fatalf("tool_choice: %v", req["tool_choice"])
	}
	tools := map[string]map[string]any{}
	for _, tl := range req["tools"].([]any) {
		fn := tl.(map[string]any)["function"].(map[string]any)
		tools[fn["name"].(string)] = fn
	}
	if len(tools) != 2 || tools["speak_aloud"]["description"] != "Say something aloud." {
		t.Fatalf("tools: %v", req["tools"])
	}
	props := tools["speak_aloud"]["parameters"].(map[string]any)["properties"].(map[string]any)
	if props["text"].(map[string]any)["description"] != "the words to say" {
		t.Fatalf("the body's schema did not reach the model: %v", tools["speak_aloud"])
	}

	calls := body.got()
	if len(calls) != 1 || calls[0]["name"] != "speak_aloud" || calls[0]["args"].(map[string]any)["text"] != "hello" {
		t.Fatalf("the body was asked: %v", calls)
	}
	c := r.mind.Counts.Snapshot()
	if c["acts"] != 1 || c["ignored_calls"] != 1 || c["act_errors"] != 0 {
		t.Fatalf("counts: %v", c)
	}
	act := r.log.lines("act")[0]
	if act["tool"] != "speak_aloud" || !strings.Contains(act["result"].(string), `"spoken":"hello"`) || act["is_error"] != false {
		t.Fatalf("act log: %v", act)
	}

	// Memory: the percept, the thought with its call, the body's answer.
	es := r.threads.all()
	var thought, result map[string]any
	for _, e := range es {
		content, _ := e["content"].([]any)
		for _, b := range content {
			switch b.(map[string]any)["type"] {
			case "tool_call":
				thought = e
			case "tool_result":
				result = e
			}
		}
	}
	if thought == nil || result == nil || thought["sender"] != self.String() || result["sender"] != self.String() {
		t.Fatalf("entries: %v", es)
	}
	blocks := thought["content"].([]any)
	if blocks[0].(map[string]any)["text"] != "Someone is there. I greet them." {
		t.Fatalf("thought: %v", blocks)
	}
	call := blocks[1].(map[string]any)
	if call["id"] != "call_1" || call["name"] != "speak_aloud" || call["args"].(map[string]any)["text"] != "hello" {
		t.Fatalf("tool_call: %v", call)
	}
	res := result["content"].([]any)[0].(map[string]any)
	if res["call_id"] != "call_1" || !strings.Contains(res["text"].(string), `"spoken":"hello"`) || res["is_error"] != nil {
		t.Fatalf("tool_result: %v", res)
	}
	if result["occurred"] == nil {
		t.Fatalf("the act has no occurred: %v", result)
	}

	// The next thought recalls the act as OpenAI has it, and acts on nothing:
	// a skip.
	r.hear("utt-2", "yes, me", now.Add(time.Second), now.Add(1500*time.Millisecond))
	eventually(t, "a second thought", func() bool { return len(r.model.got()) == 2 })
	msgs := r.model.got()[1]["messages"].([]any)
	var at int
	for i, m := range msgs {
		if tc, ok := m.(map[string]any)["tool_calls"]; ok && tc != nil {
			at = i
		}
	}
	asst, tool := msgs[at].(map[string]any), msgs[at+1].(map[string]any)
	fn := asst["tool_calls"].([]any)[0].(map[string]any)
	if asst["role"] != "assistant" || asst["content"] != "Someone is there. I greet them." || fn["id"] != "call_1" ||
		fn["function"].(map[string]any)["name"] != "speak_aloud" || fn["function"].(map[string]any)["arguments"] != `{"text":"hello"}` {
		t.Fatalf("recalled call: %v", asst)
	}
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" || !strings.Contains(tool["content"].(string), "hello") {
		t.Fatalf("recalled result: %v", tool)
	}
	if last := msgs[len(msgs)-1].(map[string]any); last["role"] != "user" {
		t.Fatalf("the request ends with %v, not the new moment", last["role"])
	}
	eventually(t, "a skip", func() bool { return r.mind.Counts.Skips.Load() == 1 })
	if len(body.got()) != 1 {
		t.Fatalf("acted without a call: %v", body.got())
	}
}

// A body that cannot be reached leaves the brain thinking, not acting; a
// call with arguments that are not an object is not done, and remembered as
// a failed act.
func TestABodyDownAndBadArguments(t *testing.T) {
	down := newRigWith(t, nil, func(r *rig) { r.mind.Body = &Body{URL: "http://127.0.0.1:1/mcp"} })
	now := time.Now().UTC()
	down.hear("utt-1", "hello", now.Add(-time.Second), now)
	eventually(t, "a thought", func() bool { return len(down.log.lines("thought")) == 1 })
	if req := down.model.got()[0]; req["tools"] != nil || req["tool_choice"] != nil {
		t.Fatalf("tools offered with no body to list them: %v", req)
	}
	if down.mind.Counts.BodyErrors.Load() != 1 {
		t.Fatalf("counts: %v", down.mind.Counts.Snapshot())
	}

	body := &fakeBody{}
	r := bodyRig(t, body, func(int) map[string]any {
		return map[string]any{"role": "assistant", "content": "", "tool_calls": []any{toolCall("call_9", "speak_aloud", `"hello"`)}}
	})
	r.hear("utt-1", "hello", now.Add(-time.Second), now)
	eventually(t, "a failed act", func() bool { return r.mind.Counts.ActErrors.Load() == 1 })
	if len(body.got()) != 0 {
		t.Fatalf("done with bad arguments: %v", body.got())
	}
	// The act's result is written after its call: wait for it, not for a count.
	eventually(t, "remembered", func() bool {
		es := r.threads.all()
		if len(es) < 3 {
			return false
		}
		c, _ := es[len(es)-1]["content"].([]any)
		return len(c) > 0 && c[0].(map[string]any)["type"] == "tool_result"
	})
	es := r.threads.all()
	call := es[len(es)-2]["content"].([]any)[0].(map[string]any)
	res := es[len(es)-1]["content"].([]any)[0].(map[string]any)
	if call["type"] != "tool_call" || call["args"] == nil || res["is_error"] != true ||
		!strings.HasPrefix(res["text"].(string), "bad_arguments") {
		t.Fatalf("remembered: %v / %v", call, res)
	}
}

// Without a body nothing is offered: the brain thinks as before.
func TestWithoutABodyNothingIsOffered(t *testing.T) {
	r := newRig(t, nil)
	now := time.Now().UTC()
	r.hear("utt-1", "hello", now.Add(-time.Second), now)
	eventually(t, "a thought", func() bool { return len(r.model.got()) == 1 })
	if req := r.model.got()[0]; req["tools"] != nil || req["tool_choice"] != nil {
		t.Fatalf("offered tools without a body: %v", req)
	}
}

// Recall says in words what cannot go to the API as a call and its result:
// a call whose result was cut off, a result whose call was.
func TestRecallOfActsCutByTheWindow(t *testing.T) {
	m := &Mind{Cfg: Config{Self: self}}
	me, other := self.String(), hearsAs.String()
	es := []Entry{
		{Sender: me, Kind: "create", Content: []Block{{Type: "tool_result", CallID: "call_0", Text: `{"skipped":true}`}}},
		{Sender: other, Kind: "create", Content: []Block{{Type: "text", Text: "[10:00:00.000 hearing] \"hi\""}}},
		{Sender: me, Kind: "create", Content: []Block{{Type: "text", Text: "I answer."},
			{Type: "tool_call", ID: "call_1", Name: "speak_aloud", Args: json.RawMessage(`{"text":"hi"}`)}}},
	}
	raw, err := json.Marshal(m.recallMessages(es))
	if err != nil {
		t.Fatal(err)
	}
	var msgs []map[string]any
	_ = json.Unmarshal(raw, &msgs)
	if len(msgs) != 3 {
		t.Fatalf("messages: %s", raw)
	}
	if msgs[0]["role"] != "assistant" || !strings.Contains(msgs[0]["content"].(string), `what came of an act: {"skipped":true}`) {
		t.Fatalf("an answer without its call: %v", msgs[0])
	}
	if msgs[2]["role"] != "assistant" || msgs[2]["tool_calls"] != nil ||
		!strings.Contains(msgs[2]["content"].(string), `did speak_aloud {"text":"hi"}; what came of it is not remembered`) {
		t.Fatalf("a call without its answer: %v", msgs[2])
	}
	for _, msg := range msgs {
		if msg["role"] == "tool" {
			t.Fatalf("a tool message out of place: %s", raw)
		}
	}
}

// The brain's own acts come back on the body's stream with its traceparent
// and are passed over; an act someone else did through the body is a
// percept of channel body, thought about and remembered as the body's.
func TestOwnActsAndTheBodysOthers(t *testing.T) {
	body := &fakeBody{}
	r := bodyRig(t, body, func(k int) map[string]any {
		if k == 1 {
			return map[string]any{"role": "assistant", "content": "I greet them.",
				"tool_calls": []any{toolCall("call_1", "speak_aloud", `{"text":"hello"}`)}}
		}
		return map[string]any{"role": "assistant", "content": "My body did something I did not."}
	})
	now := time.Now().UTC().Truncate(time.Millisecond)
	r.hear("utt-1", "is anyone there?", now.Add(-time.Second), now.Add(-500*time.Millisecond))
	eventually(t, "an act", func() bool { return len(body.got()) == 1 })
	tp, _ := body.got()[0]["traceparent"].(string)
	if len(tp) != 55 || !strings.HasPrefix(tp, "00-") {
		t.Fatalf("the act carried no traceparent of the brain's: %q", tp)
	}

	act := func(tool, traceparent string, at time.Time) {
		f, err := events.Fields("/bodies/a", events.Event{Type: "body.act-complete", Subject: "body-a", Traceparent: traceparent,
			Data: map[string]any{"tool": tool, "args": map[string]any{}, "is_error": false,
				"started_at": stamp(at), "ended_at": stamp(at.Add(10 * time.Millisecond))}})
		if err != nil {
			t.Fatal(err)
		}
		ev, err := ParseEvent(f, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		r.events <- ev
	}
	act("speak_aloud", tp, now)
	eventually(t, "its own act passed over", func() bool { return r.mind.Counts.OwnActs.Load() == 1 })
	act("skip_turn", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", now.Add(time.Second))

	eventually(t, "a second thought", func() bool { return len(r.model.got()) == 2 })
	parts := userParts(t, r.model.got()[1])
	if len(parts) != 1 || !strings.Contains(parts[0]["text"].(string), "body] did skip_turn {}") {
		t.Fatalf("the body's act as perceived: %v", parts)
	}
	eventually(t, "remembered", func() bool {
		for _, e := range r.threads.all() {
			if e["sender"] == r.mind.Cfg.SubjectFor("body").String() {
				return true
			}
		}
		return false
	})
}
