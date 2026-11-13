package run

import (
	"context"
	"testing"
	"time"

	"github.com/jurasikasan/units/job_runner/internal/engine/enginetest"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// The expected values below come from CONTRACT.md, section 3 (peek, list).

func callTool(t *testing.T, r *Runner, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	_, err := NewMCPServer(r, "test").Connect(ctx, st, nil)
	require.NoError(t, err)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil).Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

// peek answers at once: running with the last lines while the run goes,
// ended with run's answer after, unknown for a key with no run. Secrets are
// masked there too.
func TestPeek(t *testing.T) {
	f := enginetest.New("bash:5.2")
	f.Behave = func(c enginetest.Created) enginetest.Outcome {
		if c.Labels[labelKey] == "" {
			return enginetest.Outcome{}
		}
		return enginetest.Outcome{Output: "one\ntwo\nthree s3cret\n", Hold: true}
	}
	r := newRunner(t, f)
	r.Secrets = map[string]string{"S": "s3cret"}
	done := make(chan *Answer)
	go func() {
		a, _ := r.Run(context.Background(), Request{Key: "p", Script: "work"})
		done <- a
	}()
	require.Eventually(t, func() bool { return f.Running() == 1 }, 2*time.Second, 5*time.Millisecond)

	g, err := r.Peek(context.Background(), "p", 2)
	require.NoError(t, err)
	require.Equal(t, StateRunning, g.State)
	require.Equal(t, "two\nthree ***", g.OutputTail)
	require.Nil(t, g.Answer)
	require.False(t, g.StartedAt.IsZero())

	res := callTool(t, r, "peek", map[string]any{"key": "p"})
	require.False(t, res.IsError)
	require.Regexp(t, `^running for \S+\n\nOutput:\none\ntwo\nthree \*\*\*$`, text(res))

	f.Release("jr-p", 3, "")
	a := <-done
	require.Equal(t, 3, a.ExitCode)
	g, err = r.Peek(context.Background(), "p", 0)
	require.NoError(t, err)
	require.Equal(t, StateEnded, g.State)
	require.Equal(t, a, g.Answer)

	g, err = r.Peek(context.Background(), "nobody", 0)
	require.NoError(t, err)
	require.Equal(t, StateUnknown, g.State)

	for _, lines := range []int{-1, 2001} {
		_, err = r.Peek(context.Background(), "p", lines)
		var ue *UsageError
		require.ErrorAs(t, err, &ue, lines)
	}
}

// list: every run held, running or ended, with its image and network names.
func TestList(t *testing.T) {
	f := enginetest.New("bash:5.2", "alpine:3.19")
	f.Behave = func(c enginetest.Created) enginetest.Outcome {
		return enginetest.Outcome{Hold: c.Labels[labelKey] == "held"}
	}
	r := newRunner(t, f)
	_, err := r.Run(context.Background(), Request{Key: "done", Script: "true", Image: "alp", Network: "internet"})
	require.NoError(t, err)
	go func() { _, _ = r.Run(context.Background(), Request{Key: "held", Script: "work"}) }()
	require.Eventually(t, func() bool { return f.Running() == 1 }, 2*time.Second, 5*time.Millisecond)

	l, err := r.List(context.Background())
	require.NoError(t, err)
	byKey := map[string]Listed{}
	for _, e := range l {
		byKey[e.Key] = e
	}
	require.Len(t, byKey, 2)
	require.Equal(t, StateEnded, byKey["done"].State)
	require.Equal(t, "alp", byKey["done"].Image)
	require.Equal(t, "internet", byKey["done"].Network)
	require.Equal(t, StateRunning, byKey["held"].State)
	require.Equal(t, "shell", byKey["held"].Image)
	require.Equal(t, "none", byKey["held"].Network)

	res := callTool(t, r, "list", nil)
	require.False(t, res.IsError)
	require.Contains(t, text(res), "held  running  shell  none")
	f.Release("jr-held", 0, "")
}
