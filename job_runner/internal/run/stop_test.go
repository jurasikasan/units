package run

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jurasikasan/units/job_runner/internal/engine"
	"github.com/jurasikasan/units/job_runner/internal/engine/enginetest"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// The expected values below come from CONTRACT.md, section 3 (stop).

// holdWriting keeps a run going, with a file it has written so far.
func holdWriting(c enginetest.Created) enginetest.Outcome {
	if c.Labels[labelKey] == "" {
		return enginetest.Outcome{}
	}
	return enginetest.Outcome{Hold: true, Output: "working\n", Write: map[string]string{"out/partial.txt": "half"}}
}

// A stopped run ends at once, long before its limit; its outputs and its log
// still go out, and its answer says its caller stopped it.
func TestStopEndsARunAndItsOutputsStillGo(t *testing.T) {
	f := enginetest.New("bash:5.2")
	f.Behave = holdWriting
	s := newStore(t, map[string]string{})
	r := transferRunner(t, f, s)
	r.Minute = time.Hour // the limit is far away

	got := make(chan *Answer, 1)
	go func() {
		a, err := r.Run(context.Background(), Request{Key: "k", Script: "sleep 600",
			Outputs: []Output{{From: "out", URL: s.URL + "/v1/files/"}},
			Log:     &LogTo{URL: s.URL + "/v1/logs/k.log"}})
		require.NoError(t, err)
		got <- a
	}()
	require.Eventually(t, func() bool { return f.Running() == 1 }, time.Second, 5*time.Millisecond)

	state, err := r.Stop(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, StopStopped, state)

	var a *Answer
	select {
	case a = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("the run did not end after stop")
	}
	require.True(t, a.StoppedByCaller)
	require.False(t, a.Stopped, "not by its limit")
	require.True(t, strings.HasPrefix(Render(a), "stopped by its caller after"))
	require.Equal(t, "working", a.Output)
	require.Equal(t, []Sent{{From: "out/partial.txt", Status: "PUT 201"}}, a.Outputs)
	require.Equal(t, "PUT 201", a.Log)
	d, ok := s.get("/v1/files/partial.txt")
	require.True(t, ok)
	require.Equal(t, "half", d)
	l, _ := s.get("/v1/logs/k.log")
	require.Contains(t, l, "stopped by its caller after")

	res := result(a)
	require.Nil(t, res.ExitCode)
	require.True(t, res.StoppedByCaller)

	// The same key answers the same until its keep is over.
	again, err := r.Run(context.Background(), Request{Key: "k", Script: "anything"})
	require.NoError(t, err)
	require.True(t, again.StoppedByCaller)
}

// stop of a run that has ended does nothing; of a key nobody knows, nothing
// either; a wrong key is a usage error.
func TestStopOfWhatIsNotRunning(t *testing.T) {
	f := enginetest.New("bash:5.2")
	r := newRunner(t, f)
	a, err := r.Run(context.Background(), Request{Key: "done", Script: "true"})
	require.NoError(t, err)

	state, err := r.Stop(context.Background(), "done")
	require.NoError(t, err)
	require.Equal(t, StopEnded, state)
	again, err := r.Run(context.Background(), Request{Key: "done", Script: "true"})
	require.NoError(t, err)
	require.Equal(t, a, again, "its answer is as it was")
	require.False(t, r.stopAsked(context.Background(), "done"))

	state, err = r.Stop(context.Background(), "nobody")
	require.NoError(t, err)
	require.Equal(t, StopUnknown, state)

	_, err = r.Stop(context.Background(), "a b")
	var ue *UsageError
	require.ErrorAs(t, err, &ue)
}

// A stop asked for while this process went down before the kill: the sweep
// kills it, and the answer still says its caller stopped it.
func TestTheSweepFinishesAStopAfterARestart(t *testing.T) {
	f := enginetest.New("bash:5.2")
	f.Behave = holdRuns
	r := newRunner(t, f)
	r.Minute = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		require.Eventually(t, func() bool { return f.Running() == 1 }, time.Second, 5*time.Millisecond)
		cancel()
	}()
	_, err := r.Run(ctx, Request{Key: "k", Script: "sleep 600"})
	require.ErrorIs(t, err, context.Canceled)

	// What Stop leaves behind when it gets no further than the ask.
	_, err = r.Docker.Create(context.Background(), engine.Spec{Name: stopName("k"), Image: "bash:5.2",
		Labels: map[string]string{labelStopOf: "k"}})
	require.NoError(t, err)

	// A fresh process: nobody follows the key.
	r2 := newRunner(t, f)
	r2.Minute = time.Hour
	require.Eventually(t, func() bool { return !r.followed("k") }, time.Second, 5*time.Millisecond)
	r2.Sweep(context.Background())
	require.Equal(t, 0, f.Running())
	r2.Sweep(context.Background()) // sends its outputs, puts its answer together
	g, err := r2.Peek(context.Background(), "k", 0)
	require.NoError(t, err)
	require.Equal(t, StateEnded, g.State)
	require.NotNil(t, g.Answer)
	require.True(t, g.Answer.StoppedByCaller)

	// Its keep over, the run goes, its stop with it.
	f.Age("jr-k", r2.MaxKeep+time.Second)
	r2.Sweep(context.Background())
	require.Equal(t, 0, f.Containers())
}

// A stop left behind by an earlier run of the key does not end a fresh one.
func TestAStaleStopDoesNotEndAFreshRun(t *testing.T) {
	f := enginetest.New("bash:5.2")
	f.Behave = echo
	r := newRunner(t, f)
	_, err := r.Docker.Create(context.Background(), engine.Spec{Name: stopName("k"), Image: "bash:5.2",
		Labels: map[string]string{labelStopOf: "k"}})
	require.NoError(t, err)

	a, err := r.Run(context.Background(), Request{Key: "k", Script: "echo fine"})
	require.NoError(t, err)
	require.False(t, a.StoppedByCaller)
	require.Equal(t, 0, a.ExitCode)
	require.Equal(t, "fine", a.Output)
}

// A stop is never listed as a run.
func TestAStopIsNotARun(t *testing.T) {
	f := enginetest.New("bash:5.2")
	f.Behave = holdRuns
	r := newRunner(t, f)
	r.Minute = time.Hour
	go func() { _, _ = r.Run(context.Background(), Request{Key: "k", Script: "sleep 600"}) }()
	require.Eventually(t, func() bool { return f.Running() == 1 }, time.Second, 5*time.Millisecond)
	_, err := r.Stop(context.Background(), "k")
	require.NoError(t, err)

	l, err := r.List(context.Background())
	require.NoError(t, err)
	require.Len(t, l, 1)
	require.Equal(t, "k", l[0].Key)
}

// The tool: what it found, as text and as structured content.
func TestTheStopTool(t *testing.T) {
	f := enginetest.New("bash:5.2")
	r := newRunner(t, f)
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	_, err := NewMCPServer(r, "test").Connect(ctx, st, nil)
	require.NoError(t, err)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil).Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "stop", Arguments: map[string]any{"key": "nobody"}})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.True(t, strings.HasPrefix(text(res), "unknown"))
	require.Equal(t, map[string]any{"state": "unknown"}, res.StructuredContent)

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "stop", Arguments: map[string]any{"key": "a b"}})
	require.NoError(t, err)
	require.True(t, res.IsError)
}
