package http

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type plainResponseWriter struct {
	header     http.Header
	statusCode int
	body       []byte
}

func newPlainResponseWriter() *plainResponseWriter {
	return &plainResponseWriter{header: make(http.Header)}
}

func (w *plainResponseWriter) Header() http.Header { return w.header }
func (w *plainResponseWriter) Write(b []byte) (int, error) {
	w.body = append(w.body, b...)
	return len(b), nil
}
func (w *plainResponseWriter) WriteHeader(code int) { w.statusCode = code }

type flushingResponseWriter struct {
	*plainResponseWriter
	flushed bool
}

func (w *flushingResponseWriter) Flush() { w.flushed = true }

func TestLoggingMiddlewarePreservesFlusher(t *testing.T) {
	t.Run("underlying writer supports Flusher: wrapped writer must too", func(t *testing.T) {
		underlying := &flushingResponseWriter{plainResponseWriter: newPlainResponseWriter()}

		var gotFlusher bool
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("wrapped writer does not implement http.Flusher, but the underlying writer does")
			}
			gotFlusher = true
			f.Flush()
		})

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		LoggingMiddleware(next).ServeHTTP(underlying, req)

		if !gotFlusher {
			t.Fatal("handler never got a chance to assert Flusher")
		}
		if !underlying.flushed {
			t.Fatal("Flush() on the wrapper did not reach the underlying writer")
		}
	})

	t.Run("underlying writer does not support Flusher: wrapped writer must not claim to", func(t *testing.T) {
		underlying := newPlainResponseWriter()

		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := w.(http.Flusher); ok {
				t.Fatal("wrapped writer wrongly claims to implement http.Flusher")
			}
		})

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		LoggingMiddleware(next).ServeHTTP(underlying, req)
	})
}

func TestLoggingMiddlewareUnwrap(t *testing.T) {
	underlying := newPlainResponseWriter()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			t.Fatal("wrapped writer does not implement Unwrap() http.ResponseWriter")
		}
		if u.Unwrap() != http.ResponseWriter(underlying) {
			t.Fatal("Unwrap() did not return the underlying writer")
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	LoggingMiddleware(next).ServeHTTP(underlying, req)
}

type hijackingResponseWriter struct {
	*plainResponseWriter
	hijacked bool
}

func (w *hijackingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacked = true
	return nil, nil, nil
}

func TestLoggingMiddlewarePreservesHijacker(t *testing.T) {
	t.Run("underlying writer supports Hijacker: wrapped writer must too", func(t *testing.T) {
		underlying := &hijackingResponseWriter{plainResponseWriter: newPlainResponseWriter()}

		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("wrapped writer does not implement http.Hijacker, but the underlying writer does")
			}
			if _, _, err := h.Hijack(); err != nil {
				t.Fatalf("Hijack() returned an error: %v", err)
			}
		})

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		LoggingMiddleware(next).ServeHTTP(underlying, req)

		if !underlying.hijacked {
			t.Fatal("Hijack() on the wrapper did not reach the underlying writer")
		}
	})

	t.Run("underlying writer does not support Hijacker: wrapped writer must not claim to", func(t *testing.T) {
		underlying := newPlainResponseWriter()

		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := w.(http.Hijacker); ok {
				t.Fatal("wrapped writer wrongly claims to implement http.Hijacker")
			}
		})

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		LoggingMiddleware(next).ServeHTTP(underlying, req)
	})
}

type readerFromResponseWriter struct {
	*plainResponseWriter
	readFromCalled bool
}

func (w *readerFromResponseWriter) ReadFrom(src io.Reader) (int64, error) {
	w.readFromCalled = true
	b, err := io.ReadAll(src)
	w.body = append(w.body, b...)
	return int64(len(b)), err
}

func TestLoggingMiddlewarePreservesReaderFrom(t *testing.T) {
	t.Run("underlying writer supports io.ReaderFrom: wrapped writer must too", func(t *testing.T) {
		underlying := &readerFromResponseWriter{plainResponseWriter: newPlainResponseWriter()}

		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rf, ok := w.(io.ReaderFrom)
			if !ok {
				t.Fatal("wrapped writer does not implement io.ReaderFrom, but the underlying writer does")
			}
			if _, err := rf.ReadFrom(bytes.NewBufferString("payload")); err != nil {
				t.Fatalf("ReadFrom() returned an error: %v", err)
			}
		})

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		LoggingMiddleware(next).ServeHTTP(underlying, req)

		if !underlying.readFromCalled {
			t.Fatal("ReadFrom() on the wrapper did not reach the underlying writer")
		}
		if string(underlying.body) != "payload" {
			t.Fatalf("unexpected body: %q", underlying.body)
		}
	})

	t.Run("underlying writer does not support io.ReaderFrom: wrapped writer must not claim to", func(t *testing.T) {
		underlying := newPlainResponseWriter()

		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := w.(io.ReaderFrom); ok {
				t.Fatal("wrapped writer wrongly claims to implement io.ReaderFrom")
			}
		})

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		LoggingMiddleware(next).ServeHTTP(underlying, req)
	})
}

func doPostBody(t *testing.T, body string) (logged, gotBody string) {
	t.Helper()

	var err error
	logged = captureLog(func() {
		req := httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader(body))
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, readErr := io.ReadAll(r.Body)
			err = readErr
			gotBody = string(b)
		})
		LoggingMiddleware(next).ServeHTTP(newPlainResponseWriter(), req)
	})
	if err != nil {
		t.Fatalf("handler failed to read the body: %v", err)
	}
	return logged, gotBody
}

func TestLoggingMiddlewareBodyNotLoggedByDefault(t *testing.T) {
	t.Setenv("HTTP_LOG_BODIES", "")
	const marker = "USER-DATA-MARKER-XYZ"

	logged, got := doPostBody(t, marker)

	if got != marker {
		t.Errorf("handler received %q, want %q", got, marker)
	}
	if strings.Contains(logged, marker) {
		t.Errorf("log contains the request body by default:\n%s", logged)
	}
}

func TestLoggingMiddlewareBodyLoggedWhenFlagSet(t *testing.T) {
	t.Setenv("HTTP_LOG_BODIES", "true")
	const marker = "USER-DATA-MARKER-XYZ"

	logged, got := doPostBody(t, marker)

	if got != marker {
		t.Errorf("reading a body in order to log it must not consume it; handler got %q, want %q", got, marker)
	}
	if !strings.Contains(logged, marker) {
		t.Errorf("flag is set but the log does not contain the request body:\n%s", logged)
	}
}
