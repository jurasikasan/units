package world

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jurasikasan/units/reins/internal/holds"
)

// fakeHolds: subject → "<principal>/<device>" holding it, or "brain:x".
type fakeHolds struct {
	mu   sync.Mutex
	held map[uuid.UUID]string
	seen []string
}

func (f *fakeHolds) Holding(_ context.Context, p, d string, s uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.held[s] == p+"/"+d {
		return nil
	}
	return &holds.Held{By: f.held[s]}
}

func (f *fakeHolds) Seen(_ context.Context, p, d string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, p+"/"+d)
	return nil
}

// fakeStewards: principal → the avatars it steers.
type fakeStewards map[string][]uuid.UUID

func (f fakeStewards) Steered(_ context.Context, p string) ([]uuid.UUID, error) { return f[p], nil }
func (f fakeStewards) Steers(_ context.Context, p string, s uuid.UUID) (bool, error) {
	return slices.Contains(f[p], s), nil
}

// fakeJournal is threads in memory, members from joins and leaves as
// threads reads them.
type fakeJournal struct {
	mu      sync.Mutex
	threads map[uuid.UUID]*Thread
	entries map[uuid.UUID][]map[string]any
	// appended is called after each append, as threads announces it.
	appended func(thread uuid.UUID, seq int64, kind string)
}

func newFakeJournal() *fakeJournal {
	return &fakeJournal{threads: map[uuid.UUID]*Thread{}, entries: map[uuid.UUID][]map[string]any{}}
}

func (j *fakeJournal) Member(_ context.Context, s uuid.UUID) ([]Thread, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := []Thread{}
	for _, t := range j.threads {
		if slices.Contains(t.Members, s) {
			out = append(out, Thread{ID: t.ID, CreatedAt: t.CreatedAt, OpenedBy: t.OpenedBy})
		}
	}
	return out, nil
}

func (j *fakeJournal) Get(_ context.Context, id uuid.UUID) (Thread, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	t, ok := j.threads[id]
	if !ok {
		return Thread{}, ErrNoThread
	}
	return *t, nil
}

func (j *fakeJournal) Open(_ context.Context, by uuid.UUID) (Thread, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	t := &Thread{ID: uuid.New(), OpenedBy: &by, CreatedAt: time.Now(), Members: []uuid.UUID{}}
	j.threads[t.ID] = t
	return *t, nil
}

func (j *fakeJournal) Append(_ context.Context, id uuid.UUID, entry any) (json.RawMessage, error) {
	j.mu.Lock()
	t, ok := j.threads[id]
	if !ok {
		j.mu.Unlock()
		return nil, ErrNoThread
	}
	b, _ := json.Marshal(entry)
	var e map[string]any
	_ = json.Unmarshal(b, &e)
	if e["kind"] == "create" {
		if c, _ := e["content"].([]any); len(c) == 0 {
			j.mu.Unlock()
			return nil, &Refused{Reason: "invalid entry: a create entry carries at least one block"}
		}
	}
	seq := int64(len(j.entries[id]) + 1)
	e["seq"], e["thread"] = seq, id.String()
	j.entries[id] = append(j.entries[id], e)
	sender := uuid.MustParse(e["sender"].(string))
	switch e["kind"] {
	case "join":
		t.Members = append(t.Members, sender)
	case "leave":
		t.Members = slices.DeleteFunc(t.Members, func(m uuid.UUID) bool { return m == sender })
	}
	kind := e["kind"].(string)
	j.mu.Unlock()
	if j.appended != nil {
		j.appended(id, seq, kind)
	}
	return json.Marshal(e)
}

func (j *fakeJournal) Entries(_ context.Context, id uuid.UUID, q url.Values) (json.RawMessage, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.threads[id]; !ok {
		return nil, ErrNoThread
	}
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit == 0 {
		limit = 100
	}
	out := []map[string]any{}
	for _, e := range j.entries[id] {
		if e["seq"].(int64) > after && len(out) < limit {
			out = append(out, e)
		}
	}
	return json.Marshal(out)
}

var (
	alice     = uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	assistant = uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	bob       = uuid.MustParse("00000000-0000-0000-0000-00000000000b")
)

// device is the device alice and bob each hold their avatars from.
const device = "d1"

var held *fakeHolds

func setup() (*Service, *fakeJournal, *mux.Router) {
	j := newFakeJournal()
	held = &fakeHolds{held: map[uuid.UUID]string{alice: "alice/d1", assistant: "alice/d1", bob: "bob/d1"}}
	svc := NewService(fakeStewards{"alice": {alice, assistant}, "bob": {bob}}, j, held)
	r := mux.NewRouter()
	RegisterRoutes(r, NewHandler(svc, nil))
	return svc, j, r
}

