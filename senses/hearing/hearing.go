// Package hearing is the sense of hearing (contract C5): it opens audio
// sessions that receive RTP speech, runs voice activity detection and
// speech-to-text on the utterances, and reports what was heard on the
// hearing_events stream.
package hearing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

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
	DefaultMaxSources      = 8
	DefaultIdleTimeout     = 2 * time.Minute
	DefaultAudioInHost     = "hearing"
	DefaultPortMin         = 44000
	DefaultPortMax         = 44199
	DefaultPort            = 8080
)

// What becomes of a burst of speech shorter than HEARING_ONSET (C5 §3, Short).
const (
	// ShortTranscribe sends it to STT once the merge window closes it, and
	// reports it only when words come back: start-of-input and
	// recognition-complete together, after the word.
	ShortTranscribe = "transcribe"
	// ShortDrop treats it as no speech (a cough, a click), as before UNIT-6.
	ShortDrop = "drop"
	// DefaultShortWords is HEARING_SHORT's default.
	DefaultShortWords = ShortTranscribe
)

// shortLettersPerSecond caps the text a burst under HEARING_ONSET may come
// back with: at most this many letters per second of the onset, 10 at the
// default 200 ms (UNIT-13). Whisper large-v3, local and groq's, answers some
// ~0.85 s clips of one short word with a sentence from its subtitle training
// («Субтитры сделал DimaTorzok», «С вами был Игорь Негода.»: 19–22 letters);
// those segments are no less confident than the real words (no_speech_prob
// 0.06–0.57, avg_logprob -0.30 to -0.49, against up to 0.68 and down to -0.86
// for real ones), so STT's own numbers cannot tell them apart. Their length
// can: the fastest piper phrase measured is 20 letters per second of speech,
// 4 letters in 200 ms, and real short-burst transcripts were 1 word of at
// most 6 letters. The cap is 2.5 times that rate, and half the shortest
// hallucination seen.
const shortLettersPerSecond = 50

// plausibleShort says whether text can be what was said in a burst under
// onset: a few letters, not a sentence.
func plausibleShort(text string, onset time.Duration) bool {
	letters := 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return float64(letters) <= shortLettersPerSecond*onset.Seconds()
}

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
	ShortWords      string        // ShortTranscribe or ShortDrop: a burst under the onset
	MaxUtterance    time.Duration
	MaxSessions     int
	MaxSources      int // senders heard at once in one session (C5 §4)
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
	if cfg.ShortWords != ShortTranscribe && cfg.ShortWords != ShortDrop {
		cfg.ShortWords = DefaultShortWords
	}
	if cfg.MaxUtterance <= 0 {
		cfg.MaxUtterance = DefaultMaxUtterance
	}
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = DefaultMaxSessions
	}
	if cfg.MaxSources <= 0 {
		cfg.MaxSources = DefaultMaxSources
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
	return s.OpenWith(ctx, Options{Recognize: recognize, Language: language}, traceparent)
}

// Options is what an open may ask for beyond the deployment's config: the
// language, the ear (STT model and prompt) and the turn timings. Zero
// values keep the deployment's; PreRoll is a pointer, since none (0) is a
// value of its own.
type Options struct {
	Recognize   []string
	Language    string
	STTModel    string
	STTPrompt   string
	Onset       time.Duration
	MergeWindow time.Duration
	PreRoll     *time.Duration
}

// OpenWith is Open with the session's own ear and timings (C5 §2.1).
func (s *Service) OpenWith(ctx context.Context, o Options, traceparent string) (string, string, error) {
	recognize, language := o.Recognize, o.Language
	if o.Onset < 0 || o.MergeWindow < 0 || o.PreRoll != nil && *o.PreRoll < 0 {
		return "", "", fmt.Errorf("bad_argument: a timing below zero")
	}
	cfg := s.cfg
	if o.STTModel != "" {
		cfg.STTModel = o.STTModel
	}
	if o.Onset > 0 {
		cfg.Onset = o.Onset
	}
	if o.MergeWindow > 0 {
		cfg.MergeWindow = o.MergeWindow
	}
	if o.PreRoll != nil {
		cfg.PreRoll = *o.PreRoll
	}
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
		sources:     map[uint32]*source{},
		created:     time.Now(),
		cfg:         cfg,
		prompt:      o.STTPrompt,
		language:    language,
		traceparent: traceparent,
	}
	s.mu.Lock()
	s.sessions[id] = se
	s.mu.Unlock()
	// The session outlives the open call, whose context ends when it answers.
	go se.worker(context.WithoutCancel(ctx))
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
	se.beginClose("closed")
	return nil
}

