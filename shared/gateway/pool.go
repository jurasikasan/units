package gateway

import (
	"context"
	"errors"
	"fmt"
	"log"
)

type Backend struct {
	Name string
	URL  string
}

type fatal struct{ err error }

func (f fatal) Error() string { return f.err.Error() }
func (f fatal) Unwrap() error { return f.err }

func Fatal(err error) error { return fatal{err: err} }

func IsFatal(err error) bool {
	var f fatal
	return errors.As(err, &f)
}

type Result[T any] struct {
	Value   T
	Backend string
}

func Do[T any](
	ctx context.Context,
	backends []Backend,
	attempt func(context.Context, Backend) (T, error),
) (Result[T], error) {
	if len(backends) == 0 {
		return Result[T]{}, errors.New("no backends configured")
	}

	var lastErr error
	for _, b := range backends {
		v, err := attempt(ctx, b)
		if err == nil {
			return Result[T]{Value: v, Backend: b.Name}, nil
		}

		if IsFatal(err) {
			return Result[T]{}, err
		}

		log.Printf("[gateway] backend %q failed: %v — falling through", b.Name, err)
		lastErr = err
	}

	return Result[T]{}, fmt.Errorf("all %d backend(s) failed, last error: %w", len(backends), lastErr)
}
