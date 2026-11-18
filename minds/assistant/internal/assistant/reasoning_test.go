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
)

// rawTurn is one answer of the model as LiteLLM streams it: the deltas
// exactly as they go on the wire, and who LiteLLM says answered.
type rawTurn struct {
	model  string // x-litellm-model-name; none when empty
	deltas []string
}

// rawModel answers each request with the next turn (the last again when
// they run out) and keeps every request's body.
type rawModel struct {
	mu    sync.Mutex
	turns []rawTurn
	asked []map[string]any
}

func (f *rawModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	n := len(f.asked)
	f.asked = append(f.asked, body)
	f.mu.Unlock()
	t := f.turns[min(n, len(f.turns)-1)]
	if t.model != "" {
		w.Header().Set("X-Litellm-Model-Name", t.model)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, d := range t.deltas {
		_, _ = fmt.Fprintf(w, `data: {"id":"x","object":"chat.completion.chunk","model":"group","choices":[{"index":0,"delta":%s}]}`+"\n\n", d)
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

// rEvent is an AG-UI event with the fields reasoning uses.
type rEvent struct {
	Type           string `json:"type"`
	MessageID      string `json:"messageId"`
	Role           string `json:"role"`
	Delta          string `json:"delta"`
	Subtype        string `json:"subtype"`
	EntityID       string `json:"entityId"`
	EncryptedValue string `json:"encryptedValue"`
	ToolCallID     string `json:"toolCallId"`
	Message        string `json:"message"`
}

func runRaw(t *testing.T, f *rawModel, servers []Server, body string) []rEvent {
	t.Helper()
	model := httptest.NewServer(f)
	t.Cleanup(model.Close)
	a := New(model.URL, "group", "k", "")
	if servers != nil {
		a.UseTools(servers, 0)
	}
	mind := httptest.NewServer(a)
	t.Cleanup(mind.Close)
	resp := post(t, context.Background(), mind.URL, "alice", body)
	defer func() { _ = resp.Body.Close() }()
	var out []rEvent
	for _, line := range strings.Split(readAll(t, resp), "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			var e rEvent
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				t.Fatalf("not an event: %s", data)
			}
			out = append(out, e)
		}
	}
	return out
}

func rKinds(evs []rEvent) string {
	var s []string
	for _, e := range evs {
		s = append(s, e.Type)
	}
	return strings.Join(s, " ")
}

func find(evs []rEvent, kind string) []rEvent {
	var out []rEvent
	for _, e := range evs {
		if e.Type == kind {
			out = append(out, e)
		}
	}
	return out
}

const question = `{"messages":[{"id":"1","role":"user","content":"Is 1001 prime?"}]}`

// GLM through Cloudflare's OpenAI door, as LiteLLM v1.108 streams it:
// reasoning_content and the same words again as reasoning, then the answer;
// nothing opaque.
func TestGLMReasoningIsShownOnceBeforeTheText(t *testing.T) {
	f := &rawModel{turns: []rawTurn{{model: "openai/@cf/zai-org/glm-4.7-flash", deltas: []string{
		`{"role":"assistant"}`,
		`{"reasoning_content":"1001 = ","reasoning":"1001 = "}`,
		`{"reasoning_content":"7·11·13","reasoning":"7·11·13"}`,
		`{"content":"No."}`,
	}}}}
	evs := runRaw(t, f, nil, question)
	want := "RUN_STARTED REASONING_START REASONING_MESSAGE_START REASONING_MESSAGE_CONTENT REASONING_MESSAGE_CONTENT " +
		"REASONING_MESSAGE_END REASONING_END TEXT_MESSAGE_START TEXT_MESSAGE_CONTENT TEXT_MESSAGE_END RUN_FINISHED"
	if got := rKinds(evs); got != want {
		t.Fatalf("events\n got %s\nwant %s", got, want)
	}
	id := evs[1].MessageID
	if id == "" || evs[2].MessageID != id || evs[2].Role != "reasoning" || evs[5].MessageID != id || evs[6].MessageID != id {
		t.Errorf("one reasoning message expected: %+v", evs[1:7])
	}
	if evs[3].Delta+evs[4].Delta != "1001 = 7·11·13" {
		t.Errorf("reasoning %q", evs[3].Delta+evs[4].Delta)
	}
	if evs[7].MessageID == id {
		t.Error("the answer is a message of its own")
	}
}

// Gemini 3.1 flash-lite at reasoning_effort high, as LiteLLM v1.108 streams
// it: reasoning_content; then thought_signatures in the message's
// provider_specific_fields in the same delta as the tool call, which carries
// its own thought_signature and the signature again inside its id. The
// answer after the tool: words, then a new signature, no reasoning shown.
var geminiTurns = []rawTurn{
	{model: "gemini/gemini-3.1-flash-lite", deltas: []string{
		`{"reasoning_content":"**Adding**\n\nUse the tool.","role":"assistant"}`,
		`{"provider_specific_fields":{"thought_signatures":["SIG1"]},"tool_calls":[{"id":"call_1__thought__SIG1","function":{"arguments":"{\"a\": 2, \"b\": 3}","name":"calc__add"},"type":"function","index":0,"provider_specific_fields":{"thought_signature":"SIG1"}}]}`,
	}},
	{model: "gemini/gemini-3.1-flash-lite", deltas: []string{
		`{"role":"assistant","content":"It is 5."}`,
		`{"provider_specific_fields":{"thought_signatures":["SIG2"]}}`,
	}},
}

func TestGeminiOpaquePartGoesBackThroughTheToolLoop(t *testing.T) {
	f := &rawModel{turns: geminiTurns}
	evs := runRaw(t, f, []Server{{Name: "calc", URL: toolServer(t).URL}}, `{"messages":[{"id":"1","role":"user","content":"2+3?"}]}`)
	want := "RUN_STARTED REASONING_START REASONING_MESSAGE_START REASONING_MESSAGE_CONTENT REASONING_MESSAGE_END REASONING_END " +
		"TOOL_CALL_START TOOL_CALL_ARGS REASONING_ENCRYPTED_VALUE TOOL_CALL_END TOOL_CALL_RESULT " +
		"TEXT_MESSAGE_START TEXT_MESSAGE_CONTENT REASONING_START REASONING_ENCRYPTED_VALUE REASONING_END TEXT_MESSAGE_END RUN_FINISHED"
	if got := rKinds(evs); got != want {
		t.Fatalf("events\n got %s\nwant %s", got, want)
	}

	sealed := find(evs, "REASONING_ENCRYPTED_VALUE")
	if sealed[0].Subtype != "message" || sealed[0].EntityID != evs[1].MessageID {
		t.Errorf("the first opaque part belongs to the turn's reasoning message %s: %+v", evs[1].MessageID, sealed[0])
	}
	if sealed[1].EntityID != evs[13].MessageID || sealed[1].EntityID == evs[1].MessageID {
		t.Errorf("the second turn showed no reasoning: its opaque part has an empty reasoning message of its own: %+v", sealed[1])
	}
	o := unseal(sealed[0].EncryptedValue)
	if o == nil || o.Model != "gemini/gemini-3.1-flash-lite" || fmt.Sprint(o.Fields["thought_signatures"]) != "[SIG1]" ||
		o.Calls["call_1__thought__SIG1"]["thought_signature"] != "SIG1" {
		t.Errorf("opaque part %+v", o)
	}

	// Asked again, the same model gets its signatures back where they came.
	a := assistantMsgs(f.asked[1])
	if len(a) != 1 {
		t.Fatalf("one answer in the second request: %v", a)
	}
	if got := jsonOf(a[0]["provider_specific_fields"]); got != `{"thought_signatures":["SIG1"]}` {
		t.Errorf("message's fields back: %s", got)
	}
	c := a[0]["tool_calls"].([]any)[0].(map[string]any)
	if c["id"] != "call_1__thought__SIG1" || jsonOf(c["provider_specific_fields"]) != `{"thought_signature":"SIG1"}` {
		t.Errorf("the call back: %s", jsonOf(c))
	}
	if _, shown := a[0]["reasoning_content"]; shown {
		t.Error("the words of the reasoning are the person's, not sent back")
	}
}

// A run in which LiteLLM falls back to another model after the first turn:
// what the first model made is not shown to the second, what the second made
// goes back to it, and the run completes.
func TestFallbackDropsTheOtherModelsOpaquePart(t *testing.T) {
	f := &rawModel{turns: []rawTurn{
		geminiTurns[0],
		{model: "openai/other", deltas: []string{
			`{"provider_specific_fields":{"state":"B1"},"tool_calls":[{"id":"c2","function":{"arguments":"{\"a\":5,\"b\":1}","name":"calc__add"},"type":"function","index":0,"provider_specific_fields":{"s":"B1"}}]}`,
		}},
		{model: "openai/other", deltas: []string{`{"content":"6."}`}},
	}}
	evs := runRaw(t, f, []Server{{Name: "calc", URL: toolServer(t).URL}}, `{"messages":[{"id":"1","role":"user","content":"2+3+1?"}]}`)
	if got := rKinds(evs); !strings.HasSuffix(got, "TEXT_MESSAGE_END RUN_FINISHED") {
		t.Fatalf("events %s", got)
	}
	if len(f.asked) != 3 {
		t.Fatalf("asked %d times", len(f.asked))
	}
	// The second request was meant for gemini, which answered the first.
	if a := assistantMsgs(f.asked[1]); jsonOf(a[0]["provider_specific_fields"]) != `{"thought_signatures":["SIG1"]}` {
		t.Errorf("second request: %s", jsonOf(a))
	}
	// The third goes to the model that answered the second.
	a := assistantMsgs(f.asked[2])
	if _, has := a[0]["provider_specific_fields"]; has {
		t.Errorf("gemini's part went to another model: %s", jsonOf(a[0]))
	}
	if c := a[0]["tool_calls"].([]any)[0].(map[string]any); c["id"] != "call_1__thought__SIG1" || c["provider_specific_fields"] != nil {
		t.Errorf("gemini's call: id kept, its fields not: %s", jsonOf(c))
	}
	if jsonOf(a[1]["provider_specific_fields"]) != `{"state":"B1"}` {
		t.Errorf("the other model's own part: %s", jsonOf(a[1]))
	}
}

// The next run: the client sends the history back with the reasoning
// messages, words and opaque parts, where assistant-ui puts them (seen in
// the lab): a tool turn's reasoning after the tool's result, an answer's
// after the answer. Each opaque part names its answer, so the place does not
// matter.
func TestHistoryReturnsOpaquePartsToTheirModelOnly(t *testing.T) {
	gemini := (&opaque{Model: "gemini/x", Message: "a1", Fields: map[string]any{"thought_signatures": []any{"G1"}},
		Calls: map[string]map[string]any{"c1": {"thought_signature": "G1"}}}).seal()
	answer := func(model string) string {
		return (&opaque{Model: model, Message: "a2", Fields: map[string]any{"thought_signatures": []any{"G2"}}}).seal()
	}
	history := func(last string) string {
		return `{"messages":[
		{"id":"1","role":"user","content":"2+3?"},
		{"id":"a1","role":"assistant","toolCalls":[{"id":"c1","type":"function","function":{"name":"calc__add","arguments":"{}"}}]},
		{"id":"3","role":"tool","toolCallId":"c1","content":"5"},
		{"id":"r1","role":"reasoning","content":"Use the tool.","encryptedValue":"` + gemini + `"},
		{"id":"a2","role":"assistant","content":"5."},
		{"id":"r2","role":"reasoning","content":"","encryptedValue":"` + last + `"},
		{"id":"r3","role":"reasoning","content":"lost","encryptedValue":"not ours"},
		{"id":"5","role":"user","content":"and again?"}]}`
	}

	// The latest answer was gemini's: gemini is asked with each part on its turn.
	f := &rawModel{turns: []rawTurn{{model: "gemini/x", deltas: []string{`{"content":"5."}`}}}}
	runRaw(t, f, nil, history(answer("gemini/x")))
	a := assistantMsgs(f.asked[0])
	if len(a) != 2 || jsonOf(a[0]["provider_specific_fields"]) != `{"thought_signatures":["G1"]}` ||
		jsonOf(a[0]["tool_calls"].([]any)[0].(map[string]any)["provider_specific_fields"]) != `{"thought_signature":"G1"}` ||
		jsonOf(a[1]["provider_specific_fields"]) != `{"thought_signatures":["G2"]}` {
		t.Errorf("gemini's parts back to gemini, each on its turn: %s", jsonOf(a))
	}
	if msgs := jsonOf(f.asked[0]["messages"]); strings.Contains(msgs, "Use the tool") || strings.Contains(msgs, "lost") {
		t.Errorf("reasoning words reached the model: %s", msgs)
	}

	// The latest answer was another model's (a fallback): gemini's is left out.
	f = &rawModel{turns: []rawTurn{{model: "openai/other", deltas: []string{`{"content":"5."}`}}}}
	runRaw(t, f, nil, history(answer("openai/other")))
	a = assistantMsgs(f.asked[0])
	if _, has := a[0]["provider_specific_fields"]; has {
		t.Errorf("gemini's part went to another model: %s", jsonOf(a))
	}
	if jsonOf(a[1]["provider_specific_fields"]) != `{"thought_signatures":["G2"]}` {
		t.Errorf("the other model's own part: %s", jsonOf(a[1]))
	}
}

// thinking_blocks streamed in pieces (Anthropic's shape through LiteLLM;
// not seen live here) go back as whole blocks.
func TestThinkingBlocksAreJoined(t *testing.T) {
	f := &rawModel{turns: []rawTurn{
		{model: "anthropic/x", deltas: []string{
			`{"reasoning_content":"Hm","thinking_blocks":[{"type":"thinking","thinking":"Hm"}]}`,
			`{"reasoning_content":"m.","thinking_blocks":[{"type":"thinking","thinking":"m.","signature":"S"}]}`,
			`{"tool_calls":[{"id":"c1","function":{"arguments":"{}","name":"calc__add"},"type":"function","index":0}]}`,
		}},
		{model: "anthropic/x", deltas: []string{`{"content":"ok"}`}},
	}}
	runRaw(t, f, []Server{{Name: "calc", URL: toolServer(t).URL}}, question)
	a := assistantMsgs(f.asked[1])
	if got := jsonOf(a[0]["thinking_blocks"]); got != `[{"signature":"S","thinking":"Hmm.","type":"thinking"}]` {
		t.Errorf("thinking blocks back: %s", got)
	}
}

func assistantMsgs(req map[string]any) []map[string]any {
	var out []map[string]any
	msgs, _ := req["messages"].([]any)
	for _, m := range msgs {
		if m := m.(map[string]any); m["role"] == "assistant" {
			out = append(out, m)
		}
	}
	return out
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
