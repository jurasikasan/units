package brain

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The brain is tested at its walls: a fake model, a fake threads and a fake
// artifact_store, each speaking only the routes CONTRACT.md names, and sense
// events in the envelope a sense writes.

// fakeModel is an OpenAI chat door that records what it was sent and
// answers with a numbered thought.
type fakeModel struct {
	mu       sync.Mutex
	requests []map[string]any
	delay    time.Duration
	inFlight atomic.Int32
	maxIn    atomic.Int32
	// answer, when set, is the k-th answer's message (tool calls and all).
	answer func(k int) map[string]any
}

func (f *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		old := f.maxIn.Load()
		if n <= old || f.maxIn.CompareAndSwap(old, n) {
			break
		}
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	time.Sleep(f.delay)
	f.mu.Lock()
	f.requests = append(f.requests, body)
	k := len(f.requests)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
		"message": f.message(k)}}})
}

func (f *fakeModel) message(k int) map[string]any {
	if f.answer != nil {
		return f.answer(k)
	}
	return map[string]any{"role": "assistant", "content": "thought " + strconv.Itoa(k)}
}

func (f *fakeModel) got() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.requests...)
}

// fakeThreads keeps threads in memory and answers the routes the brain uses.
type fakeThreads struct {
	mu      sync.Mutex
	down    bool
	opened  []string
	entries map[string][]map[string]any
}

func (f *fakeThreads) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	if f.entries == nil {
		f.entries = map[string][]map[string]any{}
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/threads":
		var out []map[string]any
		for i := len(f.opened) - 1; i >= 0; i-- {
			for _, e := range f.entries[f.opened[i]] {
				if e["kind"] == "join" && e["sender"] == r.URL.Query().Get("member") {
					out = append(out, map[string]any{"id": f.opened[i]})
					break
				}
			}
		}
		_ = json.NewEncoder(w).Encode(append([]map[string]any{}, out...))
	case r.Method == http.MethodPost && r.URL.Path == "/threads":
		id := uuid.NewString()
		f.opened = append(f.opened, id)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
	case len(parts) == 3 && parts[2] == "entries" && r.Method == http.MethodPost:
		var e map[string]any
		d := json.NewDecoder(r.Body)
		if err := d.Decode(&e); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		e["seq"] = len(f.entries[parts[1]]) + 1
		f.entries[parts[1]] = append(f.entries[parts[1]], e)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(e)
	case len(parts) == 3 && parts[2] == "entries" && r.Method == http.MethodGet:
		es := f.entries[parts[1]]
		if r.URL.Query().Get("tail") != "true" {
			http.Error(w, "the brain reads the tail", http.StatusBadRequest)
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if len(es) > limit {
			es = es[len(es)-limit:]
		}
		_ = json.NewEncoder(w).Encode(append([]map[string]any{}, es...))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeThreads) all() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, id := range f.opened {
		out = append(out, f.entries[id]...)
	}
	return out
}

// fakeStore answers GET /v1/_sha256/{hex}.
type fakeStore struct{ frames map[string][]byte }

func (f *fakeStore) put(b []byte) string {
	sum := sha256.Sum256(b)
	h := hex.EncodeToString(sum[:])
	f.frames[h] = b
	return h
}

func (f *fakeStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, ok := f.frames[strings.TrimPrefix(r.URL.Path, "/v1/_sha256/")]
	if r.Method != http.MethodGet || !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	_, _ = w.Write(b)
}

// syncBuffer is a log the test reads while the mind writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) lines(msg string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, l := range strings.Split(s.b.String(), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

var (
	self    = uuid.MustParse("00000000-0000-0000-0000-00000000b0a1")
	hearsAs = uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
)

type rig struct {
	model   *fakeModel
	threads *fakeThreads
	store   *fakeStore
	log     *syncBuffer
	mind    *Mind
	events  chan Event
	t       *testing.T
}

func newRig(t *testing.T, tune func(*Config)) *rig { return newRigWith(t, tune, nil) }

