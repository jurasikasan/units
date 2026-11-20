package world

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
)

// ThreadsStream is where threads announces each append (threads/CONTRACT.md,
// section 3): a wake-up naming the entry, never its content.
const ThreadsStream = "threads_events"

// Stream is one principal's view of the world as it changes: every entry
// appended to a thread one of its avatars is in, read back from `threads`
// on its wake-up, and the pieces of replies still being written there,
// relayed from the streams their writers publish them on. Server-sent
// events:
//
//   - `ready` once, first: the position the stream starts after, as its id;
//   - `entry` per appended entry, threads' entry as data, its id the
//     wake-up's id on threads_events: a reconnect with Last-Event-ID (or
//     ?since=) resumes after it;
//   - `writing` per piece of a reply being written, no id: what is not
//     journaled is not resumed;
//   - `reset` when the position asked for is no longer on the stream: the
//     reader reloads what it shows and goes on from here.
type Stream struct {
	svc     *Service
	rdb     *redis.Client
	writing []string
	// Block is how long one read waits before a keep-alive is sent.
	Block time.Duration
	// Refresh is how often the principal's avatars are read again.
	Refresh time.Duration
}

func NewStream(svc *Service, rdb *redis.Client, writing []string) *Stream {
	return &Stream{svc: svc, rdb: rdb, writing: writing, Block: 15 * time.Second, Refresh: 30 * time.Second}
}

var positionPattern = regexp.MustCompile(`^\d+-\d+$`)

// Serve streams to the principal until the request ends or something it
// reads from fails; the reader reconnects from its last id.
func (st *Stream) Serve(w http.ResponseWriter, r *http.Request, principal string) {
	ctx := r.Context()
	rc := http.NewResponseController(w)
	since := r.Header.Get("Last-Event-ID")
	if since == "" {
		since = r.URL.Query().Get("since")
	}

	last, err := st.last(ctx, ThreadsStream)
	if err != nil {
		log.Printf("reins: stream for %q: %v", principal, err)
		http.Error(w, "the bus did not answer", http.StatusBadGateway)
		return
	}
	pos, reset := last, false
	if since != "" {
		pos, reset, err = st.resume(ctx, since, last)
		if err != nil {
			log.Printf("reins: stream for %q: %v", principal, err)
			http.Error(w, "the bus did not answer", http.StatusBadGateway)
			return
		}
	}
	// The writing streams start where they are now: what was written before
	// the reader came is in the journal or was never finished.
	ids := []string{pos}
	for _, s := range st.writing {
		l, err := st.last(ctx, s)
		if err != nil {
			log.Printf("reins: stream for %q: %v", principal, err)
			http.Error(w, "the bus did not answer", http.StatusBadGateway)
			return
		}
		ids = append(ids, l)
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // nginx: pass each event on as it comes
	w.WriteHeader(http.StatusOK)
	send := func(event, id string, data []byte) error {
		var b strings.Builder
		if id != "" {
			b.WriteString("id: " + id + "\n")
		}
		b.WriteString("event: " + event + "\n")
		for _, line := range strings.Split(string(data), "\n") {
			b.WriteString("data: " + line + "\n")
		}
		b.WriteString("\n")
		if _, err := w.Write([]byte(b.String())); err != nil {
			return err
		}
		return rc.Flush()
	}
	if reset {
		if send("reset", "", []byte(`{"reason":"the position asked for is no longer on the stream"}`)) != nil {
			return
		}
	}
	if send("ready", pos, []byte(fmt.Sprintf(`{"position":%q}`, pos))) != nil {
		return
	}

	sees := &seer{svc: st.svc, principal: principal, refresh: st.Refresh}
	streams := append([]string{ThreadsStream}, st.writing...)
	for {
		res, err := st.rdb.XRead(ctx, &redis.XReadArgs{
			Streams: append(slices.Clone(streams), ids...),
			Count:   100,
			Block:   st.Block,
		}).Result()
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, redis.Nil) {
			if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil || rc.Flush() != nil {
				return
			}
			continue
		}
		if err != nil {
			log.Printf("reins: stream for %q: read the bus: %v", principal, err)
			return
		}
		for _, xs := range res {
			i := slices.Index(streams, xs.Stream)
			for _, m := range xs.Messages {
				var err error
				if i == 0 {
					err = st.entry(ctx, sees, m, send)
				} else {
					err = st.piece(ctx, sees, xs.Stream, m, send)
				}
				if err != nil {
					// Not sent, and not passed: the reader comes back from
					// its last id and this one is tried again.
					log.Printf("reins: stream for %q: %s %s: %v", principal, xs.Stream, m.ID, err)
					return
				}
				ids[i] = m.ID
			}
		}
	}
}

// wakeup is threads.entry.appended's data.
type wakeup struct {
	Thread uuid.UUID `json:"thread"`
	Seq    int64     `json:"seq"`
	Kind   string    `json:"kind"`
}

