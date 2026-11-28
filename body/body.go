package body

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-redis/redis/v8"
)

// Ear is where a hearing session takes sound in: send RTP to audio_in.
type Ear struct {
	AudioIn string `json:"audio_in"`
}

// Eye is where a sight session takes light in: RTP video to video_in,
// worded observations (SenML) to sight_in.
type Eye struct {
	VideoIn string `json:"video_in"`
	SightIn string `json:"sight_in,omitempty"`
}

// Self is what GET /body and the resource body://self answer: who this body
// is, what an observer perceives of it, and where its senses take input.
type Self struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Ears        []Ear  `json:"ears"`
	Eyes        []Eye  `json:"eyes"`
}

// Counts are what GET /health reports.
type Counts struct {
	Relayed  atomic.Int64 // percepts of its own sessions republished
	Dropped  atomic.Int64 // its own sessions' events of a type it does not relay
	Opened   atomic.Int64 // sense sessions opened (reopened included)
	OpenErrs atomic.Int64
	Acts     atomic.Int64
	ActErrs  atomic.Int64
	Errors   atomic.Int64 // reads, publishes, acks that failed
}

// Snapshot is the counts as JSON.
func (c *Counts) Snapshot() map[string]int64 {
	return map[string]int64{
		"relayed": c.Relayed.Load(), "dropped": c.Dropped.Load(),
		"opened": c.Opened.Load(), "open_errors": c.OpenErrs.Load(),
		"acts": c.Acts.Load(), "act_errors": c.ActErrs.Load(), "errors": c.Errors.Load(),
	}
}

// Body is one running body: one subject.
type Body struct {
	Cfg    Config
	Redis  *redis.Client
	HTTP   *http.Client // for GET /body of the bodies in earshot
	Log    *slog.Logger
	Counts *Counts

	hearing, sight *sense
	speaking       *Door

	mu      sync.Mutex
	mine    map[string]*sense              // a sense session id → its sense
	mouths  map[string]*mouth              // a listener's base URL → the speaking session aimed at its ear
	spoken  map[string]chan map[string]any // a speaking session id → its speak-completes, while an act waits
	actLock sync.Mutex                     // one act at a time: one body, one mouth
	ready   chan struct{}
}

// New builds a body from its config; Run brings it to life.
func New(cfg Config, rdb *redis.Client, client *http.Client, log *slog.Logger) *Body {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Retry <= 0 {
		cfg.Retry = DefaultRetry
	}
	if cfg.SpeakTimeout <= 0 {
		cfg.SpeakTimeout = DefaultSpeakTimeout
	}
	b := &Body{Cfg: cfg, Redis: rdb, HTTP: client, Log: log, Counts: &Counts{},
		mine: map[string]*sense{}, mouths: map[string]*mouth{}, spoken: map[string]chan map[string]any{},
		ready: make(chan struct{})}
	// A sense's session may wait on a door for as long as an act does.
	doorClient := &http.Client{Timeout: cfg.SpeakTimeout + 10*time.Second}
	if cfg.HearingURL != "" {
		b.hearing = &sense{channel: "hearing", stream: HearingStream, closed: "speechrecog.session-closed",
			door: &Door{Name: "hearing", URL: cfg.HearingURL, HTTP: doorClient}}
		// An ear in the air is always there: silence is heard, so the
		// session never idles out and the ear keeps its address (UNIT-131).
		b.hearing.args = map[string]any{"idle_timeout_ms": 0}
		if cfg.Language != "" {
			b.hearing.args["speech_language"] = cfg.Language
		}
	}
	if cfg.SightURL != "" {
		b.sight = &sense{channel: "sight", stream: SightStream, closed: "sightrecog.session-closed",
			door: &Door{Name: "sight", URL: cfg.SightURL, HTTP: doorClient}}
		// An eye that sees nothing is still an eye: it never idles out
		// either, and keeps its address.
		b.sight.args = map[string]any{"idle_timeout_ms": 0}
		if cfg.Language != "" {
			b.sight.args["language"] = cfg.Language
		}
	}
	if cfg.SpeakingURL != "" {
		b.speaking = &Door{Name: "speaking", URL: cfg.SpeakingURL, HTTP: doorClient}
	}
	return b
}

// Self is the body as GET /body tells it, its senses as they are now.
func (b *Body) Self() Self {
	s := Self{ID: b.Cfg.ID.String(), Name: b.Cfg.Name, Description: b.Cfg.Description, Ears: []Ear{}, Eyes: []Eye{}}
	if b.hearing != nil {
		if at := b.hearing.address(); at["audio_in"] != "" {
			s.Ears = append(s.Ears, Ear{AudioIn: at["audio_in"]})
		}
	}
	if b.sight != nil {
		if at := b.sight.address(); at["video_in"] != "" {
			s.Eyes = append(s.Eyes, Eye{VideoIn: at["video_in"], SightIn: at["sight_in"]})
		}
	}
	return s
}

// Ready is closed once the body is alive in its host, whose reader is
// already reading: what its senses report from then on is its.
func (b *Body) Ready() <-chan struct{} { return b.ready }

// live keeps the senses open until ctx ends, then closes its sessions and
// its mouths. The host reads the streams and hands the body what is its.
func (b *Body) live(ctx context.Context) {
	close(b.ready)
	var wg sync.WaitGroup
	for _, s := range []*sense{b.hearing, b.sight} {
		if s == nil {
			continue
		}
		wg.Add(1)
		go func(s *sense) { defer wg.Done(); b.keep(ctx, s) }(s)
	}
	<-ctx.Done()
	b.closeMouths()
	wg.Wait()
}

// owns reports whether an entry of stream about subject (a session) is this
// body's: one of its sense sessions, or one of its speaking sessions.
func (b *Body) owns(stream, subject string) bool {
	if stream == SpeechStream {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.spoken[subject]; ok {
			return true
		}
		for _, m := range b.mouths {
			if m.session == subject {
				return true
			}
		}
		return false
	}
	return b.senseOf(subject) != nil
}
