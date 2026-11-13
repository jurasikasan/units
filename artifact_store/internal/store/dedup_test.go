package store

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

// The fakes below stand in for the two halves of the real store: the
// bytes, keyed by their sha256 (ByteStore), and the names pointing at
// them (NameDB). The expectations in these tests come from CONTRACT.md
// section 5: identical bytes are stored once — the only visible sign is
// equal sha256 — and the bytes no name points to any more are removed in
// the background.

// fakeBytes is the ByteStore the unit tests stand in for MinIO: the
// objects are keyed by their sha256, and Put stamps the store's clock,
// so a test can age an object or put one in the future (a write in
// flight).
type fakeBytes struct {
	objects map[string]fakeStored
	now     func() time.Time
	putErr  error
	getErr  error
	listErr error
	delErr  error
}

type fakeStored struct {
	data     []byte
	modified time.Time
}

func newFakeBytes() *fakeBytes {
	return &fakeBytes{
		objects: map[string]fakeStored{},
		now:     time.Now,
	}
}

func (f *fakeBytes) Put(ctx context.Context, sha string, data []byte, contentType string) error {
	if f.putErr != nil {
		return f.putErr
	}
	f.objects[sha] = fakeStored{data: append([]byte(nil), data...), modified: f.now()}
	return nil
}

func (f *fakeBytes) Get(ctx context.Context, sha string) ([]byte, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	st, ok := f.objects[sha]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), st.data...), nil
}

func (f *fakeBytes) Delete(ctx context.Context, sha string) error {
	if f.delErr != nil {
		return f.delErr
	}
	delete(f.objects, sha)
	return nil
}

func (f *fakeBytes) List(ctx context.Context) ([]StoredBytes, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]StoredBytes, 0, len(f.objects))
	for sha, st := range f.objects {
		out = append(out, StoredBytes{Key: sha, Size: int64(len(st.data)), Modified: st.modified})
	}
	return out, nil
}

func (f *fakeBytes) has(sha string) bool {
	_, ok := f.objects[sha]
	return ok
}

// seed lays an object down at a given modified time, the way MinIO would
// report it back to the sweep.
func (f *fakeBytes) seed(sha string, data []byte, modified time.Time) {
	f.objects[sha] = fakeStored{data: append([]byte(nil), data...), modified: modified}
}

// fakeNames is the NameDB the unit tests stand in for Postgres: rows of
// (space, name) pointing at a sha256, per operation.
type fakeNames struct {
	files     map[string]map[string]NamedFile
	upsertErr error
	getErr    error
	listErr   error
	delErr    error
	expErr    error
	refErr    error
}

func newFakeNames() *fakeNames {
	return &fakeNames{files: map[string]map[string]NamedFile{}}
}

func (f *fakeNames) Upsert(ctx context.Context, file NamedFile) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	if f.files[file.Space] == nil {
		f.files[file.Space] = map[string]NamedFile{}
	}
	f.files[file.Space][file.Name] = file
	return nil
}

func (f *fakeNames) Get(ctx context.Context, space, name string) (NamedFile, bool, error) {
	if f.getErr != nil {
		return NamedFile{}, false, f.getErr
	}
	file, ok := f.files[space][name]
	return file, ok, nil
}

