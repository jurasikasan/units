package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
)

// HostConfig is what a host of brains has for all of them: where memory
// and frames are, and the defaults a spec does not name.
type HostConfig struct {
	ThreadsURL, ArtifactURL string
	// Prompt is the system prompt of a brain whose spec names none.
	Prompt string
	// ModelTimeout bounds one thought of a policy that names no timeout.
	ModelTimeout time.Duration
}

// PolicySpec is who answers a brain's thoughts (CONTRACT §2.7): a model at
// an OpenAI chat door. Timeout is a duration ("90s"); empty: the host's.
type PolicySpec struct {
	ModelURL string `json:"model_url"`
	Model    string `json:"model"`
	Key      string `json:"key,omitempty"`
	Timeout  string `json:"timeout,omitempty"`
}

// Spec is what PUT /brains/{name} says a brain is (CONTRACT §2.7). The
// knobs take the env's syntax; an empty one is the default.
type Spec struct {
	// Subject is the character's subject id: its memory is found by it.
	// Empty: derived from the name.
	Subject string `json:"subject,omitempty"`
	// Body is the body's URL (/bodies/<name> on a body host): its MCP is
	// Body + "/mcp", its stream body_<name>_events. Empty: it only thinks.
	Body string `json:"body,omitempty"`
	// Senses are the streams to read; empty: the body's stream.
	Senses []string `json:"senses,omitempty"`
	Thread string   `json:"thread,omitempty"`
	// Self is who the mind is (a name, what it looks like, its language):
	// put before the prompt, which says how a mind works.
	Self       string      `json:"self,omitempty"`
	Prompt     string      `json:"prompt,omitempty"`
	Gate       string      `json:"gate,omitempty"`
	Keep       string      `json:"keep,omitempty"`
	Senders    string      `json:"senders,omitempty"`
	BufferMax  int         `json:"buffer_max,omitempty"`
	Settle     string      `json:"settle,omitempty"`
	MaxWait    string      `json:"max_wait,omitempty"`
	Bind       string      `json:"bind,omitempty"`
	Recall     *int        `json:"recall,omitempty"`
	ActTimeout string      `json:"act_timeout,omitempty"`
	Policy     *PolicySpec `json:"policy,omitempty"`
}

// ErrSpec is a spec the host refuses (400).
var ErrSpec = errors.New("bad_spec")

// Built is a brain as its spec makes it, before it runs.
type Built struct {
	Cfg     Config
	Senses  []string
	Group   string
	BodyMCP string
	Thread  string
	Policy  *Policy
}

// Listed is a brain as GET /brains and PUT /brains/{name} answer it, its
// policy's key left out.
type Listed struct {
	Name     string      `json:"name"`
	Subject  string      `json:"subject"`
	Spec     Spec        `json:"spec"`
	Policy   *PolicySpec `json:"policy"`
	Buffered int         `json:"buffered"`
	Counts   any         `json:"counts,omitempty"`
}

type hostedBrain struct {
	mind   *Mind
	body   *Body
	spec   Spec // without its policy
	policy *PolicySpec
	counts *Counts
	cancel context.CancelFunc
	done   chan struct{}
}

// Host carries many brains in one process (CONTRACT §2.7): made, changed and
// removed through PUT and DELETE, kept nowhere. Each reads its own streams
// with a group of its own, brain_<name>.
type Host struct {
	Cfg   HostConfig
	Redis *redis.Client
	Log   *slog.Logger
	// Default is the brain made from the env, whose counts GET /health
	// reports at its top, as a brain run alone always did.
	Default string
	// Overrides adjust the default brain after its spec is built (the env's
	// BRAIN_BODY as an MCP URL, BRAIN_GROUP).
	Overrides func(*Built)

	mu     sync.Mutex
	brains map[string]*hostedBrain
	ctx    context.Context
}

// NewHost builds a host of brains; Run brings it to life.
func NewHost(cfg HostConfig, rdb *redis.Client, log *slog.Logger) *Host {
	if cfg.Prompt == "" {
		cfg.Prompt = DefaultPrompt
	}
	if cfg.ModelTimeout <= 0 {
		cfg.ModelTimeout = DefaultModelTimeout
	}
	if cfg.ThreadsURL == "" {
		cfg.ThreadsURL = DefaultThreadsURL
	}
	if cfg.ArtifactURL == "" {
		cfg.ArtifactURL = DefaultArtifactURL
	}
	return &Host{Cfg: cfg, Redis: rdb, Log: log, brains: map[string]*hostedBrain{}}
}

