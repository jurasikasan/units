package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func call(t *testing.T, method, url string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		r = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, url, r)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func startHost(t *testing.T, threads string) (*Host, string) {
	t.Helper()
	c := liveRedis(t)
	h := NewHost(HostConfig{ThreadsURL: threads, Prompt: "be a mind"}, c, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	h.Start(ctx)
	srv := httptest.NewServer(h.Handler("test"))
	t.Cleanup(func() { srv.Close(); cancel(); h.Stop() })
	return h, srv.URL
}

// A brain made through the API thinks its body's perception and acts
// through it; its policy changes alone, memory and all kept; with no policy
// it perceives and does not think; the same spec changes nothing, another
// makes it anew and it finds its memory again; DELETE lets it go.
func TestHostBrainAndItsPolicy(t *testing.T) {
	threads := &fakeThreads{}
	ts := httptest.NewServer(threads)
	defer ts.Close()
	m1, m2 := &fakeModel{}, &fakeModel{}
	answer := func(k int) map[string]any {
		return map[string]any{"role": "assistant", "content": "thought",
			"tool_calls": []any{toolCall("call_"+uuid.NewString()[:8], "skip_turn", "{}")}}
	}
	m1.answer, m2.answer = answer, answer
	s1, s2 := httptest.NewServer(m1), httptest.NewServer(m2)
	defer s1.Close()
	defer s2.Close()
	body := &fakeBody{}
	bs := body.server(t)
	h, base := startHost(t, ts.URL)
	c := h.Redis
	stream := "host_test_" + uuid.NewString()[:8]
	subject := uuid.MustParse("00000000-0000-0000-0000-0000000000c1")

	spec := Spec{Subject: subject.String(), Body: bs.URL, Senses: []string{stream}, Settle: "20ms",
		Policy: &PolicySpec{ModelURL: s1.URL + "/v1", Model: "one", Key: "secret"}}
	code, raw := call(t, http.MethodPut, base+"/brains/a", spec)
	var l Listed
	_ = json.Unmarshal(raw, &l)
	if code != http.StatusCreated || l.Subject != subject.String() || l.Policy == nil || l.Policy.Key != "" || l.Policy.Model != "one" {
		t.Fatalf("made: %d %s", code, raw)
	}
	first := h.Mind("a")

	hear := func(text string) {
		at := time.Now()
		publish(t, c, stream, "/bodies/a/hearing", "speechrecog.recognition-complete", map[string]any{
			"request_id": uuid.NewString(), "completion_cause": "success", "text": text, "input_type": "speech",
			"started_at": stamp(at), "ended_at": stamp(at.Add(100 * time.Millisecond))})
	}
	hear("hello")
	eventually(t, "the first policy thought and acted", func() bool { return len(m1.got()) == 1 && len(body.got()) == 1 })

	// The policy alone changes: the next thought goes to the other model,
	// recalling what came before.
	if code, raw := call(t, http.MethodPut, base+"/brains/a/policy", PolicySpec{ModelURL: s2.URL + "/v1", Model: "two"}); code != http.StatusOK {
		t.Fatalf("policy: %d %s", code, raw)
	}
	if h.Mind("a") != first {
		t.Fatal("a policy change made the brain anew")
	}
	hear("are you there")
	eventually(t, "the second policy thought", func() bool { return len(m2.got()) == 1 })
	msgs := m2.got()[0]["messages"].([]any)
	if len(msgs) < 4 || len(m1.got()) != 1 {
		t.Fatalf("the second policy did not recall the first's thought: %d messages", len(msgs))
	}

	// No policy: perceived, not thought, not remembered.
	if code, raw := call(t, http.MethodPut, base+"/brains/a/policy", "null"); code != http.StatusOK || !strings.Contains(string(raw), `"policy":null`) {
		t.Fatalf("no policy: %d %s", code, raw)
	}
	// What the second thought writes is written after its act: wait for it to settle.
	eventually(t, "the second act", func() bool { return len(body.got()) == 2 })
	written := -1
	eventually(t, "memory settled", func() bool {
		n := len(threads.all())
		settled := n == written
		written = n
		time.Sleep(50 * time.Millisecond)
		return settled
	})
	hear("anyone?")
	eventually(t, "unthought", func() bool { return first.Counts.Unthought.Load() == 1 })
	if len(m1.got())+len(m2.got()) != 2 || len(threads.all()) != written {
		t.Fatalf("thought or remembered with no policy: %d %d", len(m1.got())+len(m2.got()), len(threads.all()))
	}

	// The same spec changes nothing but the policy it names.
	if code, _ := call(t, http.MethodPut, base+"/brains/a", spec); code != http.StatusOK || h.Mind("a") != first {
		t.Fatalf("same spec: %d", code)
	}
	if p := h.Mind("a").Policy(); p == nil || p.Model.Name != "one" {
		t.Fatalf("the spec's policy: %+v", p)
	}
	// Another spec makes it anew; it finds its thread again by its subject.
	spec.Prompt = "be another mind"
	if code, _ := call(t, http.MethodPut, base+"/brains/a", spec); code != http.StatusOK || h.Mind("a") == first {
		t.Fatalf("another spec: %d", code)
	}
	hear("again")
	eventually(t, "the new brain thought", func() bool { return len(m1.got()) == 2 })
	if len(threads.opened) != 1 {
		t.Fatalf("threads opened: %v", threads.opened)
	}

	var all []Listed
	_, raw = call(t, http.MethodGet, base+"/brains", nil)
	if json.Unmarshal(raw, &all) != nil || len(all) != 1 || all[0].Spec.Prompt != "be another mind" || strings.Contains(string(raw), "secret") {
		t.Fatalf("list: %s", raw)
	}
	if code, raw := call(t, http.MethodGet, base+"/health", nil); code != http.StatusOK || !strings.Contains(string(raw), `"a":{"buffered"`) {
		t.Fatalf("health: %s", raw)
	}
	if code, _ := call(t, http.MethodDelete, base+"/brains/a", nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	for _, path := range []string{"/brains/a", "/brains/a/policy"} {
		if code, _ := call(t, http.MethodGet, base+path, nil); code != http.StatusNotFound {
			t.Fatalf("%s after delete: %d", path, code)
		}
	}
}

func TestHostRefusesASpec(t *testing.T) {
	h, base := startHost(t, "http://127.0.0.1:1")
	good := PolicySpec{ModelURL: "http://m/v1", Model: "x"}
	for what, spec := range map[string]any{
		"nothing to read":     Spec{},
		"a bare body alone":   Spec{Body: "http://body:8080"},
		"body not a URL":      Spec{Body: "body_a", Senses: []string{"s"}},
		"subject not a uuid":  Spec{Subject: "x", Senses: []string{"s"}},
		"recall over 1000":    Spec{Senses: []string{"s"}, Recall: new(1001)},
		"settle not duration": Spec{Senses: []string{"s"}, Settle: "soon"},
		"gate not open":       Spec{Senses: []string{"s"}, Gate: "sight:ajar"},
		"policy no model":     Spec{Senses: []string{"s"}, Policy: &PolicySpec{ModelURL: "http://m/v1"}},
		"policy bad URL":      Spec{Senses: []string{"s"}, Policy: &PolicySpec{ModelURL: "m", Model: "x"}},
		"unknown field":       `{"senses": ["s"], "polcy": {}}`,
	} {
		if code, raw := call(t, http.MethodPut, base+"/brains/a", spec); code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", what, code, raw)
		}
	}
	if code, _ := call(t, http.MethodPut, base+"/brains/zz/policy", good); code != http.StatusNotFound {
		t.Fatalf("policy of no brain: %d", code)
	}
	if len(h.List(false)) != 0 {
		t.Fatal("refused, yet made")
	}
}

// A body at …/bodies/<name> gives its MCP and its stream.
func TestBuildFromABodyURL(t *testing.T) {
	h := NewHost(HostConfig{}, nil, slog.Default())
	b, err := h.Build("a", Spec{Body: "http://bodies:8080/bodies/alice/"})
	if err != nil || b.BodyMCP != "http://bodies:8080/bodies/alice/mcp" || len(b.Senses) != 1 ||
		b.Senses[0] != "body_alice_events" || b.Group != "brain_a" || b.Cfg.Self != DerivedSubject("a") || b.Policy != nil {
		t.Fatalf("%+v %v", b, err)
	}
}

// UNIT-133: self is who the mind is, put before how a mind works — the
// host's prompt when the spec names none, the spec's when it does.
func TestSelfComesBeforeThePrompt(t *testing.T) {
	h := NewHost(HostConfig{Prompt: "how a mind works"}, nil, slog.Default())
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{Senses: []string{"s"}, Self: "You are Ann.\n"}, "You are Ann.\n\nhow a mind works"},
		{Spec{Senses: []string{"s"}, Self: "You are Ann.", Prompt: "think briefly"}, "You are Ann.\n\nthink briefly"},
		{Spec{Senses: []string{"s"}, Prompt: "think briefly"}, "think briefly"},
		{Spec{Senses: []string{"s"}}, "how a mind works"},
	} {
		b, err := h.Build("a", c.spec)
		if err != nil || b.Cfg.Prompt != c.want {
			t.Errorf("%+v: prompt %q, %v; want %q", c.spec, b.Cfg.Prompt, err, c.want)
		}
	}
}
