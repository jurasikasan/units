// Package speech manages the live speaking sessions behind the open / speak /
// stop / barge_in_occurred / close tools (MRCPv2's synthesizer verbs, RFC 6787
// §8): it wires each session's mouth (text → audio) to its RTP sink (audio → the
// line over UDP) and publishes speak-complete / session-closed to the
// speech_events stream.
package speech

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/jurasikasan/units/abilities/speaking/internal/events"
	"github.com/jurasikasan/units/abilities/speaking/internal/mouth"
	"github.com/jurasikasan/units/abilities/speaking/internal/tts"
)

// Version is stamped into the MCP server identity.
const Version = "0.1.0"

// CodecL16 is the only codec this speech can send: 48 kHz PCM16 mono in
// network byte order (C4 §4). Anything else is reserved (C4 §6).
const CodecL16 = "L16"

// Config is this service's configuration (C4 §5).
type Config struct {
	// MaxSessions is how many sessions may be open at once (SPEECH_MAX_SESSIONS).
	MaxSessions int
	// IdleTimeout is how long an open-but-quiet session lives before it ends
	// on its own with reason "idle" (SPEECH_IDLE_TIMEOUT).
	IdleTimeout time.Duration
}

// SynthClient is the TTS side the mouth needs. *tts.Client satisfies it; tests
// may stand in with a fake.
type SynthClient interface {
	Synth(ctx context.Context, text, voice string) ([]int16, error)
}

// Speech is the session manager behind the three tools.
type Speech struct {
	cfg Config
	tts SynthClient
	pub events.Publisher

	mu       sync.Mutex
	sessions map[string]*session // the live sessions; an ended one is forgotten
	reserved int                 // slots in an in-flight open that has no session yet
	finished map[string]struct{} // ids of ended sessions: close on them is ok
}

// New builds the manager. Zero cfg fields fall back to the C4 defaults:
// 4 sessions, 2m idle timeout.
func New(cfg Config, tts SynthClient, pub events.Publisher) *Speech {
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = 4
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 2 * time.Minute
	}
	return &Speech{
		cfg:      cfg,
		tts:      tts,
		pub:      pub,
		sessions: map[string]*session{},
		finished: map[string]struct{}{},
	}
}

// Open starts a new session and returns its id (C4 §2.1). Codec, capacity,
// target and voice are checked in that order; each failure carries its own
// code. The capacity slot is taken under the manager lock — concurrent opens
// cannot exceed the limit — and given back when the open then fails.
// traceparent is the W3C traceparent of the open call (C0 §5); when empty,
// the session's events carry no ce_traceparent at all.
func (sp *Speech) Open(ctx context.Context, to, voice, codec, traceparent string) (string, error) {
	// the codec is strict: exactly L16, nothing else (C4 §2.1)
	if codec == "" {
		codec = CodecL16
	}
	if codec != CodecL16 {
		return "", fmt.Errorf("unsupported_codec: %q (this speech sends %s)", codec, CodecL16)
	}
	// The voice is checked where it is stored (a subject's preferences, against
	// the engines' GET /v1/audio/voices); here only its shape. A voice an engine
	// does not have fails the first speak, cause error.
	if _, _, ok := tts.ModelVoice(voice); !ok {
		return "", fmt.Errorf("unknown_voice: %q is not <engine>_<voice>", voice)
	}
	sp.mu.Lock()
	open := len(sp.sessions) + sp.reserved
	if open >= sp.cfg.MaxSessions {
		sp.mu.Unlock()
		return "", fmt.Errorf("capacity: %d session(s) already open", open)
	}
	sp.reserved++
	sp.mu.Unlock()

	target, err := net.ResolveUDPAddr("udp", to)
	if err != nil {
		sp.mu.Lock()
		sp.reserved--
		sp.mu.Unlock()
		return "", fmt.Errorf("bad_target: %q: %v", to, err)
	}

	sess := newSession(sp, sp.cfg, target, voice, sp.tts, sp.pub, traceparent)
	// The session takes the reserved slot over in one step: released apart,
	// a concurrent open would count the slot twice and fail with capacity
	// below the limit.
	sp.mu.Lock()
	sp.sessions[sess.id] = sess
	sp.reserved--
	sp.mu.Unlock()
	slog.Info("session opened", "session_id", sess.id, "to", to, "voice", voice, "codec", CodecL16)
	return sess.id, nil
}

