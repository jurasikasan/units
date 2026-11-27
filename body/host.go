package body

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
)

// HostConfig is what a host of bodies has for all of them: the units a body
// may be given (an empty URL is a sense, or a mouth, no body here can
// have), its consumer group on their streams, and the bodies' timings.
type HostConfig struct {
	HearingURL, SightURL, SpeakingURL string
	// Group is the host's consumer group on the senses' and speaking's
	// streams: one per running host, never shared by two.
	Group        string
	SpeakTimeout time.Duration
	Retry        time.Duration
}

// Spec is what PUT /bodies/{name} says a body is (CONTRACT §2.6). The units
// its senses and mouth are made of are the host's; a spec names which.
type Spec struct {
	// ID is the subject's uuid; empty: derived from the name.
	ID string `json:"id,omitempty"`
	// Description is what an observer perceives of it.
	Description string `json:"description,omitempty"`
	// Senses it has: hearing, sight.
	Senses []string `json:"senses,omitempty"`
	// Voice is speaking's voice_name; a body with a voice has a mouth.
	Voice string `json:"voice,omitempty"`
	// Language is the senses' session language, ISO 639-1; empty: they guess.
	Language string `json:"language,omitempty"`
	// Earshot is the bodies that hear it: their URLs, /bodies/<name> on a
	// host, or a body run alone by its base URL.
	Earshot []string `json:"earshot,omitempty"`
}

// ErrSpec is a spec the host refuses (400).
var ErrSpec = errors.New("bad_spec")

// Listed is a body as GET /bodies and PUT /bodies/{name} answer it: its
// spec and its addresses, relative to the host.
type Listed struct {
	Name   string `json:"name"`
	ID     string `json:"id"`
	Spec   Spec   `json:"spec"`
	Body   string `json:"body"`
	MCP    string `json:"mcp"`
	Events string `json:"events"`
}

type hosted struct {
	body   *Body
	spec   Spec
	mcp    http.Handler
	cancel context.CancelFunc
	done   chan struct{}
}

// Host carries many bodies in one process (CONTRACT §2.6): made, changed and
// removed through PUT and DELETE, kept nowhere — after a restart it has none
// until they are declared again. One reader takes the senses' and speaking's
// streams for all of them and hands each entry to the body whose session it
// is about.
type Host struct {
	Cfg   HostConfig
	Redis *redis.Client
	HTTP  *http.Client
	Log   *slog.Logger
	// Default is the body served at /mcp and GET /body too: one made from
	// the env, as a body run alone always was.
	Default string
	// Overrides adjust a body's config after its spec is read (the env's
	// BODY_STREAM for the default body).
	Overrides func(*Config)

	Foreign atomic.Int64 // entries about nobody's session here
	Errors  atomic.Int64 // reads and acks that failed

	mu     sync.Mutex
	bodies map[string]*hosted
	ctx    context.Context
	ready  chan struct{}
}

// NewHost builds a host; Run brings it to life.
func NewHost(cfg HostConfig, rdb *redis.Client, client *http.Client, log *slog.Logger) *Host {
	if cfg.Retry <= 0 {
		cfg.Retry = DefaultRetry
	}
	if cfg.SpeakTimeout <= 0 {
		cfg.SpeakTimeout = DefaultSpeakTimeout
	}
	if cfg.Group == "" {
		cfg.Group = DefaultHostGroup
	}
	return &Host{Cfg: cfg, Redis: rdb, HTTP: client, Log: log, bodies: map[string]*hosted{}, ready: make(chan struct{})}
}

// Ready is closed once the host reads its streams and takes bodies.
func (h *Host) Ready() <-chan struct{} { return h.ready }

func (h *Host) streams() []string {
	var out []string
	if h.Cfg.HearingURL != "" {
		out = append(out, HearingStream)
	}
	if h.Cfg.SightURL != "" {
		out = append(out, SightStream)
	}
	if h.Cfg.SpeakingURL != "" {
		out = append(out, SpeechStream)
	}
	return out
}

// Run makes the consumer groups, reads until ctx ends, then lets every body
// go (their sessions closed) and returns.
func (h *Host) Run(ctx context.Context) error {
	streams := h.streams()
	if err := ensureGroups(ctx, h.Redis, streams, h.Cfg.Group); err != nil {
		return err
	}
	h.mu.Lock()
	h.ctx = ctx
	h.mu.Unlock()
	done := make(chan struct{})
	go func() { defer close(done); h.read(ctx, streams) }()
	close(h.ready)
	<-ctx.Done()
	<-done
	h.mu.Lock()
	all := h.bodies
	h.bodies = map[string]*hosted{}
	h.mu.Unlock()
	for _, x := range all {
		x.cancel()
	}
	for _, x := range all {
		<-x.done
	}
	return nil
}

