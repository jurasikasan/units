// Package hearing is the hearing organ (contract C5): it opens audio
// sessions that receive RTP speech, runs voice activity detection and
// speech-to-text on the utterances, and reports what was heard on the
// hearing_events stream.
package hearing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jurasikasan/units/senses/hearing/internal/rtpin"
	"github.com/jurasikasan/units/senses/hearing/internal/stt"
	"github.com/jurasikasan/units/senses/hearing/internal/vad"
)

// Defaults per C5 section 5.
const (
	// DefaultSTTURL is an OpenAI-shaped audio base: a router such as
	// LiteLLM, its key in STTKey, or one engine with none.
	DefaultSTTURL          = "http://litellm:4000/v1"
	DefaultSTTModel        = "stt"
	DefaultVADURL          = "http://vad:8096"
	DefaultVADKind         = "silero"
	DefaultEnergyThreshold = 500
	DefaultOnset           = 200 * time.Millisecond
	DefaultMergeWindow     = 400 * time.Millisecond
	DefaultPreRoll         = 300 * time.Millisecond
	DefaultMaxUtterance    = 25 * time.Second
	DefaultMaxSessions     = 4
	DefaultIdleTimeout     = 2 * time.Minute
	DefaultAudioInHost     = "hearing"
	DefaultPortMin         = 44000
	DefaultPortMax         = 44199
	DefaultPort            = 8080
)

// RecognizerSpeech is the only recognizer this step supports.
const RecognizerSpeech = "speech"

// windowSamples is one VAD window: 32 ms at 16 kHz.
const windowSamples = 512

// guessAfter is the pause after which the utterance so far goes to
// STT in case the pause ends it, so the transcription runs during
// HEARING_MERGE_WINDOW instead of after it. Speech after the pause voids
// the guess. 192 ms is the silence an earlier VAD waited before it sent a
// segment to STT on real calls.
const guessAfter = 192 * time.Millisecond

// Config holds the runtime configuration (C5 section 5).
type Config struct {
	Sink            Sink
	STTURL          string
	STTKey          string // the router's bearer; empty for an engine
	STTModel        string // OpenAI's model: the router's group, or the engine's name
	VADKind         string // "silero" or "energy"
	VADURL          string
	EnergyThreshold float64
	Onset           time.Duration
	MergeWindow     time.Duration
	PreRoll         time.Duration // audio before the first speech window that goes to STT; 0 is none
	MaxUtterance    time.Duration
	MaxSessions     int
	IdleTimeout     time.Duration
	AudioInHost     string
	PortMin         int
	PortMax         int
	HTTPClient      *http.Client
}

// Service runs hearing sessions.
type Service struct {
	cfg      Config
	mu       sync.Mutex
	sessions map[string]*session
	active   int
}

