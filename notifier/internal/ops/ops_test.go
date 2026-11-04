package ops

import (
	sharedOps "github.com/jurasikasan/units/shared/ops"

	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLimiter(t *testing.T) {
	l := NewLimiter(15 * time.Minute)
	base := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	now := base
	l.now = func() time.Time { return now }

	send, sup := l.Allow("k")
	assert.True(t, send, "first occurrence always goes out")
	assert.Zero(t, sup)

	for i := 0; i < 5; i++ {
		now = now.Add(time.Minute)
		send, _ = l.Allow("k")
		assert.False(t, send, "occurrence inside the cooldown is swallowed")
	}

	send, _ = l.Allow("other")
	assert.True(t, send)

	now = base.Add(16 * time.Minute)
	send, sup = l.Allow("k")
	assert.True(t, send, "cooldown expired, silence breaks")
	assert.Equal(t, 5, sup, "and carries how many were swallowed")

	now = now.Add(20 * time.Minute)
	send, sup = l.Allow("k")
	assert.True(t, send)
	assert.Zero(t, sup)
}

func TestLimiterZeroCooldownDisablesSuppression(t *testing.T) {
	l := NewLimiter(0)
	for i := 0; i < 3; i++ {
		send, sup := l.Allow("k")
		assert.True(t, send)
		assert.Zero(t, sup)
	}
}

func TestLimiterEvictsStaleKeys(t *testing.T) {

	l := NewLimiter(time.Minute)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	for i := 0; i < 50; i++ {
		l.Allow(string(rune('a' + i%26)))
	}
	require.NotEmpty(t, l.seen)

	now = now.Add(time.Hour)
	l.Allow("trigger")
	assert.Len(t, l.seen, 1, "everything older than the horizon is gone")
}

func TestFormat(t *testing.T) {
	name := "agent@home"
	e := sharedOps.Event{
		Service: "classifier", Stage: "classify", Severity: sharedOps.SeverityError,
		Entity: "document", EntityID: "7f3a", Reason: "model returned prose instead of JSON",
	}

	got := Format(name, e, 0, 15*time.Minute)
	assert.True(t, strings.HasPrefix(got, name), "every message starts with the notifier name: %q", got)
	assert.Contains(t, got, "classifier / classify")
	assert.Contains(t, got, "document 7f3a")
	assert.Contains(t, got, "model returned prose instead of JSON")
	assert.NotContains(t, got, "more in the last", "no count when nothing was suppressed")

	got = Format(name, e, 6, 15*time.Minute)
	assert.True(t, strings.HasPrefix(got, name), "the name stays first once a count is appended: %q", got)
	assert.Contains(t, got, "(+6 more in the last 15m)")

	warn := e
	warn.Severity = sharedOps.SeverityWarn
	assert.Contains(t, Format(name, e, 0, time.Minute), "🔴")
	assert.Contains(t, Format(name, warn, 0, time.Minute), "🟡", "severity must be visible at a glance")
}

func TestFormatWithoutEntity(t *testing.T) {
	e := sharedOps.Event{Service: "notifier", Stage: "boot", Severity: sharedOps.SeverityError, Reason: "no chat id"}
	got := Format("worker@office", e, 0, time.Minute)
	assert.True(t, strings.HasPrefix(got, "worker@office"), "every message starts with the notifier name: %q", got)
	assert.Contains(t, got, "notifier / boot")
	assert.Contains(t, got, "no chat id")
}
