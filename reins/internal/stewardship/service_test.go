package stewardship

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStore keeps the relation in memory; its lock is one mutex per store,
// which is enough to serialise a principal.
type fakeStore struct {
	mu      sync.Mutex
	lock    sync.Mutex
	rows    map[string][]uuid.UUID
	settled map[string]bool
	// recordFails makes the nth Record fail, to cut a run half way.
	recordFails int
	records     int
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[string][]uuid.UUID{}, settled: map[string]bool{}}
}

func (f *fakeStore) Steered(_ context.Context, p string) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uuid.UUID{}, f.rows[p]...), nil
}

func (f *fakeStore) Steers(_ context.Context, p string, s uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return indexOf(f.rows[p], s) >= 0, nil
}

func (f *fakeStore) Settled(_ context.Context, p string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settled[p], nil
}

func (f *fakeStore) Provisioning(_ context.Context, p string, fn func(Locked) error) error {
	f.lock.Lock()
	defer f.lock.Unlock()
	return fn(&fakeLocked{f, p})
}

func (f *fakeStore) Handover(_ context.Context, s uuid.UUID, from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := indexOf(f.rows[from], s)
	if i < 0 {
		return ErrNotSteward
	}
	f.rows[from] = append(f.rows[from][:i], f.rows[from][i+1:]...)
	if indexOf(f.rows[to], s) < 0 {
		f.rows[to] = append(f.rows[to], s)
	}
	return nil
}

func indexOf(ids []uuid.UUID, id uuid.UUID) int {
	for i, x := range ids {
		if x == id {
			return i
		}
	}
	return -1
}

type fakeLocked struct {
	f *fakeStore
	p string
}

func (l *fakeLocked) Steered(ctx context.Context) ([]uuid.UUID, error) { return l.f.Steered(ctx, l.p) }
func (l *fakeLocked) Settled(ctx context.Context) (bool, error)        { return l.f.Settled(ctx, l.p) }
func (l *fakeLocked) Record(_ context.Context, s uuid.UUID) error {
	l.f.mu.Lock()
	defer l.f.mu.Unlock()
	l.f.records++
	if l.f.records == l.f.recordFails {
		return errors.New("database went away")
	}
	l.f.rows[l.p] = append(l.f.rows[l.p], s)
	return nil
}
func (l *fakeLocked) Settle(context.Context) error {
	l.f.mu.Lock()
	defer l.f.mu.Unlock()
	l.f.settled[l.p] = true
	return nil
}

// fakeRegistry is `subjects`: nicks unique forever, deleted ones included.
// extra holds fields reins does not know, which a change must keep.
type fakeRegistry struct {
	mu      sync.Mutex
	byID    map[uuid.UUID]Subject
	extra   map[uuid.UUID]map[string]json.RawMessage
	deleted map[uuid.UUID]bool
	created int
	put     map[string]json.RawMessage // the last PUT body
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{byID: map[uuid.UUID]Subject{}, extra: map[uuid.UUID]map[string]json.RawMessage{}, deleted: map[uuid.UUID]bool{}}
}

func (r *fakeRegistry) Raw(_ context.Context, id uuid.UUID) (map[string]json.RawMessage, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.byID[id]
	if !ok || r.deleted[id] {
		return nil, false, nil
	}
	b, _ := json.Marshal(s)
	raw := map[string]json.RawMessage{}
	_ = json.Unmarshal(b, &raw)
	for k, v := range r.extra[id] {
		raw[k] = v
	}
	return raw, true, nil
}

