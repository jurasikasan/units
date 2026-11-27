package body

import (
	"context"
	"testing"
	"time"
)

type probeKey struct{}

// A call out of the body carries none of the head's request values (go-sdk
// keeps the head's protocol version there), but stops when the head's
// request stops and keeps its deadline.
func TestDetachedDropsValuesKeepsCancelAndDeadline(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.WithValue(context.Background(), probeKey{}, "2026-07-28"), time.Minute)
	defer cancelParent()
	ctx, cancel := detached(parent)
	defer cancel()
	if v := ctx.Value(probeKey{}); v != nil {
		t.Fatalf("value crossed: %v", v)
	}
	pd, _ := parent.Deadline()
	if d, ok := ctx.Deadline(); !ok || !d.Equal(pd) {
		t.Fatalf("deadline %v %v, want %v", d, ok, pd)
	}
	cancelParent()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("not cancelled with its parent")
	}
}