func call(r http.Handler, principal, method, path, body string) *httptest.ResponseRecorder {
	return callFrom(r, principal, device, method, path, body)
}

func callFrom(r http.Handler, principal, dev, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if principal != "" {
		req.Header.Set(principalHeader, principal)
	}
	if dev != "" {
		req.Header.Set(holds.DeviceHeader, dev)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestOpen_AsAndWithJoin_OnlySteered(t *testing.T) {
	_, j, r := setup()

	w := call(r, "alice", "POST", "/me/threads", fmt.Sprintf(`{"as":%q,"with":[%q]}`, alice, assistant))
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var th Thread
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &th))
	assert.Equal(t, []uuid.UUID{alice, assistant}, th.Members)
	assert.Equal(t, alice, *th.OpenedBy)
	es := j.entries[th.ID]
	require.Len(t, es, 2)
	assert.Equal(t, "join", es[0]["kind"])
	assert.Equal(t, alice.String(), es[0]["sender"])
	assert.Equal(t, assistant.String(), es[1]["sender"])

	// bob is not alice's to put in, nor is alice bob's to open as.
	assert.Equal(t, http.StatusForbidden, call(r, "alice", "POST", "/me/threads", fmt.Sprintf(`{"as":%q,"with":[%q]}`, alice, bob)).Code)
	assert.Equal(t, http.StatusForbidden, call(r, "bob", "POST", "/me/threads", fmt.Sprintf(`{"as":%q}`, alice)).Code)
	assert.Equal(t, http.StatusBadRequest, call(r, "alice", "POST", "/me/threads", `{}`).Code)
	assert.Equal(t, http.StatusUnauthorized, call(r, "", "POST", "/me/threads", `{}`).Code)
	assert.Len(t, j.threads, 1, "a refused open makes no thread")
}

func TestSay_AsASteeredMember(t *testing.T) {
	svc, j, r := setup()
	th, err := svc.Open(context.Background(), "alice", device, alice, nil)
	require.NoError(t, err)
	path := "/threads/" + th.ID.String() + "/entries"

	w := call(r, "alice", "POST", path, fmt.Sprintf(`{"as":%q,"content":[{"type":"text","text":"hi"}]}`, alice))
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var e map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &e))
	assert.Equal(t, "create", e["kind"])
	assert.EqualValues(t, 2, e["seq"])

	// The assistant is alice's, but not in this thread; bob is in no way alice.
	assert.Equal(t, http.StatusForbidden, call(r, "alice", "POST", path, fmt.Sprintf(`{"as":%q,"content":[{"type":"text","text":"x"}]}`, assistant)).Code)
	assert.Equal(t, http.StatusForbidden, call(r, "bob", "POST", path, fmt.Sprintf(`{"as":%q,"content":[{"type":"text","text":"x"}]}`, alice)).Code)
	// threads' refusal, in its words.
	w = call(r, "alice", "POST", path, fmt.Sprintf(`{"as":%q,"content":[]}`, alice))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "at least one block")
	assert.Equal(t, http.StatusNotFound, call(r, "alice", "POST", "/threads/"+uuid.NewString()+"/entries", fmt.Sprintf(`{"as":%q,"content":[{"type":"text","text":"x"}]}`, alice)).Code)
	assert.Equal(t, http.StatusBadRequest, call(r, "alice", "POST", path, `{"as":"x","content":[],"extra":1}`).Code)
	assert.Len(t, j.entries[th.ID], 2)
}

// The one wheel: a steward who does not hold the avatar's reins from this
// device watches, and writing is 409, saying who holds it.
func TestSay_NeedsTheReins(t *testing.T) {
	svc, j, r := setup()
	th, err := svc.Open(context.Background(), "alice", device, alice, []uuid.UUID{assistant})
	require.NoError(t, err)
	path := "/threads/" + th.ID.String() + "/entries"
	say := fmt.Sprintf(`{"as":%q,"content":[{"type":"text","text":"x"}]}`, alice)

	w := callFrom(r, "alice", "d2", "POST", path, say)
	assert.Equal(t, http.StatusConflict, w.Code, "another device of hers")
	assert.Contains(t, w.Body.String(), "held by alice/d1")
	held.held[assistant] = "brain:fake"
	w = call(r, "alice", "POST", path, fmt.Sprintf(`{"as":%q,"content":[{"type":"text","text":"x"}]}`, assistant))
	assert.Equal(t, http.StatusConflict, w.Code, "a brain holds it")
	assert.Contains(t, w.Body.String(), "held by brain:fake")
	w = call(r, "alice", "POST", "/me/threads", fmt.Sprintf(`{"as":%q}`, assistant))
	assert.Equal(t, http.StatusConflict, w.Code, "opening is an act of `as`")
	assert.Len(t, j.entries[th.ID], 2, "nothing written")
	assert.Len(t, j.threads, 1, "nothing opened")
}

