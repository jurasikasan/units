package holds

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jurasikasan/units/reins/internal/db"
)

var (
	alice     = uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	assistant = uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	bob       = uuid.MustParse("00000000-0000-0000-0000-00000000000b")
)

type stewards map[string][]uuid.UUID

func (s stewards) Steers(_ context.Context, p string, id uuid.UUID) (bool, error) {
	for _, x := range s[p] {
		if x == id {
			return true, nil
		}
	}
	return false, nil
}

// memStore keeps holds in memory; Lapse is the live store's to test.
type memStore struct {
	mu    sync.Mutex
	holds map[uuid.UUID]Hold
}

func newMem() *memStore { return &memStore{holds: map[uuid.UUID]Hold{}} }

func (m *memStore) Get(_ context.Context, s uuid.UUID) (Hold, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.holds[s]
	return h, ok, nil
}
func (m *memStore) Of(_ context.Context, ss []uuid.UUID) (map[uuid.UUID]Hold, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[uuid.UUID]Hold{}
	for _, s := range ss {
		if h, ok := m.holds[s]; ok {
			out[s] = h
		}
	}
	return out, nil
}
func (m *memStore) Set(_ context.Context, h Hold) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.holds[h.Subject]
	h.Since = time.Now()
	m.holds[h.Subject] = h
	return !ok || old.Holder != h.Holder || old.Device != h.Device, nil
}
func (m *memStore) SetIfNone(_ context.Context, h Hold) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.holds[h.Subject]; ok {
		return false, nil
	}
	m.holds[h.Subject] = h
	return true, nil
}
func (m *memStore) Clear(_ context.Context, s uuid.UUID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.holds[s]
	delete(m.holds, s)
	return ok, nil
}
func (m *memStore) ClearIf(_ context.Context, s uuid.UUID, holder string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h, ok := m.holds[s]; !ok || h.Holder != holder {
		return false, nil
	}
	delete(m.holds, s)
	return true, nil
}
func (m *memStore) Brains(context.Context) ([]Hold, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Hold{}
	for _, h := range m.holds {
		if _, ok := h.Profile(); ok {
			out = append(out, h)
		}
	}
	return out, nil
}
func (m *memStore) Seen(context.Context, string, string) error { return nil }
func (m *memStore) Lapse(context.Context, time.Duration) ([]Hold, error) {
	return nil, nil
}

type event struct {
	typ  string
	data map[string]any
}

type memBus struct {
	mu     sync.Mutex
	events []event
}

func (b *memBus) Publish(_ context.Context, typ, _ string, data map[string]any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, event{typ, data})
	return nil
}

type profiles []string

func (p profiles) Profiles(context.Context) ([]string, error) { return p, nil }

func setup() (*Service, *memStore, *memBus) {
	st, bus := newMem(), &memBus{}
	return NewService(st, stewards{"alice": {alice, assistant}, "bob": {bob}, "carol": {assistant}}, bus, profiles{"fake", "glm"}), st, bus
}