// allocateIn binds the first free UDP port of the audio_in range
// (or an ephemeral port when PortMin is negative, as in the tests).
func (s *Service) allocateIn() (*rtpin.In, error) {
	var lastErr error
	if s.cfg.PortMin < 0 {
		in, err := rtpin.New(s.cfg.AudioInHost, 0, s.cfg.MaxSources)
		if err != nil {
			return nil, fmt.Errorf("capacity: cannot bind audio_in: %w", err)
		}
		return in, nil
	}
	for p := s.cfg.PortMin; p <= s.cfg.PortMax; p++ {
		in, err := rtpin.New(s.cfg.AudioInHost, p, s.cfg.MaxSources)
		if err == nil {
			return in, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("capacity: no free audio_in port in %d-%d (last: %v)", s.cfg.PortMin, s.cfg.PortMax, lastErr)
}

// newDetector makes one source's voice activity detector; id names its
// state on the VAD engine.
func (s *Service) newDetector(id string) vad.Detector {
	switch s.cfg.VADKind {
	case "energy":
		return &vad.Energy{Threshold: s.cfg.EnergyThreshold}
	default:
		return vad.NewSilero(s.cfg.VADURL, id, s.cfg.HTTPClient)
	}
}

// session is one open hearing session. After creation it is driven by
// one worker goroutine, which is the only place events are queued. Each
// sender heard on audio_in is a source of its own (C5 §4), with its own
// detector, utterances and emitter; the emitter sends a source's events in
// the order queued, so the order per source is total and a transcription in
// flight holds back only that source's events after it, never the audio and
// never another source's.
type session struct {
	id      string
	svc     *Service
	in      *rtpin.In
	created time.Time

	// traceparent of the open call that made this session; every event
	// of the session carries it in ce_traceparent (contracts/events.md).
	traceparent string

	// language is the session's Speech-Language, passed to STT; "" = guess.
	language string
	// cfg is the service's config with what the open asked for: the ear
	// and the turn timings are the session's own.
	cfg Config
	// prompt is passed to STT with every utterance; "" = none.
	prompt string

	closeMu     sync.Mutex
	closing     bool
	closeReason string

	// sources are the senders heard now, by SSRC. The worker adds and
	// removes them; srcMu lets a close reach their detectors.
	srcMu    sync.Mutex
	sources  map[uint32]*source
	emitters sync.WaitGroup
}

// source is one sender on the session's audio_in: one RTP SSRC, heard as a
// stream of its own (C5 §4).
type source struct {
	se *session
	// name is the SSRC as 8 hex digits, the events' "source".
	name string
	det  vad.Detector

	// worker-owned state
	win  []int16 // the VAD window being filled
	cand *utterance
	// preroll is the latest audio heard while no candidate was open, at
	// most PreRoll long; a new candidate starts with it (C5 §3, Pre-roll).
	preroll []int16

	out chan func() // the source's events in order, sent by its emitter
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
		tr.res, tr.err = stt.Transcribe(ctx, stt.Door{URL: se.cfg.STTURL, Key: se.cfg.STTKey,
			Model: se.cfg.STTModel, Prompt: se.prompt}, se.cfg.HTTPClient, audio, se.language)
	}()
	return tr
}

// sourceOf is the source of ssrc, made when it is first heard.
func (se *session) sourceOf(ssrc uint32) *source {
	if src := se.sources[ssrc]; src != nil {
		return src
	}
	name := fmt.Sprintf("%08x", ssrc)
	src := &source{se: se, name: name, det: se.svc.newDetector(se.id + "-" + name), out: make(chan func(), 64)}
	se.srcMu.Lock()
	se.sources[ssrc] = src
	se.srcMu.Unlock()
	se.emitters.Add(1)
	go func() {
		defer se.emitters.Done()
		for send := range src.out {
			send()
		}
	}()
	return src
}

// end finishes a source: a started utterance is reported with what there
// is, and its emitter stops once it has sent everything queued.
func (src *source) end(ctx context.Context) {
	if src.cand != nil && src.cand.started {
		src.emitHeard(ctx, src.cand, false)
	}
	src.cand = nil
	close(src.out)
}

// dropSource lets a sender that has been silent for long leave the session
// (rtpin already forgot it): its place is free for another.
func (se *session) dropSource(ctx context.Context, ssrc uint32) {
	src := se.sources[ssrc]
	if src == nil {
		return
	}
	src.end(ctx)
	se.srcMu.Lock()
	delete(se.sources, ssrc)
	se.srcMu.Unlock()
	src.det.Forget(context.Background())
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
	// Every way a session closes (the tool, the idle timeout) gives its
	// place back; an idle close once kept it, and the limit filled up.
	se.svc.mu.Lock()
	se.svc.active--
	se.svc.mu.Unlock()
	se.srcMu.Lock()
	for _, src := range se.sources {
		src.det.Forget(context.Background())
	}
	se.srcMu.Unlock()
	se.in.Stop()
	return true
}

// worker consumes the decoded 16 kHz streams in 32 ms windows and drives
// each source's utterance state machine. It ends by finalizing every
// started utterance and, once every source's events are out, emitting the
// single speechrecog.session-closed.
func (se *session) worker(ctx context.Context) {
	frames := se.in.Out()
	idle := time.NewTicker(200 * time.Millisecond)
	defer idle.Stop()
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				for _, src := range se.sources {
					src.end(ctx)
				}
				se.emitters.Wait()
				se.cfg.Sink.Emit(ctx, Event{Type: TypeSessionClosed, Subject: se.id, Traceparent: se.traceparent,
					Data: map[string]any{"session_id": se.id, "reason": se.closeReason}})
				return
			}
			src := se.sourceOf(f.Source)
			for i, s := range f.Pcm {
				src.win = append(src.win, s)
				if len(src.win) == windowSamples {
					winEnd := f.FirstTime.Add(time.Duration(i+1) * (time.Second / 16000))
					// A window is exactly windowSamples at 16 kHz long.
					winStart := winEnd.Add(-time.Duration(windowSamples) * (time.Second / 16000))
					src.processWindow(ctx, src.win, winStart, winEnd)
					src.win = src.win[:0]
				}
			}
		case <-idle.C:
			ref := se.created
			if lp := se.in.LastPacket(); !lp.IsZero() {
				ref = lp
			}
			if time.Since(ref) >= se.cfg.IdleTimeout {
				se.beginClose("idle")
			}
			// A sender that stops sending after it spoke (silence suppressed,
			// a mouth that is quiet between phrases) closes no silence window,
			// so the utterance would wait for the next packet. No audio at all
			// from it for the merge window, with nothing left to process, ends
			// it as a silence gap would; its times stay the RTP timeline's.
			heard := se.in.Sources()
			for ssrc, src := range se.sources {
				if src.cand == nil || len(frames) != 0 {
					continue
				}
				if lp, live := heard[ssrc]; live && time.Since(lp) < se.cfg.MergeWindow {
					continue
				}
				if src.cand.started {
					src.emitHeard(ctx, src.cand, false)
				} else {
					src.closeShort(ctx, src.cand)
				}
				src.cand = nil
			}
			// A sender silent for HEARING_IDLE_TIMEOUT leaves the session's
			// set; if it speaks again it is heard anew.
			for _, ssrc := range se.in.DropIdle(time.Now().Add(-se.svc.cfg.IdleTimeout)) {
				se.dropSource(ctx, ssrc)
			}
		}
	}
}

