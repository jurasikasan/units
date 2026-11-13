package objects

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestDownload_NilClientFails(t *testing.T) {
	data, err := Download(context.Background(), nil, "artifact-store", "abc")
	if err == nil {
		t.Fatal("expected an error for a nil client, got nil")
	}
	if data != nil {
		t.Fatalf("expected a nil slice on failure, got %q", data)
	}
}

func TestDownload_MissingObjectIsErrNotFound(t *testing.T) {
	client := newTestMinioClient(t, func(w http.ResponseWriter, r *http.Request) {
		// A plain 404 body makes minio-go surface a NoSuchKey error response.
		w.WriteHeader(http.StatusNotFound)
	})

	_, err := Download(context.Background(), client, "artifact-store", "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDownload_MinioErrorIsNotErrNotFound(t *testing.T) {
	client := newTestMinioClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	_, err := Download(context.Background(), client, "artifact-store", "abc")
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("500 must not be reported as not-found: %v", err)
	}
}

func TestDownload_SuccessReturnsContent(t *testing.T) {
	content := "RIFF..WAVE"
	client := newTestMinioClient(t, func(w http.ResponseWriter, r *http.Request) {
		// minio-go parses Last-Modified on the first read of the object body.
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(content)); err != nil {
			t.Errorf("write: %v", err)
		}
	})

	data, err := Download(context.Background(), client, "artifact-store", "abc")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if string(data) != content {
		t.Fatalf("expected %q, got %q", content, data)
	}
}
