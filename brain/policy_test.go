package brain

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A policy changed while a thought is held ends that thought at once: its
// request is withdrawn (the model sees its caller go), nothing of it is
// remembered, its percepts go back to their buffers — those past their
// channel's lifetime dropped and counted — and the next thought goes to the
// new policy. The same policy put again is no change.
func TestAPolicyChangeWithdrawsTheThoughtInFlight(t *testing.T) {
	r := newRigWith(t, func(c *Config) {
		c.Keep = map[string]time.Duration{"hearing": time.Second}
		c.ModelTimeout = 30 * time.Second
	}, func(r *rig) { r.model.hold = make(chan struct{}) })
	next := &fakeModel{}
	ns := httptest.NewServer(next)
	t.Cleanup(ns.Close)

	now := time.Now().UTC().Truncate(time.Millisecond)
	r.see("look-1", "a person at the door", now.Add(-800*time.Millisecond), "")
	r.hear("utt-1", "knock knock", now.Add(-time.Second), now.Add(-500*time.Millisecond))
	eventually(t, "the thought held", func() bool { return r.model.held.Load() == 1 })

	same := *r.mind.Policy()
	r.mind.SetPolicy(&same)
	time.Sleep(100 * time.Millisecond)
	if r.model.withdrawn.Load() != 0 || r.mind.Counts.Withdrawn.Load() != 0 {
		t.Fatal("the same policy again withdrew the thought")
	}

	// Heard while the thought is held: hearing's clock moves past utt-1's
	// lifetime, so utt-1, given back, is dropped; the sight is kept.
	r.hear("utt-2", "it is me", now.Add(2*time.Second), now.Add(2500*time.Millisecond))
	eventually(t, "utt-2 buffered", func() bool { return r.mind.Buffered() == 1 })

	asked := time.Now()
	r.mind.SetPolicy(&Policy{Model: Model{URL: ns.URL + "/v1", Name: "next"}})
	eventually(t, "the request withdrawn", func() bool { return r.model.withdrawn.Load() == 1 })
	if took := time.Since(asked); took > time.Second {
		t.Fatalf("withdrawn after %v", took)
	}
	eventually(t, "the next policy's thought", func() bool { return len(next.got()) == 1 })
	var texts []string // every moment the next thought was given
	for _, m := range next.got()[0]["messages"].([]any) {
		if parts, ok := m.(map[string]any)["content"].([]any); ok && m.(map[string]any)["role"] == "user" {
			for _, p := range parts {
				texts = append(texts, p.(map[string]any)["text"].(string))
			}
		}
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "a person at the door") || !strings.Contains(joined, `"it is me"`) || strings.Contains(joined, "knock knock") {
		t.Fatalf("the next thought's moment: %v", texts)
	}
	if next.got()[0]["model"] != "next" || len(r.model.got()) != 0 {
		t.Fatalf("the old policy answered: %v", r.model.got())
	}

	// Memory holds the next thought only: what it perceived, once, and the
	// percept dropped nowhere.
	eventually(t, "remembered", func() bool {
		for _, e := range r.threads.all() {
			if e["sender"] == self.String() && e["kind"] == "create" {
				return true
			}
		}
		return false
	})
	var lines []string
	for _, e := range r.threads.all() {
		content, _ := e["content"].([]any)
		for _, b := range content {
			if tx, _ := b.(map[string]any)["text"].(string); tx != "" {
				lines = append(lines, tx)
			}
		}
	}
	all := strings.Join(lines, "\n")
	if strings.Contains(all, "knock knock") || strings.Count(all, "a person at the door") != 1 {
		t.Fatalf("memory: %v", lines)
	}
	c := r.mind.Counts.Snapshot()
	if c["withdrawn"] != 1 || c["expired"] != 1 || c["thoughts"] != 1 || c["model_errors"] != 0 || c["memory_errors"] != 0 {
		t.Fatalf("counts: %v", c)
	}
	if back := r.log.lines("percepts given back"); len(back) != 1 || back[0]["percepts"] != float64(2) || back[0]["dropped"] != float64(1) {
		t.Fatalf("given back: %v", back)
	}
}
