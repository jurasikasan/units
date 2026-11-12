// Package mouth speaks into one session: utterances queue FIFO, each is
// synthesised sentence by sentence and played as 20 ms frames in real time, and
// each ends in exactly one Played that credits what actually went out
// (speech/CONTRACT.md §2.2, §2.3, §3 "spoken").
package mouth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"sync"
	"time"
)

// FrameSamples is one 20 ms frame at 48 kHz.
const FrameSamples = 960

// FrameInterval is the playout pace; a variable only so tests can run faster.
var FrameInterval = 20 * time.Millisecond

// TTS turns one sentence into 48 kHz mono PCM.
type TTS interface {
	Synth(ctx context.Context, text, voice string) ([]int16, error)
}

// Sink takes one 20 ms frame of PCM; the session's RTP sender behind it.
type Sink interface {
	WriteFrame(pcm []int16) error
}

// Completion causes of SPEAK-COMPLETE (RFC 6787 §8.4.4) this mouth reports.
const (
	CauseNormal    = "normal"    // 000: the whole text went out
	CauseBargeIn   = "barge-in"  // 001: cut by BARGE-IN-OCCURRED
	CauseError     = "error"     // 004: synthesis or the sink failed
	CauseCancelled = "cancelled" // 007: cut by STOP, or the session closed
)

// Played is what one request amounted to: its SPEAK-COMPLETE.
type Played struct {
	RequestID string
	Spoken    string
	Cause     string
}

var ErrClosed = errors.New("mouth closed")

// Request is one utterance to speak and how.
type Request struct {
	Text, Voice string
	// SynthesisTimeout is the longest the line waits for a piece of this
	// utterance's audio: when the next piece is due and not synthesised within
	// it, the utterance ends there, cause error, and the next one plays. Zero
	// waits as long as synthesis takes.
	SynthesisTimeout time.Duration
}

type utterance struct {
	id, text, voice string
	synthTimeout    time.Duration
	ctx             context.Context
	cancel          context.CancelFunc
	done            chan struct{}
	// sentences and their synthesis, started by synthesize: for the utterance
	// being played, and for the next in the queue while this one plays.
	sentences []sentence
	ready     chan synthesized
	// cutCause is why a cut stopped it; set under Mouth.mu before cancel.
	cutCause string
}

type Mouth struct {
	tts      TTS
	sink     Sink
	onPlayed func(Played)

	mu     sync.Mutex
	queue  []*utterance
	cur    *utterance
	closed bool
	wake   chan struct{}
	exited chan struct{}
}

func New(tts TTS, sink Sink, onPlayed func(Played)) *Mouth {
	m := &Mouth{tts: tts, sink: sink, onPlayed: onPlayed, wake: make(chan struct{}, 1), exited: make(chan struct{})}
	go m.loop()
	return m
}

// Speak queues a text with no synthesis timeout and returns its request_id.
func (m *Mouth) Speak(text, voice string) (string, error) {
	return m.SpeakWith(Request{Text: text, Voice: voice})
}

// SpeakWith queues a request and returns its request_id.
func (m *Mouth) SpeakWith(r Request) (string, error) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &utterance{
		id: newUtteranceID(), text: r.Text, voice: r.Voice, synthTimeout: r.SynthesisTimeout,
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		return "", ErrClosed
	}
	m.queue = append(m.queue, s)
	if m.cur != nil && len(m.queue) == 1 {
		m.synthesize(s)
	}
	select {
	case m.wake <- struct{}{}:
	default:
	}
	m.mu.Unlock()
	return s.id, nil
}

// Idle reports whether nothing is queued and nothing is playing.
func (m *Mouth) Idle() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.queue) == 0 && m.cur == nil
}

// Close ends the mouth for good: the session is over. Says after Close are
// refused.
func (m *Mouth) Close() []string {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	cur, dropped := m.cur, m.queue
	m.queue = nil
	close(m.wake)
	m.mu.Unlock()
	ids := m.cut(cur, dropped, CauseCancelled)
	<-m.exited
	return ids
}

// Stop cuts what is playing and drops what is queued, each with its Played in
// speak order and the given cause (cancelled for STOP, barge-in for
// BARGE-IN-OCCURRED), and returns their ids once all are out. The mouth goes
// on: a later Speak plays.
func (m *Mouth) Stop(cause string) []string {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	cur, dropped := m.cur, m.queue
	m.queue = nil
	m.mu.Unlock()
	return m.cut(cur, dropped, cause)
}

func (m *Mouth) cut(cur *utterance, dropped []*utterance, cause string) []string {
	var ids []string
	if cur != nil {
		m.mu.Lock()
		cur.cutCause = cause
		m.mu.Unlock()
		cur.cancel()
		<-cur.done
		ids = append(ids, cur.id)
	}
	for _, s := range dropped {
		s.cancel()
		m.onPlayed(Played{RequestID: s.id, Cause: cause})
		ids = append(ids, s.id)
	}
	return ids
}

