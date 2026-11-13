package store

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The expectations in these tests come from CONTRACT.md section 5:
// RETENTION="docs=90d,archive=never" — a file not written for that long is
// removed; a space not listed never expires; a malformed value stops the
// start with a message that names it.

// seedFile lays a file down directly in the fakes, with the given last
// write, the way the store would have recorded it — so a test can age a
// file past its rule's time.
func seedFile(t *testing.T, names *fakeNames, b *fakeBytes, space, name, body string, at time.Time) {
	t.Helper()
	sha := sha256hex([]byte(body))
	if err := names.Upsert(context.Background(), NamedFile{
		Space: space, Name: name, Sha256: sha,
		Size: int64(len(body)), ContentType: "text/plain", UpdatedAt: at,
	}); err != nil {
		t.Fatalf("seed %s/%s: %v", space, name, err)
	}
	b.seed(sha, []byte(body), at)
}

// TestParseRetention pins the read of the contract's own example,
// RETENTION="docs=90d,archive=never": docs keeps a 90-day rule, and "never"
// is not a rule — a space with it never expires. An empty value is no
// rules, so nothing expires.
func TestParseRetention(t *testing.T) {
	rules, err := ParseRetention("docs=90d,archive=never")
	if err != nil {
		t.Fatalf("ParseRetention(docs=90d,archive=never): %v", err)
	}
	if got, want := rules["docs"], 90*24*time.Hour; got != want {
		t.Errorf("rule for docs = %v, want %v (90d)", got, want)
	}
	if _, ok := rules["archive"]; ok {
		t.Errorf("archive is set to never, so it never expires and must carry no rule: %v", rules)
	}
	if got, want := len(rules), 1; got != want {
		t.Errorf("rules = %v, want only docs with a rule", rules)
	}

	// Without RETENTION there are no rules: nothing expires.
	rules, err = ParseRetention("")
	if err != nil {
		t.Fatalf("ParseRetention(\"\"): %v", err)
	}
	if rules != nil {
		t.Errorf("rules for an empty RETENTION = %v, want none", rules)
	}
}

// TestParseRetentionRefusesMalformed: a malformed item refuses the whole
// value — the start stops — and the refusal names the bad item
// (CONTRACT.md section 5: "a malformed value stops the start with a
// message that names it").
func TestParseRetentionRefusesMalformed(t *testing.T) {
	// named is the piece of the value the refusal must name: for a lone
	// bad item the item itself, for a mixed list the bad item.
	for _, tc := range []struct{ in, named string }{
		{"docs", "docs"},         // no rule at all
		{"docs=", "docs="},       // no rule
		{"=90d", "=90d"},         // no space
		{"docs=90", "docs=90"},   // a number with no "d"
		{"docs=d", "docs=d"},     // a "d" with no number
		{"docs=-1d", "docs=-1d"}, // not a whole number of days
		{"docs=9.5d", "docs=9.5d"},
		{"docs=90w", "docs=90w"},        // not days
		{"docs=90d,archive", "archive"}, // one bad item in an otherwise fine list
	} {
		rules, err := ParseRetention(tc.in)
		if err == nil {
			t.Errorf("ParseRetention(%q) succeeded with rules %v, want a refusal", tc.in, rules)
			continue
		}
		if rules != nil {
			t.Errorf("ParseRetention(%q) returned rules %v with an error; the start must stop, not run half", tc.in, rules)
		}
		if !strings.Contains(err.Error(), tc.named) {
			t.Errorf("the refusal of %q is %q; it must name the value", tc.in, err)
		}
	}
}

