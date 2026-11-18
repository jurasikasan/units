package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runError is the RUN_ERROR a run ended with, as a client reads it.
type runError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    string `json:"code"`
	Usage   []struct {
		Model       string `json:"model"`
		InputTokens int    `json:"inputTokens"`
	} `json:"usage"`
}

// lastEvent runs the question against mind and returns the event types and
// the last event in full.
func lastEvent(t *testing.T, mindURL string) (string, runError) {
	t.Helper()
	resp := post(t, context.Background(), mindURL, "alice", hello)
	defer func() { _ = resp.Body.Close() }()
	var kinds []string
	var last runError
	for _, line := range strings.Split(readAll(t, resp), "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			last = runError{}
			if err := json.Unmarshal([]byte(data), &last); err != nil {
				t.Fatalf("not an event: %s", data)
			}
			kinds = append(kinds, last.Type)
		}
	}
	return strings.Join(kinds, " "), last
}

func mindOn(t *testing.T, model http.Handler) (*Assistant, string) {
	t.Helper()
	m := httptest.NewServer(model)
	t.Cleanup(m.Close)
	a := New(m.URL, "m", "k", "")
	mind := httptest.NewServer(a)
	t.Cleanup(mind.Close)
	return a, mind.URL
}

// An endpoint that answers with a status says why in its own words, as
// LiteLLM does: those words reach the person, and the code says which kind.
func TestAFailedCallSaysWhichAndWhy(t *testing.T) {
	for _, c := range []struct {
		status int
		code   string
	}{
		{429, CodeModelRateLimited},
		{400, CodeModelRefused},
		{401, CodeModelRefused},
		{500, CodeModelFailed},
		{503, CodeModelFailed},
		{408, CodeModelTimedOut},
		{504, CodeModelTimedOut},
	} {
		t.Run(fmt.Sprint(c.status), func(t *testing.T) {
			words := fmt.Sprintf("litellm says %d (fake)", c.status)
			_, url := mindOn(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.status)
				_, _ = fmt.Fprintf(w, `{"error":{"message":%q,"type":"x","code":"%d"}}`, words, c.status)
			}))
			kinds, last := lastEvent(t, url)
			if kinds != "RUN_STARTED RUN_ERROR" {
				t.Fatalf("events %s", kinds)
			}
			if last.Code != c.code {
				t.Errorf("code %q, want %q", last.Code, c.code)
			}
			if !strings.Contains(last.Message, words) || !strings.Contains(last.Message, fmt.Sprint(c.status)) {
				t.Errorf("message %q does not carry the endpoint's words", last.Message)
			}
		})
	}
}

func TestNoEndpointIsUnreachable(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	mind := httptest.NewServer(New(gone.URL, "m", "k", ""))
	t.Cleanup(mind.Close)
	kinds, last := lastEvent(t, mind.URL)
	if kinds != "RUN_STARTED RUN_ERROR" || last.Code != CodeModelUnreachable {
		t.Fatalf("events %s, %+v", kinds, last)
	}
}

// LiteLLM reports a provider failing midway as an error in the stream.
func TestAnErrorInTheStreamEndsTheRun(t *testing.T) {
	_, url := mindOn(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"Hal"}}]}`+"\n\n")
		_, _ = fmt.Fprint(w, `data: {"error":{"message":"the provider fell over","code":"503"}}`+"\n\n")
	}))
	kinds, last := lastEvent(t, url)
	if kinds != "RUN_STARTED TEXT_MESSAGE_START TEXT_MESSAGE_CONTENT RUN_ERROR" {
		t.Fatalf("events %s", kinds)
	}
	if last.Code != CodeModelFailed || !strings.Contains(last.Message, "the provider fell over") {
		t.Errorf("%+v", last)
	}
}

func TestAConnectionCutMidwayEndsTheRun(t *testing.T) {
	_, url := mindOn(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"Hal"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
	}))
	kinds, last := lastEvent(t, url)
	if !strings.HasSuffix(kinds, "RUN_ERROR") || last.Code != CodeModelFailed {
		t.Fatalf("events %s, %+v", kinds, last)
	}
}

// A model that stops saying anything is dropped after the idle limit: the
// run ends, and the endpoint sees its request go.
func TestASilentModelIsDropped(t *testing.T) {
	left := make(chan struct{})
	a, url := mindOn(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"Hal"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(left)
	}))
	a.SetIdle(300 * time.Millisecond)
	start := time.Now()
	kinds, last := lastEvent(t, url)
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v", d)
	}
	if !strings.HasSuffix(kinds, "RUN_ERROR") || last.Code != CodeModelTimedOut {
		t.Fatalf("events %s, %+v", kinds, last)
	}
	select {
	case <-left:
	case <-time.After(2 * time.Second):
		t.Fatal("the silent call was not dropped")
	}
}

// A model that keeps talking, slowly, is not silent: the limit is between
// bytes, not on the whole answer.
func TestASlowModelIsNotSilent(t *testing.T) {
	a, url := mindOn(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for range 5 {
			_, _ = fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"."}}]}`+"\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(150 * time.Millisecond)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	a.SetIdle(400 * time.Millisecond)
	if kinds, _ := lastEvent(t, url); !strings.HasSuffix(kinds, "RUN_FINISHED") {
		t.Fatalf("events %s", kinds)
	}
}

