package body

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func do(t *testing.T, method, url string, body any) (int, []byte) {
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

func clientAt(t *testing.T, endpoint string) *mcp.ClientSession {
	t.Helper()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "head", Version: "0"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// Bodies are made, kept, made anew and let go through the API; each lives
// under /bodies/{name}/; the host keeps none of them across a restart.
func TestHostMakesAndLetsGoBodies(t *testing.T) {
	rdb := testRedis(t)
	hearing := &fakeSense{kind: "hearing"}
	hs := hearing.server()
	defer hs.Close()
	speaking := &fakeSpeaking{rdb: rdb}
	ss := speaking.server()
	defer ss.Close()
	r := startHost(t, rdb, "bodies", HostConfig{HearingURL: hs.URL, SpeakingURL: ss.URL})
	r.srv = httptest.NewServer(r.host.Handler())
	base := r.srv.URL

	spec := Spec{Description: "a cat", Senses: []string{"hearing"}, Language: "en"}
	code, raw := do(t, http.MethodPut, base+"/bodies/a", spec)
	var l Listed
	_ = json.Unmarshal(raw, &l)
	if code != http.StatusCreated || l.ID != DerivedID("a").String() || l.MCP != "/bodies/a/mcp" ||
		l.Events != "/bodies/a/events" || l.Body != "/bodies/a" {
		t.Fatalf("made: %d %s", code, raw)
	}
	eventually(t, "a's ear", func() bool { return len(r.host.Body("a").Self().Ears) == 1 })
	first := r.host.Body("a")

	// The same spec again changes nothing.
	if code, raw := do(t, http.MethodPut, base+"/bodies/a", spec); code != http.StatusOK || r.host.Body("a") != first {
		t.Fatalf("same spec: %d %s", code, raw)
	}
	if hearing.openCount() != 1 {
		t.Fatalf("the same spec opened again: %d", hearing.openCount())
	}

	// GET /bodies/{name} is what an observer perceives; /bodies lists.
	var self Self
	_, raw = do(t, http.MethodGet, base+"/bodies/a", nil)
	if json.Unmarshal(raw, &self) != nil || self.Description != "a cat" || len(self.Ears) != 1 {
		t.Fatalf("self: %s", raw)
	}

	// Another spec makes it anew: its old session closed, a new one opened.
	spec.Description, spec.Voice = "a black cat", "piper_lessac"
	if code, raw := do(t, http.MethodPut, base+"/bodies/a", spec); code != http.StatusOK {
		t.Fatalf("another spec: %d %s", code, raw)
	}
	eventually(t, "a new ear", func() bool { b := r.host.Body("a"); return b != first && len(b.Self().Ears) == 1 })
	if !equal(hearing.closed(), []string{"hs-1"}) || hearing.openCount() != 2 {
		t.Fatalf("made anew: closed %v, opened %d", hearing.closed(), hearing.openCount())
	}

	// Its MCP under its name: a body with a voice has a mouth.
	res, err := clientAt(t, base+"/bodies/a/mcp").ListTools(context.Background(), nil)
	if err != nil || len(res.Tools) != 2 {
		t.Fatalf("tools: %v %v", res, err)
	}
	if code, _ := do(t, http.MethodGet, base+"/bodies/zz", nil); code != http.StatusNotFound {
		t.Fatalf("no such body: %d", code)
	}
	if code, _ := do(t, http.MethodPost, base+"/bodies/zz/mcp", "{}"); code != http.StatusNotFound {
		t.Fatalf("no such body's mcp: %d", code)
	}

	// A second body; the list has both.
	if code, raw := do(t, http.MethodPut, base+"/bodies/b", Spec{Senses: []string{"hearing"}}); code != http.StatusCreated {
		t.Fatalf("b: %d %s", code, raw)
	}
	var all []Listed
	_, raw = do(t, http.MethodGet, base+"/bodies", nil)
	if json.Unmarshal(raw, &all) != nil || len(all) != 2 || all[0].Name != "a" || all[1].Name != "b" ||
		all[0].Spec.Description != "a black cat" {
		t.Fatalf("list: %s", raw)
	}

	// DELETE lets it go: its session closed, gone from the list.
	eventually(t, "b's ear", func() bool { return len(r.host.Body("b").Self().Ears) == 1 })
	if code, _ := do(t, http.MethodDelete, base+"/bodies/b", nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := do(t, http.MethodDelete, base+"/bodies/b", nil); code != http.StatusNotFound {
		t.Fatalf("delete again: %d", code)
	}
	if !equal(hearing.closed(), []string{"hs-1", "hs-3"}) {
		t.Fatalf("closed: %v", hearing.closed())
	}

	// A restart keeps nothing: a new host has no bodies until they are PUT.
	r.stop()
	again := startHost(t, rdb, "bodies", HostConfig{HearingURL: hs.URL})
	if got := again.host.List(); len(got) != 0 {
		t.Fatalf("after a restart: %v", got)
	}
}

func TestHostRefusesASpec(t *testing.T) {
	rdb := testRedis(t)
	r := startHost(t, rdb, "bodies", HostConfig{HearingURL: "http://127.0.0.1:1/"})
	r.srv = httptest.NewServer(r.host.Handler())
	for what, c := range map[string]struct {
		path string
		body any
	}{
		"a sense the host has not": {"/bodies/a", Spec{Senses: []string{"sight"}}},
		"no such sense":            {"/bodies/a", Spec{Senses: []string{"smell"}}},
		"a voice, no speaking":     {"/bodies/a", Spec{Voice: "piper_lessac"}},
		"not a uuid":               {"/bodies/a", Spec{ID: "x"}},
		"earshot not a URL":        {"/bodies/a", Spec{Earshot: []string{"body_b"}}},
		"a name out of rule":       {"/bodies/A", Spec{}},
		"an unknown field":         {"/bodies/a", `{"descripton": "typo"}`},
	} {
		if code, raw := do(t, http.MethodPut, r.srv.URL+c.path, c.body); code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", what, code, raw)
		}
	}
	if len(r.host.List()) != 0 {
		t.Fatalf("refused, yet made: %v", r.host.List())
	}
}

// Two bodies in one host: each perceives only its own sessions' events, and
// one speaks to the other through an earshot entry that is /bodies/<name>.
func TestHostBodiesHearEachOther(t *testing.T) {
	rdb := testRedis(t)
	hearing := &fakeSense{kind: "hearing"}
	hs := hearing.server()
	defer hs.Close()
	speaking := &fakeSpeaking{rdb: rdb}
	ss := speaking.server()
	defer ss.Close()
	r := startHost(t, rdb, "bodies", HostConfig{HearingURL: hs.URL, SpeakingURL: ss.URL})
	r.srv = httptest.NewServer(r.host.Handler())
	base := r.srv.URL

	do(t, http.MethodPut, base+"/bodies/a", Spec{Senses: []string{"hearing"}, Voice: "piper_lessac", Earshot: []string{base + "/bodies/b"}})
	do(t, http.MethodPut, base+"/bodies/b", Spec{Senses: []string{"hearing"}})
	eventually(t, "ears", func() bool {
		return len(r.host.Body("a").Self().Ears) == 1 && len(r.host.Body("b").Self().Ears) == 1
	})
	bEar := r.host.Body("b").Self().Ears[0].AudioIn
	bSession := r.host.Body("b").hearing.session

	out := call(t, clientAt(t, base+"/bodies/a/mcp"), "speak_aloud", map[string]any{"text": "hi b"})
	if out["listeners"] != float64(1) || out["spoken"] != "hi b" {
		t.Fatalf("speak_aloud: %v", out)
	}
	opens, _, _ := speaking.snapshot()
	if len(opens) != 1 || opens[0]["to"] != bEar {
		t.Fatalf("aimed at %v, b's ear is %s", opens, bEar)
	}

	publish(t, rdb, HearingStream, "/senses/hearing", "speechrecog.recognition-complete", bSession,
		map[string]any{"session_id": bSession, "text": "hi b"})
	eventually(t, "b heard", func() bool { return len(entries(t, rdb, "body_b_events")) == 1 })
	for _, e := range entries(t, rdb, "body_a_events") {
		if e["ce_type"] != actComplete {
			t.Fatalf("a's stream has b's perception: %v", e)
		}
	}
}
