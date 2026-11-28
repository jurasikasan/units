package body

import (
	"context"
	"sync"
	"time"
)

// sense is one of the body's senses: one session at a time, opened on the
// sense's MCP tool open, its address the body's ear or eye.
type sense struct {
	channel string         // hearing, sight: the last segment of the republished ce_source
	stream  string         // the sense's stream
	closed  string         // its session-closed type
	door    *Door          // its MCP
	args    map[string]any // open's arguments

	mu      sync.Mutex
	session string            // the open session; empty while there is none
	at      map[string]string // what open answered: audio_in, or video_in and sight_in
	gone    chan struct{}     // signalled when the session closed by itself
}

func (s *sense) address() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.at
}

// keep holds a session of s open until ctx ends: opens it, waits for it to
// close by itself (failure; sight's idle — its session-closed on the stream), opens
// it again; a sense that will not open is asked again every Retry. On the
// way out the session is closed.
func (b *Body) keep(ctx context.Context, s *sense) {
	s.mu.Lock()
	s.gone = make(chan struct{}, 1)
	s.mu.Unlock()
	for ctx.Err() == nil {
		var out map[string]any
		args := s.args
		if args == nil {
			args = map[string]any{}
		}
		err := s.door.Call(ctx, "open", args, "", &out)
		id, _ := out["session_id"].(string)
		if err == nil && id == "" {
			err = &ToolError{Text: "open answered no session_id"}
		}
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			b.Counts.OpenErrs.Add(1)
			b.Log.Warn("sense: not opened, asking again", "sense", s.channel, "in", b.Cfg.Retry.String(), "err", err.Error())
			select {
			case <-ctx.Done():
			case <-time.After(b.Cfg.Retry):
			}
			continue
		}
		at := map[string]string{}
		for k, v := range out {
			if str, ok := v.(string); ok && k != "session_id" {
				at[k] = str
			}
		}
		b.mu.Lock()
		b.mine[id] = s
		b.mu.Unlock()
		s.mu.Lock()
		s.session, s.at = id, at
		s.mu.Unlock()
		b.Counts.Opened.Add(1)
		b.Log.Info("sense: opened", "sense", s.channel, "session", id, "at", at)

		select {
		case <-ctx.Done():
		case <-s.gone:
			b.Log.Info("sense: its session closed, opening again", "sense", s.channel, "session", id)
		}
		s.mu.Lock()
		s.session, s.at = "", nil
		s.mu.Unlock()
		if ctx.Err() != nil {
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if err := s.door.Call(cctx, "close", map[string]any{"session_id": id}, "", nil); err != nil {
				b.Log.Warn("sense: close failed", "sense", s.channel, "session", id, "err", err.Error())
			}
			cancel()
			b.forget(id)
		}
	}
	s.door.Close()
}

// closedBy is called by the reader on a session-closed of one of the body's
// sessions: the last event of that session, so it is forgotten, and its
// sense opens a new one.
func (b *Body) closedBy(s *sense, id string) {
	b.forget(id)
	s.mu.Lock()
	current := s.session == id
	gone := s.gone
	s.mu.Unlock()
	if current && gone != nil {
		select {
		case gone <- struct{}{}:
		default:
		}
	}
}

func (b *Body) forget(id string) {
	b.mu.Lock()
	delete(b.mine, id)
	b.mu.Unlock()
}

func (b *Body) senseOf(id string) *sense {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.mine[id]
}
