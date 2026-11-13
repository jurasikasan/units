// Package run is job_runner's operation: a script in a fresh, confined
// container, with the files the call brought, answered when it has ended
// (CONTRACT.md).
//
// A run is a container named after its key, with the run's facts in labels
// and its folder in a volume of its own. The container is the whole state:
// a restart of this process finds every run where it left it. The answer is
// read from the stopped container and handed to every caller of the key —
// the ones waiting, and any that come until the run's keep runs out; then the
// run and its folder go (watch.go). Handing an answer over removes nothing: a
// caller in between (a hub) may take it for a caller that died, and the next
// call with the key must still get it, not start the work again.
//
// Outputs and the log are sent once the run has ended, by whoever finds it
// ended first: the flight following it, or the sweep after a restart. A
// restart in the middle may send them again; a PUT of the same bytes to the
// same URL is harmless.
package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jurasikasan/units/job_runner/internal/engine"
)

// Labels on a run's container, in Docker's reverse-DNS form for a tool's
// labels; they are the run's stored facts, so their names last across
// versions. The values of env are never among them.
const (
	labelPrefix   = "io.github.jurasikasan.units.job_runner."
	labelKey      = labelPrefix + "key"
	labelDeadline = labelPrefix + "deadline" // RFC 3339: killed past it
	labelKeep     = labelPrefix + "keep"     // Go duration an undelivered answer waits
	labelVolume   = labelPrefix + "volume"
	labelImage    = labelPrefix + "image"     // the name from IMAGES
	labelNetwork  = labelPrefix + "network"   // none, internet, or a deployer's name
	labelTransfer = labelPrefix + "transfers" // JSON: what came in, what goes out
)

const (
	workDir    = "/work"
	helpersDir = "/opt/helpers"
	runUser    = "1000:1000"

	// The tail of the output an answer carries.
	tailLines = 200
	tailBytes = 16 << 10
)

