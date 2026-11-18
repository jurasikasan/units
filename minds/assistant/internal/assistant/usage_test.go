package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// usageEvents runs a question and returns each event's type, metadata usage
// and run usage, as JSON, and the whole request bodies the model saw.
func usageEvents(t *testing.T, f *scriptedModel) []map[string]json.RawMessage {
	t.Helper()
	mind := serveWithTools(t, f, []Server{{Name: "calc", URL: toolServer(t).URL}}, 0)
	resp := post(t, context.Background(), mind.URL, "alice", `{"runId":"r1","messages":[{"id":"1","role":"user","content":"2+3?"}]}`)
	defer func() { _ = resp.Body.Close() }()
	var out []map[string]json.RawMessage
	for _, line := range strings.Split(readAll(t, resp), "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			var e map[string]json.RawMessage
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				t.Fatalf("not an event: %s", data)
			}
			out = append(out, e)
		}
	}
	return out
}

func ofType(evs []map[string]json.RawMessage, typ string) []map[string]json.RawMessage {
	var out []map[string]json.RawMessage
	for _, e := range evs {
		if string(e["type"]) == `"`+typ+`"` {
			out = append(out, e)
		}
	}
	return out
}

func TestEveryCallWeighsItsContext(t *testing.T) {
	f := &scriptedModel{turns: []turn{
		{calls: []scriptedCall{{id: "c1", name: "calc__add", args: []string{`{"a":2,"b":3}`}}},
			model: "gemini/gemini-3.1-flash-lite",
			usage: `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"completion_tokens_details":{"reasoning_tokens":15}}`},
		{words: "It is 5.", model: "gemini/gemini-3.1-flash-lite",
			usage: `{"prompt_tokens":130,"completion_tokens":5,"total_tokens":135,"prompt_tokens_details":{"cached_tokens":100}}`},
	}}
	evs := usageEvents(t, f)

	for i, req := range f.asked {
		if got, _ := json.Marshal(req["stream_options"]); string(got) != `{"include_usage":true}` {
			t.Errorf("request %d stream_options %s", i, got)
		}
	}
	// Each turn's weight on the event that ends it.
	callEnd := ofType(evs, "TOOL_CALL_END")
	if got := string(callEnd[0]["metadata"]); got != `{"usage":{"provider":"gemini","model":"gemini-3.1-flash-lite","inputTokens":100,"outputTokens":20,"totalTokens":120,"reasoningTokens":15}}` {
		t.Errorf("TOOL_CALL_END metadata %s", got)
	}
	textEnd := ofType(evs, "TEXT_MESSAGE_END")
	if got := string(textEnd[0]["metadata"]); got != `{"usage":{"provider":"gemini","model":"gemini-3.1-flash-lite","inputTokens":130,"outputTokens":5,"totalTokens":135,"cachedInputTokens":100}}` {
		t.Errorf("TEXT_MESSAGE_END metadata %s", got)
	}
	// The run's, summed by model.
	fin := ofType(evs, "RUN_FINISHED")
	if got := string(fin[0]["usage"]); got != `[{"provider":"gemini","model":"gemini-3.1-flash-lite","inputTokens":230,"outputTokens":25,"totalTokens":255,"reasoningTokens":15,"cachedInputTokens":100}]` {
		t.Errorf("RUN_FINISHED usage %s", got)
	}
}

func TestAFallbackIsCountedApart(t *testing.T) {
	f := &scriptedModel{turns: []turn{
		{calls: []scriptedCall{{id: "c1", name: "calc__add", args: []string{`{"a":2,"b":3}`}}},
			model: "openai/glm-4.7-flash", usage: `{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}`},
		{words: "5", model: "openai/@cf/zai-org/glm-4.7-flash", usage: `{"prompt_tokens":20,"completion_tokens":1,"total_tokens":21}`},
	}}
	fin := ofType(usageEvents(t, f), "RUN_FINISHED")
	if got := string(fin[0]["usage"]); got != `[{"provider":"openai","model":"glm-4.7-flash","inputTokens":10,"outputTokens":2,"totalTokens":12},{"provider":"openai","model":"@cf/zai-org/glm-4.7-flash","inputTokens":20,"outputTokens":1,"totalTokens":21}]` {
		t.Errorf("RUN_FINISHED usage %s", got)
	}
}

func TestNoUsageToldNoneMadeUp(t *testing.T) {
	evs := usageEvents(t, &scriptedModel{turns: []turn{{words: "hi"}}})
	if m := ofType(evs, "TEXT_MESSAGE_END")[0]["metadata"]; m != nil {
		t.Errorf("metadata %s", m)
	}
	if u := ofType(evs, "RUN_FINISHED")[0]["usage"]; u != nil {
		t.Errorf("usage %s", u)
	}
}

func TestAFailureAfterACallCarriesItsUsage(t *testing.T) {
	// Steps run out after one call: RUN_ERROR carries what was used.
	f := &scriptedModel{turns: []turn{{calls: []scriptedCall{{id: "c1", name: "calc__add", args: []string{`{"a":2,"b":3}`}}},
		usage: `{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}`}}}
	mind := serveWithTools(t, f, []Server{{Name: "calc", URL: toolServer(t).URL}}, 2)
	resp := post(t, context.Background(), mind.URL, "alice", `{"messages":[{"role":"user","content":"loop"}]}`)
	defer func() { _ = resp.Body.Close() }()
	body := readAll(t, resp)
	if !strings.Contains(body, `"type":"RUN_ERROR"`) || !strings.Contains(body, `"usage":[{"model":"m","inputTokens":14,"outputTokens":6,"totalTokens":20}]`) {
		t.Errorf("events: %s", body[strings.LastIndex(body, "data: "):])
	}
}