// Replace is subjects' PUT: the body replaces the subject, and a nick in it
// must be the stored one.
func (r *fakeRegistry) Replace(_ context.Context, id uuid.UUID, raw map[string]json.RawMessage) (Subject, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.byID[id]
	if !ok || r.deleted[id] {
		return Subject{}, ErrNoSubject
	}
	r.put = raw
	b, _ := json.Marshal(raw)
	var s Subject
	if err := json.Unmarshal(b, &s); err != nil {
		return Subject{}, err
	}
	if s.Nick != "" && s.Nick != stored.Nick {
		return Subject{}, errors.New("subjects: 400 nick cannot be changed")
	}
	s.ID, s.Nick = id, stored.Nick
	r.byID[id] = s
	extra := map[string]json.RawMessage{}
	for k, v := range raw {
		switch k {
		case "id", "name", "nick", "bio", "preferences":
		default:
			extra[k] = v
		}
	}
	r.extra[id] = extra
	return s, nil
}

func (r *fakeRegistry) Get(_ context.Context, id uuid.UUID) (Subject, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.byID[id]
	if !ok || r.deleted[id] {
		return Subject{}, false, nil
	}
	return s, true, nil
}

func (r *fakeRegistry) Create(_ context.Context, n NewSubject) (Subject, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.byID {
		if s.Nick == n.Nick {
			return Subject{}, ErrNickTaken
		}
	}
	s := Subject{ID: uuid.New(), Nick: n.Nick, Name: n.Name}
	r.byID[s.ID] = s
	r.created++
	return s, nil
}

func nicks(subs []Subject) []string {
	out := []string{}
	for _, s := range subs {
		out = append(out, s.Nick)
	}
	return out
}

func TestFirstVisitMakesBothPersonasOnce(t *testing.T) {
	store, reg := newFakeStore(), newFakeRegistry()
	svc := NewService(store, reg)
	ctx := context.Background()

	first, err := svc.MySubjects(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, []string{"alice", "alice-assistant"}, nicks(first))
	assert.Empty(t, first[0].Name, "names are left empty")

	again, err := svc.MySubjects(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, first, again, "a second visit gets the same subjects")
	assert.Equal(t, 2, reg.created)
}

// Welcome runs once the personas are made, with the login's and the
// assistant's ids, before the principal is settled: a welcome that fails
// leaves it unsettled, and the next visit runs it again.
func TestWelcomeBeforeSettled(t *testing.T) {
	store, reg := newFakeStore(), newFakeRegistry()
	svc := NewService(store, reg)
	ctx := context.Background()
	var got [][2]uuid.UUID
	fail := true
	svc.Welcome = func(_ context.Context, p string, login, assistant uuid.UUID) error {
		assert.Equal(t, "alice", p)
		got = append(got, [2]uuid.UUID{login, assistant})
		if fail {
			return errors.New("threads went away")
		}
		return nil
	}
	_, err := svc.MySubjects(ctx, "alice")
	require.Error(t, err)
	assert.False(t, store.settled["alice"])
	fail = false
	subs, err := svc.MySubjects(ctx, "alice")
	require.NoError(t, err)
	assert.True(t, store.settled["alice"])
	_, err = svc.MySubjects(ctx, "alice")
	require.NoError(t, err)
	require.Len(t, got, 2, "run again after the failure, then never")
	assert.Equal(t, [2]uuid.UUID{subs[0].ID, subs[1].ID}, got[1])
	assert.Equal(t, 2, reg.created)
}

func TestConcurrentFirstVisitsMakeEachPersonaOnce(t *testing.T) {
	store, reg := newFakeStore(), newFakeRegistry()
	svc := NewService(store, reg)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.MySubjects(context.Background(), "alice")
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	assert.Equal(t, 2, reg.created)
}

func TestRecordLostAfterCreateLeavesTheNickRefused(t *testing.T) {
	store, reg := newFakeStore(), newFakeRegistry()
	store.recordFails = 2 // the assistant is made, its record fails
	svc := NewService(store, reg)
	ctx := context.Background()

	_, err := svc.MySubjects(ctx, "alice")
	require.Error(t, err)
	assert.False(t, store.settled["alice"])
	assert.Len(t, store.rows["alice"], 1, "the first persona was recorded at once")

	// The cut fell between subjects' 201 and our record: the assistant's nick
	// is now held by a subject nobody steers. It is refused, not adopted.
	_, err = svc.MySubjects(ctx, "alice")
	var ref *Refusal
	require.ErrorAs(t, err, &ref)
	assert.Equal(t, 409, ref.Status)
}

