package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// say is a tool a client offers: the model calls it, the client makes it.
const sayTool = `{"name":"say","description":"say something in the thread","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}`

func events_(t *testing.T, mind string, body string) []toolEvent {
	t.Helper()
	resp := post(t, context.Background(), mind, "alice", body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readAll(t, resp))
	}
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

func offeredNames(req map[string]any) string {
	var names []string
	tools, _ := req["tools"].([]any)
	for _, tool := range tools {
		fn := tool.(map[string]any)["function"].(map[string]any)
		names = append(names, fn["name"].(string))
	}
	return strings.Join(names, " ")
}

// The client's tool is offered beside the mind's own; a turn calling both
// has the mind's made and the client's left, and the run ends there.
func TestClientToolIsOfferedAndLeftToTheClient(t *testing.T) {
	f := &scriptedModel{turns: []turn{
		{calls: []scriptedCall{
			{id: "c1", name: "calc__add", args: []string{`{"a":2,"b":3}`}},
			{id: "c2", name: "say", args: []string{`{"text":`, `"five"}`}},
		}},
		{words: "never asked"},
	}}
	mind := serveWithTools(t, f, []Server{{Name: "calc", URL: toolServer(t).URL}}, 0)
	evs := events_(t, mind.URL, `{"messages":[{"id":"1","role":"user","content":"2+3, and say it"}],"tools":[`+sayTool+`]}`)

	want := "RUN_STARTED TOOL_CALL_START TOOL_CALL_ARGS TOOL_CALL_START TOOL_CALL_ARGS TOOL_CALL_ARGS " +
		"TOOL_CALL_END TOOL_CALL_END TOOL_CALL_RESULT RUN_FINISHED"
	if got := types_(evs); got != want {
		t.Fatalf("events\n got %s\nwant %s", got, want)
	}
	if evs[3].ToolCallID != "c2" || evs[3].ToolCallName != "say" || evs[4].Delta+evs[5].Delta != `{"text":"five"}` {
		t.Errorf("the client's call: %+v %+v %+v", evs[3], evs[4], evs[5])
	}
	if evs[8].ToolCallID != "c1" || evs[8].Content != "5" {
		t.Errorf("only the mind's call is answered: %+v", evs[8])
	}
	if len(f.asked) != 1 {
		t.Errorf("the model was asked %d times; the run should end after the call", len(f.asked))
	}
	if got := offeredNames(f.asked[0]); got != "calc__add calc__draw calc__fail calc__slow say" {
		t.Errorf("offered: %s", got)
	}
	tools, _ := json.Marshal(f.asked[0]["tools"])
	if !strings.Contains(string(tools), `"required":["text"]`) {
		t.Errorf("the client's schema did not go as it came: %s", tools)
	}
}

// The client's next run ends with the result of its tool, and the model
// goes on from it, the call and the result in its prompt.
func TestClientToolResultCarriesOn(t *testing.T) {
	f := &scriptedModel{turns: []turn{{words: "Said it."}}}
	mind := serveWithTools(t, f, nil, 0)
	evs := events_(t, mind.URL, `{"messages":[
		{"id":"1","role":"user","content":"say five"},
		{"id":"2","role":"assistant","toolCalls":[{"id":"c2","type":"function","function":{"name":"say","arguments":"{\"text\":\"five\"}"}}]},
		{"id":"3","role":"tool","toolCallId":"c2","content":"said"}
	],"tools":[`+sayTool+`]}`)

	if got := types_(evs); got != "RUN_STARTED TEXT_MESSAGE_START TEXT_MESSAGE_CONTENT TEXT_MESSAGE_END RUN_FINISHED" {
		t.Fatalf("events %s", got)
	}
	if got := callsAndResults(f.asked[0]); got != `call c2 say {"text":"five"}; result c2 said` {
		t.Errorf("prompt: %s", got)
	}
	if got := offeredNames(f.asked[0]); got != "say" {
		t.Errorf("offered: %s", got)
	}
}

// A tool whose name a model cannot take, or a name taken already, is left
// out; the run goes on with the rest. A client tool with no parameters is
// offered as an object with none.
func TestClientToolsLeftOut(t *testing.T) {
	f := &scriptedModel{turns: []turn{{words: "ok"}}}
	mind := serveWithTools(t, f, []Server{{Name: "calc", URL: toolServer(t).URL}}, 0)
	events_(t, mind.URL, `{"messages":[{"id":"1","role":"user","content":"hi"}],"tools":[
		{"name":"skip turn","description":"a space"},
		{"name":"calc__add","description":"the mind's"},
		{"name":"skip","description":"first"},
		{"name":"skip","description":"again"}
	]}`)
	if got := offeredNames(f.asked[0]); got != "calc__add calc__draw calc__fail calc__slow skip" {
		t.Errorf("offered: %s", got)
	}
	tools, _ := json.Marshal(f.asked[0]["tools"])
	if !strings.Contains(string(tools), `"description":"first","name":"skip","parameters":{"properties":{},"type":"object"}`) {
		t.Errorf("tools: %s", tools)
	}
}

// A run ending with a tool's result that answers no call before it is not
// one the mind can answer.
func TestToolResultWithoutItsCallIsRefused(t *testing.T) {
	mind := serveWithTools(t, &scriptedModel{turns: []turn{{words: "x"}}}, nil, 0)
	resp := post(t, context.Background(), mind.URL, "alice", `{"messages":[
		{"id":"1","role":"user","content":"hi"},
		{"id":"3","role":"tool","toolCallId":"nobody","content":"said"}]}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d", resp.StatusCode)
	}
}