func TestTakeHandGiveBack(t *testing.T) {
	svc, _, bus := setup()
	ctx := context.Background()

	b, err := svc.Take(ctx, "alice", "d1", alice)
	require.NoError(t, err)
	assert.Equal(t, "principal", b.Kind)
	assert.True(t, b.You && b.Here)
	require.NoError(t, svc.Holding(ctx, "alice", "d1", alice))
	assert.ErrorIs(t, svc.Holding(ctx, "alice", "d2", alice), ErrHeld, "another device of hers")
	assert.EqualError(t, svc.Holding(ctx, "alice", "d2", alice), "held by you on another device")

	_, err = svc.Take(ctx, "alice", "d1", alice)
	require.NoError(t, err)
	assert.Len(t, bus.events, 1, "taking it again from the same device changes nothing")

	b, err = svc.Hand(ctx, "alice", assistant, "fake")
	require.NoError(t, err)
	assert.Equal(t, Brain{Kind: "brain", Profile: "fake", Since: b.Since}, b)
	assert.EqualError(t, svc.Holding(ctx, "alice", "d1", assistant), "held by brain fake")
	_, err = svc.Hand(ctx, "alice", assistant, "gpt-9")
	assert.ErrorIs(t, err, ErrNoProfile)

	// Another steward takes it from the brain: the kill switch.
	_, err = svc.Take(ctx, "carol", "c1", assistant)
	require.NoError(t, err)
	assert.EqualError(t, svc.Holding(ctx, "alice", "d1", assistant), "held by carol")
	views, err := svc.Views(ctx, "alice", "d1", []uuid.UUID{alice, assistant, bob})
	require.NoError(t, err)
	assert.Equal(t, "principal", views[assistant].Kind)
	assert.Equal(t, "carol", views[assistant].Principal)
	assert.False(t, views[assistant].You)
	assert.Equal(t, "offline", views[bob].Kind)

	require.NoError(t, svc.GiveBack(ctx, "alice", assistant))
	assert.EqualError(t, svc.Holding(ctx, "carol", "c1", assistant), "held by nobody: take the reins first")

	var types []string
	var holders []any
	for _, e := range bus.events {
		types = append(types, e.typ)
		holders = append(holders, e.data["holder"])
	}
	assert.Equal(t, []string{HoldChanged, HoldChanged, HoldChanged, HoldChanged}, types)
	assert.Equal(t, []any{"principal:alice", "brain:fake", "principal:carol", ""}, holders)
	assert.Equal(t, "d1", bus.events[0].data["device"])
}

func TestOnlyStewards(t *testing.T) {
	svc, _, bus := setup()
	ctx := context.Background()
	_, err := svc.Take(ctx, "bob", "b1", alice)
	assert.ErrorIs(t, err, ErrNotSteward)
	_, err = svc.Hand(ctx, "bob", alice, "fake")
	assert.ErrorIs(t, err, ErrNotSteward)
	assert.ErrorIs(t, svc.GiveBack(ctx, "bob", alice), ErrNotSteward)
	assert.ErrorIs(t, svc.Stop(ctx, "bob", uuid.New(), alice), ErrNotSteward)
	_, err = svc.Take(ctx, "alice", "", alice)
	assert.ErrorIs(t, err, ErrNoDevice)
	assert.Empty(t, bus.events)
}

func TestDefaultOnlyWhenNobodyHolds(t *testing.T) {
	svc, st, bus := setup()
	ctx := context.Background()
	require.NoError(t, svc.Default(ctx, assistant, "fake"))
	require.NoError(t, svc.Default(ctx, assistant, "glm"))
	assert.Equal(t, "brain:fake", st.holds[assistant].Holder)
	_, err := svc.Take(ctx, "alice", "d1", alice)
	require.NoError(t, err)
	require.NoError(t, svc.Default(ctx, alice, "fake"))
	assert.Equal(t, "principal:alice", st.holds[alice].Holder, "one somebody holds is left as it is")
	assert.Len(t, bus.events, 2)
}