// The first thread: opened by the assistant, which joins, then the login's
// avatar; nothing said; once only.
func TestFirstThread_Once(t *testing.T) {
	svc, j, _ := setup()
	ctx := context.Background()
	require.NoError(t, svc.FirstThread(ctx, assistant, alice))
	require.NoError(t, svc.FirstThread(ctx, assistant, alice))
	require.Len(t, j.threads, 1)
	for id, th := range j.threads {
		assert.Equal(t, assistant, *th.OpenedBy)
		assert.Equal(t, []uuid.UUID{assistant, alice}, th.Members)
		assert.Len(t, j.entries[id], 2, "two joins, nothing said")
	}
}

func TestEntriesAndList_OnlyWhereAnAvatarIs(t *testing.T) {
	svc, _, r := setup()
	ctx := context.Background()
	mine, err := svc.Open(ctx, "alice", device, alice, []uuid.UUID{assistant})
	require.NoError(t, err)
	_, err = svc.Say(ctx, "alice", device, mine.ID, assistant, json.RawMessage(`[{"type":"text","text":"from the assistant"}]`))
	require.NoError(t, err)
	bobs, err := svc.Open(ctx, "bob", device, bob, nil)
	require.NoError(t, err)

	w := call(r, "alice", "GET", "/threads/"+mine.ID.String()+"/entries?after=2", "")
	require.Equal(t, http.StatusOK, w.Code)
	var es []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &es))
	require.Len(t, es, 1, "after= passed through")
	assert.Equal(t, assistant.String(), es[0]["sender"], "the channel itself: every sender")
	assert.Equal(t, http.StatusForbidden, call(r, "alice", "GET", "/threads/"+bobs.ID.String()+"/entries", "").Code)

	w = call(r, "alice", "GET", "/me/threads", "")
	require.Equal(t, http.StatusOK, w.Code)
	var ts []Thread
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &ts))
	require.Len(t, ts, 1, "listed once, though two of alice's avatars are in it")
	assert.Equal(t, mine.ID, ts[0].ID)
	assert.Equal(t, []uuid.UUID{alice, assistant}, ts[0].Members)
}

func TestNoThreads_503(t *testing.T) {
	r := mux.NewRouter()
	RegisterRoutes(r, NewHandler(nil, nil))
	assert.Equal(t, http.StatusServiceUnavailable, call(r, "alice", "GET", "/me/threads", "").Code)
	assert.Equal(t, http.StatusServiceUnavailable, call(r, "alice", "GET", "/me/stream", "").Code)
}

func TestCompare(t *testing.T) {
	assert.Equal(t, -1, compare("5-1", "10-0"))
	assert.Equal(t, 1, compare("10-2", "10-1"))
	assert.Equal(t, 0, compare("7-3", "7-3"))
}

// sse is one event read off a stream.
type sse struct{ id, event, data string }

func readEvents(t *testing.T, body *bufio.Reader, out chan<- sse) {
	t.Helper()
	var cur sse
	for {
		line, err := body.ReadString('\n')
		if err != nil {
			close(out)
			return
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			if cur.event != "" {
				out <- cur
			}
			cur = sse{}
		case strings.HasPrefix(line, "id: "):
			cur.id = line[4:]
		case strings.HasPrefix(line, "event: "):
			cur.event = line[7:]
		case strings.HasPrefix(line, "data: "):
			cur.data += line[6:]
		}
	}
}

func next(t *testing.T, events <-chan sse) sse {
	t.Helper()
	select {
	case e, ok := <-events:
		require.True(t, ok, "the stream ended")
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5 s")
		return sse{}
	}
}

