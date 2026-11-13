package store

import (
	"fmt"
	"time"
)

// ParseSweepPeriod reads STORE_SWEEP_PERIOD at start (CONTRACT.md
// section 2): a Go duration, as in "10m" or "1h" — how often the
// expiry and the removal of unnamed bytes run, after the pass right
// after start. An empty value is the default period. A malformed
// value is an error that names it, and it stops the start with that
// message (CONTRACT.md section 2: "a malformed value stops the
// start"), exactly as a malformed RETENTION does. A period not
// positive is no period at all: NewTicker would refuse it, so it is
// refused here.
func ParseSweepPeriod(env string) (time.Duration, error) {
	if env == "" {
		return DefaultSweepPeriod, nil
	}
	d, err := time.ParseDuration(env)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("STORE_SWEEP_PERIOD %q is malformed: want a Go duration like 10m or 1h", env)
	}
	return d, nil
}