// Start lets the host take brains, living as long as ctx; Stop lets them go.
func (h *Host) Start(ctx context.Context) {
	h.mu.Lock()
	h.ctx = ctx
	h.mu.Unlock()
}

// Stop lets every brain go.
func (h *Host) Stop() {
	h.mu.Lock()
	all := h.brains
	h.brains = map[string]*hostedBrain{}
	h.mu.Unlock()
	for _, x := range all {
		x.cancel()
	}
	for _, x := range all {
		<-x.done
	}
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// CheckName: a brain's name goes into its group's name.
func CheckName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%q: a name is lower-case letters, digits, '_' and '-', at most 63", name)
	}
	return nil
}

func dur(s string, def time.Duration, what string) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%w: %s: %q is not a positive duration", ErrSpec, what, s)
	}
	return d, nil
}

// BuildPolicy reads a policy spec; nil stays nil (nobody answers).
func BuildPolicy(p *PolicySpec) (*Policy, error) {
	if p == nil {
		return nil, nil
	}
	u, err := url.Parse(p.ModelURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%w: policy: model_url %q is not an http(s) URL", ErrSpec, p.ModelURL)
	}
	if strings.TrimSpace(p.Model) == "" {
		return nil, fmt.Errorf("%w: policy: model is empty", ErrSpec)
	}
	timeout, err := dur(p.Timeout, 0, "policy: timeout")
	if err != nil {
		return nil, err
	}
	return &Policy{Model: Model{URL: p.ModelURL, Key: p.Key, Name: p.Model, Client: &http.Client{}}, Timeout: timeout}, nil
}

// bodyName is the body's name in a URL …/bodies/<name>, or "".
func bodyName(u string) string {
	if i := strings.LastIndex(u, "/bodies/"); i >= 0 {
		if name := u[i+len("/bodies/"):]; CheckName(name) == nil {
			return name
		}
	}
	return ""
}

// Build reads a spec into a brain, or refuses it.
func (h *Host) Build(name string, spec Spec) (Built, error) {
	var b Built
	if err := CheckName(name); err != nil {
		return b, fmt.Errorf("%w: name: %v", ErrSpec, err)
	}
	cfg := Config{Prompt: h.Cfg.Prompt, ModelTimeout: h.Cfg.ModelTimeout, Self: DerivedSubject(name)}
	if spec.Subject != "" {
		id, err := uuid.Parse(spec.Subject)
		if err != nil || id == uuid.Nil {
			return b, fmt.Errorf("%w: subject %q is not a uuid", ErrSpec, spec.Subject)
		}
		cfg.Self = id
	}
	if spec.Prompt != "" {
		cfg.Prompt = spec.Prompt
	}
	// Who the mind is comes first, then how a mind works (UNIT-133): an
	// application names its character without copying the unit's prompt.
	if self := strings.TrimSpace(spec.Self); self != "" {
		cfg.Prompt = self + "\n\n" + cfg.Prompt
	}
	var err error
	if cfg.Closed, err = ParseGate(spec.Gate); err != nil {
		return b, fmt.Errorf("%w: gate: %v", ErrSpec, err)
	}
	if cfg.Keep, cfg.KeepDefault, err = ParseKeep(spec.Keep, DefaultKeep); err != nil {
		return b, fmt.Errorf("%w: keep: %v", ErrSpec, err)
	}
	if cfg.Senders, err = ParseSenders(spec.Senders); err != nil {
		return b, fmt.Errorf("%w: senders: %v", ErrSpec, err)
	}
	cfg.BufferMax = DefaultBufferMax
	if spec.BufferMax < 0 {
		return b, fmt.Errorf("%w: buffer_max is negative", ErrSpec)
	} else if spec.BufferMax > 0 {
		cfg.BufferMax = spec.BufferMax
	}
	cfg.Recall = DefaultRecall
	if spec.Recall != nil {
		if *spec.Recall < 0 || *spec.Recall > 1000 {
			return b, fmt.Errorf("%w: recall is 0 to 1000, threads' page", ErrSpec)
		}
		cfg.Recall = *spec.Recall
	}
	if cfg.Settle, err = dur(spec.Settle, DefaultSettle, "settle"); err != nil {
		return b, err
	}
	if cfg.MaxWait, err = dur(spec.MaxWait, DefaultMaxWait, "max_wait"); err != nil {
		return b, err
	}
	if cfg.Bind, err = dur(spec.Bind, DefaultBind, "bind"); err != nil {
		return b, err
	}
	if cfg.ActTimeout, err = dur(spec.ActTimeout, DefaultActTimeout, "act_timeout"); err != nil {
		return b, err
	}
	if spec.Thread != "" {
		if _, err := uuid.Parse(spec.Thread); err != nil {
			return b, fmt.Errorf("%w: thread %q is not a uuid", ErrSpec, spec.Thread)
		}
	}
	b.Senses = spec.Senses
	if spec.Body != "" {
		base := strings.TrimRight(spec.Body, "/")
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return b, fmt.Errorf("%w: body %q is not an http(s) URL", ErrSpec, spec.Body)
		}
		b.BodyMCP = base + "/mcp"
		if len(b.Senses) == 0 {
			if n := bodyName(base); n != "" {
				b.Senses = []string{"body_" + n + "_events"}
			}
		}
	}
	if len(b.Senses) == 0 {
		return b, fmt.Errorf("%w: senses: none named, and no body at …/bodies/<name> to read", ErrSpec)
	}
	if b.Policy, err = BuildPolicy(spec.Policy); err != nil {
		return b, err
	}
	b.Cfg, b.Group, b.Thread = cfg, "brain_"+name, spec.Thread
	if h.Overrides != nil && name == h.Default {
		h.Overrides(&b)
	}
	return b, nil
}

