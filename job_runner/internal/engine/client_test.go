package engine

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The compose daemon listens on a unix socket only.
func TestAUnixSocketReachesTheDaemon(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/images/bash:5.2/json", r.URL.Path)
		_, _ = w.Write([]byte(`{}`))
	}))
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	ok, err := New("unix://"+sock).HasImage(context.Background(), "bash:5.2")
	require.NoError(t, err)
	require.True(t, ok)
}

// The daemon answers a pull with 200 and puts a refusal inside the progress
// stream (docs.docker.com, ImageCreate): reading only the status would call
// a failed pull a success.
func TestAPullFailureInsideTheStreamIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"Pulling from library/nope"}` + "\n" +
			`{"errorDetail":{"message":"manifest unknown"},"error":"manifest unknown"}` + "\n"))
	}))
	defer srv.Close()
	err := NewHTTP(srv.URL, srv.Client()).Pull(context.Background(), "nope:1")
	require.ErrorContains(t, err, "manifest unknown")
}

// Without a TTY the daemon frames stdout and stderr; the frames join in order.
func TestDemuxJoinsTheFramesInOrder(t *testing.T) {
	frame := func(stream byte, s string) []byte {
		n := len(s)
		return append([]byte{stream, 0, 0, 0, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}, s...)
	}
	raw := append(append(frame(1, "out 1\n"), frame(2, "err\n")...), frame(1, "out 2\n")...)
	out, err := Demux(raw)
	require.NoError(t, err)
	require.Equal(t, "out 1\nerr\nout 2\n", string(out))

	_, err = Demux(raw[:len(raw)-2])
	require.Error(t, err)
}
