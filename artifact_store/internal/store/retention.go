package store

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Retention is the per-space rule of CONTRACT.md section 5: for each
// space with a rule, how long a file may go without a write before it is
// removed. A space that is not in the map — set to "never", or not listed
// at all — never expires.
type Retention map[string]time.Duration

// ParseRetention reads the RETENTION value at start (CONTRACT.md section
// 5: RETENTION="docs=90d,archive=never"): a comma-separated list of
// "space=<N>d" and "space=never" items. An empty value is no rules, so
// nothing expires. A malformed item is an error that names it, and it
// stops the start with that message (CONTRACT.md section 5: "a malformed
// value stops the start with a message that names it").
func ParseRetention(env string) (Retention, error) {
	if env == "" {
		return nil, nil
	}

	rules := Retention{}
	for _, item := range strings.Split(env, ",") {
		space, period, ok := strings.Cut(item, "=")
		if !ok || space == "" {
			return nil, malformedRetention(item)
		}
		if period != "never" {
			days, ok := wholeDays(period)
			if !ok {
				return nil, malformedRetention(item)
			}
			rules[space] = days
		}
	}
	return rules, nil
}

// malformedRetention is the refusal of a RETENTION item that does not
// read as "space=<N>d" or "space=never". It names the item, so the
// message that stops the start does.
func malformedRetention(item string) error {
	return fmt.Errorf("RETENTION item %q is malformed: want space=<N>d or space=never", item)
}

// wholeDays reads "<N>d" with N a non-negative whole number of days, as
// in "90d".
func wholeDays(period string) (time.Duration, bool) {
	n, ok := strings.CutSuffix(period, "d")
	if !ok {
		return 0, false
	}
	days, err := strconv.Atoi(n)
	if err != nil || days < 0 {
		return 0, false
	}
	return time.Duration(days) * 24 * time.Hour, true
}
