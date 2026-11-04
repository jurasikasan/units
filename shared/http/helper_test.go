package http

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
)

func captureLog(fn func()) string {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	fn()

	return buf.String()
}

func TestRespondWithJSONRedactsTheLogButNotTheResponse(t *testing.T) {
	cases := []struct {
		name           string
		payload        any
		logMustHave    string
		logMustNotHave string
	}{
		{
			name:           "a field the redactor should mask",
			payload:        map[string]any{"username": "someone", "token": "not-a-real-token-1111"},
			logMustHave:    redacted,
			logMustNotHave: "not-a-real-token-1111",
		},
		{
			name:        "nothing sensitive passes through unchanged",
			payload:     map[string]any{"destination_code": "359", "calls_count": 6},
			logMustHave: "359",
		},
		{
			name:        "empty payload",
			payload:     map[string]any{},
			logMustHave: "{}",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()

			logged := captureLog(func() {
				RespondWithJSON(w, 200, c.payload)
			})

			if !strings.Contains(logged, "Returning") {
				t.Fatalf("expected a log line, got %q", logged)
			}
			if c.logMustHave != "" && !strings.Contains(logged, c.logMustHave) {
				t.Errorf("expected %q in log %q", c.logMustHave, logged)
			}
			if c.logMustNotHave != "" && strings.Contains(logged, c.logMustNotHave) {
				t.Errorf("leaked %q in log %q", c.logMustNotHave, logged)
			}

			wantBody, err := json.Marshal(c.payload)
			if err != nil {
				t.Fatalf("failed to marshal expected body: %v", err)
			}
			if gotBody := w.Body.String(); gotBody != string(wantBody) {
				t.Errorf("response body was redacted or altered: got %q, want %q", gotBody, string(wantBody))
			}
			if w.Code != 200 {
				t.Errorf("status code = %d, want 200", w.Code)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
		})
	}
}
