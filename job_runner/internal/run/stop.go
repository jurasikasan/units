package run

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/jurasikasan/units/job_runner/internal/engine"
)

// A stop asked for is kept as a container of its own, created and never
// started: a run's labels cannot change once it exists, and nothing is kept in
// the process. It outlives a restart between the ask and the kill, which the
// flight or the sweep then does, and it tells the answer that the run was
// ended by its caller, not by its limit or by itself. Its name cannot be a
// run's: those all start with "jr-". It goes with the run.
const labelStopOf = labelPrefix + "stop-of" // the key; not labelKey, so it is never listed as a run

func stopName(key string) string { return "jrstop-" + key }

// What a stop found.
const (
	StopStopped = "stopped" // it was running or starting; it is being ended
	StopEnded   = "ended"   // it had already ended; nothing done
	StopUnknown = "unknown" // no run by that key
)

// Stop ends the key's run now, before its limit. Its outputs and the log are
// sent as after any end, and its answer says it was stopped by its caller.
// Answers at once, without waiting for the answer.
func (r *Runner) Stop(ctx context.Context, key string) (string, error) {
	if !keyPattern.MatchString(key) {
		return "", usage("key %q: letters, digits, '.', '_', '-', starting with a letter or digit, at most 128", key)
	}
	c, err := r.Docker.Inspect(ctx, containerName(key))
	if errors.Is(err, engine.ErrNotFound) {
		return StopUnknown, nil
	}
	if err != nil {
		return "", err
	}
	if !c.State.Running && !c.State.FinishedAt.IsZero() {
		return StopEnded, nil
	}
	_, err = r.Docker.Create(ctx, engine.Spec{
		Name: stopName(key), Image: r.Images[0].Ref, Entrypoint: []string{"true"},
		Network: "none", Labels: map[string]string{labelStopOf: key},
	})
	if err != nil && !errors.Is(err, engine.ErrConflict) {
		return "", fmt.Errorf("stop %s: %w", key, err)
	}
	// Created and not started yet (its inputs going in): the kill has
	// nothing to end, and the flight kills it once started (waitOut).
	if err := r.Docker.Kill(ctx, c.ID); err != nil && !errors.Is(err, engine.ErrConflict) {
		return "", fmt.Errorf("stop %s: %w", key, err)
	}
	log.Printf("run %s: stopped by its caller", key)
	return StopStopped, nil
}

// stopAsked is whether a stop of the key's run has been asked for.
func (r *Runner) stopAsked(ctx context.Context, key string) bool {
	_, err := r.Docker.Inspect(ctx, stopName(key))
	return err == nil
}

// dropStop removes the key's stop, if any, with its run.
func (r *Runner) dropStop(ctx context.Context, key string) {
	if err := r.Docker.Remove(ctx, stopName(key)); err != nil && !errors.Is(err, engine.ErrNotFound) {
		log.Printf("remove the stop of %s: %v", key, err)
	}
}
