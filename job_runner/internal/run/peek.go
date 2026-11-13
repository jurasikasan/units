package run

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jurasikasan/units/job_runner/internal/engine"
)

// The states peek and list tell.
const (
	StateRunning = "running"
	StateEnded   = "ended"
	StateUnknown = "unknown"
)

// Glance is a run as peek sees it: never waited for, never started.
type Glance struct {
	State      string
	StartedAt  time.Time
	Elapsed    time.Duration
	OutputTail string
	Answer     *Answer // once the run has ended and its answer is put together
}

// Peek tells where the key's run is now, with the last lines of its output.
func (r *Runner) Peek(ctx context.Context, key string, lines int) (*Glance, error) {
	if !keyPattern.MatchString(key) {
		return nil, usage("key %q: letters, digits, '.', '_', '-', starting with a letter or digit, at most 128", key)
	}
	if lines == 0 {
		lines = 50
	}
	if lines < 1 || lines > 2000 {
		return nil, usage("lines %d: from 1 to 2000", lines)
	}
	c, err := r.Docker.Inspect(ctx, containerName(key))
	if errors.Is(err, engine.ErrNotFound) {
		return &Glance{State: StateUnknown}, nil
	}
	if err != nil {
		return nil, err
	}
	g := &Glance{State: StateRunning, StartedAt: c.State.StartedAt}
	switch {
	case c.State.Running:
		g.Elapsed = r.Now().Sub(c.State.StartedAt)
	case c.State.StartedAt.IsZero():
		// Created, its inputs on the way in.
	default:
		g.State = StateEnded
		g.Elapsed = c.State.FinishedAt.Sub(c.State.StartedAt)
		g.Answer = r.endedAnswer(c.ID)
	}
	if g.Elapsed < 0 {
		g.Elapsed = 0
	}
	g.Elapsed = g.Elapsed.Round(time.Millisecond)
	if !c.State.StartedAt.IsZero() {
		out, err := r.Docker.LogsTail(ctx, c.ID, lines)
		if err != nil {
			return nil, err
		}
		g.OutputTail = r.mask(normalize(string(out)))
	}
	return g, nil
}

// Listed is one run of list.
type Listed struct {
	Key       string        `json:"key"`
	State     string        `json:"state"`
	Image     string        `json:"image"`
	Network   string        `json:"network"`
	StartedAt time.Time     `json:"started_at"`
	Elapsed   time.Duration `json:"-"`
	ElapsedMs int64         `json:"elapsed_ms"`
}

// List is every run this instance holds, running or ended within its keep.
func (r *Runner) List(ctx context.Context) ([]Listed, error) {
	runs, err := r.Docker.List(ctx, labelKey)
	if err != nil {
		return nil, err
	}
	out := []Listed{}
	for _, l := range runs {
		c, err := r.Docker.Inspect(ctx, l.ID)
		if err != nil {
			continue
		}
		e := Listed{Key: l.Labels[labelKey], State: StateRunning, Image: l.Labels[labelImage],
			Network: l.Labels[labelNetwork], StartedAt: c.State.StartedAt}
		switch {
		case c.State.Running:
			e.Elapsed = r.Now().Sub(c.State.StartedAt)
		case !c.State.StartedAt.IsZero():
			e.State = StateEnded
			e.Elapsed = c.State.FinishedAt.Sub(c.State.StartedAt)
		}
		if e.Elapsed < 0 {
			e.Elapsed = 0
		}
		e.ElapsedMs = e.Elapsed.Milliseconds()
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, nil
}