// entry sends the entry a wake-up names, read back from threads, when the
// principal sees its thread.
func (st *Stream) entry(ctx context.Context, sees *seer, m redis.XMessage, send func(string, string, []byte) error) error {
	if s, _ := m.Values["ce_type"].(string); s != "threads.entry.appended" {
		return nil
	}
	var wk wakeup
	data, _ := m.Values["data"].(string)
	if err := json.Unmarshal([]byte(data), &wk); err != nil || wk.Thread == uuid.Nil || wk.Seq < 1 {
		log.Printf("reins: a wake-up %s that names no entry: %q", m.ID, data)
		return nil
	}
	if wk.Kind == "join" || wk.Kind == "leave" {
		sees.forget(wk.Thread)
	}
	ok, err := sees.thread(ctx, wk.Thread)
	if err != nil || !ok {
		return err
	}
	page, err := st.svc.journal.Entries(ctx, wk.Thread, url.Values{
		"after": {strconv.FormatInt(wk.Seq-1, 10)}, "limit": {"1"},
	})
	if errors.Is(err, ErrNoThread) {
		return nil // deleted since
	}
	if err != nil {
		return err
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(page, &entries); err != nil {
		return fmt.Errorf("decode entries: %w", err)
	}
	if len(entries) == 0 {
		return nil
	}
	return send("entry", m.ID, entries[0])
}

// piece relays one ephemeral event about a thread the principal sees: its
// data with its type beside, as `writing`.
func (st *Stream) piece(ctx context.Context, sees *seer, stream string, m redis.XMessage, send func(string, string, []byte) error) error {
	data, _ := m.Values["data"].(string)
	var fields map[string]any
	if err := json.Unmarshal([]byte(data), &fields); err != nil {
		log.Printf("reins: %s %s is not JSON: %q", stream, m.ID, data)
		return nil
	}
	thread, err := uuid.Parse(fmt.Sprint(fields["thread"]))
	if err != nil {
		return nil
	}
	ok, err := sees.thread(ctx, thread)
	if err != nil || !ok {
		return err
	}
	fields["type"], _ = m.Values["ce_type"].(string)
	out, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return send("writing", "", out)
}

// last is the id of the newest entry on a stream, "0-0" when it has none.
func (st *Stream) last(ctx context.Context, stream string) (string, error) {
	ms, err := st.rdb.XRevRangeN(ctx, stream, "+", "-", 1).Result()
	if err != nil {
		return "", fmt.Errorf("the bus: %s: %w", stream, err)
	}
	if len(ms) == 0 {
		return "0-0", nil
	}
	return ms[0].ID, nil
}

// resume is where a reader that saw up to `since` goes on from. A position
// the stream no longer holds (trimmed away, or past its end: a bus that
// came back empty) is a reset, from the stream's end.
func (st *Stream) resume(ctx context.Context, since, last string) (string, bool, error) {
	if !positionPattern.MatchString(since) {
		return last, true, nil
	}
	if compare(since, last) > 0 {
		return last, true, nil
	}
	ms, err := st.rdb.XRangeN(ctx, ThreadsStream, "-", "+", 1).Result()
	if err != nil {
		return "", false, fmt.Errorf("the bus: %s: %w", ThreadsStream, err)
	}
	if len(ms) > 0 && compare(since, ms[0].ID) < 0 && since != "0-0" {
		// Entries between since and the first kept one may be gone. An id
		// just before the first is indistinguishable from a trimmed one;
		// a reset then costs a reload, never a missed entry.
		return last, true, nil
	}
	return since, false, nil
}

// compare orders two stream ids, ms-seq.
func compare(a, b string) int {
	am, as, _ := strings.Cut(a, "-")
	bm, bs, _ := strings.Cut(b, "-")
	x, _ := strconv.ParseUint(am, 10, 64)
	y, _ := strconv.ParseUint(bm, 10, 64)
	if x != y {
		if x < y {
			return -1
		}
		return 1
	}
	x, _ = strconv.ParseUint(as, 10, 64)
	y, _ = strconv.ParseUint(bs, 10, 64)
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

// seer remembers, for one stream, which threads its principal sees: a
// thread is asked of threads once, and again after a join or leave in it.
// The principal's avatars are read again every refresh, which forgets all.
type seer struct {
	svc       *Service
	principal string
	refresh   time.Duration
	avatars   []uuid.UUID
	read      time.Time
	threads   map[uuid.UUID]bool
}

func (s *seer) forget(thread uuid.UUID) { delete(s.threads, thread) }

func (s *seer) thread(ctx context.Context, thread uuid.UUID) (bool, error) {
	if s.threads == nil || time.Since(s.read) > s.refresh {
		avatars, err := s.svc.stewards.Steered(ctx, s.principal)
		if err != nil {
			return false, err
		}
		s.avatars, s.read, s.threads = avatars, time.Now(), map[uuid.UUID]bool{}
	}
	if ok, known := s.threads[thread]; known {
		return ok, nil
	}
	t, err := s.svc.journal.Get(ctx, thread)
	if errors.Is(err, ErrNoThread) {
		s.threads[thread] = false
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ok := anyIn(s.avatars, t.Members)
	s.threads[thread] = ok
	return ok, nil
}