// Speak queues text to an open session and returns the request id (C4 §2.2).
func (sp *Speech) Speak(sessionID, text string) (string, error) {
	sess := sp.get(sessionID)
	if sess == nil {
		return "", fmt.Errorf("unknown_session: %s", sessionID)
	}
	if sess.closed() {
		return "", fmt.Errorf("unknown_session: %s has closed", sessionID)
	}
	sess.touch()
	uid, err := sess.mouth.Speak(text, sess.voice)
	if err != nil {
		if errors.Is(err, mouth.ErrClosed) {
			return "", fmt.Errorf("unknown_session: %s has closed", sessionID)
		}
		return "", err
	}
	return uid, nil
}

// Stop cuts what the session is playing and drops its queue at the driver's
// word; the session stays (C4 §2.4, MRCP STOP). It returns once every affected
// request has its speak-complete, cause cancelled.
func (sp *Speech) Stop(sessionID string) error {
	return sp.cut(sessionID, mouth.CauseCancelled)
}

// BargeInOccurred is Stop because the other side started talking (C4 §2.4,
// MRCP BARGE-IN-OCCURRED): the same cut, cause barge-in.
func (sp *Speech) BargeInOccurred(sessionID string) error {
	return sp.cut(sessionID, mouth.CauseBargeIn)
}

func (sp *Speech) cut(sessionID, cause string) error {
	sess := sp.get(sessionID)
	if sess == nil || sess.closed() {
		return fmt.Errorf("unknown_session: %s", sessionID)
	}
	sess.touch()
	if ids := sess.mouth.Stop(cause); len(ids) > 0 {
		slog.Info("session stopped", "session_id", sess.id, "cause", cause, "requests", len(ids))
	}
	return nil
}

// Close ends an open session with reason "closed" (C4 §2.3). Closing a
// session that has already ended is {"ok": true} with no second event.
func (sp *Speech) Close(sessionID string) error {
	sp.mu.Lock()
	sess := sp.sessions[sessionID]
	if sess == nil {
		_, ended := sp.finished[sessionID]
		sp.mu.Unlock()
		if ended {
			return nil // already ended: ok, no second event
		}
		return fmt.Errorf("unknown_session: %s", sessionID)
	}
	sp.mu.Unlock()
	sess.end("closed")
	return nil
}

func (sp *Speech) get(id string) *session {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return sp.sessions[id]
}

// Len is how many sessions the manager currently holds: an ended session is
// no longer among them.
func (sp *Speech) Len() int {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return len(sp.sessions)
}

// remove forgets an ended session: its slot is open again and its id no
// longer resolves. Called only after the session's session.closed is out.
func (sp *Speech) remove(id string) {
	sp.mu.Lock()
	delete(sp.sessions, id)
	sp.finished[id] = struct{}{}
	sp.mu.Unlock()
}

// session is one live conversation leg: one mouth, one RTP stream (one SSRC
// for its whole life, C4 §4), one idle watchdog.
type session struct {
	id     string
	voice  string
	target *net.UDPAddr
	ssrc   uint32

	// traceparent is the trace of the open call that made the session (C0 §5):
	// every event of the session carries exactly that value, or none.
	traceparent string

	sp    *Speech
	sink  *rtpSink
	mouth *mouth.Mouth
	pub   events.Publisher
	cfg   Config

	stoppedOnce sync.Once
	stopped     chan struct{} // closed when the session has ended

	mu        sync.Mutex
	idleSince time.Time // zero = playing or never idle
}