// processWindow advances the utterance state machine with one 32 ms
// window of 16 kHz samples.
func (src *source) processWindow(ctx context.Context, win []int16, winStart, winEnd time.Time) {
	se := src.se
	speech, err := src.det.Speech(ctx, win)
	if err != nil {
		speech = false
	}
	cfg := se.cfg
	if speech {
		if src.cand != nil &&
			!winStart.Before(src.cand.lastSpeechEnd.Add(cfg.MergeWindow)) {
			// A gap of at least HEARING_MERGE_WINDOW with no audio at all
			// (the sender suppressed silence, so no silence window ever
			// closed the utterance): the same split as a silence gap.
			if src.cand.started {
				src.emitHeard(ctx, src.cand, false)
			} else {
				src.closeShort(ctx, src.cand)
			}
			src.cand = nil
		}
		if src.cand != nil && src.cand.guess != nil {
			src.cand.guess.cancel()
			src.cand.guess = nil
		}
		if src.cand == nil {
			src.cand = &utterance{id: newID("utt"), start: winStart, lastSpeechEnd: winEnd,
				audio: src.takePreroll()}
		} else {
			src.cand.lastSpeechEnd = winEnd
		}
		if !src.cand.started {
			// The burst is shorter than HEARING_ONSET: no speech at all
			// (the candidate, with its audio, is dropped on the next
			// silence window).
			if winEnd.Before(src.cand.start.Add(cfg.Onset)) {
				src.cand.audio = append(src.cand.audio, win...)
				return
			}
			src.cand.started = true
			src.emit(ctx, TypeStartOfInput, map[string]any{
				"session_id": se.id,
				"request_id": src.cand.id,
				"input_type": "speech",
				"started_at": stamp(src.cand.start),
			})
		}
		// HEARING_MAX_UTTERANCE: close the utterance there and start a
		// new one at once if the speech continues.
		cut := src.cand.start.Add(cfg.MaxUtterance)
		if !winEnd.Before(cut) {
			src.cutAt(ctx, cut, win, winStart, winEnd)
			return
		}
		src.cand.audio = append(src.cand.audio, win...)
	} else {
		switch {
		case src.cand == nil:
			// silence before any speech: kept as the next one's pre-roll
			src.keepPreroll(win)
		case !src.cand.started && !winStart.Before(src.cand.lastSpeechEnd.Add(cfg.MergeWindow)):
			// the candidate never reached the onset: a cough, a click, or a
			// word too short for the onset (C5 §3, Short)
			src.closeShort(ctx, src.cand)
			src.keepPreroll(win)
			src.cand = nil
		case !src.cand.started:
			// a pause inside the merge window does not end it before the
			// onset either (C5 §3): keep the silence with the audio
			src.cand.audio = append(src.cand.audio, win...)
		case !winStart.Before(src.cand.lastSpeechEnd.Add(cfg.MergeWindow)):
			// the speaker stopped for HEARING_MERGE_WINDOW
			src.emitHeard(ctx, src.cand, false)
			src.cand = nil
			src.keepPreroll(win)
		default:
			// still inside the merge window: keep the utterance open and
			// record the trailing silence
			src.cand.audio = append(src.cand.audio, win...)
			if src.cand.guess == nil && !winEnd.Before(src.cand.lastSpeechEnd.Add(guessAfter)) {
				src.cand.guess = se.transcribe(ctx, slices.Clone(src.cand.audio))
			}
		}
	}
}

