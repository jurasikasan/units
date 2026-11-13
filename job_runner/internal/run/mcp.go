package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Params is run's input: Request with the schema a caller reads.
type Params struct {
	Key          string            `json:"key" jsonschema:"your name for this run: letters, digits, '.', '_', '-', at most 128. A run with the key of a run still going waits for it; the key of a run that ended gets that run's answer again until keep_minutes have passed. Use a new key for a new run"`
	Script       string            `json:"script" jsonschema:"the script, run by bash (sh where the image has no bash) in /work, which holds only the inputs"`
	Image        string            `json:"image,omitempty" jsonschema:"the name of an image this server offers; its first if left out"`
	Network      string            `json:"network,omitempty" jsonschema:"none (the default): no network at all; internet: public addresses only; or a network this server names"`
	Inputs       []Input           `json:"inputs,omitempty" jsonschema:"files fetched into /work before the script starts; a failed fetch fails the call and nothing runs"`
	Outputs      []Output          `json:"outputs,omitempty" jsonschema:"files of /work sent after the script has ended, however it ended; a missing file or a failed send is a line of the answer"`
	Log          *LogTo            `json:"log,omitempty" jsonschema:"where the whole output and the answer go after the run, for reading it once its keep is over"`
	Env          map[string]string `json:"env,omitempty" jsonschema:"environment of the script, name to value. Not secret: use secrets for that"`
	Secrets      []string          `json:"secrets,omitempty" jsonschema:"names of secrets this server holds, put in the script's environment under those names; masked in every answer. Prefer secret on an input or output: then the value never enters the run"`
	LimitMinutes int               `json:"limit_minutes,omitempty" jsonschema:"how long the run may take, pulling the image and fetching the inputs included; 30 if left out, at most 120"`
	KeepMinutes  int               `json:"keep_minutes,omitempty" jsonschema:"how long the answer waits for a caller that went away; the server's maximum if left out"`
}

// Result is an answer as structured content.
type Result struct {
	ExitCode       *int     `json:"exit_code" jsonschema:"null when the run was stopped by its limit"`
	StoppedByLimit bool     `json:"stopped_by_limit"`
	DurationMs     int64    `json:"duration_ms"`
	Changes        *Changes `json:"changes,omitempty" jsonschema:"what the run did to /work against its inputs"`
	Outputs        []Sent   `json:"outputs,omitempty"`
	Log            string   `json:"log,omitempty" jsonschema:"how sending the log went"`
	Notes          []string `json:"notes,omitempty"`
	OutputTail     string   `json:"output_tail"`
}

// PeekParams is peek's input.
type PeekParams struct {
	Key   string `json:"key" jsonschema:"the run's key"`
	Lines int    `json:"lines,omitempty" jsonschema:"how many of the last lines of output; 50 if left out, at most 2000"`
}

// PeekResult is peek's answer.
type PeekResult struct {
	State      string    `json:"state" jsonschema:"running, ended or unknown"`
	StartedAt  time.Time `json:"started_at,omitzero"`
	ElapsedMs  int64     `json:"elapsed_ms"`
	OutputTail string    `json:"output_tail"`
	Answer     *Result   `json:"answer,omitempty" jsonschema:"run's answer, once the run has ended and its outputs are sent"`
}

// ListResult is list's answer.
type ListResult struct {
	Runs []Listed `json:"runs"`
}

