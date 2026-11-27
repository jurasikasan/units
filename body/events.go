package body

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
)

// keepAlive is how long a watcher's connection may stay silent: a comment
// line goes out when the stream had nothing new for that long, so proxies
// keep the connection and a watcher can tell a quiet body from a lost one.
const keepAlive = 15 * time.Second

// entryID is a Redis stream entry id: milliseconds, and a sequence.
var entryID = regexp.MustCompile(`^\d+(-\d+)?$`)

// serveEvents is GET /bodies/{name}/events: the body's stream as server-sent
// events, for a head outside the inside network. Each entry is one event:
// its SSE id the Redis entry id, its data the entry as a CloudEvent in the
// JSON format. From after Last-Event-ID (a reconnect) or ?from=<id>
// (0: from the stream's start), else from now.
func (b *Body) serveEvents(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("name") != b.Cfg.Name {
		http.Error(w, "no such body here", http.StatusNotFound)
		return
	}
	from := r.Header.Get("Last-Event-ID")
	if from == "" {
		from = r.URL.Query().Get("from")
	}
	if from != "" && !entryID.MatchString(from) {
		http.Error(w, "bad_argument: from / Last-Event-ID is not a stream entry id", http.StatusBadRequest)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	if from == "" {
		// From now: the id of the last entry at the moment of asking, so that
		// nothing written between two reads is missed.
		last, err := b.Redis.XRevRangeN(ctx, b.Cfg.Stream, "+", "-", 1).Result()
		if err != nil {
			http.Error(w, "stream: "+err.Error(), http.StatusBadGateway)
			return
		}
		from = "0"
		if len(last) == 1 {
			from = last[0].ID
		}
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, ": ", b.Cfg.Stream, "\n\n")
	fl.Flush()

	for ctx.Err() == nil {
		res, err := b.Redis.XRead(ctx, &redis.XReadArgs{
			Streams: []string{b.Cfg.Stream, from}, Count: 64, Block: keepAlive,
		}).Result()
		if errors.Is(err, redis.Nil) {
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			fl.Flush()
			continue
		}
		if err != nil {
			if ctx.Err() == nil {
				b.Counts.Errors.Add(1)
				b.Log.Warn("events: read failed, the watcher is let go", "err", err.Error())
			}
			return
		}
		for _, st := range res {
			for _, msg := range st.Messages {
				raw, err := json.Marshal(CloudEvent(msg.Values))
				if err != nil {
					continue
				}
				if _, err := fmt.Fprintf(w, "id: %s\ndata: %s\n\n", msg.ID, raw); err != nil {
					return
				}
				from = msg.ID
			}
		}
		fl.Flush()
	}
}

// CloudEvent is a stream entry (binary mode, contracts/events.md) in
// CloudEvents' JSON format: the ce_ prefix dropped from each attribute, and
// data as JSON when it is JSON, else as the string it is.
func CloudEvent(fields map[string]any) map[string]any {
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		switch {
		case k == "data":
			s, _ := v.(string)
			if json.Valid([]byte(s)) {
				out["data"] = json.RawMessage(s)
			} else {
				out["data"] = s
			}
		case strings.HasPrefix(k, "ce_"):
			out[strings.TrimPrefix(k, "ce_")] = v
		}
	}
	return out
}