// TestExpiryRemovesOnlyWhatTheRuleSays: with docs=90d, the file not
// written for 90d in docs is removed, the fresh one is kept, and the old
// files in spaces with no rule (archive, zzz) are kept — a space not listed
// never expires.
func TestExpiryRemovesOnlyWhatTheRuleSays(t *testing.T) {
	names, b := newFakeNames(), newFakeBytes()
	m := newDedupStore(names, b)
	retention, err := ParseRetention("docs=90d")
	if err != nil {
		t.Fatalf("ParseRetention(docs=90d): %v", err)
	}
	m.Retention = retention
	svc := &DefaultService{Store: m, MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

	now := time.Now()
	agedOut := now.Add(-91 * 24 * time.Hour) // past docs' 90-day rule
	seedFile(t, names, b, "docs", "old.a", "aged out", agedOut)
	seedFile(t, names, b, "docs", "fresh.a", "still warm", now)
	seedFile(t, names, b, "archive", "old.b", "no rule here", agedOut)
	seedFile(t, names, b, "zzz", "old.c", "no rule here", agedOut)

	if err := m.Expire(context.Background()); err != nil {
		t.Fatalf("Expire: %v", err)
	}

	// The file past docs' rule is gone.
	if w := doGet(t, r, "docs", "old.a"); w.Code != http.StatusNotFound {
		t.Errorf("GET docs/old.a after expiry status = %d, want 404 (not written for 90d)", w.Code)
	}
	if got := namesOf(t, decodeListing(t, doList(t, r, "docs", ""))); !reflect.DeepEqual(got, []string{"fresh.a"}) {
		t.Errorf("listing of docs after expiry = %v, want [fresh.a]", got)
	}

	// The fresh file in docs is kept.
	if w := doGet(t, r, "docs", "fresh.a"); w.Code != http.StatusOK || w.Body.String() != "still warm" {
		t.Errorf("GET docs/fresh.a: status = %d, body = %q, want 200 %q", w.Code, w.Body.String(), "still warm")
	}
	// Old files in spaces with no rule are kept: a space not listed
	// never expires.
	if w := doGet(t, r, "archive", "old.b"); w.Code != http.StatusOK || w.Body.String() != "no rule here" {
		t.Errorf("GET archive/old.b: status = %d, body = %q, want 200 %q (archive has no rule)", w.Code, w.Body.String(), "no rule here")
	}
	if w := doGet(t, r, "zzz", "old.c"); w.Code != http.StatusOK || w.Body.String() != "no rule here" {
		t.Errorf("GET zzz/old.c: status = %d, body = %q, want 200 %q (zzz is not listed)", w.Code, w.Body.String(), "no rule here")
	}
}

// TestExpiryNeverIsNotListed: archive=never says the same as not listing
// archive — its files, however old, are kept.
func TestExpiryNeverIsNotListed(t *testing.T) {
	names, b := newFakeNames(), newFakeBytes()
	m := newDedupStore(names, b)
	retention, err := ParseRetention("docs=90d,archive=never")
	if err != nil {
		t.Fatalf("ParseRetention(docs=90d,archive=never): %v", err)
	}
	m.Retention = retention
	svc := &DefaultService{Store: m, MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

	agedOut := time.Now().Add(-91 * 24 * time.Hour)
	seedFile(t, names, b, "docs", "old.a", "past its rule", agedOut)
	seedFile(t, names, b, "archive", "old.b", "never expires", agedOut)

	if err := m.Expire(context.Background()); err != nil {
		t.Fatalf("Expire: %v", err)
	}

	if w := doGet(t, r, "docs", "old.a"); w.Code != http.StatusNotFound {
		t.Errorf("GET docs/old.a after expiry status = %d, want 404", w.Code)
	}
	if w := doGet(t, r, "archive", "old.b"); w.Code != http.StatusOK || w.Body.String() != "never expires" {
		t.Errorf("GET archive/old.b: status = %d, body = %q, want 200 %q (archive=never)", w.Code, w.Body.String(), "never expires")
	}
}

// TestExpiryFreesBytesOnlyWhenUnreferenced: expiry moves the name only;
// the sweep then takes the bytes no name points to — and only those.
func TestExpiryFreesBytesOnlyWhenUnreferenced(t *testing.T) {
	names, b := newFakeNames(), newFakeBytes()
	m := newDedupStore(names, b)
	retention, err := ParseRetention("docs=90d")
	if err != nil {
		t.Fatalf("ParseRetention(docs=90d): %v", err)
	}
	m.Retention = retention
	svc := &DefaultService{Store: m, MaxSize: DefaultMaxSize}
	r := newTestRouter(svc)

	// The same bytes under an expired name in docs and a kept name in
	// archive (archive has no rule).
	shared := "the same bytes, one expired and one not"
	sha := sha256hex([]byte(shared))
	agedOut := time.Now().Add(-91 * 24 * time.Hour)
	seedFile(t, names, b, "docs", "old.a", shared, agedOut)
	seedFile(t, names, b, "archive", "keep.a", shared, agedOut)

	if err := m.Expire(context.Background()); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	// The name is gone, and the sweep took the bytes no name points to.
	if w := doGet(t, r, "docs", "old.a"); w.Code != http.StatusNotFound {
		t.Errorf("GET docs/old.a after expiry status = %d, want 404", w.Code)
	}
	if err := m.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if !b.has(sha) {
		t.Fatalf("the bytes under %s are gone, but archive/keep.a still points to them", sha)
	}

	// Once no name points to the bytes any more, the sweep takes them.
	if w := doDelete(t, r, "archive", "keep.a"); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE archive/keep.a status = %d, want 204", w.Code)
	}
	if err := m.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep after the last name is gone: %v", err)
	}
	if b.has(sha) {
		t.Errorf("the bytes under %s survived the sweep; no name points to them", sha)
	}
}

// TestExpiryRunsOnTheSweepPeriod: the expiry runs in the background on
// the store's period — the same variable a test can set — and its names
// are swept, so a file past its rule's time disappears and the bytes it
// freed are gone, with no one calling for it.
func TestExpiryRunsOnTheSweepPeriod(t *testing.T) {
	names, b := newFakeNames(), newFakeBytes()
	m := newDedupStore(names, b)
	m.SweepPeriod = 20 * time.Millisecond
	retention, err := ParseRetention("docs=90d")
	if err != nil {
		t.Fatalf("ParseRetention(docs=90d): %v", err)
	}
	m.Retention = retention

	agedOut := time.Now().Add(-91 * 24 * time.Hour)
	body := "aged out"
	sha := sha256hex([]byte(body))
	seedFile(t, names, b, "docs", "old.a", body, agedOut)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.StartSweep(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for {
		_, ok, _ := names.Get(ctx, "docs", "old.a")
		if !ok || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok, _ := names.Get(ctx, "docs", "old.a"); ok {
		t.Errorf("the expiry did not run within its period; docs/old.a is still there")
	}
	// The sweep in the same pass took the bytes no name points to.
	deadline = time.Now().Add(5 * time.Second)
	for b.has(sha) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b.has(sha) {
		t.Errorf("the bytes under %s survived the background sweep; no name points to them", sha)
	}
}