// NewMCPServer offers the runner as three tools: run, peek and list.
func NewMCPServer(r *Runner, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "job_runner", Version: version}, nil)
	mcp.AddTool(s, &mcp.Tool{
		Name: "run",
		Description: "Run a script in a fresh, isolated machine and wait until it ends: seconds or hours. " +
			"Files come in through inputs and go out through outputs; /work holds nothing else, and is gone after the run. " +
			"No network unless you ask for one. " +
			"Returns how it ended (exit code, or stopped by its limit), how long it took, what changed in /work, how the outputs went and the end of its output. " +
			"To look at a run without waiting for it, use peek.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in Params) (*mcp.CallToolResult, *Result, error) {
		a, err := r.Run(ctx, Request(in))
		if res := toolError(err); res != nil {
			return res, nil, nil
		}
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: Render(a)}}}, result(a), nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name: "peek",
		Description: "Where a run is now — running, ended or unknown — how long it has taken and the last lines of its output. " +
			"Answers at once; never starts or waits for anything. An ended run's answer comes with it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in PeekParams) (*mcp.CallToolResult, *PeekResult, error) {
		g, err := r.Peek(ctx, in.Key, in.Lines)
		if res := toolError(err); res != nil {
			return res, nil, nil
		}
		if err != nil {
			return nil, nil, err
		}
		out := &PeekResult{State: g.State, StartedAt: g.StartedAt, ElapsedMs: g.Elapsed.Milliseconds(), OutputTail: g.OutputTail}
		text := g.State
		if g.State != StateUnknown {
			text += fmt.Sprintf(" for %s", g.Elapsed)
		}
		if g.Answer != nil {
			out.Answer = result(g.Answer)
			text = Render(g.Answer)
		} else if g.OutputTail != "" {
			text += "\n\nOutput:\n" + g.OutputTail
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, out, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list",
		Description: "The runs this server holds: running, and ended ones whose answers still wait. Answers at once.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, *ListResult, error) {
		runs, err := r.List(ctx)
		if err != nil {
			return nil, nil, err
		}
		var b strings.Builder
		if len(runs) == 0 {
			b.WriteString("no runs")
		}
		for _, l := range runs {
			fmt.Fprintf(&b, "%s  %s  %s  %s  %s\n", l.Key, l.State, l.Image, l.Network, l.Elapsed.Round(time.Second))
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.TrimRight(b.String(), "\n")}}},
			&ListResult{Runs: runs}, nil
	})
	return s
}

func toolError(err error) *mcp.CallToolResult {
	var ue *UsageError
	if errors.As(err, &ue) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: ue.Error()}}}
	}
	return nil
}

func result(a *Answer) *Result {
	res := &Result{StoppedByLimit: a.Stopped, DurationMs: a.Ran.Milliseconds(), Changes: a.Changes,
		Outputs: a.Outputs, Log: a.Log, Notes: a.Notes, OutputTail: a.Output}
	if !a.Stopped {
		code := a.ExitCode
		res.ExitCode = &code
	}
	return res
}

// reportFiles is how many files the text names; the counts and the
// structured content have them all.
const reportFiles = 20

// Render is the answer as a caller reads it: how it ended first — that line
// is fixed, callers parse it — then the changes, the outputs, the output.
func Render(a *Answer) string {
	var b strings.Builder
	if a.Stopped {
		fmt.Fprintf(&b, "stopped by its limit after %s", a.Ran)
	} else {
		fmt.Fprintf(&b, "exit code %d after %s", a.ExitCode, a.Ran)
	}
	if c := a.Changes; c != nil {
		fmt.Fprintf(&b, "\n\nChanges: +%d ~%d -%d", len(c.Added), len(c.Changed), len(c.Removed))
		n := 0
		for _, g := range []struct {
			mark string
			l    []Changed
		}{{"+", c.Added}, {"~", c.Changed}, {"-", c.Removed}} {
			for _, f := range g.l {
				if n == reportFiles {
					break
				}
				fmt.Fprintf(&b, "\n  %s %s %d", g.mark, f.Path, f.Size)
				n++
			}
		}
		if total := len(c.Added) + len(c.Changed) + len(c.Removed); total > n {
			fmt.Fprintf(&b, "\n  … %d more", total-n)
		}
	}
	if len(a.Outputs) > 0 {
		b.WriteString("\n\nOutputs:")
		for _, o := range a.Outputs {
			fmt.Fprintf(&b, "\n  %s -> %s", o.From, o.Status)
		}
	}
	if a.Log != "" {
		fmt.Fprintf(&b, "\n\nLog: %s", a.Log)
	}
	for _, n := range a.Notes {
		fmt.Fprintf(&b, "\n\nNote: %s", n)
	}
	fmt.Fprintf(&b, "\n\nOutput:\n%s", a.Output)
	return b.String()
}
