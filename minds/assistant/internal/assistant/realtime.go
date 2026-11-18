package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Realtime is OpenAI's Realtime API, served by the mind at GET /v1/realtime
// (a WebSocket) and relayed to the endpoint's (LiteLLM's) /v1/realtime: a
// call, sound both ways, live. As with Audio, the mind is the door: the
// client never holds the endpoint's key, the model is the mind's, and every
// event is logged, its direction, time, type and size. Nothing is changed
// on the way: each message goes on as it came, text or binary, and the
// endpoint's events, errors and close come back as they are, so any client
// of OpenAI's Realtime schema is a client of this. The mind takes no part
// in the session yet (UNIT-40): no tools of its own, no thread.
type Realtime struct {
	base  string // the endpoint's /v1/realtime, ws:// or wss://
	key   string
	model string
	// opened is how long the endpoint may take to answer the handshake.
	opened time.Duration
}

// MaxRealtimeMessage is the largest message taken either way: a second of
// 24 kHz PCM16 is 64 KB in base64, and an endpoint may send several at once.
const MaxRealtimeMessage = 16 << 20

// NewRealtime relays calls to an OpenAI-shaped endpoint (base without /v1,
// http or https); key is sent as a Bearer token when set. model is the one
// asked for; empty leaves the route unserved.
func NewRealtime(base, key, model string) *Realtime {
	ws := strings.TrimRight(base, "/") + "/v1/realtime"
	switch {
	case strings.HasPrefix(ws, "https://"):
		ws = "wss://" + strings.TrimPrefix(ws, "https://")
	case strings.HasPrefix(ws, "http://"):
		ws = "ws://" + strings.TrimPrefix(ws, "http://")
	}
	return &Realtime{base: ws, key: key, model: model, opened: 30 * time.Second}
}

// Routes puts GET /v1/realtime on mux when a model is set.
func (rt *Realtime) Routes(mux *http.ServeMux) {
	if rt.model != "" {
		mux.HandleFunc("GET /v1/realtime", rt.serve)
	}
}

// A close the mind makes itself: the endpoint did not answer, or a side
// went away without a close of its own.
const (
	closeUnreachable = websocket.StatusTryAgainLater
	closeGone        = websocket.StatusGoingAway
)

func (rt *Realtime) serve(w http.ResponseWriter, r *http.Request) {
	principal := r.Header.Get("X-Principal-Id")
	if principal == "" {
		openAIError(w, http.StatusUnauthorized, "unauthorized", "no principal")
		return
	}
	client, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept has answered the request already
	}
	client.SetReadLimit(MaxRealtimeMessage)
	log := slog.With("call", "realtime", "principal", principal, "model", rt.model)
	start := time.Now()

	u := rt.base + "?" + url.Values{"model": {rt.model}}.Encode()
	header := http.Header{}
	if rt.key != "" {
		header.Set("Authorization", "Bearer "+rt.key)
	}
	dctx, cancel := context.WithTimeout(r.Context(), rt.opened)
	model, resp, err := websocket.Dial(dctx, u, &websocket.DialOptions{HTTPHeader: header})
	cancel()
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		log.Info("realtime call failed: the endpoint did not answer", "err", err, "status", status)
		// The endpoint never spoke, so the error is the mind's, in the
		// schema's own shape, before the close.
		ev, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{
			"type": "server_error", "code": CodeModelUnreachable, "message": "The model's endpoint did not answer: " + err.Error(),
		}})
		_ = client.Write(r.Context(), websocket.MessageText, ev)
		_ = client.Close(closeUnreachable, "the model's endpoint did not answer")
		return
	}
	model.SetReadLimit(MaxRealtimeMessage)
	log.Info("realtime call opened", "after", time.Since(start))

	ctx := context.Background() // the call ends by a close, not by the request
	var once sync.Once
	ended := make(chan struct{})
	var why struct {
		from   string
		status websocket.StatusCode
		reason string
	}
	end := func(from string, err error) {
		once.Do(func() {
			why.from, why.status, why.reason = from, websocket.CloseStatus(err), closeReason(err)
			close(ended)
		})
	}
	counts := [2]map[string]int{{}, {}}
	relay := func(dir int, from, to *websocket.Conn, name, other string) {
		for {
			kind, data, err := from.Read(ctx)
			if err != nil {
				end(name, err)
				return
			}
			typ := eventType(kind, data)
			counts[dir][typ]++
			log.Info("realtime event", "dir", []string{"in", "out"}[dir], "t", time.Since(start).Milliseconds(), "type", typ, "bytes", len(data))
			if err := to.Write(ctx, kind, data); err != nil {
				end(other, err)
				return
			}
		}
	}
	done := make(chan struct{}, 2)
	go func() { relay(0, client, model, "client", "model"); done <- struct{}{} }()
	go func() { relay(1, model, client, "model", "client"); done <- struct{}{} }()
	<-ended

	// The side that closed is closed on the other with its code and reason;
	// one that went away without a close leaves the other a going-away.
	status, reason := why.status, why.reason
	if status == websocket.StatusNoStatusRcvd {
		status = websocket.StatusNormalClosure
	}
	if status == -1 {
		status, reason = closeGone, "the "+why.from+" went away"
	}
	other := model
	if why.from == "model" {
		other = client
	}
	_ = other.Close(status, reason)
	_ = client.CloseNow()
	_ = model.CloseNow()
	<-done
	<-done
	log.Info("realtime call closed", "by", why.from, "code", int(why.status), "reason", why.reason,
		"after", time.Since(start), "events_in", counts[0], "events_out", counts[1])
}

// eventType is a message's "type", read without decoding the rest (an
// audio event is mostly its base64); binary is not the schema's, said so.
func eventType(kind websocket.MessageType, data []byte) string {
	if kind != websocket.MessageText {
		return "<binary>"
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return "<not an object>"
	}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			break
		}
		if k == "type" {
			var s string
			if dec.Decode(&s) == nil {
				return s
			}
			break
		}
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			break
		}
	}
	return "<no type>"
}

func closeReason(err error) string {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Reason
	}
	return ""
}