// A brain lets go of its own hold only: nobody holds it then, said with
// holder ""; one taken meanwhile is left with whoever took it.
func TestReleaseOnlyItsOwn(t *testing.T) {
	svc, st, bus := setup()
	ctx := context.Background()
	_, err := svc.Hand(ctx, "alice", assistant, "fake")
	require.NoError(t, err)

	released, err := svc.Release(ctx, assistant, "glm")
	require.NoError(t, err)
	assert.False(t, released, "another profile's")
	released, err = svc.Release(ctx, assistant, "fake")
	require.NoError(t, err)
	assert.True(t, released)
	assert.NotContains(t, st.holds, assistant, "nobody holds it")
	last := bus.events[len(bus.events)-1]
	assert.Equal(t, HoldChanged, last.typ)
	assert.Equal(t, "", last.data["holder"])
	assert.Equal(t, assistant, last.data["subject"])

	released, err = svc.Release(ctx, assistant, "fake")
	require.NoError(t, err)
	assert.False(t, released, "let go already")

	// Taken from the brain before its late letting go: carol keeps it.
	_, err = svc.Hand(ctx, "alice", assistant, "fake")
	require.NoError(t, err)
	_, err = svc.Take(ctx, "carol", "c1", assistant)
	require.NoError(t, err)
	n := len(bus.events)
	released, err = svc.Release(ctx, assistant, "fake")
	require.NoError(t, err)
	assert.False(t, released)
	assert.Equal(t, "principal:carol", st.holds[assistant].Holder)
	assert.Len(t, bus.events, n, "nothing changed, nothing said")

	_, err = svc.Release(ctx, assistant, " ")
	assert.ErrorIs(t, err, ErrNoProfile)
}

func TestStopIsPublished(t *testing.T) {
	svc, _, bus := setup()
	thread := uuid.New()
	require.NoError(t, svc.Stop(context.Background(), "alice", thread, assistant))
	require.Len(t, bus.events, 1)
	assert.Equal(t, ReplyStop, bus.events[0].typ)
	assert.Equal(t, thread, bus.events[0].data["thread"])
	assert.Equal(t, assistant, bus.events[0].data["subject"])
}