// Put makes the brain name as spec says. A spec that differs only in its
// policy changes the policy and nothing else; another one lets the brain go
// and makes it anew (its memory found again by its subject, its group
// resumed). created: there was none.
func (h *Host) Put(ctx context.Context, name string, spec Spec) (Listed, bool, error) {
	b, err := h.Build(name, spec)
	if err != nil {
		return Listed{}, false, err
	}
	policy := spec.Policy
	spec.Policy = nil
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx == nil {
		return Listed{}, false, errors.New("the host is not running")
	}
	old := h.brains[name]
	if old != nil && reflect.DeepEqual(old.spec, spec) {
		old.mind.SetPolicy(b.Policy)
		old.policy = policy
		return h.listed(name, old, false), false, nil
	}
	streams := Streams{Client: h.Redis, Names: b.Senses, Group: b.Group, Consumer: "brain"}
	if err := streams.Ensure(ctx); err != nil {
		return Listed{}, false, err
	}
	if old != nil {
		old.cancel()
		<-old.done
		h.Log.Info("host: a brain made anew", "brain", name)
	}
	x := h.start(name, b, streams)
	x.spec, x.policy = spec, policy
	h.brains[name] = x
	if old == nil {
		h.Log.Info("host: a brain made", "brain", name, "subject", b.Cfg.Self.String(), "senses", b.Senses,
			"body", b.BodyMCP, "policy", b.Policy != nil)
	}
	return h.listed(name, x, false), old == nil, nil
}

func (h *Host) start(name string, b Built, streams Streams) *hostedBrain {
	log := h.Log.With("brain", name)
	client := &http.Client{Timeout: 30 * time.Second}
	counts := &Counts{}
	x := &hostedBrain{counts: counts, done: make(chan struct{})}
	// An act may take as long as the body takes to do it: the act's own
	// timeout bounds it, not the client's.
	if b.BodyMCP != "" {
		x.body = &Body{URL: b.BodyMCP, HTTP: &http.Client{}}
	}
	memory := &Memory{URL: h.Cfg.ThreadsURL, Client: client, Self: b.Cfg.Self, Thread: b.Thread}
	x.mind = &Mind{Cfg: b.Cfg, Store: Store{URL: h.Cfg.ArtifactURL, Client: client}, Memory: memory,
		Body: x.body, Counts: counts, Log: log}
	x.mind.SetPolicy(b.Policy)
	ctx, cancel := context.WithCancel(h.ctx)
	x.cancel = cancel
	events := make(chan Event, 64)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if id, opened, err := memory.Resolve(ctx); err != nil {
			log.Warn("memory: no thread yet, trying again at each thought", "err", err.Error())
		} else {
			log.Info("memory: thread", "thread", id, "opened", opened)
		}
		streams.Run(ctx, events, counts, log)
	}()
	go func() { defer wg.Done(); x.mind.Run(ctx, events) }()
	go func() {
		wg.Wait()
		if x.body != nil {
			x.body.Close()
		}
		close(x.done)
	}()
	return x
}

