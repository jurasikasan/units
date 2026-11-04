package http

import (
	"bufio"
	"bytes"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

type flusherWriter struct {
	*responseWriter
}

func (rw *flusherWriter) Flush() {
	rw.ResponseWriter.(http.Flusher).Flush()
}

type hijackerWriter struct {
	*responseWriter
}

func (rw *hijackerWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return rw.ResponseWriter.(http.Hijacker).Hijack()
}

type readerFromWriter struct {
	*responseWriter
}

func (rw *readerFromWriter) ReadFrom(src io.Reader) (int64, error) {
	return rw.ResponseWriter.(io.ReaderFrom).ReadFrom(src)
}

type flusherHijackerWriter struct {
	*responseWriter
}

func (rw *flusherHijackerWriter) Flush() {
	rw.ResponseWriter.(http.Flusher).Flush()
}

func (rw *flusherHijackerWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return rw.ResponseWriter.(http.Hijacker).Hijack()
}

type flusherReaderFromWriter struct {
	*responseWriter
}

func (rw *flusherReaderFromWriter) Flush() {
	rw.ResponseWriter.(http.Flusher).Flush()
}

func (rw *flusherReaderFromWriter) ReadFrom(src io.Reader) (int64, error) {
	return rw.ResponseWriter.(io.ReaderFrom).ReadFrom(src)
}

type hijackerReaderFromWriter struct {
	*responseWriter
}

func (rw *hijackerReaderFromWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return rw.ResponseWriter.(http.Hijacker).Hijack()
}

func (rw *hijackerReaderFromWriter) ReadFrom(src io.Reader) (int64, error) {
	return rw.ResponseWriter.(io.ReaderFrom).ReadFrom(src)
}

type flusherHijackerReaderFromWriter struct {
	*responseWriter
}

func (rw *flusherHijackerReaderFromWriter) Flush() {
	rw.ResponseWriter.(http.Flusher).Flush()
}

func (rw *flusherHijackerReaderFromWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return rw.ResponseWriter.(http.Hijacker).Hijack()
}

func (rw *flusherHijackerReaderFromWriter) ReadFrom(src io.Reader) (int64, error) {
	return rw.ResponseWriter.(io.ReaderFrom).ReadFrom(src)
}

func wrapResponseWriter(w http.ResponseWriter) (http.ResponseWriter, *responseWriter) {
	base := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
	_, isFlusher := w.(http.Flusher)
	_, isHijacker := w.(http.Hijacker)
	_, isReaderFrom := w.(io.ReaderFrom)

	switch {
	case isFlusher && isHijacker && isReaderFrom:
		return &flusherHijackerReaderFromWriter{base}, base
	case isFlusher && isHijacker:
		return &flusherHijackerWriter{base}, base
	case isFlusher && isReaderFrom:
		return &flusherReaderFromWriter{base}, base
	case isHijacker && isReaderFrom:
		return &hijackerReaderFromWriter{base}, base
	case isFlusher:
		return &flusherWriter{base}, base
	case isHijacker:
		return &hijackerWriter{base}, base
	case isReaderFrom:
		return &readerFromWriter{base}, base
	default:
		return base, base
	}
}

func logBodies() bool {
	return os.Getenv("HTTP_LOG_BODIES") != ""
}

func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		if (r.Method == "POST" || r.Method == "PUT" || r.Method == "PATCH") && logBodies() {
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				log.Printf("Error reading request body for logging: %v", err)
				next.ServeHTTP(w, r)
				return
			}
			_ = r.Body.Close()

			log.Printf("--> %s %s %s Body: %s", r.RemoteAddr, r.Method, r.URL.Path, redactBodyForLog(bodyBytes))
			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		} else {
			log.Printf("--> %s %s %s", r.RemoteAddr, r.Method, r.URL.Path)
		}

		wrapped, base := wrapResponseWriter(w)
		next.ServeHTTP(wrapped, r)

		log.Printf("<-- %s %s %s %d (%v)", r.RemoteAddr, r.Method, r.URL.Path, base.statusCode, time.Since(start))
	})
}