func newSession(sp *Speech, cfg Config, target *net.UDPAddr, voice string, tts SynthClient, pub events.Publisher, traceparent string) *session {
	s := &session{
		id:          newSessionID(),
		sp:          sp,
		voice:       voice,
		target:      target,
		ssrc:        newSSRC(),
		cfg:         cfg,
		pub:         pub,
		stopped:     make(chan struct{}),
		traceparent: traceparent,
	}
	s.sink = newRTPSink(s.ssrc, target)
	s.mouth = mouth.New(tts, s.sink, s.onPlayed)
	go s.watchdog()
	return s
}

func (s *session) closed() bool {
	select {
	case <-s.stopped:
		return true
	default:
		return false
	}
}

// touch marks activity: the idle clock starts (or restarts) from now.
func (s *session) touch() {
	s.mu.Lock()
	s.idleSince = time.Time{}
	s.mu.Unlock()
}

// end stops the session with the given reason exactly once (C4 §2.3, §3):
// what is playing is cut, what is queued is dropped, in-flight synthesis is
// abandoned; every request gets its speak-complete before session-closed follows.
func (s *session) end(reason string) {
	s.stoppedOnce.Do(func() {
		close(s.stopped)
		// the session's UDP socket is closed now: further datagrams on it
		// fail.
		s.sink.Close()
		// mouth.Close returns once every request's speak-complete has been
		// emitted, so session-closed can follow and be the last event.
		s.mouth.Close()
		s.publish(events.Event{
			Type:    events.SessionClosed,
			Subject: s.id,
			Data: map[string]any{
				"session_id": s.id,
				"reason":     reason,
			},
		})
		// only once session.closed is out does the manager forget the
		// session: its slot is open, its id is unknown_session.
		s.sp.remove(s.id)
		slog.Info("session ended", "session_id", s.id, "reason", reason)
	})
}

// onPlayed publishes one speak-complete (C4 §3). The session goes on whether
// or not the publish lands.
func (s *session) onPlayed(p mouth.Played) {
	s.publish(events.Event{
		Type:    events.SpeakComplete,
		Subject: s.id,
		Data: map[string]any{
			"session_id":       s.id,
			"request_id":       p.RequestID,
			"completion_cause": p.Cause,
			"spoken":           p.Spoken,
		},
	})
}

func (s *session) publish(e events.Event) {
	// C0 §5: the cause of every event of this session is the open call that
	// made it, so every event of the session carries exactly that traceparent
	// (or none, when the open call had none).
	e.Traceparent = s.traceparent
	if err := s.pub.Publish(context.Background(), e); err != nil {
		slog.Error("event not published", "type", e.Type, "session_id", s.id, "err", err)
	}
}

// watchdog ends the session after it has been open, idle and empty for
// IdleTimeout (C4 §2.3: ends by itself with reason "idle").
func (s *session) watchdog() {
	interval := s.cfg.IdleTimeout / 5
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	if interval > time.Second {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.stopped:
			return
		case <-t.C:
		}
		if s.closed() {
			return
		}
		s.mu.Lock()
		if !s.mouth.Idle() {
			// still playing or something is queued: the idle clock is reset
			s.idleSince = time.Time{}
			s.mu.Unlock()
			continue
		}
		if s.idleSince.IsZero() {
			s.idleSince = time.Now()
			s.mu.Unlock()
			continue
		}
		idleFor := time.Since(s.idleSince)
		s.mu.Unlock()
		if idleFor >= s.cfg.IdleTimeout {
			slog.Info("session idle, ending", "session_id", s.id, "idle_for", idleFor)
			s.end("idle")
			return
		}
	}
}

func newSessionID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("spk-%08x", uint32(b[0])<<24|uint32(b[1])<<16|uint32(b[2])<<8|uint32(b[3]))
}

func newSSRC() uint32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	if v == 0 {
		v = 1
	}
	return v
}