// The stream, against a real Redis: entries of the principal's threads as
// threads announces them, read back; another's threads left out; pieces of
// a reply relayed under `writing`; a reconnect with Last-Event-ID resumes
// after it; a position the stream no longer has is a reset.
func TestStream_Live(t *testing.T) {
	addr := os.Getenv("REINS_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("REINS_TEST_REDIS_ADDR not set; check.sh starts a Redis")
	}
	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	require.NoError(t, rdb.Del(ctx, ThreadsStream, "driver_events", holds.Stream).Err())

	svc, j, _ := setup()
	j.appended = func(thread uuid.UUID, seq int64, kind string) {
		data, _ := json.Marshal(map[string]any{"thread": thread, "seq": seq, "kind": kind})
		rdb.XAdd(ctx, &redis.XAddArgs{Stream: ThreadsStream, Values: map[string]any{
			"ce_type": "threads.entry.appended", "ce_subject": thread.String(), "data": string(data)}})
	}
	st := NewStream(svc, rdb, []string{"driver_events"})
	st.Presence = held
	st.Block = 200 * time.Millisecond
	r := mux.NewRouter()
	RegisterRoutes(r, NewHandler(svc, st))
	srv := httptest.NewServer(r)
	defer srv.Close()

	open := func(principal, lastID string) (<-chan sse, func()) {
		cctx, cancel := context.WithCancel(ctx)
		req, _ := http.NewRequestWithContext(cctx, "GET", srv.URL+"/me/stream?device="+device, nil)
		req.Header.Set(principalHeader, principal)
		if lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
		events := make(chan sse, 100)
		go readEvents(t, bufio.NewReader(resp.Body), events)
		return events, func() { cancel(); _ = resp.Body.Close() }
	}

	events, stop := open("alice", "")
	ready := next(t, events)
	assert.Equal(t, "ready", ready.event)

	bobs, err := svc.Open(ctx, "bob", device, bob, nil)
	require.NoError(t, err)
	th, err := svc.Open(ctx, "alice", device, alice, []uuid.UUID{assistant})
	require.NoError(t, err)
	_, err = svc.Say(ctx, "bob", device, bobs.ID, bob, json.RawMessage(`[{"type":"text","text":"not for alice"}]`))
	require.NoError(t, err)
	_, err = svc.Say(ctx, "alice", device, th.ID, assistant, json.RawMessage(`[{"type":"text","text":"hello alice"}]`))
	require.NoError(t, err)

	var got []map[string]any
	var lastID string
	for len(got) < 3 {
		e := next(t, events)
		require.Equal(t, "entry", e.event)
		require.NotEmpty(t, e.id)
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(e.data), &m))
		require.Equal(t, th.ID.String(), m["thread"], "only alice's threads: %s", e.data)
		got = append(got, m)
		lastID = e.id
	}
	assert.Equal(t, "join", got[0]["kind"])
	assert.Equal(t, "create", got[2]["kind"])
	assert.Equal(t, "hello alice", got[2]["content"].([]any)[0].(map[string]any)["text"], "the entry read back, content and all")

	// A piece of a reply being written, in alice's thread and in bob's.
	for _, thread := range []uuid.UUID{bobs.ID, th.ID} {
		data, _ := json.Marshal(map[string]any{"thread": thread, "sender": assistant, "reply": "r1", "text": "Hel"})
		require.NoError(t, rdb.XAdd(ctx, &redis.XAddArgs{Stream: "driver_events", Values: map[string]any{
			"ce_type": "driver.reply.delta", "data": string(data)}}).Err())
	}
	w := next(t, events)
	assert.Equal(t, "writing", w.event)
	assert.Empty(t, w.id, "what is not journaled is not resumed")
	assert.Contains(t, w.data, `"type":"driver.reply.delta"`)
	assert.Contains(t, w.data, th.ID.String())

	// Who holds an avatar: bob's left out, alice's assistant to a brain
	// shown as alice sees it.
	for _, h := range []map[string]any{{"subject": bob, "holder": "brain:fake"}, {"subject": assistant, "holder": "brain:fake"}, {"subject": alice, "holder": "principal:alice", "device": device}} {
		data, _ := json.Marshal(h)
		require.NoError(t, rdb.XAdd(ctx, &redis.XAddArgs{Stream: holds.Stream, Values: map[string]any{
			"ce_type": holds.HoldChanged, "data": string(data)}}).Err())
	}
	hd := next(t, events)
	assert.Equal(t, "hold", hd.event)
	assert.JSONEq(t, fmt.Sprintf(`{"subject":%q,"kind":"brain","profile":"fake"}`, assistant), hd.data)
	hd = next(t, events)
	assert.JSONEq(t, fmt.Sprintf(`{"subject":%q,"kind":"principal","principal":"alice","you":true,"here":true}`, alice), hd.data)
	held.mu.Lock()
	assert.Contains(t, held.seen, "alice/d1", "the open stream says its device is there")
	held.mu.Unlock()
	stop()

	// Away, then back from the last id: what came meanwhile, nothing twice.
	_, err = svc.Say(ctx, "alice", device, th.ID, alice, json.RawMessage(`[{"type":"text","text":"while away"}]`))
	require.NoError(t, err)
	events, stop = open("alice", lastID)
	assert.Equal(t, "ready", next(t, events).event)
	e := next(t, events)
	assert.Equal(t, "entry", e.event)
	assert.Contains(t, e.data, "while away")
	stop()

	// A position past the stream's end: a bus that came back empty.
	events, stop = open("alice", "99999999999999-0")
	assert.Equal(t, "reset", next(t, events).event)
	assert.Equal(t, "ready", next(t, events).event)
	stop()
}
