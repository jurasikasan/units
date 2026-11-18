package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeRealtime is an endpoint's /v1/realtime: it keeps what the handshake
// carried, says session.created, then hands the test its side of the call.
type fakeRealtime struct {
	auth, model string
	conns       chan *websocket.Conn
}

func newFakeRealtime(t *testing.T) (*fakeRealtime, *httptest.Server) {
	f := &fakeRealtime{conns: make(chan *websocket.Conn, 1)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/realtime" {
			http.NotFound(w, r)
			return
		}
		f.auth, f.model = r.Header.Get("Authorization"), r.URL.Query().Get("model")
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_ = c.Write(context.Background(), websocket.MessageText, []byte(`{"type":"session.created","session":{"id":"s1"}}`))
		f.conns <- c
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

// mindRealtime serves the mind's route, a door in front setting who asks.
func mindRealtime(t *testing.T, endpoint, principal string) string {
	mux := http.NewServeMux()
	NewRealtime(endpoint, "sk-mind", "realtime-model").Routes(mux)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principal != "" {
			r.Header.Set("X-Principal-Id", principal)
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func dialMind(t *testing.T, base string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/v1/realtime?model=the-clients-own", nil)
	if err != nil {
		t.Fatalf("dial the mind: %v", err)
	}
	c.SetReadLimit(MaxRealtimeMessage)
	return c
}

func readWS(t *testing.T, c *websocket.Conn) (websocket.MessageType, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	kind, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return kind, string(data)
}

// Each message goes on as it came, both ways; the model and the key are
// the mind's; every event is logged with its direction and type.
func TestRealtimeRelaysUnchanged(t *testing.T) {
	log := logs(t)
	f, endpoint := newFakeRealtime(t)
	client := dialMind(t, mindRealtime(t, endpoint.URL, "alice"))
	defer func() { _ = client.CloseNow() }()
	model := <-f.conns
	defer func() { _ = model.CloseNow() }()

	if f.model != "realtime-model" || f.auth != "Bearer sk-mind" {
		t.Fatalf("the endpoint was asked for %q with %q; want the mind's model and key", f.model, f.auth)
	}
	if _, got := readWS(t, client); got != `{"type":"session.created","session":{"id":"s1"}}` {
		t.Fatalf("the client got %s", got)
	}

	ctx := context.Background()
	// Spacing, key order and unknown fields are the client's; so is binary.
	sent := `{ "audio":"AAAA", "type":"input_audio_buffer.append", "x-ours":1 }`
	if err := client.Write(ctx, websocket.MessageText, []byte(sent)); err != nil {
		t.Fatal(err)
	}
	if kind, got := readWS(t, model); kind != websocket.MessageText || got != sent {
		t.Fatalf("the endpoint got %v %s; want %s as it was", kind, got, sent)
	}
	if err := client.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if kind, got := readWS(t, model); kind != websocket.MessageBinary || got != "\x01\x02\x03" {
		t.Fatalf("the endpoint got %v %q", kind, got)
	}
	big := `{"type":"response.output_audio.delta","delta":"` + strings.Repeat("A", 200<<10) + `"}`
	if err := model.Write(ctx, websocket.MessageText, []byte(big)); err != nil {
		t.Fatal(err)
	}
	if _, got := readWS(t, client); got != big {
		t.Fatalf("a large delta came back changed (%d bytes, want %d)", len(got), len(big))
	}

	_ = client.Close(websocket.StatusNormalClosure, "hung up")
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _, err := model.Read(ctx2)
	if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("the endpoint saw %v; want the client's close", err)
	}

	waitFor(t, log, "realtime call closed")
	out := log.String()
	for _, want := range []string{
		`dir=out t=`, `type=session.created`,
		`dir=in t=`, `type=input_audio_buffer.append`, `type=<binary>`,
		`type=response.output_audio.delta`, `by=client`, `principal=alice`, `reason="hung up"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the log has no %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "AAAA") {
		t.Errorf("sound got into the log:\n%s", out)
	}
}

// The endpoint's close reaches the client with its code and reason.
func TestRealtimeEndpointClose(t *testing.T) {
	f, endpoint := newFakeRealtime(t)
	client := dialMind(t, mindRealtime(t, endpoint.URL, "alice"))
	defer func() { _ = client.CloseNow() }()
	model := <-f.conns
	readWS(t, client)
	_ = model.Close(websocket.StatusInternalError, "upstream websocket closed with code 1011")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := client.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusInternalError || !strings.Contains(err.Error(), "1011") {
		t.Fatalf("the client saw %v", err)
	}
}

// An endpoint that does not answer: an error event in the schema's shape,
// then a close saying to try later.
func TestRealtimeEndpointUnreachable(t *testing.T) {
	client := dialMind(t, mindRealtime(t, "http://127.0.0.1:1", "alice"))
	defer func() { _ = client.CloseNow() }()
	_, got := readWS(t, client)
	var ev struct {
		Type  string `json:"type"`
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal([]byte(got), &ev); err != nil || ev.Type != "error" || ev.Error.Code != CodeModelUnreachable {
		t.Fatalf("the client got %s", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := client.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusTryAgainLater {
		t.Fatalf("closed with %v", err)
	}
}

// Nobody behind the door: no call, a plain 401.
func TestRealtimeNeedsPrincipal(t *testing.T) {
	_, endpoint := newFakeRealtime(t)
	resp, err := http.Get(mindRealtime(t, endpoint.URL, "") + "/v1/realtime")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// No model, no route.
func TestRealtimeUnservedWithoutModel(t *testing.T) {
	mux := http.NewServeMux()
	NewRealtime("http://x", "", "").Routes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/realtime", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
}

func waitFor(t *testing.T, b *syncBuffer, s string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if strings.Contains(b.String(), s) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the log never said %q:\n%s", s, b.String())
}

func TestEventType(t *testing.T) {
	for in, want := range map[string]string{
		`{"type":"a"}`:                   "a",
		`{"audio":"xx","type":"b"}`:      "b",
		`{"x":{"type":"no"},"type":"c"}`: "c",
		`[1]`:                            "<not an object>",
		`{"x":1}`:                        "<no type>",
	} {
		if got := eventType(websocket.MessageText, []byte(in)); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}
