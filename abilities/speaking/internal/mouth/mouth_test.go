package mouth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fake TTS makes one frame per rune of the sentence, so how much of the
// text went out is countable. Expected spoken values are worked out by hand
// from the rule in CONTRACT.md §3: a word more than half played is spoken.
const text = "Добрый день. Ваш заказ готов к выдаче."

// "Добрый день." is 12 runes → 12 frames; the second sentence starts at rune 13
// and is 25 runes → 25 frames.

type fakeTTS struct {
	mu   sync.Mutex
	ctxs []context.Context
}

func (f *fakeTTS) Synth(ctx context.Context, s, _ string) ([]int16, error) {
	f.mu.Lock()
	f.ctxs = append(f.ctxs, ctx)
	f.mu.Unlock()
	return make([]int16, len([]rune(s))*FrameSamples), nil
}

func (f *fakeTTS) lastCtx() context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ctxs[len(f.ctxs)-1]
}

// sink counts frames; at frame `blockAt` it tells the test and waits for it.
type sink struct {
	mu      sync.Mutex
	n       int
	blockAt int
	reached chan struct{}
	release chan struct{}
}

func newSink(blockAt int) *sink {
	return &sink{blockAt: blockAt, reached: make(chan struct{}), release: make(chan struct{})}
}

func (s *sink) WriteFrame(pcm []int16) error {
	s.mu.Lock()
	s.n++
	n := s.n
	s.mu.Unlock()
	if len(pcm) != FrameSamples {
		panic("frame of wrong size")
	}
	if n == s.blockAt {
		close(s.reached)
		<-s.release
	}
	return nil
}

func (s *sink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

type playedLog struct {
	mu  sync.Mutex
	all []Played
	ch  chan Played
}

func newLog() *playedLog { return &playedLog{ch: make(chan Played, 16)} }

func (l *playedLog) add(p Played) {
	l.mu.Lock()
	l.all = append(l.all, p)
	l.mu.Unlock()
	l.ch <- p
}

func (l *playedLog) next(t *testing.T) Played {
	t.Helper()
	select {
	case p := <-l.ch:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no played")
		return Played{}
	}
}

func fast(t *testing.T) {
	old := FrameInterval
	FrameInterval = time.Millisecond
	t.Cleanup(func() { FrameInterval = old })
}

func TestPlayedInFull(t *testing.T) {
	fast(t)
	sk, log := newSink(-1), newLog()
	m := New(&fakeTTS{}, sk, log.add)
	defer m.Close()

	id, err := m.Speak(text, "piper_denis")
	require.NoError(t, err)
	p := log.next(t)

	assert.Equal(t, Played{RequestID: id, Spoken: text, Cause: CauseNormal}, p)
	assert.Equal(t, 12+25, sk.count())
}

func TestSaysPlayInOrder(t *testing.T) {
	fast(t)
	log := newLog()
	m := New(&fakeTTS{}, newSink(-1), log.add)
	defer m.Close()

	a, _ := m.Speak("Раз.", "v")
	b, _ := m.Speak("Два.", "v")
	assert.Equal(t, a, log.next(t).RequestID)
	assert.Equal(t, b, log.next(t).RequestID)
}

// The call drops 7 frames into the second sentence: "Ваш" + space + "зак" of
// "заказ" went out — 3 of its 5 runes, more than half, so "заказ" is spoken.
func TestCallEndsMidSay_SpokenToWordBoundary(t *testing.T) {
	fast(t)
	sk, log := newSink(12+7), newLog()
	m := New(&fakeTTS{}, sk, log.add)

	id, _ := m.Speak(text, "v")
	<-sk.reached
	done := make(chan []string)
	go func() { done <- m.Close() }()
	waitCancelled(t, m)
	close(sk.release)

	assert.Equal(t, []string{id}, <-done)
	assert.Equal(t, Played{RequestID: id, Spoken: "Добрый день. Ваш заказ", Cause: CauseCancelled}, log.next(t))
	_, err := m.Speak("ещё", "v")
	assert.ErrorIs(t, err, ErrClosed)
}

// 6 frames in: "зак" is not out yet, only "за" — 2 of 5, not spoken.
func TestLessThanHalfAWordIsNotSpoken(t *testing.T) {
	fast(t)
	sk, log := newSink(12+6), newLog()
	m := New(&fakeTTS{}, sk, log.add)

	m.Speak(text, "v") //nolint:errcheck
	<-sk.reached
	go m.Close()
	waitCancelled(t, m)
	close(sk.release)

	assert.Equal(t, "Добрый день. Ваш", log.next(t).Spoken)
}

// Closing cuts the utterance that is playing and drops the queued ones, and
// the spoken events of both come in say order.
func TestCloseCutsCurrentDropsQueued(t *testing.T) {
	fast(t)
	sk, log := newSink(12+7), newLog()
	m := New(&fakeTTS{}, sk, log.add)

	first, _ := m.Speak(text, "v")
	second, _ := m.Speak("Второе сообщение.", "v")
	<-sk.reached
	done := make(chan []string)
	go func() { done <- m.Close() }()
	waitCancelled(t, m)
	close(sk.release)

	assert.Equal(t, []string{first, second}, <-done)
	assert.Equal(t, Played{RequestID: first, Spoken: "Добрый день. Ваш заказ", Cause: CauseCancelled}, log.next(t))
	assert.Equal(t, Played{RequestID: second, Cause: CauseCancelled}, log.next(t))
}

// Once the utterance is cancelled not one more frame goes out, even though the
// ticker is ready at the same moment (the sink was held past several ticks).
// Without the ctx check before the write, select picks the tick half the time.
func TestNoFrameAfterCancel(t *testing.T) {
	fast(t)
	for i := 0; i < 30; i++ {
		sk, log := newSink(5), newLog()
		tts := &fakeTTS{}
		m := New(tts, sk, log.add)

		m.Speak(text, "v") //nolint:errcheck
		<-sk.reached
		go m.Close()
		<-tts.lastCtx().Done()
		time.Sleep(5 * FrameInterval)
		close(sk.release)
		log.next(t)

		require.Equal(t, 5, sk.count(), "iteration %d: a frame went out after the cancel", i)
	}
}

// waitCancelled waits until the utterance being played has been cancelled.
func waitCancelled(t *testing.T, m *Mouth) {
	t.Helper()
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.cur != nil && m.cur.ctx.Err() != nil
	}, 2*time.Second, time.Millisecond)
}

