package assistant

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// msg is a prompt message as the fake model reads it.
type msg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// event is an AG-UI event as a client reads it off the wire.
type event struct {
	Type      string `json:"type"`
	ThreadID  string `json:"threadId"`
	RunID     string `json:"runId"`
	MessageID string `json:"messageId"`
	Role      string `json:"role"`
	Delta     string `json:"delta"`
	Message   string `json:"message"`
}

// fakeModel is an OpenAI-shaped chat endpoint that streams pieces, and
// remembers the last prompt it was asked.
type fakeModel struct {
	pieces []string
	status int           // non-zero: answer with this status instead
	hang   bool          // after the first piece, wait for the caller to leave
	left   chan struct{} // closed when a hanging call saw its caller leave
	asked  []msg
}

func (f *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Messages []msg `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.asked = body.Messages
	if f.status != 0 {
		http.Error(w, "the model is down", f.status)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for i, p := range f.pieces {
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": p}}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		w.(http.Flusher).Flush()
		if f.hang && i == 0 {
			<-r.Context().Done()
			close(f.left)
			return
		}
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func serve(t *testing.T, f *fakeModel) *httptest.Server {
	t.Helper()
	model := httptest.NewServer(f)
	t.Cleanup(model.Close)
	mind := httptest.NewServer(New(model.URL, "m", "k", "be brief"))
	t.Cleanup(mind.Close)
	return mind
}

func post(t *testing.T, ctx context.Context, url, principal, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url+"/run", strings.NewReader(body))
	if principal != "" {
		req.Header.Set("X-Principal-Id", principal)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func read(t *testing.T, r io.Reader) []event {
	t.Helper()
	var out []event
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev event
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			t.Fatalf("not an event: %s", data)
		}
		out = append(out, ev)
	}
	return out
}

func kinds(evs []event) string {
	var s []string
	for _, e := range evs {
		s = append(s, e.Type)
	}
	return strings.Join(s, " ")
}

const hello = `{"threadId":"t1","runId":"r1","messages":[{"id":"1","role":"user","content":"hi"}]}`

func TestAnswerStreamsInOrder(t *testing.T) {
	f := &fakeModel{pieces: []string{"Hel", "lo"}}
	resp := post(t, context.Background(), serve(t, f).URL, "alice", hello)
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	evs := read(t, resp.Body)
	want := "RUN_STARTED TEXT_MESSAGE_START TEXT_MESSAGE_CONTENT TEXT_MESSAGE_CONTENT TEXT_MESSAGE_END RUN_FINISHED"
	if got := kinds(evs); got != want {
		t.Fatalf("events\n got %s\nwant %s", got, want)
	}
	if evs[0].ThreadID != "t1" || evs[0].RunID != "r1" || evs[5].RunID != "r1" {
		t.Errorf("thread and run not echoed: %+v %+v", evs[0], evs[5])
	}
	id := evs[1].MessageID
	if id == "" || evs[2].MessageID != id || evs[4].MessageID != id || evs[1].Role != "assistant" {
		t.Errorf("one assistant message expected: %+v", evs[1:5])
	}
	if evs[2].Delta+evs[3].Delta != "Hello" {
		t.Errorf("text %q", evs[2].Delta+evs[3].Delta)
	}
	if len(f.asked) != 2 || f.asked[0].Role != "system" || f.asked[1].Content != "hi" {
		t.Errorf("prompt %+v", f.asked)
	}
}

func TestModelFailureEndsWithRunError(t *testing.T) {
	resp := post(t, context.Background(), serve(t, &fakeModel{status: 502}).URL, "alice", hello)
	defer func() { _ = resp.Body.Close() }()
	evs := read(t, resp.Body)
	if got := kinds(evs); got != "RUN_STARTED RUN_ERROR" {
		t.Fatalf("events %s", got)
	}
	if !strings.Contains(evs[1].Message, "502") {
		t.Errorf("message %q", evs[1].Message)
	}
}

func TestClientLeavingCancelsTheModel(t *testing.T) {
	f := &fakeModel{pieces: []string{"a", "b"}, hang: true, left: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	resp := post(t, ctx, serve(t, f).URL, "alice", hello)
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.Contains(sc.Text(), "TEXT_MESSAGE_CONTENT") {
			break
		}
	}
	cancel()
	select {
	case <-f.left:
	case <-time.After(5 * time.Second):
		t.Fatal("the model call went on after the client left")
	}
}

func TestNoPrincipalIsRefused(t *testing.T) {
	resp := post(t, context.Background(), serve(t, &fakeModel{}).URL, "", hello)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestBadInputIsRefused(t *testing.T) {
	url := serve(t, &fakeModel{}).URL
	for _, body := range []string{
		`not json`,
		`{"messages":[]}`,
		`{"messages":[{"role":"assistant","content":"hi"}]}`,
		`{"messages":[{"role":"user","content":""}]}`,
	} {
		resp := post(t, context.Background(), url, "alice", body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d", body, resp.StatusCode)
		}
	}
}

func TestTextIsTakenFromParts(t *testing.T) {
	f := &fakeModel{pieces: []string{"ok"}}
	body := `{"messages":[
		{"role":"user","content":"first"},
		{"role":"assistant","content":"answer"},
		{"role":"tool","content":"x","toolCallId":"c"},
		{"role":"user","content":[{"type":"text","text":"look"},{"type":"binary","mimeType":"audio/wav","data":"AAAA"},{"type":"text","text":"here"}]}]}`
	resp := post(t, context.Background(), serve(t, f).URL, "alice", body)
	defer func() { _ = resp.Body.Close() }()
	if got := kinds(read(t, resp.Body)); !strings.HasSuffix(got, "RUN_FINISHED") {
		t.Fatalf("events %s", got)
	}
	want := []msg{
		{Role: "system", Content: "be brief"},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "answer"},
		{Role: "user", Content: "look\nhere"},
	}
	if fmt.Sprint(f.asked) != fmt.Sprint(want) {
		t.Errorf("prompt\n got %v\nwant %v", f.asked, want)
	}
}

func TestIDsAreMadeWhenMissing(t *testing.T) {
	resp := post(t, context.Background(), serve(t, &fakeModel{pieces: []string{"x"}}).URL, "alice",
		`{"messages":[{"role":"user","content":"hi"}]}`)
	defer func() { _ = resp.Body.Close() }()
	evs := read(t, resp.Body)
	if evs[0].ThreadID == "" || evs[0].RunID == "" || evs[0].RunID != evs[len(evs)-1].RunID {
		t.Errorf("ids %+v … %+v", evs[0], evs[len(evs)-1])
	}
}
