package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSendPostsPlainText(t *testing.T) {
	var gotPath, gotChat, gotText, gotParseMode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		gotPath = r.URL.Path
		gotChat = r.Form.Get("chat_id")
		gotText = r.Form.Get("text")
		gotParseMode = r.Form.Get("parse_mode")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New("tok123", "555", srv.URL)
	require.NoError(t, c.Send(context.Background(), "hello_world *not markdown*"))

	assert.Equal(t, "/bottok123/sendMessage", gotPath)
	assert.Equal(t, "555", gotChat)
	assert.Equal(t, "hello_world *not markdown*", gotText)

	assert.Empty(t, gotParseMode, "parse_mode must never be set")
}

func TestSendSurfacesTelegramRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"chat not found"}`))
	}))
	defer srv.Close()

	err := New("tok", "0", srv.URL).Send(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chat not found")
}

func TestSendSurfacesRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Too Many Requests","parameters":{"retry_after":7}}`))
	}))
	defer srv.Close()

	err := New("tok", "1", srv.URL).Send(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "retry after 7s")
}

func TestSendHandlesUnparseableBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>gateway error</html>"))
	}))
	defer srv.Close()

	err := New("tok", "1", srv.URL).Send(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "502")
}

func TestSendDoesNotLeakTokenOnTransportError(t *testing.T) {

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()

	err := New("SECRET-TOKEN-VALUE", "1", base).Send(context.Background(), "x")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SECRET-TOKEN-VALUE")
}
