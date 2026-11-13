package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The version field is what a deployment check reads to name a service
// running stale code. An unstamped build must still say something:
// an empty version is indistinguishable from an image that has no field at all.
func TestHealthReportsVersion(t *testing.T) {
	rec := httptest.NewRecorder()
	healthHandler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
	}
	if body["version"] != "dev" {
		t.Errorf("version = %q, want the unstamped default \"dev\"", body["version"])
	}
}

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

// /ready is what the compose healthcheck asks: a database it cannot
// ping has to come back as non-2xx, or busybox wget exits 0 and compose keeps
// calling the container healthy with Postgres stopped.
func TestReadyFollowsPostgres(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"database answers", nil, http.StatusOK},
		{"database down", errors.New("connection refused"), http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			readyHandler(fakePinger{tc.err})(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
			if rec.Code != tc.want {
				t.Errorf("status %d, want %d (body %q)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}
