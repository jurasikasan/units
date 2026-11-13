package store

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// The read by content of CONTRACT.md section 3: GET /v1/_sha256/{hex}
// answers the bytes while at least one name points to them, under the
// Content-Type of the most recently written such name, with ETag
// "<sha256>" and Cache-Control "public, max-age=31536000, immutable";
// 404 once no name does, whether or not the sweep has run; 400 when hex
// is not 64 lowercase hex characters; 503 when a dependency is down.

const immutableCache = "public, max-age=31536000, immutable"

func doGetSha(t *testing.T, r *mux.Router, hex string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/_sha256/"+hex, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func newShaFixture() (*fakeNames, *fakeBytes, *MinioStore, *mux.Router) {
	names, b := newFakeNames(), newFakeBytes()
	m := newDedupStore(names, b)
	return names, b, m, newTestRouter(&DefaultService{Store: m, MaxSize: DefaultMaxSize})
}

func TestShaReadNamed(t *testing.T) {
	_, _, _, r := newShaFixture()

	body := "bytes read by their sha256"
	if w := doPut(t, r, "docs", "K-1.a", "text/plain", "", body); w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", w.Code, w.Body.String())
	}
	sha := sha256hex([]byte(body))

	w := doGetSha(t, r, sha)
	if w.Code != http.StatusOK {
		t.Fatalf("GET by sha status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != body {
		t.Errorf("body = %q, want %q", w.Body.String(), body)
	}
	if got := w.Header().Get("Content-Type"); got != "text/plain" {
		t.Errorf("Content-Type = %q, want text/plain", got)
	}
	if got := w.Header().Get("ETag"); got != `"`+sha+`"` {
		t.Errorf("ETag = %q, want %q", got, `"`+sha+`"`)
	}
	if got := w.Header().Get("Cache-Control"); got != immutableCache {
		t.Errorf("Cache-Control = %q, want %q", got, immutableCache)
	}
}

func TestShaReadGoneWithLastName(t *testing.T) {
	_, b, m, r := newShaFixture()

	body := "bytes under two names"
	sha := sha256hex([]byte(body))
	for _, name := range []string{"K-1.a", "K-2.b"} {
		if w := doPut(t, r, "docs", name, "text/plain", "", body); w.Code != http.StatusOK {
			t.Fatalf("PUT %s status = %d", name, w.Code)
		}
	}

	if w := doDelete(t, r, "docs", "K-1.a"); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE K-1.a status = %d", w.Code)
	}
	if w := doGetSha(t, r, sha); w.Code != http.StatusOK || w.Body.String() != body {
		t.Errorf("one name still holds the bytes: status = %d, body = %q, want 200 %q", w.Code, w.Body.String(), body)
	}

	if w := doDelete(t, r, "docs", "K-2.b"); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE K-2.b status = %d", w.Code)
	}
	if !b.has(sha) {
		t.Fatalf("the bytes are gone before the sweep; the case below would prove nothing")
	}
	if w := doGetSha(t, r, sha); w.Code != http.StatusNotFound {
		t.Errorf("no name, bytes awaiting the sweep: status = %d, want 404", w.Code)
	}

	if err := m.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if b.has(sha) {
		t.Fatalf("the sweep left the unnamed bytes")
	}
	if w := doGetSha(t, r, sha); w.Code != http.StatusNotFound {
		t.Errorf("after the sweep: status = %d, want 404", w.Code)
	}
}

func TestShaReadBadHex(t *testing.T) {
	_, _, _, r := newShaFixture()

	body := "some bytes"
	if w := doPut(t, r, "docs", "K-1.a", "text/plain", "", body); w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d", w.Code)
	}
	sha := sha256hex([]byte(body))

	for label, hex := range map[string]string{
		"uppercase": strings.ToUpper(sha),
		"short":     sha[:63],
		"long":      sha + "0",
		"non-hex":   "g" + sha[1:],
	} {
		if w := doGetSha(t, r, hex); w.Code != http.StatusBadRequest {
			t.Errorf("%s %q: status = %d, want 400", label, hex, w.Code)
		}
	}

	unknown := sha256hex([]byte("never written"))
	if w := doGetSha(t, r, unknown); w.Code != http.StatusNotFound {
		t.Errorf("a well-formed sha no name points to: status = %d, want 404", w.Code)
	}
}

func TestShaReadLatestContentType(t *testing.T) {
	_, _, _, r := newShaFixture()

	body := `{"same":"bytes"}`
	sha := sha256hex([]byte(body))
	put := func(name, contentType string) {
		t.Helper()
		// Postgres keeps microseconds; a millisecond apart is two
		// distinct writes to any clock the store reads.
		time.Sleep(2 * time.Millisecond)
		if w := doPut(t, r, "docs", name, contentType, "", body); w.Code != http.StatusOK {
			t.Fatalf("PUT %s status = %d", name, w.Code)
		}
	}

	put("K-1.a", "text/plain")
	put("K-2.b", "application/json")
	if got := doGetSha(t, r, sha).Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("after K-2.b: Content-Type = %q, want application/json", got)
	}

	put("K-1.a", "text/csv")
	if got := doGetSha(t, r, sha).Header().Get("Content-Type"); got != "text/csv" {
		t.Errorf("after K-1.a again: Content-Type = %q, want text/csv", got)
	}
}

func TestShaReadUnavailable(t *testing.T) {
	names, b, _, r := newShaFixture()

	body := "x"
	if w := doPut(t, r, "docs", "K-1.a", "text/plain", "", body); w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d", w.Code)
	}
	sha := sha256hex([]byte(body))

	names.shaErr = ErrUnavailable
	if w := doGetSha(t, r, sha); w.Code != http.StatusServiceUnavailable {
		t.Errorf("Postgres down: status = %d, want 503", w.Code)
	}
	names.shaErr = nil

	b.getErr = ErrUnavailable
	if w := doGetSha(t, r, sha); w.Code != http.StatusServiceUnavailable {
		t.Errorf("MinIO down: status = %d, want 503", w.Code)
	}
}
