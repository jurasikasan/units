package run

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Params is run's input: Request with the schema a caller reads.
type Params struct {
	Key          string            `json:"key" jsonschema:"your name for this run: letters, digits, '.', '_', '-', at most 128. A run with the key of a run still going waits for it; the key of a run that ended gets that run's answer again until keep_minutes have passed. Use a new key for a new run"`
	Script       string            `json:"script" jsonschema:"the script, run by bash (sh where the image has no bash) in an empty folder /work"`
	Image        string            `json:"image,omitempty" jsonschema:"the image to run in, any reference (bash:5.2, ghcr.io/x/y@sha256:...); the server's default if left out"`
	Env          map[string]string `json:"env,omitempty" jsonschema:"environment of the script, name to value. Not secret: use secrets for that"`
	Secrets      []string          `json:"secrets,omitempty" jsonschema:"names of secrets this server holds, put in the script's environment under those names; their values never cross the call and are masked in the answer"`
	LimitMinutes int               `json:"limit_minutes,omitempty" jsonschema:"how long the run may take, pulling the image included; 30 if left out, at most 120"`
	KeepMinutes  int               `json:"keep_minutes,omitempty" jsonschema:"how long the answer waits for a caller that went away; the server's maximum if left out"`
}

// NewMCPServer offers the runner as one tool.
func NewMCPServer(r *Runner, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "job_runner", Version: version}, nil)
	mcp.AddTool(s, &mcp.Tool{
		Name: "run",
		Description: "Run a script in a fresh container, in an empty folder, and wait until it ends: seconds or hours. " +
			"Returns how it ended (exit code, or stopped by its limit), how long it took and the end of its output. " +
			"The folder is gone after the run: whatever must outlive it, the script sends somewhere itself. " +
			"There is internet inside and nothing else.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in Params) (*mcp.CallToolResult, any, error) {
		a, err := r.Run(ctx, Request(in))
		var ue *UsageError
		if errors.As(err, &ue) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: ue.Error()}}}, nil, nil
		}
		if err != nil {
			return nil, nil, err
		}
		if a == nil {
			return nil, nil, fmt.Errorf("the run ended while nobody waited; call run with the same key for its answer")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: Render(a)}}}, nil, nil
	})
	return s
}

// Render is the answer as a caller reads it: how it ended first, then the output.
func Render(a *Answer) string {
	how := fmt.Sprintf("exit code %d", a.ExitCode)
	if a.Stopped {
		how = "stopped by its limit"
	}
	return fmt.Sprintf("%s after %s\n\nOutput:\n%s", how, a.Ran, a.Output)
}