// config turns a spec into a body's config, or refuses it.
func (h *Host) config(name string, spec Spec) (Config, Spec, error) {
	if err := CheckName(name); err != nil {
		return Config{}, spec, fmt.Errorf("%w: name: %v", ErrSpec, err)
	}
	cfg := Config{Name: name, Description: strings.TrimSpace(spec.Description), Language: spec.Language,
		Stream: StreamFor(name), Group: h.Cfg.Group, SpeakTimeout: h.Cfg.SpeakTimeout, Retry: h.Cfg.Retry}
	spec.Description = cfg.Description
	cfg.ID = DerivedID(name)
	if spec.ID != "" {
		id, err := uuid.Parse(spec.ID)
		if err != nil || id == uuid.Nil {
			return Config{}, spec, fmt.Errorf("%w: id %q is not a uuid", ErrSpec, spec.ID)
		}
		cfg.ID, spec.ID = id, id.String()
	}
	senses := slices.Clone(spec.Senses)
	sort.Strings(senses)
	senses = slices.Compact(senses)
	for _, s := range senses {
		switch {
		case s == "hearing" && h.Cfg.HearingURL != "":
			cfg.HearingURL = h.Cfg.HearingURL
		case s == "sight" && h.Cfg.SightURL != "":
			cfg.SightURL = h.Cfg.SightURL
		case s == "hearing" || s == "sight":
			return Config{}, spec, fmt.Errorf("%w: senses: this host has no %s", ErrSpec, s)
		default:
			return Config{}, spec, fmt.Errorf("%w: senses: %q is neither hearing nor sight", ErrSpec, s)
		}
	}
	spec.Senses = senses
	if spec.Voice != "" {
		if h.Cfg.SpeakingURL == "" {
			return Config{}, spec, fmt.Errorf("%w: voice: this host has no speaking, so no mouth", ErrSpec)
		}
		cfg.SpeakingURL, cfg.Voice = h.Cfg.SpeakingURL, spec.Voice
	}
	earshot, err := ParseEarshot(strings.Join(spec.Earshot, ","))
	if err != nil {
		return Config{}, spec, fmt.Errorf("%w: earshot: %v", ErrSpec, err)
	}
	cfg.Earshot, spec.Earshot = earshot, earshot
	if h.Overrides != nil && name == h.Default {
		h.Overrides(&cfg)
	}
	return cfg, spec, nil
}

// Put makes the body name as spec says. The same spec again changes
// nothing; another one lets the body go and makes it anew (its stream stays,
// its senses' sessions are opened again). created: there was none.
func (h *Host) Put(name string, spec Spec) (Listed, bool, error) {
	cfg, spec, err := h.config(name, spec)
	if err != nil {
		return Listed{}, false, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx == nil {
		return Listed{}, false, errors.New("the host is not running")
	}
	old := h.bodies[name]
	if old != nil && reflect.DeepEqual(old.spec, spec) {
		return listed(name, old), false, nil
	}
	if old != nil {
		old.cancel()
		<-old.done
		h.Log.Info("host: a body made anew", "body", name)
	}
	b := New(cfg, h.Redis, h.HTTP, h.Log.With("body", name))
	ctx, cancel := context.WithCancel(h.ctx)
	x := &hosted{body: b, spec: spec, mcp: b.mcpHandler(), cancel: cancel, done: make(chan struct{})}
	go func() { defer close(x.done); b.live(ctx) }()
	<-b.Ready()
	h.bodies[name] = x
	if old == nil {
		h.Log.Info("host: a body made", "body", name, "id", cfg.ID.String(), "senses", spec.Senses,
			"mouth", cfg.SpeakingURL != "", "earshot", spec.Earshot)
	}
	return listed(name, x), old == nil, nil
}

// Delete lets the body go: its senses' sessions and mouths closed. false:
// there was none.
func (h *Host) Delete(name string) bool {
	h.mu.Lock()
	x := h.bodies[name]
	delete(h.bodies, name)
	h.mu.Unlock()
	if x == nil {
		return false
	}
	x.cancel()
	<-x.done
	h.Log.Info("host: a body let go", "body", name)
	return true
}

// Body is the body called name here, or nil.
func (h *Host) Body(name string) *Body {
	if x := h.get(name); x != nil {
		return x.body
	}
	return nil
}

func (h *Host) get(name string) *hosted {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.bodies[name]
}

// List is every body here, by name.
func (h *Host) List() []Listed {
	h.mu.Lock()
	out := make([]Listed, 0, len(h.bodies))
	for name, x := range h.bodies {
		out = append(out, listed(name, x))
	}
	h.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func listed(name string, x *hosted) Listed {
	at := "/bodies/" + name
	return Listed{Name: name, ID: x.body.Cfg.ID.String(), Spec: x.spec, Body: at, MCP: at + "/mcp", Events: at + "/events"}
}

// owner is the body whose session an entry is about, or nil.
func (h *Host) owner(stream, subject string) *Body {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, x := range h.bodies {
		if x.body.owns(stream, subject) {
			return x.body
		}
	}
	return nil
}

// read takes what is new on the senses' and speaking's streams until ctx
// ends, hands each entry to the body whose session it is about, and
// acknowledges it.
func (h *Host) read(ctx context.Context, streams []string) {
	if len(streams) == 0 {
		return
	}
	args := make([]string, 0, 2*len(streams))
	args = append(args, streams...)
	for range streams {
		args = append(args, ">")
	}
	for ctx.Err() == nil {
		res, err := h.Redis.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group: h.Cfg.Group, Consumer: "body", Streams: args, Count: 64, Block: time.Second,
		}).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			h.Errors.Add(1)
			h.Log.Warn("streams: read failed, trying again", "err", err.Error())
			if strings.HasPrefix(err.Error(), "NOGROUP") {
				_ = ensureGroups(ctx, h.Redis, streams, h.Cfg.Group)
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
				subject, _ := msg.Values["ce_subject"].(string)
				if b := h.owner(st.Stream, subject); b != nil {
					b.take(ctx, st.Stream, msg.Values)
				} else if st.Stream != SpeechStream {
					h.Foreign.Add(1)
				}
				actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
				if err := h.Redis.XAck(actx, st.Stream, h.Cfg.Group, msg.ID).Err(); err != nil {
					h.Errors.Add(1)
					h.Log.Warn("streams: ack failed", "stream", st.Stream, "entry", msg.ID, "err", err.Error())
				}
				cancel()
			}
		}
	}
}

