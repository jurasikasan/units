package body

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jurasikasan/units/shared/events"
)

// actComplete is what a body reports of each act on its own stream.
const actComplete = "body.act-complete"

// mouth is a speaking session aimed at one listener's ear.
type mouth struct {
	ear     string // the audio_in it sends to
	session string // speaking's session_id
}

// Spoken is speak_aloud's answer: what went out to the bodies in earshot.
type Spoken struct {
	// Spoken is the part of the text whose sound went out (speaking's
	// spoken), the most that went out to any listener.
	Spoken string `json:"spoken"`
	// CompletionCause is speaking's for that phrase: normal when all of it
	// went out; absent when nobody was in earshot.
	CompletionCause string `json:"completion_cause,omitempty"`
	// Listeners is how many ears it was sent to.
	Listeners int `json:"listeners"`
}

// Skipped is skip_turn's answer.
type Skipped struct {
	Skipped bool `json:"skipped"`
}

// listener is a body in earshot as its GET /body tells it.
type listener struct {
	url, ear, name string
}

// listeners reads each body in earshot's GET /body for its ear now. A body
// that does not answer, or has no ear open, does not hear this phrase.
func (b *Body) listeners(ctx context.Context) []listener {
	var out []listener
	for _, base := range b.Cfg.Earshot {
		self, err := b.fetchSelf(ctx, base)
		if err != nil {
			b.Log.Warn("earshot: a body did not answer", "listener", base, "err", err.Error())
			continue
		}
		if len(self.Ears) == 0 || self.Ears[0].AudioIn == "" {
			b.Log.Info("earshot: a body has no ear open", "listener", base)
			continue
		}
		out = append(out, listener{url: base, ear: self.Ears[0].AudioIn, name: self.Name})
	}
	return out
}

func (b *Body) fetchSelf(ctx context.Context, base string) (Self, error) {
	var s Self
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SelfURL(base), nil)
	if err != nil {
		return s, err
	}
	resp, err := b.HTTP.Do(req)
	if err != nil {
		return s, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return s, err
	}
	if resp.StatusCode != http.StatusOK {
		return s, fmt.Errorf("status %d", resp.StatusCode)
	}
	return s, json.Unmarshal(raw, &s)
}

// SelfURL is where a body in earshot tells itself: an entry that is a body
// in a host (…/bodies/<name>) is read as it is; a bare base URL (a body run
// alone by env) at its GET /body.
func SelfURL(entry string) string {
	if i := strings.LastIndex(entry, "/bodies/"); i >= 0 && CheckName(entry[i+len("/bodies/"):]) == nil {
		return entry
	}
	return entry + "/body"
}

