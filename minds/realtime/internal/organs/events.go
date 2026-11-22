package organs

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
)

// Event is an organ's event as this mind reads it: the CloudEvents type and
// the data, which always carries the organ's session_id.
type Event struct {
	Type    string
	Session string
	Data    map[string]any
}

// Str is a field of the data as a string, "" when absent.
func (e Event) Str(key string) string {
	s, _ := e.Data[key].(string)
	return s
}

// Uint32 is a field of the data as a whole number (JSON's, a float64 here);
// ok is false when absent or not one.
func (e Event) Uint32(key string) (uint32, bool) {
	f, ok := e.Data[key].(float64)
	if !ok || f < 0 || f > 1<<32-1 || f != float64(uint32(f)) {
		return 0, false
	}
	return uint32(f), true
}

// Time is a field of the data as an RFC 3339 time; zero when absent.
func (e Event) Time(key string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, e.Str(key))
	return t
}

// Stream follows one organ's event stream from now on and hands each event
// to whoever subscribed to its session. One reader for every call: a call
// subscribes to the session it opened.
type Stream struct {
	rdb  *redis.Client
	name string

	mu   sync.Mutex
	subs map[string]chan<- Event
}

// Follow starts reading stream; it stops with ctx.
func Follow(ctx context.Context, rdb *redis.Client, stream string) *Stream {
	s := &Stream{rdb: rdb, name: stream, subs: map[string]chan<- Event{}}
	go s.run(ctx)
	return s
}

// Subscribe sends the session's events to ch until Unsubscribe. An event
// for a session nobody follows is dropped.
func (s *Stream) Subscribe(session string, ch chan<- Event) {
	s.mu.Lock()
	s.subs[session] = ch
	s.mu.Unlock()
}

// Unsubscribe stops sending the session's events.
func (s *Stream) Unsubscribe(session string) {
	s.mu.Lock()
	delete(s.subs, session)
	s.mu.Unlock()
}

// run reads from the stream's last id at start, then from the last id read:
// "$" on every read would lose what arrives between two reads. What came
// before the start belongs to calls this process never had.
func (s *Stream) run(ctx context.Context) {
	pos := "$"
	if last, err := s.rdb.XRevRangeN(ctx, s.name, "+", "-", 1).Result(); err == nil {
		pos = "0-0"
		if len(last) == 1 {
			pos = last[0].ID
		}
	}
	for ctx.Err() == nil {
		res, err := s.rdb.XRead(ctx, &redis.XReadArgs{Streams: []string{s.name, pos}, Count: 100, Block: time.Second}).Result()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("xread %s: %v", s.name, err)
				time.Sleep(time.Second)
			}
			continue
		}
		for _, st := range res {
			for _, m := range st.Messages {
				pos = m.ID
				s.deliver(ctx, m)
			}
		}
	}
}

func (s *Stream) deliver(ctx context.Context, m redis.XMessage) {
	typ, _ := m.Values["ce_type"].(string)
	raw, _ := m.Values["data"].(string)
	var data map[string]any
	if typ == "" || json.Unmarshal([]byte(raw), &data) != nil {
		return
	}
	ev := Event{Type: typ, Data: data}
	ev.Session = ev.Str("session_id")
	s.mu.Lock()
	ch := s.subs[ev.Session]
	s.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- ev:
	case <-ctx.Done():
	}
}