var (
	keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	envName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// bash where the image has it, sh where it does not (alpine): the script is
// the first argument, never part of a label. The helpers come first on PATH;
// the image's own PATH stays as it is behind them.
var entrypoint = []string{"sh", "-c",
	`export PATH="` + helpersDir + `:$PATH"; if command -v bash >/dev/null 2>&1; then exec bash -c "$1"; fi; exec sh -c "$1"`, "job_runner"}

// The networks every instance has; NETWORKS adds the deployer's.
const (
	NetworkNone     = "none"
	NetworkInternet = "internet"
)

// Request is one call of run.
type Request struct {
	Key          string
	Script       string
	Image        string // a name from IMAGES
	Network      string
	Inputs       []Input
	Outputs      []Output
	Log          *LogTo
	Env          map[string]string
	Secrets      []string
	LimitMinutes int
	KeepMinutes  int
}

// Answer is how a run ended, and what came of it.
type Answer struct {
	ExitCode int
	Stopped  bool // by its limit
	Ran      time.Duration
	Output   string // the tail
	Changes  *Changes
	Outputs  []Sent
	Log      string   // how sending the log went; "" without one
	Notes    []string // what could not be told: a report that failed, say
}

// Image is a name a run may use and the reference behind it.
type Image struct{ Name, Ref string }

// Limits hold every run.
type Limits struct {
	Memory   int64 // bytes
	Pids     int64
	NanoCPUs int64
}

// Runner starts runs and hands out their answers.
type Runner struct {
	Docker *engine.Client
	// Images are the images a run may name; the first is the default, and
	// it also prepares each run's folder.
	Images []Image
	// Networks are the deployer's networks a run may name, name → the
	// daemon's network; none and internet are always there.
	Networks map[string]string
	Limits   Limits
	// Runtime is the OCI runtime of a run: runsc. "" is the daemon's
	// default, which only tests use.
	Runtime string
	MaxKeep time.Duration
	// Secrets are the values a run or a transfer may use by name (SECRET_<NAME>).
	Secrets map[string]string
	// TransferURLs are the prefixes an input, output or log URL must start
	// with. None: no transfers at all. A secret goes only to these.
	TransferURLs []string
	// TransferTime bounds the outputs and the log of one run, sent after it.
	TransferTime time.Duration
	HTTP         *http.Client
	// HelpersDir is bound read-only at /opt/helpers in every run; "" = none.
	HelpersDir string
	// Minute is how long a minute of limit_minutes and keep_minutes is.
	// time.Minute; tests make it short.
	Minute time.Duration
	Now    func() time.Time

	mu      sync.Mutex
	flights map[string]*flight
	ended   map[string]*ending // by container id
}

// ending is a run's answer as it is put together once it has ended.
type ending struct {
	mu     sync.Mutex
	answer *Answer
}

// flight is one key's run as this process follows it. Every caller of the
// key waits on the same flight and gets the same answer.
type flight struct {
	done    chan struct{}
	waiters int
	answer  *Answer
	err     error
}

// UsageError is a call that is wrong in itself; retrying will not help.
type UsageError struct{ msg string }

func (e *UsageError) Error() string { return e.msg }

func usage(format string, a ...any) error { return &UsageError{fmt.Sprintf(format, a...)} }

func (r *Runner) validate(req *Request) error {
	if !keyPattern.MatchString(req.Key) {
		return usage("key %q: letters, digits, '.', '_', '-', starting with a letter or digit, at most 128", req.Key)
	}
	if strings.TrimSpace(req.Script) == "" {
		return usage("script is empty")
	}
	if req.LimitMinutes == 0 {
		req.LimitMinutes = 30
	}
	if req.LimitMinutes < 1 || req.LimitMinutes > 120 {
		return usage("limit_minutes %d: from 1 to 120", req.LimitMinutes)
	}
	if req.KeepMinutes < 0 {
		return usage("keep_minutes %d: not negative", req.KeepMinutes)
	}
	for name := range req.Env {
		if !envName.MatchString(name) {
			return usage("env name %q: letters, digits and '_', not starting with a digit", name)
		}
	}
	for _, name := range req.Secrets {
		if _, ok := r.Secrets[name]; !ok {
			return usage("secret %q: this instance has none by that name", name)
		}
	}
	if req.Image == "" && len(r.Images) > 0 {
		req.Image = r.Images[0].Name
	}
	if r.imageRef(req.Image) == "" {
		return usage("image %q: not one of %s", req.Image, strings.Join(r.imageNames(), ", "))
	}
	if req.Network == "" {
		req.Network = NetworkNone
	}
	if r.dockerNetwork(req.Network) == "" {
		return usage("network %q: not one of %s", req.Network, strings.Join(r.networkNames(), ", "))
	}
	return r.validateTransfers(req)
}

func (r *Runner) imageRef(name string) string {
	for _, i := range r.Images {
		if i.Name == name {
			return i.Ref
		}
	}
	return ""
}

func (r *Runner) imageNames() []string {
	var out []string
	for _, i := range r.Images {
		out = append(out, i.Name)
	}
	return out
}

// dockerNetwork is the daemon's NetworkMode for a run's network; "" for one
// this instance does not have. internet is the daemon's own bridge, which
// the daemon's egress rule holds to public addresses.
func (r *Runner) dockerNetwork(name string) string {
	switch name {
	case NetworkNone:
		return "none"
	case NetworkInternet:
		return "bridge"
	}
	return r.Networks[name]
}

func (r *Runner) networkNames() []string {
	out := []string{NetworkNone, NetworkInternet}
	var extra []string
	for n := range r.Networks {
		extra = append(extra, n)
	}
	sort.Strings(extra)
	return append(out, extra...)
}

func (r *Runner) keep(req Request) time.Duration {
	k := time.Duration(req.KeepMinutes) * r.Minute
	if k == 0 || k > r.MaxKeep {
		return r.MaxKeep
	}
	return k
}

// Run answers when the key's run has ended. A key whose run is going, or
// ended with nobody to take its answer, is not started again: the call gets
// that run's answer, whatever its own arguments.
func (r *Runner) Run(ctx context.Context, req Request) (*Answer, error) {
	if err := r.validate(&req); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.flights == nil {
		r.flights = map[string]*flight{}
	}
	f := r.flights[req.Key]
	if f == nil {
		f = &flight{done: make(chan struct{})}
		r.flights[req.Key] = f
		go r.fly(f, req)
	}
	f.waiters++
	r.mu.Unlock()

	select {
	case <-f.done:
		return f.answer, f.err
	case <-ctx.Done():
		r.mu.Lock()
		f.waiters--
		r.mu.Unlock()
		return nil, ctx.Err()
	}
}

func containerName(key string) string { return "jr-" + key }

// fly follows the key's run to its end, independently of any caller.
func (r *Runner) fly(f *flight, req Request) {
	ctx := context.Background()
	finish := func(a *Answer, err error) {
		r.mu.Lock()
		f.answer, f.err = a, err
		delete(r.flights, req.Key)
		r.mu.Unlock()
		close(f.done)
	}

	c, err := r.Docker.Inspect(ctx, containerName(req.Key))
	if errors.Is(err, engine.ErrNotFound) {
		c, err = r.start(ctx, req)
	}
	if err != nil {
		finish(nil, err)
		return
	}
	if err := r.waitOut(ctx, c); err != nil {
		finish(nil, err)
		return
	}
	// Ended: its outputs go now, whether anyone waits or not. The answer
	// stays with the run until its keep runs out.
	finish(r.finish(ctx, c.ID))
}

// start makes the key's run: the image (pulled within the limit), its
// inputs fetched, a volume for /work owned by the run's user with the
// inputs in it, and the container, confined.
func (r *Runner) start(ctx context.Context, req Request) (*engine.Container, error) {
	begun := r.Now()
	deadline := begun.Add(time.Duration(req.LimitMinutes) * r.Minute)
	pctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	ref := r.imageRef(req.Image)
	if err := r.ensureImage(pctx, ref); err != nil {
		return nil, err
	}
	if err := r.ensureImage(pctx, r.Images[0].Ref); err != nil {
		return nil, err
	}
	in, err := r.fetchInputs(pctx, req.Inputs)
	if err != nil {
		return nil, err
	}
	defer in.close()

	tr, err := json.Marshal(transfers{Inputs: in.manifest, Outputs: req.Outputs, Log: req.Log})
	if err != nil {
		return nil, err
	}
	vol := containerName(req.Key) + "-" + randomHex(4)
	if err := r.Docker.CreateVolume(ctx, vol, map[string]string{labelKey: req.Key}); err != nil {
		return nil, fmt.Errorf("create the run's folder: %w", err)
	}
	if err := r.own(ctx, vol); err != nil {
		_ = r.Docker.RemoveVolume(ctx, vol)
		return nil, err
	}

	env := make([]string, 0, len(req.Env)+len(req.Secrets))
	for k, v := range req.Env {
		env = append(env, k+"="+v)
	}
	for _, name := range req.Secrets {
		env = append(env, name+"="+r.Secrets[name])
	}
	var ro map[string]string
	if r.HelpersDir != "" {
		ro = map[string]string{r.HelpersDir: helpersDir}
	}
	id, err := r.Docker.Create(ctx, engine.Spec{
		Name: containerName(req.Key), Image: ref,
		Entrypoint: entrypoint, Cmd: []string{req.Script},
		User: runUser, WorkDir: workDir, Env: env,
		Volume: vol, VolumeAt: workDir, ReadOnly: ro,
		Runtime: r.Runtime, Network: r.dockerNetwork(req.Network),
		DropAllCaps: true, NoNewPrivileges: true,
		PidsLimit: r.Limits.Pids, Memory: r.Limits.Memory, NanoCPUs: r.Limits.NanoCPUs,
		Labels: map[string]string{
			labelKey:      req.Key,
			labelDeadline: deadline.UTC().Format(time.RFC3339Nano),
			labelKeep:     r.keep(req).String(),
			labelVolume:   vol,
			labelImage:    req.Image,
			labelNetwork:  req.Network,
			labelTransfer: string(tr),
		},
	})
	if errors.Is(err, engine.ErrConflict) {
		// Another process started the key first: follow that run.
		_ = r.Docker.RemoveVolume(ctx, vol)
		return r.Docker.Inspect(ctx, containerName(req.Key))
	}
	if err != nil {
		_ = r.Docker.RemoveVolume(ctx, vol)
		return nil, fmt.Errorf("create the run: %w", err)
	}
	fail := func(what string, err error) (*engine.Container, error) {
		_ = r.Docker.Remove(ctx, id)
		_ = r.Docker.RemoveVolume(ctx, vol)
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	if in.tar != nil {
		if _, err := in.tar.Seek(0, 0); err != nil {
			return fail("put the inputs in", err)
		}
		if err := r.Docker.PutArchive(pctx, id, workDir, in.tar); err != nil {
			return fail("put the inputs in", err)
		}
	}
	if err := r.Docker.Start(ctx, id); err != nil {
		return fail("start the run", err)
	}
	log.Printf("run %s: started in %s on network %s with %d input(s)", req.Key, req.Image, req.Network, len(in.manifest))
	return r.Docker.Inspect(ctx, id)
}

func (r *Runner) ensureImage(ctx context.Context, image string) error {
	ok, err := r.Docker.HasImage(ctx, image)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	if err := r.Docker.Pull(ctx, image); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("pull %s: the run's limit ran out while pulling", image)
		}
		return err
	}
	return nil
}