// closeShort ends a candidate that never reached the onset. Under
// ShortTranscribe its audio goes to STT and, if words come back, it is
// reported as an utterance of its own: start-of-input and
// recognition-complete together, after the word — too late to build barge-in
// on, which is what the onset is for. Empty text or a failed transcription
// reports nothing: it may have been a cough. Under ShortDrop, nothing; its
// audio is kept as the next candidate's pre-roll.
func (src *source) closeShort(ctx context.Context, u *utterance) {
	se := src.se
	if se.cfg.ShortWords != ShortTranscribe {
		src.keepPreroll(u.audio)
		return
	}
	tr := se.transcribe(ctx, u.audio)
	src.out <- func() {
		<-tr.done
		if tr.err != nil || strings.TrimSpace(tr.res.Text) == "" {
			return
		}
		if !plausibleShort(tr.res.Text, se.cfg.Onset) {
			log.Printf("hearing: session %s source %s: a burst under the onset came back as %q, too long to be what was said; dropped",
				se.id, src.name, tr.res.Text)
			return
		}
		emit := func(typ string, data map[string]any) {
			data["session_id"] = se.id
			data["source"] = src.name
			se.cfg.Sink.Emit(ctx, Event{Type: typ, Subject: se.id, Data: data, Traceparent: se.traceparent})
		}
		emit(TypeStartOfInput, map[string]any{
			"request_id": u.id,
			"input_type": "speech",
			"started_at": stamp(u.start),
		})
		emit(TypeRecognitionComplete, map[string]any{
			"request_id":       u.id,
			"completion_cause": CauseSuccess,
			"input_type":       "speech",
			"text":             tr.res.Text,
			"language":         tr.res.Language,
			"started_at":       stamp(u.start),
			"ended_at":         stamp(u.lastSpeechEnd),
		})
	}
}