// Handler serves the host's walls (CONTRACT §2.3–2.6): the bodies' API, each
// body under /bodies/{name}/, GET /health; and for a default body, /mcp and
// GET /body as a body run alone.
func (h *Host) Handler() *http.ServeMux {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	notHere := func(w http.ResponseWriter) { http.Error(w, "not_found: no such body here", http.StatusNotFound) }

	mux.HandleFunc("PUT /bodies/{name}", func(w http.ResponseWriter, r *http.Request) {
		var spec Spec
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&spec); err != nil {
			http.Error(w, "bad_spec: "+err.Error(), http.StatusBadRequest)
			return
		}
		l, created, err := h.Put(r.PathValue("name"), spec)
		switch {
		case errors.Is(err, ErrSpec):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case err != nil:
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		case created:
			writeJSON(w, http.StatusCreated, l)
		default:
			writeJSON(w, http.StatusOK, l)
		}
	})
	mux.HandleFunc("GET /bodies", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, h.List()) })
	mux.HandleFunc("GET /bodies/{name}", func(w http.ResponseWriter, r *http.Request) {
		if b := h.Body(r.PathValue("name")); b != nil {
			writeJSON(w, http.StatusOK, b.Self())
			return
		}
		notHere(w)
	})
	mux.HandleFunc("DELETE /bodies/{name}", func(w http.ResponseWriter, r *http.Request) {
		if h.Delete(r.PathValue("name")) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		notHere(w)
	})
	mux.HandleFunc("/bodies/{name}/mcp", func(w http.ResponseWriter, r *http.Request) {
		if x := h.get(r.PathValue("name")); x != nil {
			x.mcp.ServeHTTP(w, r)
			return
		}
		notHere(w)
	})
	mux.HandleFunc("GET /bodies/{name}/events", func(w http.ResponseWriter, r *http.Request) {
		if b := h.Body(r.PathValue("name")); b != nil {
			b.serveEvents(w, r)
			return
		}
		notHere(w)
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		bodies := map[string]map[string]int64{}
		for _, l := range h.List() {
			if b := h.Body(l.Name); b != nil {
				bodies[l.Name] = b.Counts.Snapshot()
			}
		}
		out := map[string]any{"status": "ok", "version": Version, "bodies": bodies,
			"foreign": h.Foreign.Load(), "errors": h.Errors.Load()}
		if b := h.Body(h.Default); b != nil {
			counts := b.Counts.Snapshot()
			counts["foreign"] = h.Foreign.Load()
			counts["errors"] += h.Errors.Load()
			out["self"], out["counts"] = b.Self(), counts
		}
		writeJSON(w, http.StatusOK, out)
	})
	if h.Default != "" {
		mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
			if x := h.get(h.Default); x != nil {
				x.mcp.ServeHTTP(w, r)
				return
			}
			notHere(w)
		})
		mux.HandleFunc("GET /body", func(w http.ResponseWriter, _ *http.Request) {
			if b := h.Body(h.Default); b != nil {
				writeJSON(w, http.StatusOK, b.Self())
				return
			}
			notHere(w)
		})
	}
	return mux
}
