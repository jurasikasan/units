package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPut(t *testing.T) {
	var answer func([]byte) string
	var gotPath, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotPath, gotType = r.Method+" "+r.URL.Path, r.Header.Get("Content-Type")
		if strings.Contains(r.URL.Path, "down") {
			http.Error(w, "minio unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"sha256": answer(b)})
	}))
	defer srv.Close()
	s := Store{URL: srv.URL + "/", Space: "sight"}
	body := []byte("\xff\xd8\xff a frame")
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])

	answer = func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	ref, err := s.Put(context.Background(), "sg-1.obs-2.change.jpg", "image/jpeg", body)
	if err != nil {
		t.Fatal(err)
	}
	if ref != (Ref{SHA256: want, Space: "sight", Name: "sg-1.obs-2.change.jpg", ContentType: "image/jpeg"}) ||
		gotPath != "PUT /v1/sight/sg-1.obs-2.change.jpg" || gotType != "image/jpeg" {
		t.Errorf("put: %+v as %s %s", ref, gotPath, gotType)
	}

	answer = func([]byte) string { return strings.Repeat("0", 64) }
	if _, err := s.Put(context.Background(), "x.jpg", "image/jpeg", body); err == nil {
		t.Error("a store that kept other bytes gave a reference")
	}
	if _, err := s.Put(context.Background(), "down.jpg", "image/jpeg", body); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("store down: %v", err)
	}
}