func (m *Mouth) loop() {
	defer close(m.exited)
	for {
		m.mu.Lock()
		if len(m.queue) == 0 {
			closed := m.closed
			m.mu.Unlock()
			if closed {
				return
			}
			if _, ok := <-m.wake; !ok {
				return
			}
			continue
		}
		s := m.queue[0]
		m.queue = m.queue[1:]
		m.cur = s
		m.synthesize(s)
		if len(m.queue) > 0 {
			m.synthesize(m.queue[0])
		}
		m.mu.Unlock()

		spoken, full := m.play(s)

		m.mu.Lock()
		m.cur = nil
		cause := CauseNormal
		if !full {
			// cut by Stop or Close, or else synthesis or the sink failed
			if cause = s.cutCause; cause == "" {
				cause = CauseError
			}
		}
		m.mu.Unlock()
		m.onPlayed(Played{RequestID: s.id, Spoken: spoken, Cause: cause})
		s.cancel()
		close(s.done)
	}
}

type synthesized struct {
	pcm []int16
	err error
}

// synthesize starts an utterance's synthesis, once. Its sentences are
// synthesised one ahead of playback: the next sentence while the current one
// plays, and — started for the head of the queue — the next utterance's first
// sentence while the current utterance plays. One goroutine for both would leave
// a gap the length of a synthesis between sentences, or between utterances when
// a driver says a reply sentence by sentence. Called with m.mu held.
func (m *Mouth) synthesize(s *utterance) {
	if s.ready != nil {
		return
	}
	s.sentences = splitSentences(s.text)
	ready := make(chan synthesized, 1)
	s.ready = ready
	ctx, sentences := s.ctx, s.sentences
	go func() {
		defer close(ready)
		for _, sn := range sentences {
			pcm, err := m.tts.Synth(ctx, sn.text, s.voice)
			select {
			case ready <- synthesized{pcm, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
}

// play speaks one utterance and returns what of it was spoken.
func (m *Mouth) play(s *utterance) (string, bool) {
	sentences, ready := s.sentences, s.ready
	if len(sentences) == 0 {
		return "", true
	}
	ctx := s.ctx

	credit := credited{text: s.text}
	ticker := time.NewTicker(FrameInterval)
	defer ticker.Stop()

	for i, sn := range sentences {
		got, ok := await(ctx, ready, s.synthTimeout)
		if !ok || got.err != nil {
			switch {
			case got.err != nil:
				log.Printf("[mouth %s] tts: %v", s.id, got.err)
			case ctx.Err() == nil:
				log.Printf("[mouth %s] tts: no audio for sentence %d within %s", s.id, i+1, s.synthTimeout)
			}
			return credit.spoken(), false
		}
		total := (len(got.pcm) + FrameSamples - 1) / FrameSamples
		for f := 0; f < total; f++ {
			// Both cases of a select can be ready at once and Go picks one at
			// random: without this check a frame leaks after a cancel.
			if ctx.Err() != nil {
				credit.partial(sn, f, total)
				return credit.spoken(), false
			}
			select {
			case <-ctx.Done():
				credit.partial(sn, f, total)
				return credit.spoken(), false
			case <-ticker.C:
			}
			if ctx.Err() != nil {
				credit.partial(sn, f, total)
				return credit.spoken(), false
			}
			if err := m.sink.WriteFrame(frame(got.pcm, f)); err != nil {
				log.Printf("[mouth %s] write: %v", s.id, err)
				credit.partial(sn, f, total)
				return credit.spoken(), false
			}
		}
		credit.upTo = sn.end
		if i == len(sentences)-1 {
			return s.text, true
		}
	}
	return s.text, true
}

// await is the next synthesised sentence; ok is false when ctx ends first, the
// synthesis gave up, or timeout (if not zero) passes with nothing ready. The
// clock starts when playback needs the sentence: time it spent synthesising
// while the one before played is not counted against it.
func await(ctx context.Context, ready <-chan synthesized, timeout time.Duration) (synthesized, bool) {
	var expired <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		expired = t.C
	}
	select {
	case got, ok := <-ready:
		return got, ok
	case <-ctx.Done():
	case <-expired:
	}
	return synthesized{}, false
}

func frame(pcm []int16, f int) []int16 {
	start := f * FrameSamples
	end := start + FrameSamples
	if end <= len(pcm) {
		return pcm[start:end]
	}
	out := make([]int16, FrameSamples)
	copy(out, pcm[start:])
	return out
}

func newUtteranceID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "utt-" + hex.EncodeToString(b[:])
}