func call(r http.Handler, principal, device, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if principal != "" {
		req.Header.Set(principalHeader, principal)
	}
	if device != "" {
		req.Header.Set(DeviceHeader, device)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestRoutes(t *testing.T) {
	svc, _, bus := setup()
	r := mux.NewRouter()
	RegisterRoutes(r, NewHandler(svc))
	inner := mux.NewRouter()
	RegisterInternal(inner, NewHandler(svc))
	a := "/subjects/" + assistant.String()

	w := call(r, "alice", "d1", "POST", a+"/take", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"here":true`)
	assert.Equal(t, http.StatusBadRequest, call(r, "alice", "", "POST", a+"/take", "").Code, "no device")
	assert.Equal(t, http.StatusOK, call(r, "alice", "", "POST", a+"/take?device=d2", "").Code, "?device= where no header can be set")
	assert.Equal(t, http.StatusForbidden, call(r, "bob", "b1", "POST", a+"/take", "").Code)
	assert.Equal(t, http.StatusUnauthorized, call(r, "", "d1", "POST", a+"/take", "").Code)

	w = call(r, "alice", "", "GET", "/brains", "")
	assert.JSONEq(t, `["fake","glm"]`, w.Body.String())
	w = call(r, "alice", "", "POST", a+"/hand", `{"profile":"glm"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"profile":"glm"`)
	assert.Equal(t, http.StatusUnprocessableEntity, call(r, "alice", "", "POST", a+"/hand", `{"profile":"nope"}`).Code)

	w = httptest.NewRecorder()
	inner.ServeHTTP(w, httptest.NewRequest("GET", "/holds", nil))
	var hs []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &hs))
	require.Len(t, hs, 1)
	assert.Equal(t, assistant.String(), hs[0]["subject"])
	assert.Equal(t, "glm", hs[0]["profile"])
	assert.Equal(t, http.StatusNotFound, call(r, "alice", "", "GET", "/holds", "").Code, "not at the door")

	release := func(subject, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		inner.ServeHTTP(w, httptest.NewRequest("POST", "/holds/"+subject+"/release", strings.NewReader(body)))
		return w
	}
	w = release(assistant.String(), `{"profile":"fake"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"released":false}`, w.Body.String(), "glm holds it, not fake")
	assert.Equal(t, http.StatusBadRequest, release(assistant.String(), `{}`).Code)
	assert.Equal(t, http.StatusBadRequest, release("nope", `{"profile":"glm"}`).Code)
	assert.Equal(t, http.StatusNotFound, call(r, "alice", "", "POST", "/holds/"+assistant.String()+"/release", `{"profile":"glm"}`).Code, "not at the door")
	w = release(assistant.String(), `{"profile":"glm"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"released":true}`, w.Body.String())
	assert.Equal(t, "", bus.events[len(bus.events)-1].data["holder"])

	assert.Equal(t, http.StatusNoContent, call(r, "alice", "", "POST", a+"/give-back", "").Code)
	assert.Equal(t, http.StatusAccepted, call(r, "alice", "", "POST", "/threads/"+uuid.NewString()+"/stop", `{"subject":"`+assistant.String()+`"}`).Code)
	assert.Equal(t, http.StatusBadRequest, call(r, "alice", "", "POST", "/threads/"+uuid.NewString()+"/stop", `{}`).Code)
	assert.Equal(t, ReplyStop, bus.events[len(bus.events)-1].typ)

	// No brains here: nothing to hand to.
	nb := NewService(newMem(), stewards{"alice": {alice}}, nil, nil)
	r2 := mux.NewRouter()
	RegisterRoutes(r2, NewHandler(nb))
	assert.Equal(t, http.StatusServiceUnavailable, call(r2, "alice", "", "GET", "/brains", "").Code)
	assert.Equal(t, http.StatusServiceUnavailable, call(r2, "alice", "", "POST", "/threads/"+uuid.NewString()+"/stop", `{"subject":"`+alice.String()+`"}`).Code)
}

// The SQL, against a real Postgres when REINS_TEST_DATABASE_URL names one.
func livePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("REINS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("REINS_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	// A schema of its own: stewardship's live tests migrate the same
	// database at the same time.
	setup, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	_, err = setup.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS holds_test`)
	setup.Close()
	require.NoError(t, err)
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = "holds_test"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, db.Migrate(ctx, pool))
	return pool
}

func TestLiveStore(t *testing.T) {
	pool := livePool(t)
	st := NewPGXStore(pool)
	ctx := context.Background()
	s, p := uuid.New(), "p"+uuid.NewString()[:8]

	_, ok, err := st.Get(ctx, s)
	require.NoError(t, err)
	assert.False(t, ok)
	changed, err := st.Set(ctx, Hold{Subject: s, Holder: PrincipalPrefix + p, Device: "d1"})
	require.NoError(t, err)
	assert.True(t, changed)
	first, _, _ := st.Get(ctx, s)
	changed, err = st.Set(ctx, Hold{Subject: s, Holder: PrincipalPrefix + p, Device: "d1"})
	require.NoError(t, err)
	assert.False(t, changed, "the same holder from the same device")
	again, _, _ := st.Get(ctx, s)
	assert.Equal(t, first.Since, again.Since, "since moves only when the holder does")
	changed, err = st.Set(ctx, Hold{Subject: s, Holder: PrincipalPrefix + p, Device: "d2"})
	require.NoError(t, err)
	assert.True(t, changed, "another device")

	made, err := st.SetIfNone(ctx, Hold{Subject: s, Holder: "brain:fake"})
	require.NoError(t, err)
	assert.False(t, made)
	b := uuid.New()
	made, err = st.SetIfNone(ctx, Hold{Subject: b, Holder: "brain:fake"})
	require.NoError(t, err)
	assert.True(t, made)
	brains, err := st.Brains(ctx)
	require.NoError(t, err)
	assert.Contains(t, subjects(brains), b)
	assert.NotContains(t, subjects(brains), s)
	of, err := st.Of(ctx, []uuid.UUID{s, b, uuid.New()})
	require.NoError(t, err)
	assert.Len(t, of, 2)

	cleared, err := st.Clear(ctx, b)
	require.NoError(t, err)
	assert.True(t, cleared)
	cleared, err = st.Clear(ctx, b)
	require.NoError(t, err)
	assert.False(t, cleared)

	// ClearIf: only while that holder holds it.
	cleared, err = st.ClearIf(ctx, s, "brain:fake")
	require.NoError(t, err)
	assert.False(t, cleared, "a principal holds it")
	_, ok, _ = st.Get(ctx, s)
	assert.True(t, ok, "left as it was")
	_, err = st.Set(ctx, Hold{Subject: b, Holder: "brain:fake"})
	require.NoError(t, err)
	cleared, err = st.ClearIf(ctx, b, "brain:glm")
	require.NoError(t, err)
	assert.False(t, cleared, "another brain's")
	cleared, err = st.ClearIf(ctx, b, "brain:fake")
	require.NoError(t, err)
	assert.True(t, cleared)
	_, ok, _ = st.Get(ctx, b)
	assert.False(t, ok)
	cleared, err = st.ClearIf(ctx, b, "brain:fake")
	require.NoError(t, err)
	assert.False(t, cleared, "nobody holds it")
}

// A principal's hold lapses when its device has been gone past the grace,
// counted from the later of its last sight and the take; a device seen
// within it keeps it; a brain's never lapses.
func TestLiveLapse(t *testing.T) {
	pool := livePool(t)
	st := NewPGXStore(pool)
	ctx := context.Background()
	p := "p" + uuid.NewString()[:8]
	gone, kept, brain := uuid.New(), uuid.New(), uuid.New()
	_, err := st.Set(ctx, Hold{Subject: gone, Holder: PrincipalPrefix + p, Device: "away"})
	require.NoError(t, err)
	_, err = st.Set(ctx, Hold{Subject: kept, Holder: PrincipalPrefix + p, Device: "here"})
	require.NoError(t, err)
	_, err = st.Set(ctx, Hold{Subject: brain, Holder: "brain:fake"})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE holds SET since = now() - interval '10 minutes' WHERE subject_id = ANY($1)`, []uuid.UUID{gone, kept, brain})
	require.NoError(t, err)
	require.NoError(t, st.Seen(ctx, p, "away"))
	_, err = pool.Exec(ctx, `UPDATE presence SET seen_at = now() - interval '3 minutes' WHERE principal_id = $1`, p)
	require.NoError(t, err)
	require.NoError(t, st.Seen(ctx, p, "here"))

	lapsed, err := st.Lapse(ctx, 2*time.Minute)
	require.NoError(t, err)
	assert.Contains(t, subjects(lapsed), gone)
	assert.NotContains(t, subjects(lapsed), kept)
	assert.NotContains(t, subjects(lapsed), brain)
	_, ok, _ := st.Get(ctx, gone)
	assert.False(t, ok)

	// Swept through the service: each said, as nobody.
	_, err = st.Set(ctx, Hold{Subject: gone, Holder: PrincipalPrefix + p, Device: "away"})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE holds SET since = now() - interval '10 minutes' WHERE subject_id = $1`, gone)
	require.NoError(t, err)
	bus := &memBus{}
	NewService(st, stewards{}, bus, nil).Sweep(ctx, 2*time.Minute)
	require.Len(t, bus.events, 1)
	assert.Equal(t, gone, bus.events[0].data["subject"])
	assert.Equal(t, "", bus.events[0].data["holder"])
}

func subjects(hs []Hold) []uuid.UUID {
	out := []uuid.UUID{}
	for _, h := range hs {
		out = append(out, h.Subject)
	}
	return out
}

func TestViewAndDescribe(t *testing.T) {
	h := Hold{Subject: alice, Holder: "principal:alice", Device: "d1"}
	assert.Equal(t, "principal", View(&h, "bob", "d1").Kind)
	assert.False(t, View(&h, "bob", "d1").You)
	assert.True(t, View(&h, "alice", "d1").Here)
	assert.False(t, View(&h, "alice", "d2").Here)
	assert.Equal(t, "offline", View(nil, "alice", "d1").Kind)
	assert.Equal(t, "alice", Describe(&h, "bob"))
	assert.True(t, errors.Is(&Held{By: "x"}, ErrHeld))
}