// newRigWith lets setup change the rig (the model's answers, a body) before
// the mind runs.
func newRigWith(t *testing.T, tune func(*Config), setup func(*rig)) *rig {
	t.Helper()
	r := &rig{model: &fakeModel{}, threads: &fakeThreads{}, store: &fakeStore{frames: map[string][]byte{}}, log: &syncBuffer{}, t: t}
	ms, ts, ss := httptest.NewServer(r.model), httptest.NewServer(r.threads), httptest.NewServer(r.store)
	t.Cleanup(func() { ms.Close(); ts.Close(); ss.Close() })
	cfg := Config{
		Closed: map[string]bool{}, KeepDefault: 4 * time.Second, BufferMax: 32,
		Settle: 50 * time.Millisecond, MaxWait: 2 * time.Second, Bind: 200 * time.Millisecond,
		Recall: 10, Prompt: "be a mind", Self: self, Senders: map[string]uuid.UUID{"hearing": hearsAs},
		ModelTimeout: 5 * time.Second,
	}
	if tune != nil {
		tune(&cfg)
	}
	r.mind = &Mind{
		Cfg:    cfg,
		Model:  Model{URL: ms.URL + "/v1", Name: "brain"},
		Store:  Store{URL: ss.URL},
		Memory: &Memory{URL: ts.URL, Self: self},
		Counts: &Counts{},
		Log:    slog.New(slog.NewJSONHandler(r.log, nil)),
	}
	r.events = make(chan Event, 16)
	if setup != nil {
		setup(r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.mind.Run(ctx, r.events); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return r
}

func (r *rig) send(source, typ string, data map[string]any) {
	r.t.Helper()
	ev, err := ParseEvent(fields(r.t, source, typ, data), time.Now())
	if err != nil {
		r.t.Fatal(err)
	}
	r.events <- ev
}

func (r *rig) hear(id, text string, start, end time.Time) {
	r.send("/senses/hearing", "speechrecog.recognition-complete", map[string]any{
		"request_id": id, "completion_cause": "success", "text": text, "input_type": "speech",
		"started_at": stamp(start), "ended_at": stamp(end)})
}

func (r *rig) startHearing(id string, start time.Time) {
	r.send("/senses/hearing", "speechrecog.start-of-input", map[string]any{"request_id": id, "started_at": stamp(start)})
}

func (r *rig) see(id, text string, at time.Time, sha string) {
	data := map[string]any{"request_id": id, "completion_cause": "success", "text": text, "input_type": "video",
		"started_at": stamp(at), "ended_at": stamp(at)}
	if sha != "" {
		data["frame"] = map[string]any{"sha256": sha, "space": "sight", "name": id + ".jpg", "content_type": "image/jpeg", "at": stamp(at)}
	}
	r.send("/senses/sight", "sightrecog.recognition-complete", data)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// userParts gives the content parts of a request's last user message.
func userParts(t *testing.T, req map[string]any) []map[string]any {
	t.Helper()
	msgs := req["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "user" {
		t.Fatalf("last message is %v", last["role"])
	}
	var out []map[string]any
	for _, p := range last["content"].([]any) {
		out = append(out, p.(map[string]any))
	}
	return out
}

// The whole loop through every wall: a sound and a sight of one moment, the
// picture fetched by its digest and sent as a data URL, the thought in the
// log with both times and the lag, memory written with occurred.
func TestOneMomentThroughEveryWall(t *testing.T) {
	r := newRig(t, nil)
	jpeg := []byte("\xff\xd8 a picture \xff\xd9")
	sha := r.store.put(jpeg)
	now := time.Now().UTC().Truncate(time.Millisecond)
	start := now.Add(-time.Second)

	r.startHearing("utt-1", start)
	r.see("obs-1", "a person in the corridor", start.Add(100*time.Millisecond), sha)
	time.Sleep(150 * time.Millisecond) // a start is open: no thought yet
	if n := len(r.model.got()); n != 0 {
		t.Fatalf("thought with a start open: %d", n)
	}
	r.hear("utt-1", "stop", start, start.Add(600*time.Millisecond))

	eventually(t, "the model asked", func() bool { return len(r.model.got()) == 1 })
	req := r.model.got()[0]
	if req["model"] != "brain" {
		t.Fatalf("model %v", req["model"])
	}
	msgs := req["messages"].([]any)
	if sys := msgs[0].(map[string]any); sys["role"] != "system" || sys["content"] != "be a mind" {
		t.Fatalf("system: %v", sys)
	}
	parts := userParts(t, req)
	if len(parts) != 3 {
		t.Fatalf("parts: %v", parts)
	}
	hh := start.Format("15:04:05.000")
	if parts[0]["text"] != "["+hh+` hearing] "stop"` {
		t.Fatalf("first part: %v", parts[0])
	}
	if !strings.HasSuffix(parts[1]["text"].(string), "sight] a person in the corridor") {
		t.Fatalf("second part: %v", parts[1])
	}
	img := parts[2]["image_url"].(map[string]any)["url"].(string)
	if parts[2]["type"] != "image_url" || img != "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(jpeg) {
		t.Fatalf("image part: %v", parts[2])
	}

	eventually(t, "the thought logged", func() bool { return len(r.log.lines("thought")) == 1 })
	th := r.log.lines("thought")[0]
	if th["thought"] != "thought 1" || th["span_start"] != start.Format(time.RFC3339Nano) ||
		th["span_end"] != start.Add(600*time.Millisecond).Format(time.RFC3339Nano) {
		t.Fatalf("thought line: %v", th)
	}
	if lag := th["lag_ms"].(float64); lag < 400 {
		t.Fatalf("lag %v: now − end of span", lag)
	}
	if _, ok := th["model_ms"]; !ok {
		t.Fatalf("no model latency: %v", th)
	}
	if ps := r.log.lines("percept"); len(ps) != 2 || ps[0]["ce_time"] == nil || ps[0]["started_at"] == nil {
		t.Fatalf("percept lines: %v", ps)
	}

	eventually(t, "memory written", func() bool { return len(r.threads.all()) == 4 })
	es := r.threads.all()
	if es[0]["kind"] != "join" || es[0]["sender"] != self.String() {
		t.Fatalf("join: %v", es[0])
	}
	hearing, sight, thought := es[1], es[2], es[3]
	if hearing["sender"] != hearsAs.String() || hearing["kind"] != "create" {
		t.Fatalf("hearing entry: %v", hearing)
	}
	if sight["sender"] != DerivedSubject("channel:sight").String() {
		t.Fatalf("sight entry sender: %v", sight["sender"])
	}
	block := sight["content"].([]any)[1].(map[string]any)
	if block["type"] != "image" || block["sha256"] != sha || !strings.HasSuffix(block["url"].(string), "/v1/_sha256/"+sha) {
		t.Fatalf("image block: %v", block)
	}
	occ := thought["occurred"].(map[string]any)
	if thought["sender"] != self.String() || thought["content"].([]any)[0].(map[string]any)["text"] != "thought 1" ||
		occ["start"] != start.Format(time.RFC3339Nano) || occ["end"] != start.Add(600*time.Millisecond).Format(time.RFC3339Nano) {
		t.Fatalf("thought entry: %v", thought)
	}
	if hocc := hearing["occurred"].(map[string]any); hocc["start"] != start.Format(time.RFC3339Nano) {
		t.Fatalf("percept occurred: %v", hocc)
	}
}

// A start whose recognition never comes holds the tick only BRAIN_MAX_WAIT.
func TestMaxWaitFiresPastAnOpenStart(t *testing.T) {
	r := newRig(t, func(c *Config) { c.MaxWait = 300 * time.Millisecond })
	now := time.Now().UTC()
	r.startHearing("utt-9", now)
	r.see("obs-1", "a door", now, "")
	time.Sleep(150 * time.Millisecond)
	if len(r.model.got()) != 0 {
		t.Fatal("thought before max wait")
	}
	eventually(t, "thought after max wait", func() bool { return len(r.model.got()) == 1 })
}

// One thought in flight: what comes while it is made waits, and goes into
// the next one, with the first recalled from memory before it.
func TestOneThoughtAtATimeAndRecall(t *testing.T) {
	r := newRig(t, nil)
	r.model.delay = 300 * time.Millisecond
	now := time.Now().UTC()
	r.hear("utt-1", "hello", now, now.Add(300*time.Millisecond))
	eventually(t, "the first thought asked", func() bool { return r.model.inFlight.Load() == 1 })
	r.hear("utt-2", "anyone?", now.Add(2*time.Second), now.Add(2500*time.Millisecond))
	r.see("obs-1", "an empty room", now.Add(2100*time.Millisecond), "")

	eventually(t, "two thoughts", func() bool { return len(r.model.got()) == 2 })
	time.Sleep(200 * time.Millisecond)
	if n := len(r.model.got()); n != 2 {
		t.Fatalf("%d thoughts", n)
	}
	if m := r.model.maxIn.Load(); m != 1 {
		t.Fatalf("%d thoughts in flight at once", m)
	}
	second := r.model.got()[1]
	msgs := second["messages"].([]any)
	// system, recall (the first percept, the first thought), the new moment
	if len(msgs) != 4 {
		t.Fatalf("messages: %v", msgs)
	}
	recalled, thought := msgs[1].(map[string]any), msgs[2].(map[string]any)
	if recalled["role"] != "user" || !strings.HasSuffix(recalled["content"].(string), `hearing] "hello"`) {
		t.Fatalf("recalled percept: %v", recalled)
	}
	if thought["role"] != "assistant" || thought["content"] != "thought 1" {
		t.Fatalf("recalled thought: %v", thought)
	}
	if parts := userParts(t, second); len(parts) != 2 {
		t.Fatalf("the second moment: %v", parts)
	}
}

// threads down and a frame that cannot be fetched: the thought still goes
// to the log, the picture is said to be missing, the loop goes on.
func TestOutagesDoNotStopThinking(t *testing.T) {
	r := newRig(t, nil)
	r.threads.mu.Lock()
	r.threads.down = true
	r.threads.mu.Unlock()
	now := time.Now().UTC()
	r.see("obs-1", "a hall", now, digest) // not in the store

	eventually(t, "the thought logged", func() bool { return len(r.log.lines("thought")) == 1 })
	parts := userParts(t, r.model.got()[0])
	if len(parts) != 2 || parts[1]["type"] != "text" || !strings.Contains(parts[1]["text"].(string), "could not be fetched") {
		t.Fatalf("parts: %v", parts)
	}
	eventually(t, "memory errors counted", func() bool { return r.mind.Counts.MemoryErrs.Load() >= 2 })
	if r.mind.Counts.FrameErrors.Load() != 1 {
		t.Fatalf("frame errors %d", r.mind.Counts.FrameErrors.Load())
	}

	r.threads.mu.Lock()
	r.threads.down = false
	r.threads.mu.Unlock()
	r.see("obs-2", "a hall, still", now.Add(time.Second), "")
	eventually(t, "memory back", func() bool { return len(r.threads.all()) == 3 }) // join, percept, thought
}

// A closed gate drops a channel before the buffer; a type the brain does not
// read is counted and dropped; an event delivered twice is taken once.
func TestGateIgnoredAndDuplicates(t *testing.T) {
	r := newRig(t, func(c *Config) { c.Closed = map[string]bool{"sight": true}; c.MaxWait = 200 * time.Millisecond })
	now := time.Now().UTC()
	r.see("obs-1", "a hall", now, "")
	r.send("/senses/hearing", "speechrecog.session-closed", map[string]any{"reason": "idle"})
	ev, err := ParseEvent(fields(t, "/senses/hearing", "speechrecog.recognition-complete", map[string]any{
		"request_id": "utt-1", "text": "once", "started_at": stamp(now)}), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r.events <- ev
	r.events <- ev

	eventually(t, "one thought", func() bool { return len(r.model.got()) == 1 })
	time.Sleep(300 * time.Millisecond)
	parts := userParts(t, r.model.got()[0])
	if len(r.model.got()) != 1 || len(parts) != 1 || !strings.Contains(parts[0]["text"].(string), "once") {
		t.Fatalf("thoughts %d, parts %v", len(r.model.got()), parts)
	}
	c := r.mind.Counts
	if c.Gated.Load() != 1 || c.Ignored.Load() != 1 || c.Duplicates.Load() != 1 {
		t.Fatalf("counts %v", c.Snapshot())
	}
}

// A thread is found again by the brain's subject: a restart keeps memory.
func TestMemoryFindsItsThreadAgain(t *testing.T) {
	ts := httptest.NewServer(&fakeThreads{})
	defer ts.Close()
	ctx := context.Background()
	first := &Memory{URL: ts.URL, Self: self}
	id, opened, err := first.Resolve(ctx)
	if err != nil || !opened {
		t.Fatalf("%v %v", opened, err)
	}
	again := &Memory{URL: ts.URL, Self: self}
	id2, opened, err := again.Resolve(ctx)
	if err != nil || opened || id2 != id {
		t.Fatalf("%s %v %v, want %s", id2, opened, err, id)
	}
	other := &Memory{URL: ts.URL, Self: uuid.New()}
	if id3, _, _ := other.Resolve(ctx); id3 == id {
		t.Fatal("another brain found this brain's thread")
	}
	pinned := &Memory{URL: ts.URL, Self: self, Thread: "11111111-1111-1111-1111-111111111111"}
	if id4, _, _ := pinned.Resolve(ctx); id4 != pinned.Thread {
		t.Fatal("BRAIN_THREAD not kept")
	}
}
