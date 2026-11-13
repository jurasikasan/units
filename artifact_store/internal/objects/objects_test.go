package objects

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// newTestMinioClient points a real minio.Client at an httptest server so
// BucketExists/MakeBucket exercise the actual request/response parsing
// without needing a live MinIO instance.
func newTestMinioClient(t *testing.T, handler http.HandlerFunc) *minio.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	endpoint := server.Listener.Addr().String()
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4("test", "test", ""),
		Secure: false,
		// Fixing the region avoids an extra GetBucketLocation round trip
		// that the client would otherwise make before the first request.
		Region: "us-east-1",
	})
	if err != nil {
		t.Fatalf("failed to create test minio client: %v", err)
	}
	return client
}

func TestEnsureBucketExists_AlreadyExists(t *testing.T) {
	var headCount, putCount int32
	client := newTestMinioClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			atomic.AddInt32(&headCount, 1)
			w.WriteHeader(http.StatusOK)
		case http.MethodPut:
			atomic.AddInt32(&putCount, 1)
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})

	if err := ensureBucketExists(context.Background(), client, "my-bucket"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if atomic.LoadInt32(&headCount) != 1 {
		t.Fatalf("expected 1 HEAD request, got %d", headCount)
	}
	if atomic.LoadInt32(&putCount) != 0 {
		t.Fatalf("expected no MakeBucket call for an existing bucket, got %d", putCount)
	}
}

func TestEnsureBucketExists_CreatesWhenMissing(t *testing.T) {
	var headCount, putCount int32
	client := newTestMinioClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			atomic.AddInt32(&headCount, 1)
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPut:
			atomic.AddInt32(&putCount, 1)
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})

	if err := ensureBucketExists(context.Background(), client, "fresh-bucket"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if atomic.LoadInt32(&putCount) != 1 {
		t.Fatalf("expected MakeBucket to be called once, got %d", putCount)
	}
}

// TestEnsureBucketExists_RaceIsTreatedAsSuccess simulates two concurrent
// first-uploads: the initial HEAD sees the bucket missing, MakeBucket fails
// because a concurrent caller already created it, and the follow-up
// BucketExists check confirms it now exists.
func TestEnsureBucketExists_RaceIsTreatedAsSuccess(t *testing.T) {
	var headCount, putCount int32
	client := newTestMinioClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			n := atomic.AddInt32(&headCount, 1)
			if n == 1 {
				w.WriteHeader(http.StatusNotFound)
			} else {
				w.WriteHeader(http.StatusOK)
			}
		case http.MethodPut:
			atomic.AddInt32(&putCount, 1)
			w.WriteHeader(http.StatusConflict)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})

	if err := ensureBucketExists(context.Background(), client, "raced-bucket"); err != nil {
		t.Fatalf("expected race to be treated as success, got error: %v", err)
	}
	if atomic.LoadInt32(&headCount) != 2 {
		t.Fatalf("expected a follow-up BucketExists check after MakeBucket failure, got %d HEAD calls", headCount)
	}
}

func TestEnsureBucketExists_MakeBucketFailureIsReturned(t *testing.T) {
	client := newTestMinioClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPut:
			w.WriteHeader(http.StatusBadRequest)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})

	if err := ensureBucketExists(context.Background(), client, "broken-bucket"); err == nil {
		t.Fatal("expected an error when the bucket is missing and MakeBucket fails for real")
	}
}
