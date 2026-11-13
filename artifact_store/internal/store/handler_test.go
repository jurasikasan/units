package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// fakeObjectStore is the object store the unit tests stand in for MinIO.
// A missing key is ErrNotFound, a broken store is ErrUnavailable — the
// same vocabulary MinioStore keeps.
type fakeObjectStore struct {
	objects   map[string]fakeObject
	putErr    error // if set, Put fails
	getErr    error // if set, Get fails
	listErr   error // if set, List fails
	delErr    error // if set, Delete fails
	storedPut int
}

type fakeObject struct {
	data []byte
	meta ObjectMeta
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: map[string]fakeObject{}}
}

func (f *fakeObjectStore) Put(ctx context.Context, key string, data []byte, meta ObjectMeta) error {
	if f.putErr != nil {
		return f.putErr
	}
	f.storedPut++
	f.objects[key] = fakeObject{data: append([]byte(nil), data...), meta: meta}
	return nil
}

func (f *fakeObjectStore) Get(ctx context.Context, key string) ([]byte, string, error) {
	if f.getErr != nil {
		return nil, "", f.getErr
	}
	fo, ok := f.objects[key]
	if !ok {
		return nil, "", ErrNotFound
	}
	return append([]byte(nil), fo.data...), fo.meta.ContentType, nil
}