// Stop cuts the request that is playing and drops the queued ones like Close,
// SPEAK-COMPLETEs in speak order with the cause it was given, but the mouth goes
// on: the next Speak plays.
func TestStopCutsCurrentDropsQueuedAndGoesOn(t *testing.T) {
	fast(t)
	sk, log := newSink(12+7), newLog()
	m := New(&fakeTTS{}, sk, log.add)
	defer m.Close()

	first, _ := m.Speak(text, "v")
	second, _ := m.Speak("Второе сообщение.", "v")
	<-sk.reached
	done := make(chan []string)
	go func() { done <- m.Stop(CauseBargeIn) }()
	waitCancelled(t, m)
	close(sk.release)

	assert.Equal(t, []string{first, second}, <-done)
	assert.Equal(t, Played{RequestID: first, Spoken: "Добрый день. Ваш заказ", Cause: CauseBargeIn}, log.next(t))
	assert.Equal(t, Played{RequestID: second, Cause: CauseBargeIn}, log.next(t))

	third, err := m.Speak("Раз.", "v")
	require.NoError(t, err)
	assert.Equal(t, Played{RequestID: third, Spoken: "Раз.", Cause: CauseNormal}, log.next(t))
}

// Stop on a quiet mouth cuts nothing.
func TestStopWhenIdle(t *testing.T) {
	m := New(&fakeTTS{}, newSink(-1), newLog().add)
	defer m.Close()
	assert.Empty(t, m.Stop(CauseCancelled))
}

// failingTTS synthesises the first sentence and fails on the next.
type failingTTS struct{ n atomic.Int32 }

func (f *failingTTS) Synth(ctx context.Context, text, _ string) ([]int16, error) {
	if f.n.Add(1) > 1 {
		return nil, errors.New("tts down")
	}
	return (&fakeTTS{}).Synth(ctx, text, "")
}

// Nothing cut it, synthesis failed: SPEAK-COMPLETE says error, spoken up to the
// failure, and the mouth goes on.
func TestSynthesisFailureIsError(t *testing.T) {
	fast(t)
	log := newLog()
	m := New(&failingTTS{}, newSink(-1), log.add)
	defer m.Close()

	id, _ := m.Speak(text, "v")
	assert.Equal(t, Played{RequestID: id, Spoken: "Добрый день.", Cause: CauseError}, log.next(t))
}

// slowTTS takes d per sentence, like a real engine.
type slowTTS struct{ d time.Duration }

func (s slowTTS) Synth(ctx context.Context, text, _ string) ([]int16, error) {
	select {
	case <-time.After(s.d):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return make([]int16, len([]rune(text))*FrameSamples), nil
}

// timedSink records when each frame went out.
type timedSink struct {
	mu sync.Mutex
	at []time.Time
}

func (s *timedSink) WriteFrame([]int16) error {
	s.mu.Lock()
	s.at = append(s.at, time.Now())
	s.mu.Unlock()
	return nil
}

// The next utterance is synthesised while the current one plays: a reply said
// sentence by sentence has no gap the length of a synthesis between them.
func TestNextUtteranceIsSynthesisedWhileTheCurrentPlays(t *testing.T) {
	old := FrameInterval
	FrameInterval = 5 * time.Millisecond
	t.Cleanup(func() { FrameInterval = old })
	const synth = 150 * time.Millisecond
	sk, log := &timedSink{}, newLog()
	m := New(slowTTS{d: synth}, sk, log.add)
	defer m.Close()

	first := "Сорок рун в этой фразе, она играет долго." // 41 runes → 205 ms
	_, err := m.Speak(first, "v")
	require.NoError(t, err)
	_, err = m.Speak("Вторая.", "v")
	require.NoError(t, err)
	log.next(t)
	log.next(t)

	sk.mu.Lock()
	defer sk.mu.Unlock()
	n := len([]rune(first))
	require.Greater(t, len(sk.at), n)
	gap := sk.at[n].Sub(sk.at[n-1])
	assert.Less(t, gap, synth/2, "a gap of %v between the utterances: the second was synthesised only after the first", gap)
}