// keepPreroll appends samples to the pre-roll and keeps its last PreRoll.
func (src *source) keepPreroll(samples []int16) {
	se := src.se
	limit := int(se.cfg.PreRoll / (time.Second / 16000))
	if limit <= 0 {
		return
	}
	src.preroll = append(src.preroll, samples...)
	if over := len(src.preroll) - limit; over > 0 {
		src.preroll = append(src.preroll[:0], src.preroll[over:]...)
	}
}

// takePreroll hands the pre-roll to a new candidate and starts a new one.
func (src *source) takePreroll() []int16 {
	audio := src.preroll
	src.preroll = nil
	return audio
}

// cutAt finalizes the current utterance at cut and opens the next one
// at the same instant (which is reported started at once).
func (src *source) cutAt(ctx context.Context, cut time.Time, win []int16, winStart, winEnd time.Time) {
	se := src.se
	old := src.cand
	idx := int(cut.Sub(winStart) / (time.Second / 16000))
	if idx < 0 {
		idx = 0
	}
	if idx > len(win) {
		idx = len(win)
	}
	old.audio = append(old.audio, win[:idx]...)
	old.lastSpeechEnd = cut
	src.emitHeard(ctx, old, true)
	tail := make([]int16, len(win)-idx)
	copy(tail, win[idx:])
	src.cand = &utterance{
		id:            newID("utt"),
		started:       true,
		start:         cut,
		lastSpeechEnd: winEnd,
		audio:         tail,
	}
	src.emit(ctx, TypeStartOfInput, map[string]any{
		"session_id": se.id,
		"request_id": src.cand.id,
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
func (src *source) emitHeard(ctx context.Context, u *utterance, maxtime bool) {
	se := src.se
	tr := u.guess
	if tr == nil {
		tr = se.transcribe(ctx, u.audio)
	}
	data := map[string]any{
		"session_id":       se.id,
		"request_id":       u.id,
		"completion_cause": CauseRecognizerError,
		"input_type":       "speech",
		"text":             "",
		"language":         "",
		"started_at":       stamp(u.start),
		"ended_at":         stamp(u.lastSpeechEnd),
	}
	src.queue(ctx, TypeRecognitionComplete, data, func() {
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

func (src *source) emit(ctx context.Context, typ string, data map[string]any) {
	src.queue(ctx, typ, data, nil)
}

// queue hands an event to the emitter; before, if set, runs there first.
func (src *source) queue(ctx context.Context, typ string, data map[string]any, before func()) {
	se := src.se
	data["session_id"] = se.id
	data["source"] = src.name
	src.out <- func() {
		if before != nil {
			before()
		}
		se.cfg.Sink.Emit(ctx, Event{Type: typ, Subject: se.id, Data: data, Traceparent: se.traceparent})
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
