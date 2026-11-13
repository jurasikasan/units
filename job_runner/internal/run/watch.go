package run

import (
	"context"
	"log"
	"time"
)

// Watch keeps the promises nobody is waiting to keep: a run past its limit is
// killed, an answer past its keep is dropped with its run, and a volume no run
// holds any more (a start that failed halfway) is removed.
func (r *Runner) Watch(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		r.Sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Sweep is one pass of Watch.
func (r *Runner) Sweep(ctx context.Context) {
	runs, err := r.Docker.List(ctx, labelKey)
	if err != nil {
		log.Printf("watch: list runs: %v", err)
		return
	}
	now := r.Now()
	held := map[string]bool{}
	for _, l := range runs {
		held[l.Labels[labelVolume]] = true
		c, err := r.Docker.Inspect(ctx, l.ID)
		if err != nil {
			continue
		}
		key := l.Labels[labelKey]
		if c.State.Running {
			if !now.Before(deadlineOf(l.Labels)) {
				if err := r.Docker.Kill(ctx, l.ID); err == nil {
					log.Printf("watch: run %s stopped by its limit with nobody waiting", key)
				}
			}
			continue
		}
		keep, err := time.ParseDuration(l.Labels[labelKeep])
		if err != nil {
			keep = r.MaxKeep
		}
		if c.State.FinishedAt.IsZero() || now.Sub(c.State.FinishedAt) < keep || r.followed(key) {
			continue
		}
		log.Printf("watch: run %s: its answer waited %s and nobody took it; dropped", key, keep)
		r.remove(ctx, l.ID, l.Labels[labelVolume])
	}

	vols, err := r.Docker.Volumes(ctx, labelKey)
	if err != nil {
		log.Printf("watch: list volumes: %v", err)
		return
	}
	for _, v := range vols {
		// A minute of grace: a start in progress has its volume before its
		// container.
		if held[v.Name] || v.CreatedAt.IsZero() || now.Sub(v.CreatedAt) < time.Minute {
			continue
		}
		if err := r.Docker.RemoveVolume(ctx, v.Name); err == nil {
			log.Printf("watch: removed volume %s that no run holds", v.Name)
		}
	}
}

// followed is whether a caller in this process is about to take the key's
// answer: the sweep must not remove a run from under it.
func (r *Runner) followed(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.flights[key]
	return ok
}