// mouthFor gives the speaking session aimed at l's ear: the one there is, or
// a new one when there is none or the listener's ear has moved (its old
// session closed first).
func (b *Body) mouthFor(ctx context.Context, l listener, traceparent string) (string, error) {
	b.mu.Lock()
	m := b.mouths[l.url]
	b.mu.Unlock()
	if m != nil && m.ear == l.ear {
		return m.session, nil
	}
	if m != nil {
		b.Log.Info("mouth: the listener's ear moved, aiming again", "listener", l.url, "from", m.ear, "to", l.ear)
		b.closeMouth(ctx, l.url, m)
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := b.speaking.Call(ctx, "open", map[string]any{"to": l.ear, "voice_name": b.Cfg.Voice}, traceparent, &out); err != nil {
		return "", err
	}
	b.mu.Lock()
	b.mouths[l.url] = &mouth{ear: l.ear, session: out.SessionID}
	b.mu.Unlock()
	b.Log.Info("mouth: aimed", "listener", l.url, "ear", l.ear, "session", out.SessionID)
	return out.SessionID, nil
}

func (b *Body) closeMouth(ctx context.Context, url string, m *mouth) {
	b.mu.Lock()
	if b.mouths[url] == m {
		delete(b.mouths, url)
	}
	b.mu.Unlock()
	if err := b.speaking.Call(ctx, "close", map[string]any{"session_id": m.session}, "", nil); err != nil {
		b.Log.Warn("mouth: close failed", "session", m.session, "err", err.Error())
	}
}

func (b *Body) closeMouths() {
	if b.speaking == nil {
		return
	}
	b.mu.Lock()
	ms := b.mouths
	b.mouths = map[string]*mouth{}
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for url, m := range ms {
		b.closeMouth(ctx, url, m)
	}
	b.speaking.Close()
}

type phrase struct {
	l         listener
	session   string
	requestID string
	done      chan map[string]any
}

// SpeakAloud says text to every body in earshot through speaking, one
// speaking session per listener, and answers once each listener's phrase is
// spoken (its speak-complete), or SpeakTimeout has passed.
func (b *Body) SpeakAloud(ctx context.Context, text, traceparent string) (Spoken, error) {
	var phrases []*phrase
	defer func() {
		b.mu.Lock()
		for _, p := range phrases {
			delete(b.spoken, p.session)
		}
		b.mu.Unlock()
	}()
	var firstErr error
	for _, l := range b.listeners(ctx) {
		p, err := b.speakTo(ctx, l, text, traceparent)
		if err != nil {
			b.Log.Warn("mouth: not spoken to a listener", "listener", l.url, "err", err.Error())
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		phrases = append(phrases, p)
	}
	if len(phrases) == 0 {
		if firstErr != nil {
			return Spoken{}, fmt.Errorf("speaking: %w", firstErr)
		}
		return Spoken{}, nil // nobody in earshot: said into the void
	}

	out := Spoken{Listeners: len(phrases)}
	deadline := time.NewTimer(b.Cfg.SpeakTimeout)
	defer deadline.Stop()
	for _, p := range phrases {
		d, ok := waitFor(ctx, deadline.C, p)
		if !ok {
			b.Log.Warn("mouth: no speak-complete in time", "listener", p.l.url, "session", p.session, "request_id", p.requestID)
			continue
		}
		spoken, _ := d["spoken"].(string)
		cause, _ := d["completion_cause"].(string)
		b.Log.Info("mouth: spoken", "listener", p.l.url, "session", p.session, "request_id", p.requestID,
			"spoken", spoken, "completion_cause", cause)
		if out.CompletionCause == "" || len(spoken) > len(out.Spoken) {
			out.Spoken, out.CompletionCause = spoken, cause
		}
	}
	if out.CompletionCause == "" {
		return out, fmt.Errorf("timeout: no phrase was reported spoken within %s", b.Cfg.SpeakTimeout)
	}
	return out, nil
}

// waitFor gives p's speak-complete once it comes; false when ctx or the
// deadline ends first. Another request's (an earlier phrase that outlived
// its act) is passed over.
func waitFor(ctx context.Context, deadline <-chan time.Time, p *phrase) (map[string]any, bool) {
	for {
		select {
		case d := <-p.done:
			if rid, _ := d["request_id"].(string); rid == p.requestID {
				return d, true
			}
		case <-deadline:
			return nil, false
		case <-ctx.Done():
			return nil, false
		}
	}
}

// speakTo aims a mouth at l and speaks text there; a speaking session that
// closed meanwhile (idle) is opened again, once.
func (b *Body) speakTo(ctx context.Context, l listener, text, traceparent string) (*phrase, error) {
	for attempt := 0; ; attempt++ {
		session, err := b.mouthFor(ctx, l, traceparent)
		if err != nil {
			return nil, err
		}
		p := &phrase{l: l, session: session, done: make(chan map[string]any, 8)}
		b.mu.Lock()
		b.spoken[session] = p.done
		b.mu.Unlock()
		var out struct {
			RequestID string `json:"request_id"`
		}
		err = b.speaking.Call(ctx, "speak", map[string]any{"session_id": session, "text": text}, traceparent, &out)
		if err == nil {
			p.requestID = out.RequestID
			return p, nil
		}
		b.mu.Lock()
		delete(b.spoken, session)
		if m := b.mouths[l.url]; m != nil && m.session == session {
			delete(b.mouths, l.url)
		}
		b.mu.Unlock()
		if attempt > 0 || !IsCode(err, "unknown_session") {
			return nil, err
		}
	}
}

// act runs one act under the body's one lock and reports it on its stream.
func (b *Body) act(ctx context.Context, tool string, args any, traceparent string, do func() (any, error)) (any, error) {
	b.actLock.Lock()
	defer b.actLock.Unlock()
	started := time.Now()
	result, err := do()
	ended := time.Now()
	b.Counts.Acts.Add(1)
	data := map[string]any{
		"tool": tool, "args": args, "result": result, "is_error": err != nil,
		"started_at": stamp(started), "ended_at": stamp(ended),
	}
	if err != nil {
		b.Counts.ActErrs.Add(1)
		data["result"] = nil
		data["error"] = err.Error()
	}
	b.Log.Info("act", "tool", tool, "args", args, "result", result, "is_error", err != nil, "act_ms", ended.Sub(started).Milliseconds())
	pub := events.Redis{Client: b.Redis, Stream: b.Cfg.Stream, Source: SourceFor(b.Cfg.Name)}
	if perr := pub.Publish(context.WithoutCancel(ctx), events.Event{Type: actComplete, Subject: b.Cfg.ID.String(),
		Data: data, Traceparent: traceparent, Time: ended}); perr != nil {
		b.Counts.Errors.Add(1)
		b.Log.Warn("act: not recorded on the stream", "tool", tool, "err", perr.Error())
	}
	return result, err
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