func TestRunCutBeforeTheSecondPersonaCompletes(t *testing.T) {
	store, reg := newFakeStore(), newFakeRegistry()
	svc := NewService(store, reg)
	ctx := context.Background()
	// A run that made `alice` and stopped before the assistant.
	s, _ := reg.Create(ctx, NewSubject{Nick: "alice"})
	store.rows["alice"] = []uuid.UUID{s.ID}

	subs, err := svc.MySubjects(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, []string{"alice", "alice-assistant"}, nicks(subs))
	assert.Equal(t, s.ID, subs[0].ID, "what was made is kept, not made again")
	assert.True(t, store.settled["alice"])
}

func TestTakenNickIsRefusedNotAdopted(t *testing.T) {
	store, reg := newFakeStore(), newFakeRegistry()
	svc := NewService(store, reg)
	ctx := context.Background()
	someone, _ := reg.Create(ctx, NewSubject{Nick: "carol"})

	_, err := svc.MySubjects(ctx, "carol")
	var ref *Refusal
	require.ErrorAs(t, err, &ref)
	assert.Equal(t, 409, ref.Status)
	assert.Contains(t, ref.Reason, `"carol"`)
	assert.NotContains(t, store.rows["carol"], someone.ID)
	assert.False(t, store.settled["carol"])
}

func TestLoginThatBreaksNickRulesIsRefused(t *testing.T) {
	for _, login := range []string{"j.doe", "Alice", "a", "averyveryverylonglogin1"} {
		t.Run(login, func(t *testing.T) {
			reg := newFakeRegistry()
			_, err := NewService(newFakeStore(), reg).MySubjects(context.Background(), login)
			var ref *Refusal
			require.ErrorAs(t, err, &ref)
			assert.Equal(t, 422, ref.Status)
			assert.Zero(t, reg.created, "nothing is made for a refused login")
		})
	}
	// 22 characters: the longest whose assistant still fits in 32.
	_, err := NewService(newFakeStore(), newFakeRegistry()).MySubjects(context.Background(), "averyveryverylonglogin")
	assert.NoError(t, err)
}

func TestHandoverMovesTheSubjectAndNothingIsMadeAgain(t *testing.T) {
	store, reg := newFakeStore(), newFakeRegistry()
	svc := NewService(store, reg)
	ctx := context.Background()
	alice, err := svc.MySubjects(ctx, "alice")
	require.NoError(t, err)
	assistant := alice[1].ID

	require.NoError(t, svc.Handover(ctx, "alice", assistant, "bob"))

	a, err := svc.MySubjects(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, []string{"alice"}, nicks(a), "alice is settled: her assistant is not made anew")
	b, err := svc.MySubjects(ctx, "bob")
	require.NoError(t, err)
	assert.Equal(t, []string{"alice-assistant", "bob", "bob-assistant"}, nicks(b))
	assert.Equal(t, 4, reg.created)
}

func TestHandoverOnlyByASteward(t *testing.T) {
	store, reg := newFakeStore(), newFakeRegistry()
	svc := NewService(store, reg)
	ctx := context.Background()
	alice, _ := svc.MySubjects(ctx, "alice")

	assert.ErrorIs(t, svc.Handover(ctx, "mallory", alice[0].ID, "mallory2"), ErrNotSteward)
	assert.ErrorIs(t, svc.Handover(ctx, "alice", alice[0].ID, ""), ErrBadHandover)
	assert.ErrorIs(t, svc.Handover(ctx, "alice", alice[0].ID, "alice"), ErrBadHandover)
	assert.Len(t, store.rows["alice"], 2)
}