// SetPolicy changes the brain's policy alone; nil: nobody answers. false:
// there is no such brain.
func (h *Host) SetPolicy(name string, p *PolicySpec) (Listed, bool, error) {
	policy, err := BuildPolicy(p)
	if err != nil {
		return Listed{}, true, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	x := h.brains[name]
	if x == nil {
		return Listed{}, false, nil
	}
	x.mind.SetPolicy(policy)
	x.policy = p
	h.Log.Info("host: policy changed", "brain", name, "model", modelOf(p))
	return h.listed(name, x, false), true, nil
}

func modelOf(p *PolicySpec) string {
	if p == nil {
		return ""
	}
	return p.Model
}

// Delete lets the brain go. false: there was none.
func (h *Host) Delete(name string) bool {
	h.mu.Lock()
	x := h.brains[name]
	delete(h.brains, name)
	h.mu.Unlock()
	if x == nil {
		return false
	}
	x.cancel()
	<-x.done
	h.Log.Info("host: a brain let go", "brain", name)
	return true
}

// Mind is the brain called name here, or nil.
func (h *Host) Mind(name string) *Mind {
	h.mu.Lock()
	defer h.mu.Unlock()
	if x := h.brains[name]; x != nil {
		return x.mind
	}
	return nil
}

// List is every brain here, by name; full adds each one's counts.
func (h *Host) List(full bool) []Listed {
	h.mu.Lock()
	out := make([]Listed, 0, len(h.brains))
	for name, x := range h.brains {
		out = append(out, h.listed(name, x, full))
	}
	h.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func redacted(p *PolicySpec) *PolicySpec {
	if p == nil {
		return nil
	}
	c := *p
	c.Key = ""
	return &c
}

func (h *Host) listed(name string, x *hostedBrain, full bool) Listed {
	l := Listed{Name: name, Subject: x.mind.Cfg.Self.String(), Spec: x.spec, Policy: redacted(x.policy), Buffered: x.mind.Buffered()}
	if full {
		l.Counts = x.counts.Snapshot()
	}
	return l
}

// Handler serves the host's walls (CONTRACT §2.7): the brains' API and GET
// /health.
func (h *Host) Handler(version string) *http.ServeMux {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	notHere := func(w http.ResponseWriter) { http.Error(w, "not_found: no such brain here", http.StatusNotFound) }
	decode := func(w http.ResponseWriter, r *http.Request, v any) bool {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			http.Error(w, "bad_spec: "+err.Error(), http.StatusBadRequest)
			return false
		}
		return true
	}
	answer := func(w http.ResponseWriter, l Listed, ok bool, err error, okCode int) {
		switch {
		case errors.Is(err, ErrSpec):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case err != nil:
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		case !ok:
			notHere(w)
		default:
			writeJSON(w, okCode, l)
		}
	}

	mux.HandleFunc("PUT /brains/{name}", func(w http.ResponseWriter, r *http.Request) {
		var spec Spec
		if !decode(w, r, &spec) {
			return
		}
		l, created, err := h.Put(r.Context(), r.PathValue("name"), spec)
		code := http.StatusOK
		if created {
			code = http.StatusCreated
		}
		answer(w, l, true, err, code)
	})
	mux.HandleFunc("PUT /brains/{name}/policy", func(w http.ResponseWriter, r *http.Request) {
		var p *PolicySpec // JSON null: nobody answers
		if !decode(w, r, &p) {
			return
		}
		l, ok, err := h.SetPolicy(r.PathValue("name"), p)
		answer(w, l, ok, err, http.StatusOK)
	})
	mux.HandleFunc("GET /brains", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, h.List(false)) })
	mux.HandleFunc("GET /brains/{name}", func(w http.ResponseWriter, r *http.Request) {
		for _, l := range h.List(true) {
			if l.Name == r.PathValue("name") {
				writeJSON(w, http.StatusOK, l)
				return
			}
		}
		notHere(w)
	})
	mux.HandleFunc("GET /brains/{name}/policy", func(w http.ResponseWriter, r *http.Request) {
		for _, l := range h.List(false) {
			if l.Name == r.PathValue("name") {
				writeJSON(w, http.StatusOK, l.Policy)
				return
			}
		}
		notHere(w)
	})
	mux.HandleFunc("DELETE /brains/{name}", func(w http.ResponseWriter, r *http.Request) {
		if h.Delete(r.PathValue("name")) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		notHere(w)
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		brains := map[string]any{}
		out := map[string]any{"status": "ok", "version": version, "brains": brains}
		for _, l := range h.List(true) {
			brains[l.Name] = map[string]any{"buffered": l.Buffered, "counts": l.Counts}
			if l.Name == h.Default {
				out["buffered"], out["counts"] = l.Buffered, l.Counts
			}
		}
		writeJSON(w, http.StatusOK, out)
	})
	return mux
}
