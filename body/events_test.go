package body

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

type sseEvent struct {
	ID   string
	Data map[string]any
}

// watch opens GET path and hands each event to a channel until the test ends.
func watch(t *testing.T, url string, header http.Header) <-chan sseEvent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("watch: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	out := make(chan sseEvent, 64)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				ev.ID = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev.Data)
			case line == "" && ev.ID != "":
				out <- ev
				ev = sseEvent{}
			}
		}
	}()
	return out
}

func next(t *testing.T, ch <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event in time")
		return sseEvent{}
	}
}

// A head outside sees over HTTP what the body's stream carries — a percept
// and an act — from now, from an id, and from after Last-Event-ID.
func TestEventsOverHTTP(t *testing.T) {
	rdb := testRedis(t)
	hearing := &fakeSense{kind: "hearing"}
	hs := hearing.server()
	defer hs.Close()
	a := startBody(t, rdb, Config{Name: "a", HearingURL: hs.URL})
	eventually(t, "an ear", func() bool { return len(a.body.Self().Ears) == 1 })
	url := a.srv.URL + "/bodies/a/events"

	// Something before the watcher came: not seen from now.
	cs := client(t, a.srv.URL)
	call(t, cs, "skip_turn", nil)

	now := watch(t, url, nil)
	data := map[string]any{"session_id": "hs-1", "request_id": "utt-1", "text": "hello"}
	sent := publish(t, rdb, HearingStream, "/senses/hearing", "speechrecog.recognition-complete", "hs-1", data)
	p := next(t, now)
	if p.Data["type"] != "speechrecog.recognition-complete" || p.Data["source"] != "/bodies/a/hearing" ||
		p.Data["subject"] != DerivedID("a").String() || p.Data["id"] != sent["ce_id"] ||
		p.Data["traceparent"] != sent["ce_traceparent"] || p.Data["specversion"] != "1.0" {
		t.Fatalf("percept: %v", p.Data)
	}
	if d, _ := p.Data["data"].(map[string]any); d["text"] != "hello" {
		t.Fatalf("data is not JSON: %v", p.Data["data"])
	}
	call(t, cs, "skip_turn", nil)
	act := next(t, now)
	if act.Data["type"] != "body.act-complete" || act.Data["data"].(map[string]any)["tool"] != "skip_turn" {
		t.Fatalf("act: %v", act.Data)
	}

	// The ids are the stream's: from=0 is all of it, Last-Event-ID resumes after.
	all := entries(t, rdb, "body_a_events")
	if len(all) != 3 {
		t.Fatalf("stream: %d entries", len(all))
	}
	start := watch(t, url+"?from=0", nil)
	first := next(t, start)
	if first.Data["type"] != "body.act-complete" || next(t, start).ID != p.ID || next(t, start).ID != act.ID {
		t.Fatalf("from 0: %v", first)
	}
	resumed := watch(t, url+"?from=0", http.Header{"Last-Event-ID": {p.ID}})
	if got := next(t, resumed); got.ID != act.ID {
		t.Fatalf("Last-Event-ID wins over from and resumes after it: %v", got)
	}
}

func TestEventsRefusals(t *testing.T) {
	rdb := testRedis(t)
	a := startBody(t, rdb, Config{Name: "a"})
	for path, code := range map[string]int{
		"/bodies/b/events":          http.StatusNotFound,
		"/bodies/a/events?from=abc": http.StatusBadRequest,
	} {
		resp, err := http.Get(a.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != code {
			t.Fatalf("%s: %d, want %d", path, resp.StatusCode, code)
		}
	}
}

func TestCloudEvent(t *testing.T) {
	got := CloudEvent(map[string]any{"ce_type": "x.y", "data": `{"a":1}`, "other": "z"})
	raw, _ := json.Marshal(got)
	if string(raw) != `{"data":{"a":1},"type":"x.y"}` {
		t.Fatalf("%s", raw)
	}
	if got := CloudEvent(map[string]any{"data": "not json"}); got["data"] != "not json" {
		t.Fatalf("%v", got)
	}
}