func TestDeletedSubjectsAreLeftOut(t *testing.T) {
	store, reg := newFakeStore(), newFakeRegistry()
	svc := NewService(store, reg)
	ctx := context.Background()
	alice, _ := svc.MySubjects(ctx, "alice")
	reg.deleted[alice[0].ID] = true

	subs, err := svc.MySubjects(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, []string{"alice-assistant"}, nicks(subs))
}

func TestSubjectOnlyForASteward(t *testing.T) {
	store, reg := newFakeStore(), newFakeRegistry()
	svc := NewService(store, reg)
	mine, err := svc.MySubjects(context.Background(), "alice")
	require.NoError(t, err)

	got, err := svc.Subject(context.Background(), "alice", mine[1].ID)
	require.NoError(t, err)
	assert.Equal(t, "alice-assistant", got.Nick)

	_, err = svc.Subject(context.Background(), "mallory", mine[1].ID)
	assert.ErrorIs(t, err, ErrNotSteward)

	reg.deleted[mine[1].ID] = true
	_, err = svc.Subject(context.Background(), "alice", mine[1].ID)
	assert.ErrorIs(t, err, ErrNoSubject)
}

func TestChangeKeepsWhatItDoesNotTouch(t *testing.T) {
	store, reg := newFakeStore(), newFakeRegistry()
	svc := NewService(store, reg)
	ctx := context.Background()
	mine, err := svc.MySubjects(ctx, "alice")
	require.NoError(t, err)
	id := mine[1].ID
	reg.extra[id] = map[string]json.RawMessage{"born": json.RawMessage(`"2026-11-20"`)}
	s := reg.byID[id]
	s.Bio = "was here first"
	s.Preferences = json.RawMessage(`{"ear":"fake","theirs":{"x":1}}`)
	reg.byID[id] = s

	name := "Alice's assistant"
	got, err := svc.Change(ctx, "alice", id, Change{
		Name:        &name,
		Preferences: map[string]json.RawMessage{"ear": json.RawMessage(`"fake-stt-b"`), "voice": json.RawMessage(`"irina"`)},
	})
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)
	assert.Equal(t, "alice-assistant", got.Nick)
	assert.Equal(t, "was here first", got.Bio, "a bio left out is kept")
	assert.JSONEq(t, `{"ear":"fake-stt-b","voice":"irina","theirs":{"x":1}}`, string(got.Preferences),
		"preferences are merged: another reader's key survives")
	assert.JSONEq(t, `"2026-11-20"`, string(reg.put["born"]), "a field reins does not know goes back as it came")
	assert.JSONEq(t, `"alice-assistant"`, string(reg.put["nick"]), "the nick goes back as stored")

	got, err = svc.Change(ctx, "alice", id, Change{Preferences: map[string]json.RawMessage{"voice": json.RawMessage(`null`)}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"ear":"fake-stt-b","theirs":{"x":1}}`, string(got.Preferences), "null takes a key out")
	assert.Equal(t, name, got.Name, "a name left out is kept")

	_, err = svc.Change(ctx, "mallory", id, Change{Name: &name})
	assert.ErrorIs(t, err, ErrNotSteward)
	assert.Equal(t, name, reg.byID[id].Name)
}

func TestChangeOfAStoredPreferencesThatIsNoObject(t *testing.T) {
	store, reg := newFakeStore(), newFakeRegistry()
	svc := NewService(store, reg)
	ctx := context.Background()
	mine, _ := svc.MySubjects(ctx, "alice")
	s := reg.byID[mine[0].ID]
	s.Preferences = json.RawMessage(`[1]`)
	reg.byID[mine[0].ID] = s
	_, err := svc.Change(ctx, "alice", mine[0].ID, Change{Preferences: map[string]json.RawMessage{"ear": json.RawMessage(`"fake-stt"`)}})
	assert.ErrorIs(t, err, ErrBadChange)
}
