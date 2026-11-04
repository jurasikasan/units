package gateway

import (
	"context"
	"errors"
	"testing"
)

func TestDo_FirstBackendSucceeds(t *testing.T) {
	backends := []Backend{{Name: "a", URL: "a"}, {Name: "b", URL: "b"}}
	var calls []string

	res, err := Do(context.Background(), backends, func(_ context.Context, b Backend) (int, error) {
		calls = append(calls, b.Name)
		return 42, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Value != 42 || res.Backend != "a" {
		t.Fatalf("got value=%d backend=%q, want 42/a", res.Value, res.Backend)
	}
	if len(calls) != 1 {
		t.Fatalf("expected only primary called, got %v", calls)
	}
}

func TestDo_FallsThroughOnError(t *testing.T) {
	backends := []Backend{{Name: "a", URL: "a"}, {Name: "b", URL: "b"}}
	var calls []string

	res, err := Do(context.Background(), backends, func(_ context.Context, b Backend) (string, error) {
		calls = append(calls, b.Name)
		if b.Name == "a" {
			return "", errors.New("boom")
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Value != "ok" || res.Backend != "b" {
		t.Fatalf("got value=%q backend=%q, want ok/b", res.Value, res.Backend)
	}
	if len(calls) != 2 {
		t.Fatalf("expected both backends tried, got %v", calls)
	}
}

func TestDo_FatalStopsImmediately(t *testing.T) {
	backends := []Backend{{Name: "a", URL: "a"}, {Name: "b", URL: "b"}}
	sentinel := errors.New("bad request")
	var calls []string

	_, err := Do(context.Background(), backends, func(_ context.Context, b Backend) (int, error) {
		calls = append(calls, b.Name)
		return 0, Fatal(sentinel)
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("fatal must not fall through, got %v", calls)
	}
}

func TestDo_AllFail(t *testing.T) {
	backends := []Backend{{Name: "a", URL: "a"}, {Name: "b", URL: "b"}}
	last := errors.New("down-b")

	_, err := Do(context.Background(), backends, func(_ context.Context, b Backend) (int, error) {
		if b.Name == "b" {
			return 0, last
		}
		return 0, errors.New("down-a")
	})
	if err == nil {
		t.Fatal("expected error when all backends fail")
	}
	if !errors.Is(err, last) {
		t.Fatalf("expected wrapped last error, got %v", err)
	}
}

func TestDo_NoBackends(t *testing.T) {
	_, err := Do(context.Background(), nil, func(_ context.Context, _ Backend) (int, error) {
		return 0, nil
	})
	if err == nil {
		t.Fatal("expected error with no backends configured")
	}
}

func TestDo_FatalStaysClassifiable(t *testing.T) {
	inner := errors.New("returned 400: empty text after cleaning")
	_, err := Do(context.Background(), []Backend{{Name: "a", URL: "u"}},
		func(context.Context, Backend) (int, error) { return 0, Fatal(inner) })
	if err == nil {
		t.Fatal("want error")
	}
	if !IsFatal(err) {
		t.Fatal("fatal marker lost on the way out of Do")
	}
	if err.Error() != inner.Error() {
		t.Fatalf("message changed: %q, want %q", err, inner)
	}
}
