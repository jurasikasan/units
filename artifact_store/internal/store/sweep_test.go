package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestSweepRunsAtStart pins CONTRACT.md section 5: "expiry and the
// removal of unnamed bytes run once right after start, then every
// STORE_SWEEP_PERIOD. A process that restarts often still cleans up."
// An orphan laid down by a previous life is gone shortly after
// StartSweep even though the period — long on purpose — never
// elapses: the pass runs at start, not only on the ticker.
func TestSweepRunsAtStart(t *testing.T) {
	names, b := newFakeNames(), newFakeBytes()
	m := newDedupStore(names, b)
	m.SweepPeriod = time.Hour // long enough that no ticker pass can have fired

	body := "orphan bytes from a previous life"
	sha := sha256hex([]byte(body))
	b.seed(sha, []byte(body), time.Now().Add(-time.Hour))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.StartSweep(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for b.has(sha) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b.has(sha) {
		t.Errorf("the orphan under %s survived 2s with a 1h period: no pass ran at start", sha)
	}
}

// TestParseSweepPeriod pins the read of STORE_SWEEP_PERIOD (CONTRACT.md
// section 2): a Go duration (10m, 1h), the default 1h without it, and
// a malformed value that names itself so the message that stops the
// start does.
func TestParseSweepPeriod(t *testing.T) {
	d, err := ParseSweepPeriod("10m")
	if err != nil {
		t.Fatalf("ParseSweepPeriod(10m): %v", err)
	}
	if d != 10*time.Minute {
		t.Errorf("ParseSweepPeriod(10m) = %v, want 10m", d)
	}

	d, err = ParseSweepPeriod("1h")
	if err != nil {
		t.Fatalf("ParseSweepPeriod(1h): %v", err)
	}
	if d != time.Hour {
		t.Errorf("ParseSweepPeriod(1h) = %v, want 1h", d)
	}

	// Without the variable the default period: 1h.
	d, err = ParseSweepPeriod("")
	if err != nil {
		t.Fatalf("ParseSweepPeriod(\"\"): %v", err)
	}
	if d != time.Hour {
		t.Errorf("ParseSweepPeriod(\"\") = %v, want the default 1h", d)
	}
}

// TestParseSweepPeriodRefusesMalformed: a malformed value refuses the
// whole start with a message that names the value (CONTRACT.md section
// 2), the way a malformed RETENTION item does.
func TestParseSweepPeriodRefusesMalformed(t *testing.T) {
	for _, in := range []string{"abc", "10", "h", "1h30x", "0s", "-1h"} {
		d, err := ParseSweepPeriod(in)
		if err == nil {
			t.Errorf("ParseSweepPeriod(%q) succeeded with %v, want a refusal", in, d)
			continue
		}
		if d != 0 {
			t.Errorf("ParseSweepPeriod(%q) returned %v with an error; the start must stop, not run half", in, d)
		}
		if !strings.Contains(err.Error(), in) {
			t.Errorf("the refusal of %q does not name the value: %v", in, err)
		}
	}
}
