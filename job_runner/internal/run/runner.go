// Package run is job_runner's one operation: a script in a fresh container,
// in an empty folder, answered when it has ended (CONTRACT.md).
//
// A run is a container named after its key, with the run's facts in labels
// and its folder in a volume of its own. The container is the whole state:
// a restart of this process finds every run where it left it. The answer is
// read from the stopped container and handed to every caller of the key —
// the ones waiting, and any that come until the run's keep runs out; then the
// run and its folder go (watch.go). Handing an answer over removes nothing: a
// caller in between (a hub) may take it for a caller that died, and the next
// call with the key must still get it, not start the work again (BOT-433).
package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jurasikasan/units/job_runner/internal/engine"
)

// Labels on a run's container. The values of env are never among them.
const (
	labelKey      = "job_runner.key"
	labelDeadline = "job_runner.deadline" // RFC 3339: killed past it
	labelKeep     = "job_runner.keep"     // Go duration an undelivered answer waits
	labelVolume   = "job_runner.volume"
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

// Request is one call of run.
type Request struct {
	Key          string
	Script       string
	Image        string
	Env          map[string]string
	Secrets      []string
	LimitMinutes int
	KeepMinutes  int
}

// Answer is how a run ended.
type Answer struct {
	ExitCode int
	Stopped  bool // by its limit
	Ran      time.Duration
	Output   string // the tail
}

// Runner starts runs and hands out their answers.
type Runner struct {
	Docker       *engine.Client
	DefaultImage string
	MaxKeep      time.Duration
	// Secrets are the values a run may ask for by name (SECRET_<NAME>).
	Secrets map[string]string
	// HelpersDir is bound read-only at /opt/helpers in every run; "" = none.
	HelpersDir string
	// Minute is how long a minute of limit_minutes and keep_minutes is.
	// time.Minute; tests make it short.
	Minute time.Duration
	Now    func() time.Time

	mu      sync.Mutex
	flights map[string]*flight
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
	if req.Image == "" {
		req.Image = r.DefaultImage
	}
	return nil
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

	// Ended. The answer stays with the run until its keep runs out.
	r.mu.Lock()
	waiting := f.waiters
	r.mu.Unlock()
	if waiting == 0 {
		log.Printf("run %s: ended with nobody waiting; its answer waits for the next call with the key", req.Key)
		finish(nil, nil)
		return
	}
	a, err := r.answer(ctx, c.ID)
	finish(a, err)
}

// start makes the key's run: the image (pulled within the limit), a volume
// for /work owned by the run's user, and the container.
func (r *Runner) start(ctx context.Context, req Request) (*engine.Container, error) {
	begun := r.Now()
	deadline := begun.Add(time.Duration(req.LimitMinutes) * r.Minute)
	pctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := r.ensureImage(pctx, req.Image); err != nil {
		return nil, err
	}
	if err := r.ensureImage(pctx, r.DefaultImage); err != nil {
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
		Name: containerName(req.Key), Image: req.Image,
		Entrypoint: entrypoint, Cmd: []string{req.Script},
		User: runUser, WorkDir: workDir, Env: env,
		Volume: vol, VolumeAt: workDir, ReadOnly: ro,
		Labels: map[string]string{
			labelKey:      req.Key,
			labelDeadline: deadline.UTC().Format(time.RFC3339Nano),
			labelKeep:     r.keep(req).String(),
			labelVolume:   vol,
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
	if err := r.Docker.Start(ctx, id); err != nil {
		_ = r.Docker.Remove(ctx, id)
		_ = r.Docker.RemoveVolume(ctx, vol)
		return nil, fmt.Errorf("start the run: %w", err)
	}
	log.Printf("run %s: started in %s", req.Key, req.Image)
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
		Image: r.DefaultImage, Entrypoint: []string{"chown", runUser, workDir},
		User: "0", Volume: vol, VolumeAt: workDir,
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