// NewService applies the C5 defaults to cfg and returns a Service.
func NewService(cfg Config) *Service {
	if cfg.Sink == nil {
		cfg.Sink = &InMemorySink{}
	}
	if cfg.STTURL == "" {
		cfg.STTURL = DefaultSTTURL
	}
	if cfg.STTModel == "" {
		cfg.STTModel = DefaultSTTModel
	}
	if cfg.VADKind == "" {
		cfg.VADKind = DefaultVADKind
	}
	if cfg.VADURL == "" {
		cfg.VADURL = DefaultVADURL
	}
	if cfg.EnergyThreshold == 0 {
		cfg.EnergyThreshold = DefaultEnergyThreshold
	}
	if cfg.Onset <= 0 {
		cfg.Onset = DefaultOnset
	}
	if cfg.MergeWindow <= 0 {
		cfg.MergeWindow = DefaultMergeWindow
	}
	if cfg.MaxUtterance <= 0 {
		cfg.MaxUtterance = DefaultMaxUtterance
	}
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = DefaultMaxSessions
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	if cfg.AudioInHost == "" {
		cfg.AudioInHost = DefaultAudioInHost
	}
	if cfg.PortMin == 0 {
		cfg.PortMin = DefaultPortMin
	}
	if cfg.PortMax == 0 {
		cfg.PortMax = DefaultPortMax
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Service{cfg: cfg, sessions: map[string]*session{}}
}

// Open opens a new session with the requested recognizers (default
// ["speech"]) and returns its id and the "host:port" audio_in address.
// traceparent is the W3C traceparent of the MCP open call (from its
// _meta, contracts/events.md); every event of the session carries exactly that value
// in ce_traceparent, and an empty value means none at all.
// Open starts a session (C5 §2.1). language is the session's Speech-Language
// (RFC 6787 §9.4.24, ISO 639-1): a hint STT gets with every utterance; empty
// leaves STT to guess. What goes in it is the opener's business.
func (s *Service) Open(ctx context.Context, recognize []string, language, traceparent string) (string, string, error) {
	if len(recognize) == 0 {
		recognize = []string{RecognizerSpeech}
	}
	for _, r := range recognize {
		if r != RecognizerSpeech {
			return "", "", fmt.Errorf("unsupported_recognizer: %q is not a recognizer in this step", r)
		}
	}
	s.mu.Lock()
	if s.active >= s.cfg.MaxSessions {
		s.mu.Unlock()
		return "", "", fmt.Errorf("capacity: %d sessions already open (limit %d)", s.active, s.cfg.MaxSessions)
	}
	s.active++
	s.mu.Unlock()

	in, err := s.allocateIn()
	if err != nil {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
		return "", "", err
	}

	id := newID("hr")
	se := &session{
		id:          id,
		svc:         s,
		in:          in,
		det:         s.newDetector(ctx, id),
		created:     time.Now(),
		language:    language,
		traceparent: traceparent,
		out:         make(chan func(), 64),
	}
	s.mu.Lock()
	s.sessions[id] = se
	s.mu.Unlock()
	// The session outlives the open call, whose context ends when it answers.
	go se.worker(context.WithoutCancel(ctx))
	go se.emitter()
	return id, in.AddrString(), nil
}

// Close closes a session: it stops listening, ends any in-progress
// utterance (its recognition-complete goes out before
// speechrecog.session-closed) and releases audio_in. Closing an already
// closed session is a no-op with no second event.
func (s *Service) Close(sessionID string) error {
	s.mu.Lock()
	se, ok := s.sessions[sessionID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown_session: no such session %q", sessionID)
	}
	if se.beginClose("closed") {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}
	return nil
}

// allocateIn binds the first free UDP port of the audio_in range
// (or an ephemeral port when PortMin is negative, as in the tests).
func (s *Service) allocateIn() (*rtpin.In, error) {
	var lastErr error
	if s.cfg.PortMin < 0 {
		in, err := rtpin.New(s.cfg.AudioInHost, 0)
		if err != nil {
			return nil, fmt.Errorf("capacity: cannot bind audio_in: %w", err)
		}
		return in, nil
	}
	for p := s.cfg.PortMin; p <= s.cfg.PortMax; p++ {
		in, err := rtpin.New(s.cfg.AudioInHost, p)
		if err == nil {
			return in, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("capacity: no free audio_in port in %d-%d (last: %v)", s.cfg.PortMin, s.cfg.PortMax, lastErr)
}

func (s *Service) newDetector(ctx context.Context, sessionID string) vad.Detector {
	switch s.cfg.VADKind {
	case "energy":
		return &vad.Energy{Threshold: s.cfg.EnergyThreshold}
	default:
		return vad.NewSilero(s.cfg.VADURL, sessionID, s.cfg.HTTPClient)
	}
}

// session is one open hearing session. After creation it is driven by
// one worker goroutine, which is the only place events are queued; the
// emitter sends them in that order, so the order per session is total and
// a transcription in flight holds back only the events after it, never
// the audio.
type session struct {
	id      string
	svc     *Service
	in      *rtpin.In
	det     vad.Detector
	created time.Time

	// traceparent of the open call that made this session; every event
	// of the session carries it in ce_traceparent (contracts/events.md).
	traceparent string

	// language is the session's Speech-Language, passed to STT; "" = guess.
	language string

	closeMu     sync.Mutex
	closing     bool
	closeReason string

	// worker-owned state
	cand *utterance
	// preroll is the latest audio heard while no candidate was open, at
	// most PreRoll long; a new candidate starts with it (C5 §3, Pre-roll).
	preroll []int16

	out chan func() // events in order, sent by emitter
}

type utterance struct {
	id            string
	started       bool
	start         time.Time
	lastSpeechEnd time.Time
	audio         []int16
	guess         *transcription // started in a pause; nil once speech resumed
}

// transcription is one STT call running beside the worker.
type transcription struct {
	cancel context.CancelFunc
	done   chan struct{}
	res    stt.Result
	err    error
}

func (se *session) transcribe(ctx context.Context, audio []int16) *transcription {
	ctx, cancel := context.WithCancel(ctx)
	tr := &transcription{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(tr.done)
		defer cancel()
		tr.res, tr.err = stt.Transcribe(ctx, stt.Door{URL: se.svc.cfg.STTURL, Key: se.svc.cfg.STTKey,
			Model: se.svc.cfg.STTModel}, se.svc.cfg.HTTPClient, audio, se.language)
	}()
	return tr
}

func (se *session) emitter() {
	for send := range se.out {
		send()
	}
}

func (se *session) beginClose(reason string) bool {
	se.closeMu.Lock()
	if se.closing {
		se.closeMu.Unlock()
		return false
	}
	se.closing = true
	se.closeReason = reason
	se.closeMu.Unlock()
	se.det.Forget(context.Background())
	se.in.Stop()
	return true
}

// worker consumes the decoded 16 kHz stream in 32 ms windows and
// drives the utterance state machine. It ends by finalizing any
// started utterance and emitting the single speechrecog.session-closed.
func (se *session) worker(ctx context.Context) {
	frames := se.in.Out()
	var win []int16
	idle := time.NewTicker(200 * time.Millisecond)
	defer idle.Stop()
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				if se.cand != nil && se.cand.started {
					se.emitHeard(ctx, se.cand, false)
				}
				se.emit(ctx, TypeSessionClosed, map[string]any{
					"session_id": se.id,
					"reason":     se.closeReason,
				})
				close(se.out)
				return
			}
			for i, s := range f.Pcm {
				win = append(win, s)
				if len(win) == windowSamples {
					winEnd := f.FirstTime.Add(time.Duration(i+1) * (time.Second / 16000))
					// A window is exactly windowSamples at 16 kHz long.
					winStart := winEnd.Add(-time.Duration(windowSamples) * (time.Second / 16000))
					se.processWindow(ctx, win, winStart, winEnd)
					win = win[:0]
				}
			}
		case <-idle.C:
			ref := se.created
			if lp := se.in.LastPacket(); !lp.IsZero() {
				ref = lp
			}
			if time.Since(ref) >= se.svc.cfg.IdleTimeout {
				se.beginClose("idle")
			}
		}
	}
}