// own gives the fresh volume to the run's user: a volume is created owned by
// root, and the run is not root. Done by a short container of the default
// image, so the run's own image needs no chown.
func (r *Runner) own(ctx context.Context, vol string) error {
	id, err := r.Docker.Create(ctx, engine.Spec{
		Image: r.Images[0].Ref, Entrypoint: []string{"chown", runUser, workDir},
		User: "0", Volume: vol, VolumeAt: workDir,
		Runtime: r.Runtime, Network: "none", NoNewPrivileges: true,
	})
	if err != nil {
		return fmt.Errorf("prepare the run's folder: %w", err)
	}
	defer func() { _ = r.Docker.Remove(context.WithoutCancel(ctx), id) }()
	if err := r.Docker.Start(ctx, id); err != nil {
		return fmt.Errorf("prepare the run's folder: %w", err)
	}
	if err := r.Docker.Wait(ctx, id); err != nil {
		return fmt.Errorf("prepare the run's folder: %w", err)
	}
	c, err := r.Docker.Inspect(ctx, id)
	if err != nil {
		return fmt.Errorf("prepare the run's folder: %w", err)
	}
	if c.State.ExitCode != 0 {
		return fmt.Errorf("prepare the run's folder: chown exited %d", c.State.ExitCode)
	}
	return nil
}

