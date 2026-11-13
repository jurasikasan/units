package store

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestMinioStoreLive runs against a real Postgres and a real MinIO and
// only when STORE_TEST_DATABASE_URL and STORE_TEST_MINIO_ENDPOINT name
// them; otherwise it skips. It pins the contract answer on a live store:
// the bytes come back identical, the Content-Type is kept. (A PUT
// records its name in Postgres, so the test needs both.)
func TestMinioStoreLive(t *testing.T) {
	dsn := os.Getenv("STORE_TEST_DATABASE_URL")
	endpoint := os.Getenv("STORE_TEST_MINIO_ENDPOINT")
	if dsn == "" || endpoint == "" {
		t.Skip("STORE_TEST_DATABASE_URL/STORE_TEST_MINIO_ENDPOINT not set; no live Postgres and MinIO to test against")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("MINIO_ENDPOINT", endpoint)

	ctx := context.Background()
	store := NewMinioStore(ctx)

	// A fresh key each run: writing under the same name replaces, and we
	// must not be reading somebody else's file.
	key := "live/" + strconv.FormatInt(time.Now().UnixNano(), 10) + ".test"
	body := []byte("live minio bytes")
	contentType := "text/plain"

	meta := ObjectMeta{Sha256: sha256hex(body), ContentType: contentType, UpdatedAt: time.Now()}
	if err := store.Put(ctx, key, body, meta); err != nil {
		t.Fatalf("Put: %v", err)
	}
	data, ct, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(data, body) {
		t.Errorf("Get returned %q, want the bytes that were put", data)
	}
	if ct != contentType {
		t.Errorf("Content-Type = %q, want %q", ct, contentType)
	}

	if _, _, err := store.Get(ctx, "live/never.written"); err == nil || err != ErrNotFound {
		t.Errorf("Get of a missing object = %v, want the 404-class error ErrNotFound", err)
	}

	// The sector's dedup on the live store: two names with equal bytes
	// share one object; deleting one keeps it; deleting both lets the
	// sweep remove it; the sweep never touches what a name points to.
	keyB := key + "2"
	if err := store.Put(ctx, keyB, body, meta); err != nil {
		t.Fatalf("Put of the second name: %v", err)
	}
	sha := sha256hex(body)
	if _, err := store.bytes.Get(ctx, sha); err != nil {
		t.Errorf("the bytes under %s are missing in the live byte store", sha)
	}

	keep := []byte("live bytes that stay")
	keyKeep := key + "3"
	metaKeep := ObjectMeta{Sha256: sha256hex(keep), ContentType: contentType, UpdatedAt: time.Now()}
	if err := store.Put(ctx, keyKeep, keep, metaKeep); err != nil {
		t.Fatalf("Put of the kept file: %v", err)
	}

	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete of the first name: %v", err)
	}
	if _, err := store.bytes.Get(ctx, sha); err != nil {
		t.Errorf("the bytes under %s are gone, but %s still points to them", sha, keyB)
	}
	if data, _, err := store.Get(ctx, keyB); err != nil || !bytes.Equal(data, body) {
		t.Fatalf("Get of the second name after deleting the first: err = %v, bytes = %q", err, data)
	}

	if err := store.Delete(ctx, keyB); err != nil {
		t.Fatalf("Delete of the second name: %v", err)
	}
	if err := store.Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, err := store.bytes.Get(ctx, sha); err == nil || err != ErrNotFound {
		t.Errorf("the bytes under %s survived the sweep; no name points to them", sha)
	}
	if _, err := store.bytes.Get(ctx, sha256hex(keep)); err != nil {
		t.Errorf("the bytes under the kept file were swept, but %s points to them", keyKeep)
	}
}
