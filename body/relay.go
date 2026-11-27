package body

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"

	sharedredis "github.com/jurasikasan/units/shared/redis"
)

// relayed are the types a body republishes from its own sense sessions:
// MRCP's recognizer shape, as the senses report it. Anything else of its
// sessions is counted and passed over.
var relayed = map[string]bool{
	"speechrecog.start-of-input":       true,
	"speechrecog.recognition-complete": true,
	"speechrecog.session-closed":       true,
	"sightrecog.start-of-input":        true,
	"sightrecog.recognition-complete":  true,
	"sightrecog.session-closed":        true,
}

// What a body reads of speaking's stream, about its own speaking sessions.
const (
	speakComplete  = "speechsynth.speak-complete"
	speakingClosed = "speechsynth.session-closed"
)

// ensureGroups creates the body's group on each stream (and the stream, if
// nothing has written yet), at its end: a body perceives from its start.
func ensureGroups(ctx context.Context, rdb *redis.Client, streams []string, group string) error {
	for _, name := range streams {
		err := rdb.XGroupCreateMkStream(ctx, name, group, "$").Err()
		if err != nil && !strings.HasPrefix(err.Error(), "BUSYGROUP") {
			return fmt.Errorf("group %s on %s: %w", group, name, err)
		}
	}
	return nil
}

// read takes what is new on the senses' and speaking's streams until ctx
// ends, sorts each entry to the body's own sessions or past them, and
// acknowledges it.
func (b *Body) read(ctx context.Context, streams []string) {
	if len(streams) == 0 {
		return
	}
	args := make([]string, 0, 2*len(streams))
	args = append(args, streams...)
	for range streams {
		args = append(args, ">")
	}
	for ctx.Err() == nil {
		res, err := b.Redis.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group: b.Cfg.Group, Consumer: "body", Streams: args, Count: 64, Block: time.Second,
		}).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.Counts.Errors.Add(1)
			b.Log.Warn("streams: read failed, trying again", "err", err.Error())
			if strings.HasPrefix(err.Error(), "NOGROUP") {
				_ = ensureGroups(ctx, b.Redis, streams, b.Cfg.Group)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		for _, st := range res {
			for _, msg := range st.Messages {
				b.take(ctx, st.Stream, msg.Values)
				actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
				if err := b.Redis.XAck(actx, st.Stream, b.Cfg.Group, msg.ID).Err(); err != nil {
					b.Counts.Errors.Add(1)
					b.Log.Warn("streams: ack failed", "stream", st.Stream, "entry", msg.ID, "err", err.Error())
				}
				cancel()
			}
		}
	}
}

func (b *Body) take(ctx context.Context, stream string, f map[string]any) {
	str := func(k string) string { s, _ := f[k].(string); return s }
	typ, subject := str("ce_type"), str("ce_subject")
	if stream == SpeechStream {
		b.heardFromMouth(typ, subject, str("data"))
		return
	}
	s := b.senseOf(subject)
	if s == nil {
		b.Counts.Foreign.Add(1)
		return
	}
	if !relayed[typ] {
		b.Counts.Dropped.Add(1)
	} else if err := b.republish(ctx, s, f); err != nil {
		b.Counts.Errors.Add(1)
		b.Log.Warn("relay: not republished", "type", typ, "session", subject, "err", err.Error())
	} else {
		b.Counts.Relayed.Add(1)
	}
	if typ == s.closed {
		b.closedBy(s, subject)
	}
}

// republish puts a sense's event onto the body's stream as it came — its id,
// type, time, trace and data unchanged — from /bodies/<name>/<channel>,
// about the body.
func (b *Body) republish(ctx context.Context, s *sense, f map[string]any) error {
	out := map[string]any{
		"ce_specversion":     "1.0",
		"ce_source":          SourceFor(b.Cfg.Name) + "/" + s.channel,
		"ce_subject":         b.Cfg.ID.String(),
		"ce_datacontenttype": "application/json",
	}
	for _, k := range []string{"ce_id", "ce_type", "ce_time", "ce_traceparent", "data"} {
		if v, ok := f[k]; ok {
			out[k] = v
		}
	}
	return b.Redis.XAdd(ctx, sharedredis.XAddArgs(b.Cfg.Stream, out)).Err()
}

// heardFromMouth takes speaking's events about the body's own speaking
// sessions: a speak-complete goes to the act waiting for it; a closed
// session is forgotten, so the next phrase opens another.
func (b *Body) heardFromMouth(typ, session, data string) {
	switch typ {
	case speakComplete:
		b.mu.Lock()
		ch := b.spoken[session]
		b.mu.Unlock()
		if ch == nil {
			return
		}
		var d map[string]any
		if json.Unmarshal([]byte(data), &d) != nil {
			return
		}
		select {
		case ch <- d:
		default:
		}
	case speakingClosed:
		b.mu.Lock()
		for url, m := range b.mouths {
			if m.session == session {
				delete(b.mouths, url)
				b.Log.Info("mouth: its speaking session closed", "listener", url, "session", session)
			}
		}
		b.mu.Unlock()
	}
}
