package http

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDoRequestDoesNotLogOnFailure(t *testing.T) {

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	var (
		resp   *http.Response
		reqErr error
	)
	logged := captureLog(func() {
		h := NewHTTPClientWrapper()
		resp, reqErr = h.DoRequest("GET", "http://"+addr, nil, nil)
	})
	if reqErr == nil {
		_ = resp.Body.Close()
		t.Fatal("DoRequest to an unreachable address returned nil error")
	}
	if logged != "" {
		t.Fatalf("DoRequest wrote to the log: %q", logged)
	}
}

func TestHandleResponseDoesNotLogOnError(t *testing.T) {
	var err error
	logged := captureLog(func() {
		h := NewHTTPClientWrapper()
		resp := &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(bytes.NewBufferString("upstream blew up")),
		}
		err = h.HandleResponse(resp)
	})
	if err == nil {
		t.Fatal("HandleResponse on 500 returned nil error")
	}
	httpErr, ok := err.(*HttpError)
	if !ok {
		t.Fatalf("error type = %T, want *HttpError", err)
	}
	if httpErr.Code != http.StatusInternalServerError {
		t.Errorf("Code = %d, want %d", httpErr.Code, http.StatusInternalServerError)
	}
	if httpErr.Message != "upstream blew up" {
		t.Errorf("Message = %q, want %q", httpErr.Message, "upstream blew up")
	}
	if logged != "" {
		t.Fatalf("HandleResponse wrote to the log: %q", logged)
	}
}

func TestSharedClientHasTimeout(t *testing.T) {
	if got := GetHTTPClient().Timeout; got <= 0 {
		t.Fatalf("shared client Timeout = %v, want > 0", got)
	}
	if got := NewHTTPClientWrapper().GetHTTPClient().Timeout; got <= 0 {
		t.Fatalf("NewHTTPClientWrapper client Timeout = %v, want > 0", got)
	}
}

type testPayload struct {
	Name string `json:"name"`
}

func TestHandleResponseWithDecode_DecodesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name":"alice"}`))
	}))
	defer srv.Close()

	h := NewHTTPClientWrapper()
	resp, err := h.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result testPayload
	if err := h.HandleResponseWithDecode(resp, &result); err != nil {
		t.Fatalf("HandleResponseWithDecode returned error: %v", err)
	}
	if result.Name != "alice" {
		t.Fatalf("result.Name = %q, want %q", result.Name, "alice")
	}
}

func TestHandleResponse_DrainsSuccessBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ignored":"payload"}`))
	}))
	defer srv.Close()

	h := NewHTTPClientWrapper()
	resp, err := h.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := h.HandleResponse(resp); err != nil {
		t.Fatalf("HandleResponse returned error: %v", err)
	}

	remaining, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll after HandleResponse failed: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("body not drained: got %d leftover bytes", len(remaining))
	}
}
