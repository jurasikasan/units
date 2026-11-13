package run

import (
	"context"
	"testing"

	"github.com/jurasikasan/units/job_runner/internal/engine/enginetest"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func call(t *testing.T, r *Runner, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	_, err := NewMCPServer(r, "test").Connect(ctx, st, nil)
	require.NoError(t, err)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil).Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "run", Arguments: args})
	require.NoError(t, err)
	return res
}

func text(res *mcp.CallToolResult) string { return res.Content[0].(*mcp.TextContent).Text }

// Section 3: the answer says how it ended, what changed in /work, then the
// output, and never the script; the same as structured content.
func TestTheToolAnswers(t *testing.T) {
	f := enginetest.New("bash:5.2")
	f.Behave = echo
	res := call(t, newRunner(t, f), map[string]any{"key": "c1", "script": "echo hello world"})
	require.False(t, res.IsError)
	require.Regexp(t, `^exit code 0 after \S+\n\nChanges: \+0 ~0 -0\n\nOutput:\nhello world$`, text(res))
	sc, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok, "%T", res.StructuredContent)
	require.Equal(t, float64(0), sc["exit_code"])
	require.Equal(t, false, sc["stopped_by_limit"])
	require.Equal(t, "hello world", sc["output_tail"])
}

// Section 6: a wrong call is a tool error, and nothing runs.
func TestAWrongCallIsAToolError(t *testing.T) {
	f := enginetest.New("bash:5.2")
	res := call(t, newRunner(t, f), map[string]any{"key": "a b", "script": "true"})
	require.True(t, res.IsError)
	require.Contains(t, text(res), `key "a b"`)
	require.Empty(t, f.Log)
}

// A non-zero exit is the script's business, not a tool error.
func TestAFailedScriptIsAnAnswer(t *testing.T) {
	f := enginetest.New("bash:5.2")
	f.Behave = func(c enginetest.Created) enginetest.Outcome {
		if c.Labels[labelKey] == "" {
			return enginetest.Outcome{} // the folder's chown
		}
		return enginetest.Outcome{Exit: 2, Output: "boom\n"}
	}
	res := call(t, newRunner(t, f), map[string]any{"key": "k", "script": "exit 2"})
	require.False(t, res.IsError)
	require.Regexp(t, `^exit code 2 after`, text(res))
}
