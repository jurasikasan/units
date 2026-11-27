package body

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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

func (b *Body) take(ctx context.Context, stream string, f map[string]any) {
	str := func(k string) string { s, _ := f[k].(string); return s }
	typ, subject := str("ce_type"), str("ce_subject")
	if stream == SpeechStream {
		b.heardFromMouth(typ, subject, str("data"))
		return
	}
	s := b.senseOf(subject)
	if s == nil { // the host hands a body only its own; one closed meanwhile
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