// processWindow advances the utterance state machine with one 32 ms
// window of 16 kHz samples.
func (se *session) processWindow(ctx context.Context, win []int16, winStart, winEnd time.Time) {
	speech, err := se.det.Speech(ctx, win)
	if err != nil {
		speech = false
	}
	cfg := se.svc.cfg
	if speech {
		if se.cand != nil &&
			!winStart.Before(se.cand.lastSpeechEnd.Add(cfg.MergeWindow)) {
			// A gap of at least HEARING_MERGE_WINDOW with no audio at all
			// (the sender suppressed silence, so no silence window ever
			// closed the utterance): the same split as a silence gap.
			if se.cand.started {
				se.emitHeard(ctx, se.cand, false)
			}
			se.cand = nil
		}
		if se.cand != nil && se.cand.guess != nil {
			se.cand.guess.cancel()
			se.cand.guess = nil
		}
		if se.cand == nil {
			se.cand = &utterance{id: newID("utt"), start: winStart, lastSpeechEnd: winEnd,
				audio: se.takePreroll()}
		} else {
			se.cand.lastSpeechEnd = winEnd
		}
		if !se.cand.started {
			// The burst is shorter than HEARING_ONSET: no speech at all
			// (the candidate, with its audio, is dropped on the next
			// silence window).
			if winEnd.Before(se.cand.start.Add(cfg.Onset)) {
				se.cand.audio = append(se.cand.audio, win...)
				return
			}
			se.cand.started = true
			se.emit(ctx, TypeStartOfInput, map[string]any{
				"session_id": se.id,
				"request_id": se.cand.id,
				"input_type": "speech",
				"started_at": stamp(se.cand.start),
			})
		}
		// HEARING_MAX_UTTERANCE: close the utterance there and start a
		// new one at once if the speech continues.
		cut := se.cand.start.Add(cfg.MaxUtterance)
		if !winEnd.Before(cut) {
			se.cutAt(ctx, cut, win, winStart, winEnd)
			return
		}
		se.cand.audio = append(se.cand.audio, win...)
	} else {
		switch {
		case se.cand == nil:
			// silence before any speech: kept as the next one's pre-roll
			se.keepPreroll(win)
		case !se.cand.started && !winStart.Before(se.cand.lastSpeechEnd.Add(cfg.MergeWindow)):
			// the candidate never reached the onset: drop it, no events;
			// its tail is what preceded whatever speech comes next
			se.keepPreroll(se.cand.audio)
			se.keepPreroll(win)
			se.cand = nil
		case !se.cand.started:
			// a pause inside the merge window does not end it before the
			// onset either (C5 §3): keep the silence with the audio
			se.cand.audio = append(se.cand.audio, win...)
		case !winStart.Before(se.cand.lastSpeechEnd.Add(cfg.MergeWindow)):
			// the speaker stopped for HEARING_MERGE_WINDOW
			se.emitHeard(ctx, se.cand, false)
			se.cand = nil
			se.keepPreroll(win)
		default:
			// still inside the merge window: keep the utterance open and
			// record the trailing silence
			se.cand.audio = append(se.cand.audio, win...)
			if se.cand.guess == nil && !winEnd.Before(se.cand.lastSpeechEnd.Add(guessAfter)) {
				se.cand.guess = se.transcribe(ctx, slices.Clone(se.cand.audio))
			}
		}
	}
}

