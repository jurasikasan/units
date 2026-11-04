package ops

import (
	"fmt"
	"strings"
	"time"

	sharedOps "github.com/jurasikasan/units/shared/ops"
)

func Format(name string, e sharedOps.Event, suppressed int, cooldown time.Duration) string {
	marker := "🔴"
	if e.Severity == sharedOps.SeverityWarn {
		marker = "🟡"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s / %s", name, marker, e.Service, e.Stage)
	if e.Entity != "" || e.EntityID != "" {
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(e.Entity + " " + e.EntityID))
	}
	b.WriteString("\n")
	b.WriteString(e.Reason)

	if suppressed > 0 {
		fmt.Fprintf(&b, "\n(+%d more in the last %s)", suppressed, humanDuration(cooldown))
	}
	return b.String()
}

func humanDuration(d time.Duration) string {
	if d >= time.Hour && d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return d.String()
}
