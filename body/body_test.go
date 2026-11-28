package body

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jurasikasan/units/shared/events"
)

// The body is tested at its walls: hearing, sight and speaking are fakes
// built with the MCP SDK (httptest), answering only the tools CONTRACT.md
// names; the streams are a real Redis (BODY_TEST_REDIS_ADDR), which check.sh
// starts. speaking's fake reports speak-complete on speech_events as
// speaking does.

func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("BODY_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("BODY_TEST_REDIS_ADDR not set: no Redis for the stream tests")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	if err := rdb.FlushDB(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// fakeSense is hearing or sight: open hands out a session and its address,
// close is recorded. With idleAfter, a hearing session idles out as
// hearing's does — its session-closed on the stream — unless it was opened
// with idle_timeout_ms 0.
type fakeSense struct {
	kind      string // hearing | sight
	rdb       *redis.Client
	idleAfter time.Duration
	mu        sync.Mutex
	n         int
	opens     []map[string]any
	closes    []string
}

func (f *fakeSense) server() *httptest.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: f.kind, Version: "0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "open"}, func(_ context.Context, _ *mcp.CallToolRequest, in map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.n++
		f.opens = append(f.opens, in)
		if f.kind == "hearing" {
			id := fmt.Sprintf("hs-%d", f.n)
			if v, set := in["idle_timeout_ms"]; f.idleAfter > 0 && (!set || v != float64(0)) {
				time.AfterFunc(f.idleAfter, func() {
					fields, _ := events.Fields("/senses/hearing", events.Event{Type: "speechrecog.session-closed", Subject: id,
						Data: map[string]any{"session_id": id, "reason": "idle"}})
					_ = f.rdb.XAdd(context.Background(), &redis.XAddArgs{Stream: HearingStream, Values: fields}).Err()
				})
			}
			return nil, map[string]any{"session_id": id, "audio_in": fmt.Sprintf("hearing:%d", 44000+f.n)}, nil
		}
		id := fmt.Sprintf("sg-%d", f.n)
		return nil, map[string]any{"session_id": id, "video_in": fmt.Sprintf("sight:%d", 45000+2*f.n), "sight_in": "http://sight:8080/in/" + id}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "close"}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
		SessionID string `json:"session_id"`
	}) (*mcp.CallToolResult, map[string]any, error) {
		f.mu.Lock()
		f.closes = append(f.closes, in.SessionID)
		f.mu.Unlock()
		return nil, map[string]any{"ok": true}, nil
	})
	return serve(srv)
}

func (f *fakeSense) closed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.closes...)
}

func (f *fakeSense) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.opens)
}

// fakeSpeaking opens sessions aimed at a target and reports each speak as
// spoken whole on speech_events, as speaking does.
type fakeSpeaking struct {
	rdb      *redis.Client
	mu       sync.Mutex
	n        int
	sessions map[string]string // session → to
	opens    []map[string]any
	speaks   []map[string]any
	closes   []string
}

func (f *fakeSpeaking) server() *httptest.Server {
	f.sessions = map[string]string{}
	pub := events.Redis{Client: f.rdb, Stream: SpeechStream, Source: "/abilities/speaking"}
	srv := mcp.NewServer(&mcp.Implementation{Name: "speech", Version: "0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "open"}, func(_ context.Context, _ *mcp.CallToolRequest, in map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.n++
		id := fmt.Sprintf("spk-%d", f.n)
		f.sessions[id], _ = in["to"].(string)
		f.opens = append(f.opens, in)
		return nil, map[string]any{"session_id": id}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "speak"}, func(ctx context.Context, _ *mcp.CallToolRequest, in map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		f.mu.Lock()
		id, _ := in["session_id"].(string)
		_, ok := f.sessions[id]
		f.speaks = append(f.speaks, in)
		rid := fmt.Sprintf("utt-%d", len(f.speaks))
		f.mu.Unlock()
		if !ok {
			return nil, nil, fmt.Errorf("unknown_session: %s", id)
		}
		go func() {
			time.Sleep(50 * time.Millisecond)
			_ = pub.Publish(context.Background(), events.Event{Type: "speechsynth.speak-complete", Subject: id,
				Data: map[string]any{"session_id": id, "request_id": rid, "completion_cause": "normal", "spoken": in["text"]}})
		}()
		return nil, map[string]any{"request_id": rid}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "close"}, func(_ context.Context, _ *mcp.CallToolRequest, in map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id, _ := in["session_id"].(string)
		delete(f.sessions, id)
		f.closes = append(f.closes, id)
		return nil, map[string]any{"ok": true}, nil
	})
	return serve(srv)
}