// keepPreroll appends samples to the pre-roll and keeps its last PreRoll.
func (se *session) keepPreroll(samples []int16) {
	limit := int(se.svc.cfg.PreRoll / (time.Second / 16000))
	if limit <= 0 {
		return
	}
	se.preroll = append(se.preroll, samples...)
	if over := len(se.preroll) - limit; over > 0 {
		se.preroll = append(se.preroll[:0], se.preroll[over:]...)
	}
}

// takePreroll hands the pre-roll to a new candidate and starts a new one.
func (se *session) takePreroll() []int16 {
	audio := se.preroll
	se.preroll = nil
	return audio
}

// cutAt finalizes the current utterance at cut and opens the next one
// at the same instant (which is reported started at once).
func (se *session) cutAt(ctx context.Context, cut time.Time, win []int16, winStart, winEnd time.Time) {
	old := se.cand
	idx := int(cut.Sub(winStart) / (time.Second / 16000))
	if idx < 0 {
		idx = 0
	}
	if idx > len(win) {
		idx = len(win)
	}
	old.audio = append(old.audio, win[:idx]...)
	old.lastSpeechEnd = cut
	se.emitHeard(ctx, old, true)
	tail := make([]int16, len(win)-idx)
	copy(tail, win[idx:])
	se.cand = &utterance{
		id:            newID("utt"),
		started:       true,
		start:         cut,
		lastSpeechEnd: winEnd,
		audio:         tail,
	}
	se.emit(ctx, TypeStartOfInput, map[string]any{
		"session_id": se.id,
		"request_id": se.cand.id,
		"input_type": "speech",
		"started_at": stamp(cut),
	})
}

// emitHeard reports speechrecog.recognition-complete for u once its
// transcription is back: the guess started in the pause, or a new one when
// there is none. The completion cause is MRCP's (RFC 6787 §9.4.11): success,
// success-maxtime for an utterance cut at HEARING_MAX_UTTERANCE, no-match when
// the words came back empty, recognizer-error (with an empty text) when
// transcription failed.
func (se *session) emitHeard(ctx context.Context, u *utterance, maxtime bool) {
	tr := u.guess
	if tr == nil {
		tr = se.transcribe(ctx, u.audio)
	}
	data := map[string]any{
		"session_id":       se.id,
		"request_id":       u.id,
		"completion_cause": CauseRecognizerError,
		"text":             "",
		"language":         "",
		"started_at":       stamp(u.start),
		"ended_at":         stamp(u.lastSpeechEnd),
	}
	se.queue(ctx, TypeRecognitionComplete, data, func() {
		<-tr.done
		if tr.err != nil {
			return
		}
		data["text"] = tr.res.Text
		data["language"] = tr.res.Language
		switch {
		case strings.TrimSpace(tr.res.Text) == "":
			data["completion_cause"] = CauseNoMatch
		case maxtime:
			data["completion_cause"] = CauseSuccessMaxtime
		default:
			data["completion_cause"] = CauseSuccess
		}
	})
}

func (se *session) emit(ctx context.Context, typ string, data map[string]any) {
	se.queue(ctx, typ, data, nil)
}

// queue hands an event to the emitter; before, if set, runs there first.
func (se *session) queue(ctx context.Context, typ string, data map[string]any, before func()) {
	data["session_id"] = se.id
	se.out <- func() {
		if before != nil {
			before()
		}
		se.svc.cfg.Sink.Emit(ctx, Event{Type: typ, Subject: se.id, Data: data, Traceparent: se.traceparent})
	}
}

// stamp formats a time as RFC 3339 UTC with millisecond precision, on
// hearing's clock.
func stamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func newID(prefix string) string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return prefix + "-00000000"
	}
	return prefix + "-" + hex.EncodeToString(b)
}