func deadlineOf(labels map[string]string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, labels[labelDeadline])
	if err != nil {
		return time.Time{} // unreadable: treat as past, so the run cannot outlive it
	}
	return t
}

// waitOut waits until the container stops, killing it at its deadline.
func (r *Runner) waitOut(ctx context.Context, c *engine.Container) error {
	deadline := deadlineOf(c.Config.Labels)
	for {
		cur, err := r.Docker.Inspect(ctx, c.ID)
		if err != nil {
			return err
		}
		if !cur.State.Running {
			return nil
		}
		if !r.Now().Before(deadline) {
			if err := r.Docker.Kill(ctx, c.ID); err != nil && !errors.Is(err, engine.ErrConflict) {
				return fmt.Errorf("stop the run at its limit: %w", err)
			}
			log.Printf("run %s: stopped by its limit", c.Config.Labels[labelKey])
			return r.Docker.Wait(ctx, c.ID)
		}
		wctx, cancel := context.WithDeadline(ctx, deadline)
		err = r.Docker.Wait(wctx, c.ID)
		cancel()
		if err != nil && wctx.Err() == nil {
			return err
		}
	}
}

// finish is a stopped run's answer: how it ended, what changed in /work,
// the outputs sent and the log. Put together once per run and kept with it.
func (r *Runner) finish(ctx context.Context, id string) (*Answer, error) {
	r.mu.Lock()
	if r.ended == nil {
		r.ended = map[string]*ending{}
	}
	e := r.ended[id]
	if e == nil {
		e = &ending{}
		r.ended[id] = e
	}
	r.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.answer != nil {
		return e.answer, nil
	}
	a, err := r.answer(ctx, id)
	if err != nil {
		return nil, err
	}
	c, err := r.Docker.Inspect(ctx, id)
	if err != nil {
		return nil, err
	}
	var t transfers
	if err := json.Unmarshal([]byte(c.Config.Labels[labelTransfer]), &t); err != nil {
		a.Notes = append(a.Notes, "the run's transfers are unreadable: no change report, no outputs")
		e.answer = a
		return a, nil
	}
	tctx, cancel := context.WithTimeout(ctx, r.TransferTime)
	defer cancel()
	ch, err := r.changes(tctx, id, t.Inputs)
	if err != nil {
		a.Notes = append(a.Notes, "no change report: "+r.mask(err.Error()))
	} else {
		a.Changes = ch
	}
	for _, o := range t.Outputs {
		for _, sent := range r.sendOutput(tctx, id, o) {
			sent.Status = r.mask(sent.Status)
			a.Outputs = append(a.Outputs, sent)
		}
	}
	if t.Log != nil {
		a.Log = r.mask(r.sendLog(tctx, id, *t.Log, a))
	}
	log.Printf("run %s: ended, %s", c.Config.Labels[labelKey], firstLine(Render(a)))
	e.answer = a
	return a, nil
}