func (f *fakeNames) List(ctx context.Context, space, namePrefix string) ([]NamedFile, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]NamedFile, 0)
	for name, file := range f.files[space] {
		if strings.HasPrefix(name, namePrefix) {
			out = append(out, file)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeNames) Delete(ctx context.Context, space, name string) (bool, error) {
	if f.delErr != nil {
		return false, f.delErr
	}
	if _, ok := f.files[space][name]; !ok {
		return false, nil
	}
	delete(f.files[space], name)
	return true, nil
}

func (f *fakeNames) Expire(ctx context.Context, space string, olderThan time.Time) error {
	if f.expErr != nil {
		return f.expErr
	}
	for name, file := range f.files[space] {
		if file.UpdatedAt.Before(olderThan) {
			delete(f.files[space], name)
		}
	}
	return nil
}

func (f *fakeNames) Referenced(ctx context.Context) (map[string]bool, error) {
	if f.refErr != nil {
		return nil, f.refErr
	}
	out := map[string]bool{}
	for _, byName := range f.files {
		for _, file := range byName {
			out[file.Sha256] = true
		}
	}
	return out, nil
}

// newDedupStore stands a MinioStore over the fakes, the way the
// composite store stands over Postgres and MinIO.
func newDedupStore(names *fakeNames, b *fakeBytes) *MinioStore {
	return &MinioStore{names: names, bytes: b, SweepPeriod: DefaultSweepPeriod}
}

// TestDedupEqualBytesOneObject pins the core of the sector: equal bytes
// under two names are one object in the byte store, and both names still
// read back the same bytes (CONTRACT.md section 5: "identical bytes are
// stored once; the only visible sign is equal sha256").
func TestDedupEqualBytesOneObject(t *testing.T) {
	names, b := newFakeNames(), newFakeBytes()
	svc := &DefaultService{Store: newDedupStore(names, b), MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

	body := "the same bytes under two names"
	if w := doPut(t, r, "docs", "K-1.a", "text/plain", "alice", body); w.Code != http.StatusOK {
		t.Fatalf("PUT K-1.a status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if w := doPut(t, r, "docs", "K-2.b", "text/plain", "", body); w.Code != http.StatusOK {
		t.Fatalf("PUT K-2.b status = %d, want 200: %s", w.Code, w.Body.String())
	}

	// Stored once: exactly one object, under the sha256 of the bytes.
	if got, want := len(b.objects), 1; got != want {
		t.Errorf("objects in the byte store = %d, want %d (identical bytes stored once)", got, want)
	}
	sha := sha256hex([]byte(body))
	if !b.has(sha) {
		t.Errorf("byte store has no object under %s (the sha256 of the bytes)", sha)
	}

	// Both names still read the bytes back.
	for _, name := range []string{"K-1.a", "K-2.b"} {
		if g := doGet(t, r, "docs", name); g.Code != http.StatusOK || g.Body.String() != body {
			t.Errorf("GET %s: status = %d, body = %q, want 200 %q", name, g.Code, g.Body.String(), body)
		}
	}
}

// TestDedupDeleteOneKeepsBytes: deleting one of the two names moves the
// name only; the bytes stay while the other name still points to them.
func TestDedupDeleteOneKeepsBytes(t *testing.T) {
	names, b := newFakeNames(), newFakeBytes()
	m := newDedupStore(names, b)
	svc := &DefaultService{Store: m, MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

	body := "the same bytes under two names"
	sha := sha256hex([]byte(body))
	if w := doPut(t, r, "docs", "K-1.a", "text/plain", "", body); w.Code != http.StatusOK {
		t.Fatalf("PUT K-1.a status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if w := doPut(t, r, "docs", "K-2.b", "text/plain", "", body); w.Code != http.StatusOK {
		t.Fatalf("PUT K-2.b status = %d, want 200: %s", w.Code, w.Body.String())
	}

	if w := doDelete(t, r, "docs", "K-1.a"); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE K-1.a status = %d, want 204", w.Code)
	}

	// One name still points to the bytes: they are still there, and the
	// other name still reads them.
	if !b.has(sha) {
		t.Errorf("the bytes under %s are gone, but K-2.b still points to them", sha)
	}
	if g := doGet(t, r, "docs", "K-2.b"); g.Code != http.StatusOK || g.Body.String() != body {
		t.Errorf("GET K-2.b: status = %d, body = %q, want 200 %q", g.Code, g.Body.String(), body)
	}
}

// TestDedupDeleteBothSweepRemoves: once neither name points to the
// bytes any more, the sweep takes them (CONTRACT.md section 5).
func TestDedupDeleteBothSweepRemoves(t *testing.T) {
	names, b := newFakeNames(), newFakeBytes()
	m := newDedupStore(names, b)
	svc := &DefaultService{Store: m, MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

	body := "the same bytes under two names"
	sha := sha256hex([]byte(body))
	if w := doPut(t, r, "docs", "K-1.a", "text/plain", "", body); w.Code != http.StatusOK {
		t.Fatalf("PUT K-1.a status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if w := doPut(t, r, "docs", "K-2.b", "text/plain", "", body); w.Code != http.StatusOK {
		t.Fatalf("PUT K-2.b status = %d, want 200: %s", w.Code, w.Body.String())
	}

	if w := doDelete(t, r, "docs", "K-1.a"); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE K-1.a status = %d, want 204", w.Code)
	}
	if w := doDelete(t, r, "docs", "K-2.b"); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE K-2.b status = %d, want 204", w.Code)
	}
	// The names are gone: GET and the listing say so.
	if w := doGet(t, r, "docs", "K-2.b"); w.Code != http.StatusNotFound {
		t.Errorf("GET after both deletes status = %d, want 404", w.Code)
	}
	if got := namesOf(t, decodeListing(t, doList(t, r, "docs", ""))); len(got) != 0 {
		t.Errorf("listing after both deletes names = %v, want []", got)
	}

	// No name points to the bytes: the sweep removes them.
	if err := m.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if b.has(sha) {
		t.Errorf("the bytes under %s are still there after the sweep; no name points to them", sha)
	}
}

// TestSweepKeepsReferenced: the sweep never touches the bytes a name
// still points to — whether one or two names point to them.
func TestSweepKeepsReferenced(t *testing.T) {
	names, b := newFakeNames(), newFakeBytes()
	m := newDedupStore(names, b)
	svc := &DefaultService{Store: m, MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

	shared := "shared bytes"
	own := "unique bytes"
	sharedSha, ownSha := sha256hex([]byte(shared)), sha256hex([]byte(own))
	if w := doPut(t, r, "docs", "K-1.a", "text/plain", "", shared); w.Code != http.StatusOK {
		t.Fatalf("PUT K-1.a status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if w := doPut(t, r, "docs", "K-2.b", "text/plain", "", shared); w.Code != http.StatusOK {
		t.Fatalf("PUT K-2.b status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if w := doPut(t, r, "docs", "K-3.c", "text/plain", "", own); w.Code != http.StatusOK {
		t.Fatalf("PUT K-3.c status = %d, want 200: %s", w.Code, w.Body.String())
	}

	if err := m.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if !b.has(sharedSha) {
		t.Errorf("the bytes under %s were swept, but two names still point to them", sharedSha)
	}
	if !b.has(ownSha) {
		t.Errorf("the bytes under %s were swept, but K-3.c still points to them", ownSha)
	}
}

// TestSweepKeepsWriteInFlight: the sweep never touches the bytes a PUT
// is writing at that moment. A PUT lays its bytes down before its name;
// a sweep that begins while it is in flight sees bytes with no name yet,
// newer than the sweep began, and must leave them — the name arrives
// right after, and the bytes stay pointed to.
func TestSweepKeepsWriteInFlight(t *testing.T) {
	names, b := newFakeNames(), newFakeBytes()
	m := newDedupStore(names, b)
	svc := &DefaultService{Store: m, MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

	// A finished file elsewhere, so the sweep has something it may take.
	if w := doPut(t, r, "docs", "old.a", "text/plain", "", "old"); w.Code != http.StatusOK {
		t.Fatalf("PUT old.a status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if w := doDelete(t, r, "docs", "old.a"); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE old.a status = %d, want 204", w.Code)
	}
	oldSha := sha256hex([]byte("old"))

	// A PUT in flight: its bytes are down, its name is not yet. Its
	// bytes are newer than any sweep that has begun.
	inflight := "bytes being written right now"
	inflightSha := sha256hex([]byte(inflight))
	b.seed(inflightSha, []byte(inflight), time.Now().Add(time.Minute))

	if err := m.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if !b.has(inflightSha) {
		t.Errorf("the bytes a PUT is writing at that moment were swept")
	}
	if b.has(oldSha) {
		t.Errorf("the orphaned bytes under %s were kept, but no name points to them", oldSha)
	}

	// The PUT finishes: the name points to the bytes, and they stay —
	// even after another sweep.
	if w := doPut(t, r, "docs", "K-1.a", "text/plain", "", inflight); w.Code != http.StatusOK {
		t.Fatalf("PUT K-1.a status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if g := doGet(t, r, "docs", "K-1.a"); g.Code != http.StatusOK || g.Body.String() != inflight {
		t.Errorf("GET K-1.a: status = %d, body = %q, want 200 %q", g.Code, g.Body.String(), inflight)
	}
	if err := m.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep after the PUT finished: %v", err)
	}
	if !b.has(inflightSha) {
		t.Errorf("the bytes under %s were swept, but K-1.a points to them", inflightSha)
	}
}

// TestSweepPeriodIsSettable: the sweep's period is a variable a test can
// set; StartSweep runs the sweep on it.
func TestSweepPeriodIsSettable(t *testing.T) {
	names, b := newFakeNames(), newFakeBytes()
	m := newDedupStore(names, b)
	m.SweepPeriod = 20 * time.Millisecond

	// An orphan the sweep may take, written before the sweep began.
	body := "orphan bytes"
	sha := sha256hex([]byte(body))
	if err := m.bytes.Put(context.Background(), sha, []byte(body), "text/plain"); err != nil {
		t.Fatalf("bytes.Put: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.StartSweep(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for b.has(sha) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b.has(sha) {
		t.Errorf("the sweep did not run within its period; the orphan under %s is still there", sha)
	}

	// A new orphan is taken on a later pass: the sweep keeps running on
	// its period.
	if err := m.bytes.Put(context.Background(), sha, []byte(body), "text/plain"); err != nil {
		t.Fatalf("bytes.Put again: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for b.has(sha) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b.has(sha) {
		t.Errorf("a later pass of the sweep did not take the new orphan under %s", sha)
	}
}

// TestPostgresDownIs503: Postgres unreachable is a 503 on every file
// route, the way MinIO unreachable is (CONTRACT.md section 6).
func TestPostgresDownIs503(t *testing.T) {
	names := newFakeNames()
	b := newFakeBytes()
	m := newDedupStore(names, b)
	svc := &DefaultService{Store: m, MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

	names.upsertErr = ErrUnavailable
	names.getErr = ErrUnavailable
	names.listErr = ErrUnavailable
	names.delErr = ErrUnavailable

	if w := doPut(t, r, "docs", "K-1.a", "text/plain", "", "x"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("PUT status = %d, want 503", w.Code)
	}
	if w := doGet(t, r, "docs", "K-1.a"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET status = %d, want 503", w.Code)
	}
	if w := doList(t, r, "docs", ""); w.Code != http.StatusServiceUnavailable {
		t.Errorf("LIST status = %d, want 503", w.Code)
	}
	if w := doDelete(t, r, "docs", "K-1.a"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("DELETE status = %d, want 503", w.Code)
	}
}

// TestPostgresDownPutLeavesPrevious: the name step of a PUT fails, so
// the previous file stays as it was (PUT is all or nothing); the possibly
// orphaned bytes are the sweep's to take, not the reader's to meet.
func TestPostgresDownPutLeavesPrevious(t *testing.T) {
	names, b := newFakeNames(), newFakeBytes()
	m := newDedupStore(names, b)
	svc := &DefaultService{Store: m, MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

	if w := doPut(t, r, "docs", "K-1.a", "text/plain", "", "hello"); w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200: %s", w.Code, w.Body.String())
	}

	names.upsertErr = ErrUnavailable
	w := doPut(t, r, "docs", "K-1.a", "text/plain", "", "world")
	names.upsertErr = nil
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("broken PUT status = %d, want 503", w.Code)
	}

	// The name still points at the previous bytes.
	if g := doGet(t, r, "docs", "K-1.a"); g.Code != http.StatusOK || g.Body.String() != "hello" {
		t.Errorf("GET after a broken PUT: status = %d, body = %q, want 200 hello", g.Code, g.Body.String())
	}

	// The new bytes, no name pointing to them, are what the sweep takes.
	newSha := sha256hex([]byte("world"))
	if !b.has(newSha) {
		t.Fatalf("the bytes under %s are missing; the sweep has nothing to take", newSha)
	}
	if err := m.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if b.has(newSha) {
		t.Errorf("the orphaned bytes under %s survived the sweep", newSha)
	}
	if oldSha := sha256hex([]byte("hello")); !b.has(oldSha) {
		t.Errorf("the bytes under %s were swept, but K-1.a points to them", oldSha)
	}
}