// What the calls before the failed one weighed goes with RUN_ERROR.
func TestUsageSoFarGoesWithTheError(t *testing.T) {
	first := &scriptedModel{turns: []turn{{
		calls: []scriptedCall{{id: "c1", name: "calc__add", args: []string{`{"a":1,"b":1}`}}},
		usage: `{"prompt_tokens":120,"completion_tokens":5,"total_tokens":125}`,
	}}}
	var mu sync.Mutex
	n := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		k := n
		mu.Unlock()
		if k == 1 {
			first.ServeHTTP(w, r)
			return
		}
		http.Error(w, `{"error":{"message":"down"}}`, http.StatusInternalServerError)
	}))
	t.Cleanup(model.Close)
	a := New(model.URL, "m", "k", "")
	a.UseTools([]Server{{Name: "calc", URL: toolServer(t).URL}}, 0)
	mind := httptest.NewServer(a)
	t.Cleanup(mind.Close)
	kinds, last := lastEvent(t, mind.URL)
	if !strings.HasSuffix(kinds, "TOOL_CALL_RESULT RUN_ERROR") {
		t.Fatalf("events %s", kinds)
	}
	if len(last.Usage) != 1 || last.Usage[0].InputTokens != 120 {
		t.Errorf("usage %+v", last.Usage)
	}
}

// logs captures what the mind logs while a test runs.
func logs(t *testing.T) *syncBuffer {
	t.Helper()
	b := &syncBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(b, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return b
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// The client leaving while a tool works cuts the tool's call too, and the
// log says the run was cancelled, not that it failed.
func TestClientLeavingCancelsTheTool(t *testing.T) {
	log := logs(t)
	started, left := make(chan struct{}), make(chan struct{})
	s := mcp.NewServer(&mcp.Implementation{Name: "calc", Version: "test"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "wait", Description: "waits until the caller leaves"},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			close(started)
			<-ctx.Done()
			close(left)
			return nil, nil, ctx.Err()
		})
	// Under MCP 2026-07-28 a call is cancelled by dropping its request; a
	// go-sdk server hands that on to the tool when asked to.
	tools := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s },
		&mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true}))
	t.Cleanup(tools.Close)
	f := &scriptedModel{turns: []turn{{calls: []scriptedCall{{id: "c1", name: "calc__wait", args: []string{"{}"}}}}}}
	mind := serveWithTools(t, f, []Server{{Name: "calc", URL: tools.URL}}, 0)

	ctx, cancel := context.WithCancel(context.Background())
	resp := post(t, ctx, mind.URL, "alice", hello)
	defer func() { _ = resp.Body.Close() }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the tool was not called")
	}
	cancel()
	select {
	case <-left:
	case <-time.After(5 * time.Second):
		t.Fatal("the tool call went on after the client left")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(log.String(), "run cancelled") {
		if time.Now().After(deadline) {
			t.Fatalf("the log does not say cancelled:\n%s", log.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Contains(log.String(), "model call failed") {
		t.Errorf("a cancel logged as a failure:\n%s", log.String())
	}
}