// List answers the objects whose key starts with prefix, in key order,
// with the metadata Put recorded — the same facts MinIO would hand back.
func (f *fakeObjectStore) List(ctx context.Context, prefix string) ([]Object, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]Object, 0, len(f.objects))
	for key, fo := range f.objects {
		if strings.HasPrefix(key, prefix) {
			out = append(out, Object{Key: key, Size: int64(len(fo.data)), Meta: fo.meta})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (f *fakeObjectStore) Delete(ctx context.Context, key string) error {
	if f.delErr != nil {
		return f.delErr
	}
	if _, ok := f.objects[key]; !ok {
		return ErrNotFound
	}
	delete(f.objects, key)
	return nil
}

func (f *fakeObjectStore) has(key string) bool {
	_, ok := f.objects[key]
	return ok
}

// newTestRouter registers the file routes on a mux router, the same way
// internal/app wires them.
func newTestRouter(svc Service) *mux.Router {
	r := mux.NewRouter()
	h := NewHandler(svc)
	r.HandleFunc("/v1/{space}/{name}", h.Put).Methods(http.MethodPut)
	r.HandleFunc("/v1/{space}/{name}", h.Get).Methods(http.MethodGet)
	r.HandleFunc("/v1/{space}", h.List).Methods(http.MethodGet)
	r.HandleFunc("/v1/{space}/{name}", h.Delete).Methods(http.MethodDelete)
	return r
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func doPut(t *testing.T, r *mux.Router, space, name, contentType, principal string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/v1/"+space+"/"+name, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if principal != "" {
		req.Header.Set("X-Principal-Id", principal)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func doGet(t *testing.T, r *mux.Router, space, name string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/"+space+"/"+name, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func doDelete(t *testing.T, r *mux.Router, space, name string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/v1/"+space+"/"+name, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestPutThenGet pins the contract: PUT then GET returns the same bytes,
// the kept Content-Type, and ETag "<sha256>" of the bytes
// (CONTRACT.md section 3).
func TestPutThenGet(t *testing.T) {
	fs := newFakeObjectStore()
	svc := &DefaultService{Store: fs, MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

	body := "hello"
	w := doPut(t, r, "docs", "K-1.a", "text/plain", "alice", body)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200: %s", w.Code, w.Body.String())
	}

	// The PUT answer is the file's metadata.
	var md struct {
		Name        *string `json:"name"`
		Sha256      *string `json:"sha256"`
		Size        *int64  `json:"size"`
		ContentType *string `json:"content_type"`
		CreatedBy   *string `json:"created_by"`
		UpdatedAt   *string `json:"updated_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &md); err != nil {
		t.Fatalf("decode PUT metadata %q: %v", w.Body.String(), err)
	}
	if md.Name == nil || *md.Name != "K-1.a" {
		t.Errorf("metadata name = %v, want K-1.a", md.Name)
	}
	if md.Sha256 == nil || *md.Sha256 != sha256hex([]byte(body)) {
		t.Errorf("metadata sha256 = %v, want %s", md.Sha256, sha256hex([]byte(body)))
	}
	if md.Size == nil || *md.Size != int64(len(body)) {
		t.Errorf("metadata size = %v, want %d", md.Size, len(body))
	}
	if md.ContentType == nil || *md.ContentType != "text/plain" {
		t.Errorf("metadata content_type = %v, want text/plain", md.ContentType)
	}
	if md.CreatedBy == nil || *md.CreatedBy != "alice" {
		t.Errorf("metadata created_by = %v, want alice (from X-Principal-Id)", md.CreatedBy)
	}
	if md.UpdatedAt == nil {
		t.Error("metadata updated_at missing")
	} else if _, err := time.Parse(time.RFC3339, *md.UpdatedAt); err != nil {
		t.Errorf("metadata updated_at %q is not RFC3339: %v", *md.UpdatedAt, err)
	}

	// The bytes live under the object key of space and name.
	if !fs.has(ObjectKey("docs", "K-1.a")) {
		t.Errorf("store keys = %v, want %q", keys(fs), ObjectKey("docs", "K-1.a"))
	}

	// GET sends the bytes back with Content-Type, Content-Length, ETag.
	g := doGet(t, r, "docs", "K-1.a")
	if g.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200: %s", g.Code, g.Body.String())
	}
	if !bytes.Equal(g.Body.Bytes(), []byte(body)) {
		t.Errorf("GET body = %q, want %q", g.Body.String(), body)
	}
	if ct := g.Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("GET Content-Type = %q, want text/plain", ct)
	}
	if cl := g.Header().Get("Content-Length"); cl != "5" {
		t.Errorf("GET Content-Length = %q, want 5", cl)
	}
	if etag := g.Header().Get("ETag"); etag != `"`+sha256hex([]byte(body))+`"` {
		t.Errorf("GET ETag = %q, want %q", etag, `"`+sha256hex([]byte(body))+`"`)
	}
}

// TestPutReplaces: a second PUT under the same name makes the new bytes
// the file (CONTRACT.md section 3, "PUT replaces").
func TestPutReplaces(t *testing.T) {
	fs := newFakeObjectStore()
	svc := &DefaultService{Store: fs, MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

	if w := doPut(t, r, "docs", "K-1.a", "text/plain", "", "hello"); w.Code != http.StatusOK {
		t.Fatalf("first PUT status = %d, want 200", w.Code)
	}
	if w := doPut(t, r, "docs", "K-1.a", "text/plain", "", "world"); w.Code != http.StatusOK {
		t.Fatalf("second PUT status = %d, want 200", w.Code)
	}

	g := doGet(t, r, "docs", "K-1.a")
	if g.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", g.Code)
	}
	if g.Body.String() != "world" {
		t.Errorf("GET body = %q, want world (the second PUT's bytes)", g.Body.String())
	}
	if etag := g.Header().Get("ETag"); etag != `"`+sha256hex([]byte("world"))+`"` {
		t.Errorf("GET ETag = %q, want the sha256 of the new bytes", etag)
	}
}

// TestPutBrokenLeavesPrevious: a broken upload leaves the previous file
// as it was (CONTRACT.md section 3, "PUT is all or nothing").
func TestPutBrokenLeavesPrevious(t *testing.T) {
	fs := newFakeObjectStore()
	svc := &DefaultService{Store: fs, MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

	if w := doPut(t, r, "docs", "K-1.a", "text/plain", "", "hello"); w.Code != http.StatusOK {
		t.Fatalf("first PUT status = %d, want 200", w.Code)
	}

	fs.putErr = ErrUnavailable
	w := doPut(t, r, "docs", "K-1.a", "text/plain", "", "world")
	fs.putErr = nil
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("broken PUT status = %d, want 503", w.Code)
	}

	g := doGet(t, r, "docs", "K-1.a")
	if g.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", g.Code)
	}
	if g.Body.String() != "hello" {
		t.Errorf("GET body = %q, want hello (the previous file, unchanged)", g.Body.String())
	}
}

// TestBadAlphabet: a space or name that breaks the alphabet of
// CONTRACT.md section 4 is refused with 400 on the file routes.
func TestBadAlphabet(t *testing.T) {
	fs := newFakeObjectStore()
	svc := &DefaultService{Store: fs, MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

	badSpaces := []string{
		"T",                     // upper case, and one char
		"ab",                    // too short: needs three chars
		"1ab",                   // must start with a lower letter
		"ab_c",                  // underscore is not in the alphabet
		strings.Repeat("a", 33), // longer than 32
	}
	badNames := []string{
		".a",                     // must not start with a dot
		"a b",                    // space is not in the alphabet
		"a/b",                    // never a path
		strings.Repeat("a", 256), // longer than 255
	}
	for _, space := range badSpaces {
		if w := doPut(t, r, space, "K-1.a", "text/plain", "", "x"); w.Code != http.StatusBadRequest {
			t.Errorf("PUT /v1/%q/... status = %d, want 400", space, w.Code)
		}
		if w := doGet(t, r, space, "K-1.a"); w.Code != http.StatusBadRequest {
			t.Errorf("GET /v1/%q/... status = %d, want 400", space, w.Code)
		}
		if w := doList(t, r, space, ""); w.Code != http.StatusBadRequest {
			t.Errorf("LIST /v1/%q status = %d, want 400", space, w.Code)
		}
		if w := doDelete(t, r, space, "K-1.a"); w.Code != http.StatusBadRequest {
			t.Errorf("DELETE /v1/%q/... status = %d, want 400", space, w.Code)
		}
	}
	// Names may hold characters the router would never hand back as a
	// segment (a "/"), so the alphabet is tested on the service.
	for _, name := range badNames {
		if _, err := svc.Put(context.Background(), "docs", name, "text/plain", "", strings.NewReader("x")); err == nil {
			t.Errorf("service Put(name=%q) succeeded, want the 400-class error", name)
		}
		if _, _, err := svc.Get(context.Background(), "docs", name); err == nil {
			t.Errorf("service Get(name=%q) succeeded, want the 400-class error", name)
		}
		if err := svc.Delete(context.Background(), "docs", name); err == nil {
			t.Errorf("service Delete(name=%q) succeeded, want the 400-class error", name)
		}
	}
	// the refusals above must not have stored anything
	for k := range fs.objects {
		t.Errorf("a broken alphabet stored %q — nothing may be stored", k)
	}

	// And the boundaries of the alphabet are accepted, not refused.
	for _, space := range []string{"docs", "ab-", "a0b", strings.Repeat("a", 32)} {
		if w := doPut(t, r, space, "K-1.a", "text/plain", "", "x"); w.Code != http.StatusOK {
			t.Errorf("PUT /v1/%q/... status = %d, want 200", space, w.Code)
		}
	}
	for _, name := range []string{"a", "A", "_a", "a.b-c_", strings.Repeat("a", 255)} {
		if w := doPut(t, r, "docs", name, "text/plain", "", "x"); w.Code != http.StatusOK {
			t.Errorf("PUT /v1/docs/%q status = %d, want 200", name, w.Code)
		}
	}
}

// TestGetMissing: no such file is 404 (CONTRACT.md section 6).
func TestGetMissing(t *testing.T) {
	fs := newFakeObjectStore()
	svc := &DefaultService{Store: fs, MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

	w := doGet(t, r, "docs", "no.such")
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET status = %d, want 404", w.Code)
	}
}

// TestTooLarge: a body over the limit is refused with 413 before
// anything is stored (CONTRACT.md section 5). The limit is a variable;
// here it is set small.
func TestTooLarge(t *testing.T) {
	fs := newFakeObjectStore()
	svc := &DefaultService{Store: fs, MaxSize: 4}
	r := newTestRouter(svc)

	if w := doPut(t, r, "docs", "K-1.a", "text/plain", "", "12345"); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("PUT of 5 bytes over a limit of 4 status = %d, want 413", w.Code)
	}
	for k := range fs.objects {
		t.Errorf("a too-large body stored %q — nothing may be stored", k)
	}

	// At the limit exactly the PUT goes through.
	if w := doPut(t, r, "docs", "K-1.a", "text/plain", "", "1234"); w.Code != http.StatusOK {
		t.Fatalf("PUT of 4 bytes at a limit of 4 status = %d, want 200", w.Code)
	}
}

// TestDelete pins the contract of DELETE /v1/{space}/{name}: 204 when
// the file was there, 404 when it was not; afterwards a GET of the name
// is 404 and the listing no longer carries the name (CONTRACT.md
// section 3).
func TestDelete(t *testing.T) {
	fs := newFakeObjectStore()
	svc := &DefaultService{Store: fs, MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

	if w := doPut(t, r, "docs", "K-1.a", "text/plain", "", "hello"); w.Code != http.StatusOK {
		t.Fatalf("PUT K-1.a status = %d, want 200", w.Code)
	}
	if w := doPut(t, r, "docs", "K-2.b", "text/plain", "", "world"); w.Code != http.StatusOK {
		t.Fatalf("PUT K-2.b status = %d, want 200", w.Code)
	}

	// A file that was there: 204, and 204 has no body.
	w := doDelete(t, r, "docs", "K-1.a")
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE of a present file status = %d, want 204", w.Code)
	}
	if w.Body.String() != "" {
		t.Errorf("DELETE body = %q, want empty (a 204 has no body)", w.Body.String())
	}

	// A second DELETE of the same name: 404 — it is gone now.
	if w := doDelete(t, r, "docs", "K-1.a"); w.Code != http.StatusNotFound {
		t.Fatalf("second DELETE status = %d, want 404", w.Code)
	}
	// A name that was never there: 404 as well.
	if w := doDelete(t, r, "docs", "no.such"); w.Code != http.StatusNotFound {
		t.Fatalf("DELETE of a never-present name status = %d, want 404", w.Code)
	}

	// Afterwards: GET of the name is 404, and the listing no longer has
	// it — neither in the whole space nor under its prefix.
	if w := doGet(t, r, "docs", "K-1.a"); w.Code != http.StatusNotFound {
		t.Errorf("GET after delete status = %d, want 404", w.Code)
	}
	if got, want := namesOf(t, decodeListing(t, doList(t, r, "docs", ""))), []string{"K-2.b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("listing after delete names = %v, want %v", got, want)
	}
	if got, want := namesOf(t, decodeListing(t, doList(t, r, "docs", "K-1."))), []string{}; !reflect.DeepEqual(got, want) {
		t.Errorf("prefix K-1. after delete names = %v, want %v", got, want)
	}

	// The deletion of K-1.a leaves the other file untouched.
	if g := doGet(t, r, "docs", "K-2.b"); g.Code != http.StatusOK || g.Body.String() != "world" {
		t.Errorf("K-2.b after the deletion: status = %d, body = %q, want 200 world", g.Code, g.Body.String())
	}
}

// TestStoreUnavailable: MinIO unreachable is 503 on every file route
// (CONTRACT.md section 6).
func TestStoreUnavailable(t *testing.T) {
	fs := newFakeObjectStore()
	fs.putErr = ErrUnavailable
	fs.getErr = ErrUnavailable
	fs.listErr = ErrUnavailable
	fs.delErr = ErrUnavailable
	svc := &DefaultService{Store: fs, MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

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

// TestCreatedByNull: without X-Principal-Id the metadata's created_by is
// null (CONTRACT.md section 3).
func TestCreatedByNull(t *testing.T) {
	fs := newFakeObjectStore()
	svc := &DefaultService{Store: fs, MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

	w := doPut(t, r, "docs", "K-1.a", "text/plain", "", "x")
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", w.Code)
	}
	var md map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &md); err != nil {
		t.Fatalf("decode PUT metadata %q: %v", w.Body.String(), err)
	}
	if got, ok := md["created_by"]; !ok || string(got) != "null" {
		t.Errorf("metadata created_by = %q, want null", got)
	}
}

// TestStatusFor pins the one error-to-status mapping (CONTRACT.md
// section 6), independent of the router.
func TestStatusFor(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{ErrBadIdent, http.StatusBadRequest},
		{ErrBadBody, http.StatusBadRequest},
		{ErrNotFound, http.StatusNotFound},
		{ErrTooLarge, http.StatusRequestEntityTooLarge},
		{ErrUnavailable, http.StatusServiceUnavailable},
		{errors.New("something else"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		if code, _ := statusFor(c.err); code != c.want {
			t.Errorf("statusFor(%v) = %d, want %d", c.err, code, c.want)
		}
	}
}

// doList asks GET /v1/{space} with the given prefix query (empty for
// all files).
func doList(t *testing.T, r *mux.Router, space, prefix string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/v1/" + space
	if prefix != "" {
		target += "?prefix=" + prefix
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// listingEntry is one entry of the listing answer; the field set and
// names come from CONTRACT.md section 3.
type listingEntry struct {
	Name        *string `json:"name"`
	Sha256      *string `json:"sha256"`
	Size        *int64  `json:"size"`
	ContentType *string `json:"content_type"`
	CreatedBy   *string `json:"created_by"`
	UpdatedAt   *string `json:"updated_at"`
}

func decodeListing(t *testing.T, w *httptest.ResponseRecorder) []listingEntry {
	t.Helper()
	var entries []listingEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode listing %q: %v", w.Body.String(), err)
	}
	return entries
}

func namesOf(t *testing.T, entries []listingEntry) []string {
	t.Helper()
	out := make([]string, len(entries))
	for i, e := range entries {
		if e.Name == nil {
			t.Fatalf("entry %d has no name: %+v", i, e)
		}
		out[i] = *e.Name
	}
	return out
}

// TestList pins the contract answer of GET /v1/{space}?prefix=<p>: the
// space's files whose name starts with the prefix (all of them without
// it), ordered by name, each entry the metadata a PUT stored; an empty
// or unknown space is []; a broken alphabet is 400; a broken store is 503
// (CONTRACT.md sections 3 and 6).
func TestList(t *testing.T) {
	fs := newFakeObjectStore()
	svc := &DefaultService{Store: fs, MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

	// Write in an order that is not name order, so the answer's order is
	// the listing's, not the store's.
	puts := []struct{ name, body, ct, principal string }{
		{"K-1.z", "hello", "text/plain", "alice"},
		{"K-1.a", `{"x":1}`, "application/json", ""},
		{"K-2.b", "world", "text/plain", "bob"},
		{"K-1.m", "m", "text/plain", "alice"},
	}
	for _, p := range puts {
		if w := doPut(t, r, "docs", p.name, p.ct, p.principal, p.body); w.Code != http.StatusOK {
			t.Fatalf("PUT /v1/docs/%s status = %d, want 200: %s", p.name, w.Code, w.Body.String())
		}
	}
	// A file in another space must never leak into docs's listing.
	if w := doPut(t, r, "archive", "K-1.a", "text/plain", "eve", "other"); w.Code != http.StatusOK {
		t.Fatalf("PUT /v1/archive/K-1.a status = %d, want 200", w.Code)
	}

	// Without a prefix the whole space comes back, ordered by name.
	w := doList(t, r, "docs", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200: %s", w.Code, w.Body.String())
	}
	entries := decodeListing(t, w)
	if got, want := namesOf(t, entries), []string{"K-1.a", "K-1.m", "K-1.z", "K-2.b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v, want %v (ordered by name)", got, want)
	}

	// With a prefix, only names that start with it.
	if got, want := namesOf(t, decodeListing(t, doList(t, r, "docs", "K-1."))), []string{"K-1.a", "K-1.m", "K-1.z"}; !reflect.DeepEqual(got, want) {
		t.Errorf("prefix K-1. names = %v, want %v", got, want)
	}
	// A prefix that no name starts with is an empty list, not an error.
	if got, want := namesOf(t, decodeListing(t, doList(t, r, "docs", "K-9."))), []string{}; !reflect.DeepEqual(got, want) {
		t.Errorf("prefix K-9. names = %v, want []", got)
	}

	// Each entry carries what the PUT stored: the name without the space,
	// the sha256 of the bytes, their size, the kept Content-Type, the
	// X-Principal-Id or null, and a RFC3339 updated_at.
	byName := map[string]listingEntry{}
	for _, e := range entries {
		byName[*e.Name] = e
	}
	wantFor := func(e listingEntry, body, ct, principal string) {
		if e.Sha256 == nil || *e.Sha256 != sha256hex([]byte(body)) {
			t.Errorf("%s sha256 = %v, want the sha256 of %q (%s)", *e.Name, e.Sha256, body, sha256hex([]byte(body)))
		}
		if e.Size == nil || *e.Size != int64(len(body)) {
			t.Errorf("%s size = %v, want %d", *e.Name, e.Size, len(body))
		}
		if e.ContentType == nil || *e.ContentType != ct {
			t.Errorf("%s content_type = %v, want %s (what the PUT kept)", *e.Name, e.ContentType, ct)
		}
		if principal == "" {
			if e.CreatedBy != nil {
				t.Errorf("%s created_by = %v, want null (no X-Principal-Id)", *e.Name, *e.CreatedBy)
			}
		} else if e.CreatedBy == nil || *e.CreatedBy != principal {
			t.Errorf("%s created_by = %v, want %s (the X-Principal-Id the PUT got)", *e.Name, e.CreatedBy, principal)
		}
		if e.UpdatedAt == nil {
			t.Errorf("%s updated_at missing", *e.Name)
		} else if _, err := time.Parse(time.RFC3339, *e.UpdatedAt); err != nil {
			t.Errorf("%s updated_at %q is not RFC3339: %v", *e.Name, *e.UpdatedAt, err)
		}
	}
	wantFor(byName["K-1.a"], `{"x":1}`, "application/json", "")
	wantFor(byName["K-1.z"], "hello", "text/plain", "alice")
	wantFor(byName["K-1.m"], "m", "text/plain", "alice")
	wantFor(byName["K-2.b"], "world", "text/plain", "bob")

	// An empty or unknown space is [].
	if got, want := namesOf(t, decodeListing(t, doList(t, r, "archive", "K-1."))), []string{"K-1.a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("archive prefix K-1. names = %v, want %v", got, want)
	}
	if got, want := namesOf(t, decodeListing(t, doList(t, r, "zzz", ""))), []string{}; !reflect.DeepEqual(got, want) {
		t.Errorf("unknown space names = %v, want []", got)
	}
	if w := doList(t, r, "zzz", ""); w.Code != http.StatusOK {
		t.Errorf("unknown space status = %d, want 200 (an empty space is [])", w.Code)
	}

	// A space that breaks the alphabet is 400 (CONTRACT.md section 6).
	if w := doList(t, r, "Bad", ""); w.Code != http.StatusBadRequest {
		t.Errorf("list /v1/Bad status = %d, want 400", w.Code)
	}

	// The store unreachable is 503.
	fs.listErr = ErrUnavailable
	if w := doList(t, r, "docs", ""); w.Code != http.StatusServiceUnavailable {
		t.Errorf("list with a broken store status = %d, want 503", w.Code)
	}
	fs.listErr = nil

	// The service refuses a bad space before it reaches the store.
	if _, err := svc.List(context.Background(), "Bad", ""); err != ErrBadIdent {
		t.Errorf("service List(bad space) error = %v, want the 400-class ErrBadIdent", err)
	}
}

func keys(fs *fakeObjectStore) []string {
	var out []string
	for k := range fs.objects {
		out = append(out, k)
	}
	return out
}