// forget is a session closed by speaking on its own (idle), unannounced.
func (f *fakeSpeaking) forget(id string) {
	f.mu.Lock()
	delete(f.sessions, id)
	f.mu.Unlock()
}

func (f *fakeSpeaking) snapshot() (opens, speaks []map[string]any, closes []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.opens...), append([]map[string]any(nil), f.speaks...), append([]string(nil), f.closes...)
}

func serve(srv *mcp.Server) *httptest.Server {
	return httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
}

type running struct {
	body *Body
	host *Host
	srv  *httptest.Server
	stop func()
	done chan struct{}
}

// startHost runs a host of its own (its own group, as a process of its own
// would have) with the units' URLs of cfg; no body yet.
func startHost(t *testing.T, rdb *redis.Client, group string, hc HostConfig) *running {
	t.Helper()
	hc.Group = group
	hc.Retry, hc.SpeakTimeout = 50*time.Millisecond, 5*time.Second
	h := NewHost(hc, rdb, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{host: h, done: make(chan struct{})}
	go func() { _ = h.Run(ctx); close(r.done) }()
	<-h.Ready()
	r.stop = func() {
		cancel()
		<-r.done
		if r.srv != nil {
			r.srv.Close()
		}
	}
	t.Cleanup(func() {
		select {
		case <-r.done:
		default:
			r.stop()
		}
	})
	return r
}

// startBody runs a host with one body made of cfg, as the env makes one:
// served at /mcp and GET /body too.
func startBody(t *testing.T, rdb *redis.Client, cfg Config) *running {
	t.Helper()
	r := startHost(t, rdb, GroupFor(cfg.Name), HostConfig{HearingURL: cfg.HearingURL, SightURL: cfg.SightURL, SpeakingURL: cfg.SpeakingURL})
	r.host.Default = cfg.Name
	spec := Spec{Description: cfg.Description, Voice: cfg.Voice, Language: cfg.Language, Earshot: cfg.Earshot}
	if cfg.HearingURL != "" {
		spec.Senses = append(spec.Senses, "hearing")
	}
	if cfg.SightURL != "" {
		spec.Senses = append(spec.Senses, "sight")
	}
	if _, _, err := r.host.Put(cfg.Name, spec); err != nil {
		t.Fatal(err)
	}
	r.body = r.host.Body(cfg.Name)
	r.srv = httptest.NewServer(r.host.Handler())
	return r
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func getSelf(t *testing.T, base string) Self {
	t.Helper()
	resp, err := http.Get(base + "/body")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var s Self
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func entries(t *testing.T, rdb *redis.Client, stream string) []map[string]any {
	t.Helper()
	msgs, err := rdb.XRange(context.Background(), stream, "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, m := range msgs {
		out = append(out, m.Values)
	}
	return out
}

func publish(t *testing.T, rdb *redis.Client, stream, source, typ, subject string, data map[string]any) map[string]any {
	t.Helper()
	f, err := events.Fields(source, events.Event{Type: typ, Subject: subject, Data: data,
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rdb.XAdd(context.Background(), &redis.XAddArgs{Stream: stream, Values: f}).Err(); err != nil {
		t.Fatal(err)
	}
	return f
}

func client(t *testing.T, base string) *mcp.ClientSession {
	t.Helper()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "head", Version: "0"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: base + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("%s: tool error: %s", name, text(res))
	}
	out, _ := res.StructuredContent.(map[string]any)
	return out
}

// A body opens its senses, serves their addresses as its ears and eyes, and
// republishes its own sessions' events — only its own — unchanged but for
// source and subject.
func TestSelfAndItsOwnPerceptionOnly(t *testing.T) {
	rdb := testRedis(t)
	hearing, sight := &fakeSense{kind: "hearing"}, &fakeSense{kind: "sight"}
	hs, ss := hearing.server(), sight.server()
	defer hs.Close()
	defer ss.Close()
	a := startBody(t, rdb, Config{Name: "a", Description: "a tall woman in a grey coat", HearingURL: hs.URL, SightURL: ss.URL, Language: "en"})
	eventually(t, "ears and eyes", func() bool { s := a.body.Self(); return len(s.Ears) == 1 && len(s.Eyes) == 1 })

	self := getSelf(t, a.srv.URL)
	if self.ID != DerivedID("a").String() || self.Name != "a" || self.Description != "a tall woman in a grey coat" {
		t.Fatalf("self: %+v", self)
	}
	if self.Ears[0].AudioIn != "hearing:44001" || self.Eyes[0].VideoIn != "sight:45002" || self.Eyes[0].SightIn != "http://sight:8080/in/sg-1" {
		t.Fatalf("senses: %+v", self)
	}
	if hearing.opens[0]["speech_language"] != "en" || sight.opens[0]["language"] != "en" {
		t.Fatalf("open's language: %v %v", hearing.opens, sight.opens)
	}

	data := map[string]any{"session_id": "hs-1", "request_id": "utt-1", "completion_cause": "success", "text": "hello",
		"started_at": "2026-11-27T10:00:00.000Z", "ended_at": "2026-11-27T10:00:00.600Z"}
	mine := publish(t, rdb, HearingStream, "/senses/hearing", "speechrecog.recognition-complete", "hs-1", data)
	publish(t, rdb, HearingStream, "/senses/hearing", "speechrecog.recognition-complete", "hs-99",
		map[string]any{"session_id": "hs-99", "text": "not for a"})
	publish(t, rdb, HearingStream, "/senses/hearing", "speechrecog.something-new", "hs-1", map[string]any{"session_id": "hs-1"})
	publish(t, rdb, SightStream, "/senses/sight", "sightrecog.recognition-complete", "sg-1",
		map[string]any{"session_id": "sg-1", "text": "a door", "started_at": "2026-11-27T10:00:00.100Z"})

	eventually(t, "two relayed", func() bool { return len(entries(t, rdb, "body_a_events")) == 2 })
	time.Sleep(100 * time.Millisecond)
	got := entries(t, rdb, "body_a_events")
	if len(got) != 2 {
		t.Fatalf("relayed %d, want 2: %v", len(got), got)
	}
	h := got[0]
	if h["ce_source"] != "/bodies/a/hearing" || h["ce_subject"] != DerivedID("a").String() {
		t.Fatalf("envelope: %v", h)
	}
	for _, k := range []string{"ce_id", "ce_type", "ce_time", "ce_traceparent", "data", "ce_specversion", "ce_datacontenttype"} {
		if h[k] != mine[k] {
			t.Fatalf("%s changed: %v → %v", k, mine[k], h[k])
		}
	}
	if got[1]["ce_source"] != "/bodies/a/sight" || got[1]["ce_type"] != "sightrecog.recognition-complete" {
		t.Fatalf("sight: %v", got[1])
	}
	c := a.body.Counts.Snapshot()
	if c["relayed"] != 2 || a.host.Foreign.Load() != 1 || c["dropped"] != 1 {
		t.Fatalf("counts: %v", c)
	}

	// Stopping closes the senses' sessions.
	a.stop()
	if !equal(hearing.closed(), []string{"hs-1"}) || !equal(sight.closed(), []string{"sg-1"}) {
		t.Fatalf("closed: %v %v", hearing.closed(), sight.closed())
	}
}

// A session that closes by itself (it failed) is relayed as such, and the
// sense is opened again: the ear moves.
func TestASenseIsOpenedAgain(t *testing.T) {
	rdb := testRedis(t)
	hearing := &fakeSense{kind: "hearing"}
	hs := hearing.server()
	defer hs.Close()
	a := startBody(t, rdb, Config{Name: "a", HearingURL: hs.URL})
	eventually(t, "an ear", func() bool { return len(a.body.Self().Ears) == 1 })

	publish(t, rdb, HearingStream, "/senses/hearing", "speechrecog.session-closed", "hs-1",
		map[string]any{"session_id": "hs-1", "reason": "failed"})
	eventually(t, "the ear opened again", func() bool {
		s := a.body.Self()
		return len(s.Ears) == 1 && s.Ears[0].AudioIn == "hearing:44002"
	})
	got := entries(t, rdb, "body_a_events")
	if len(got) != 1 || got[0]["ce_type"] != "speechrecog.session-closed" {
		t.Fatalf("relayed: %v", got)
	}
	// The old session is no longer the body's.
	publish(t, rdb, HearingStream, "/senses/hearing", "speechrecog.recognition-complete", "hs-1", map[string]any{"session_id": "hs-1"})
	eventually(t, "counted foreign", func() bool { return a.host.Foreign.Load() == 1 })
	if a.body.Counts.Opened.Load() != 2 || hearing.openCount() != 2 {
		t.Fatalf("opened: %d", a.body.Counts.Opened.Load())
	}
}

// UNIT-131: a body opens its ear never to idle out, so left in silence it
// keeps the same ear and publishes no session-closed — where hearing would
// close an ear opened without it every idle timeout.
func TestAnEarInSilenceStays(t *testing.T) {
	rdb := testRedis(t)
	hearing := &fakeSense{kind: "hearing", rdb: rdb, idleAfter: 100 * time.Millisecond}
	hs := hearing.server()
	defer hs.Close()
	a := startBody(t, rdb, Config{Name: "a", HearingURL: hs.URL, Language: "en"})
	eventually(t, "an ear", func() bool { return len(a.body.Self().Ears) == 1 })

	time.Sleep(800 * time.Millisecond) // eight idle timeouts of silence
	if ears := a.body.Self().Ears; len(ears) != 1 || ears[0].AudioIn != "hearing:44001" {
		t.Fatalf("the ear moved: %v", ears)
	}
	if n := hearing.openCount(); n != 1 {
		t.Fatalf("opened %d times, want once", n)
	}
	hearing.mu.Lock()
	args := hearing.opens[0]
	hearing.mu.Unlock()
	if args["idle_timeout_ms"] != float64(0) || args["speech_language"] != "en" {
		t.Fatalf("open's arguments: %v", args)
	}
	for _, e := range entries(t, rdb, "body_a_events") {
		if e["ce_type"] == "speechrecog.session-closed" {
			t.Fatalf("published a session-closed: %v", e)
		}
	}
}

// A sense that is down at the start is asked again until it opens.
func TestASenseDownAtStartIsAskedAgain(t *testing.T) {
	rdb := testRedis(t)
	hearing := &fakeSense{kind: "hearing"}
	inner := hearing.server()
	defer inner.Close()
	var up sync.Mutex
	down := true
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.Lock()
		d := down
		up.Unlock()
		if d {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		req, _ := http.NewRequest(r.Method, inner.URL+r.URL.Path, r.Body)
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxy.Close()
	a := startBody(t, rdb, Config{Name: "a", HearingURL: proxy.URL})
	eventually(t, "open refused", func() bool { return a.body.Counts.OpenErrs.Load() >= 2 })
	if len(getSelf(t, a.srv.URL).Ears) != 0 {
		t.Fatal("an ear while hearing is down")
	}
	up.Lock()
	down = false
	up.Unlock()
	eventually(t, "an ear", func() bool { return len(a.body.Self().Ears) == 1 })
}

// A's speak_aloud goes through speaking to B's ear, found by B's GET /body;
// it answers with what was spoken once speaking says so, and is recorded on
// A's stream. When B's ear moves, A's mouth follows it.
func TestSpeakAloudReachesTheEarsInEarshot(t *testing.T) {
	rdb := testRedis(t)
	hearing := &fakeSense{kind: "hearing"}
	speaking := &fakeSpeaking{rdb: rdb}
	hs, sp := hearing.server(), speaking.server()
	defer hs.Close()
	defer sp.Close()
	b := startBody(t, rdb, Config{Name: "b", HearingURL: hs.URL})
	eventually(t, "b's ear", func() bool { return len(b.body.Self().Ears) == 1 })
	bEar := b.body.Self().Ears[0].AudioIn
	a := startBody(t, rdb, Config{Name: "a", SpeakingURL: sp.URL + "/", Voice: "piper_lessac", Earshot: []string{b.srv.URL}})

	cs := client(t, a.srv.URL)
	out := call(t, cs, "speak_aloud", map[string]any{"text": "hello, who is there?"})
	if out["spoken"] != "hello, who is there?" || out["completion_cause"] != "normal" || out["listeners"] != float64(1) {
		t.Fatalf("speak_aloud: %v", out)
	}
	opens, speaks, _ := speaking.snapshot()
	if len(opens) != 1 || opens[0]["to"] != bEar || opens[0]["voice_name"] != "piper_lessac" {
		t.Fatalf("speaking open: %v", opens)
	}
	if len(speaks) != 1 || speaks[0]["session_id"] != "spk-1" || speaks[0]["text"] != "hello, who is there?" {
		t.Fatalf("speaking speak: %v", speaks)
	}
	acts := entries(t, rdb, "body_a_events")
	if len(acts) != 1 || acts[0]["ce_type"] != "body.act-complete" || acts[0]["ce_source"] != "/bodies/a" ||
		acts[0]["ce_subject"] != DerivedID("a").String() {
		t.Fatalf("act: %v", acts)
	}
	var act map[string]any
	_ = json.Unmarshal([]byte(acts[0]["data"].(string)), &act)
	if act["tool"] != "speak_aloud" || act["is_error"] != false ||
		act["args"].(map[string]any)["text"] != "hello, who is there?" ||
		act["result"].(map[string]any)["spoken"] != "hello, who is there?" || act["started_at"] == nil || act["ended_at"] == nil {
		t.Fatalf("act data: %v", act)
	}

	// The same ear: the same speaking session.
	call(t, cs, "speak_aloud", map[string]any{"text": "anyone?"})
	if opens, _, _ := speaking.snapshot(); len(opens) != 1 {
		t.Fatalf("opened again for the same ear: %v", opens)
	}

	// B's ear moves (its hearing session failed): A aims again.
	publish(t, rdb, HearingStream, "/senses/hearing", "speechrecog.session-closed", "hs-1", map[string]any{"session_id": "hs-1", "reason": "failed"})
	eventually(t, "b's ear moved", func() bool {
		s := b.body.Self()
		return len(s.Ears) == 1 && s.Ears[0].AudioIn != bEar
	})
	call(t, cs, "speak_aloud", map[string]any{"text": "still there?"})
	opens, speaks, closes := speaking.snapshot()
	if len(opens) != 2 || opens[1]["to"] != b.body.Self().Ears[0].AudioIn || !equal(closes, []string{"spk-1"}) {
		t.Fatalf("aimed again: opens %v closes %v", opens, closes)
	}
	if speaks[2]["session_id"] != "spk-2" {
		t.Fatalf("spoken on: %v", speaks[2])
	}

	// speaking closed the session on its own: the phrase is said on a new one.
	speaking.forget("spk-2")
	out = call(t, cs, "speak_aloud", map[string]any{"text": "one more time"})
	if out["spoken"] != "one more time" {
		t.Fatalf("after speaking forgot: %v", out)
	}
	if opens, _, _ := speaking.snapshot(); len(opens) != 3 {
		t.Fatalf("not reopened: %v", opens)
	}

	// Stopping closes its speaking sessions.
	a.stop()
	if _, _, closes := speaking.snapshot(); closes[len(closes)-1] != "spk-3" {
		t.Fatalf("closed on the way out: %v", closes)
	}
}

// With nobody in earshot the phrase goes nowhere: not an error, no listeners.
func TestSpeakAloudToNobody(t *testing.T) {
	rdb := testRedis(t)
	speaking := &fakeSpeaking{rdb: rdb}
	sp := speaking.server()
	defer sp.Close()
	deaf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"x","name":"deaf","description":"","ears":[],"eyes":[]}`)
	}))
	defer deaf.Close()
	a := startBody(t, rdb, Config{Name: "a", SpeakingURL: sp.URL, Voice: "piper_lessac", Earshot: []string{deaf.URL, "http://127.0.0.1:1"}})
	out := call(t, client(t, a.srv.URL), "speak_aloud", map[string]any{"text": "hello?"})
	if out["spoken"] != "" || out["listeners"] != float64(0) {
		t.Fatalf("to nobody: %v", out)
	}
	if opens, _, _ := speaking.snapshot(); len(opens) != 0 {
		t.Fatalf("speaking opened for nobody: %v", opens)
	}
}

// The head discovers the tools; skip_turn does nothing and says so at once;
// the body describes itself as a resource. Without a mouth, no speak_aloud.
func TestToolsSkipTurnAndSelfResource(t *testing.T) {
	rdb := testRedis(t)
	a := startBody(t, rdb, Config{Name: "a", Description: "a cat", SpeakingURL: "http://127.0.0.1:1/", Voice: "piper_lessac"})
	quiet := startBody(t, rdb, Config{Name: "q"})

	names := func(cs *mcp.ClientSession) []string {
		res, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, tool := range res.Tools {
			out = append(out, tool.Name)
		}
		return out
	}
	cs := client(t, a.srv.URL)
	if got := names(cs); !equal(got, []string{"skip_turn", "speak_aloud"}) {
		t.Fatalf("tools: %v", got)
	}
	if got := names(client(t, quiet.srv.URL)); !equal(got, []string{"skip_turn"}) {
		t.Fatalf("a body without a mouth: %v", got)
	}

	if out := call(t, cs, "skip_turn", nil); out["skipped"] != true {
		t.Fatalf("skip_turn: %v", out)
	}
	acts := entries(t, rdb, "body_a_events")
	if len(acts) != 1 || !strings.Contains(acts[0]["data"].(string), `"tool":"skip_turn"`) {
		t.Fatalf("act: %v", acts)
	}

	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: SelfURI})
	if err != nil {
		t.Fatal(err)
	}
	var self Self
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &self); err != nil || self.Description != "a cat" || self.Name != "a" {
		t.Fatalf("body://self: %v %v", res.Contents[0].Text, err)
	}

	// A mouth that cannot be reached is an error the head sees, and recorded.
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"x","name":"l","description":"","ears":[{"audio_in":"hearing:44009"}],"eyes":[]}`)
	}))
	defer listener.Close()
	m := startBody(t, rdb, Config{Name: "m", SpeakingURL: "http://127.0.0.1:1/", Voice: "piper_lessac", Earshot: []string{listener.URL}})
	res2, err := client(t, m.srv.URL).CallTool(context.Background(), &mcp.CallToolParams{Name: "speak_aloud", Arguments: map[string]any{"text": "hi"}})
	if err != nil || !res2.IsError {
		t.Fatalf("an unreachable mouth: %v %v", res2, err)
	}
	if m.body.Counts.ActErrs.Load() != 1 {
		t.Fatalf("act errors: %v", m.body.Counts.Snapshot())
	}
	acts = entries(t, rdb, "body_m_events")
	if len(acts) != 1 || !strings.Contains(acts[0]["data"].(string), `"is_error":true`) {
		t.Fatalf("a failed act: %v", acts)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