// endedAnswer is a run's answer if it has been put together.
func (r *Runner) endedAnswer(id string) *Answer {
	r.mu.Lock()
	e := r.ended[id]
	r.mu.Unlock()
	if e == nil || !e.mu.TryLock() {
		return nil
	}
	defer e.mu.Unlock()
	return e.answer
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

// answer reads the answer from a stopped run.
func (r *Runner) answer(ctx context.Context, id string) (*Answer, error) {
	c, err := r.Docker.Inspect(ctx, id)
	if err != nil {
		return nil, err
	}
	out, err := r.Docker.Logs(ctx, id)
	if err != nil {
		return nil, err
	}
	a := &Answer{
		ExitCode: c.State.ExitCode,
		// A kill at the deadline: the run finished at or past it. A script
		// that exits by itself in the last second of its limit reads the same.
		Stopped: c.State.ExitCode == 137 && !c.State.FinishedAt.Before(deadlineOf(c.Config.Labels).Add(-time.Second)),
		Ran:     c.State.FinishedAt.Sub(c.State.StartedAt).Round(time.Millisecond),
		Output:  r.mask(tail(string(out))),
	}
	return a, nil
}

func (r *Runner) remove(ctx context.Context, id, vol string) {
	r.mu.Lock()
	delete(r.ended, id)
	r.mu.Unlock()
	if err := r.Docker.Remove(ctx, id); err != nil {
		log.Printf("remove run %s: %v", id, err)
	}
	if vol != "" {
		if err := r.Docker.RemoveVolume(ctx, vol); err != nil {
			log.Printf("remove volume %s: %v", vol, err)
		}
	}
}

// mask replaces every secret value this instance holds — not only the ones
// the run asked for: the answer may be read by a call that did not ask.
func (r *Runner) mask(s string) string {
	for _, v := range r.Secrets {
		if v != "" {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return s
}

func normalize(s string) string {
	return strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}

// tail is the end of the output: the last tailLines lines, at most tailBytes.
func tail(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}
	s = strings.Join(lines, "\n")
	if len(s) > tailBytes {
		s = s[len(s)-tailBytes:]
	}
	return s
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
