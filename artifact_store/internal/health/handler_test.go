package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHealth pins the contract answer for GET /health: 200 and
// {"status":"ok"} (CONTRACT.md section 3).
func TestHealth(t *testing.T) {
	h := NewHandler()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	h.Health(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", w.Code)
	}

	var body Status
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	if body.Status != "ok" {
		t.Fatalf("body = %q, want {\"status\":\"ok\"}", w.Body.String())
	}
}
