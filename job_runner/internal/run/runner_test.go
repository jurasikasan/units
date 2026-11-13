package run

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jurasikasan/units/job_runner/internal/engine"
	"github.com/jurasikasan/units/job_runner/internal/engine/enginetest"

	"github.com/stretchr/testify/require"
)

// The expected values below come from CONTRACT.md, sections 3–5 and 7.

func newRunner(t *testing.T, f *enginetest.Fake) *Runner {
	t.Helper()
	t.Cleanup(f.Close)
	return &Runner{
		Docker:       engine.NewHTTP(f.URL, f.Client()),
		Images:       []Image{{"shell", "bash:5.2"}, {"alp", "alpine:3.19"}},
		Runtime:      "runsc",
		Limits:       Limits{Memory: 1 << 30, Pids: 256, NanoCPUs: 2e9},
		TransferTime: 5 * time.Second,
		MaxKeep:      time.Hour,
		Minute:       50 * time.Millisecond,
		Now:          time.Now,
	}
}

// echo answers like a shell: the script's text is its output.
func echo(c enginetest.Created) enginetest.Outcome {
	if len(c.Cmd) == 1 && strings.HasPrefix(c.Cmd[0], "echo ") {
		return enginetest.Outcome{Output: strings.TrimPrefix(c.Cmd[0], "echo ") + "\r\n"}
	}
	return enginetest.Outcome{}
}

// holdRuns keeps a run going until it is released or killed; the short
// container that prepares the folder ends at once.
func holdRuns(c enginetest.Created) enginetest.Outcome {
	return enginetest.Outcome{Hold: c.Labels[labelKey] != ""}
}

func runs(f *enginetest.Fake) []enginetest.Created {
	var out []enginetest.Created
	for _, c := range f.Log {
		if c.Labels[labelKey] != "" {
			out = append(out, c)
		}
	}
	return out
}

// c1: exit code 0 and the output. The run stays for its keep.
func TestRunAnswers(t *testing.T) {
	f := enginetest.New("bash:5.2")
	f.Behave = echo
	r := newRunner(t, f)

	a, err := r.Run(context.Background(), Request{Key: "c1", Script: "echo hello world"})
	require.NoError(t, err)
	require.Equal(t, 0, a.ExitCode)
	require.False(t, a.Stopped)
	require.Equal(t, "hello world", a.Output)
	require.Equal(t, 1, f.Containers(), "kept for its keep")

	c := runs(f)[0]
	require.Equal(t, "1000:1000", c.User)
	require.Equal(t, "/work", c.WorkDir)
	require.Equal(t, "/work", c.Mounts[0]["Target"])
	// Without NoCopy the daemon gives the empty volume the owner of the
	// image's /work (root), undoing the chown.
	require.Equal(t, map[string]any{"NoCopy": true}, c.Mounts[0]["VolumeOptions"])
	require.Equal(t, []string{"echo hello world"}, c.Cmd)
}

// The fresh volume is handed to uid 1000 before the run, by a root container.
func TestTheFolderBelongsToTheRunsUser(t *testing.T) {
	f := enginetest.New("bash:5.2")
	r := newRunner(t, f)
	_, err := r.Run(context.Background(), Request{Key: "k", Script: "true"})
	require.NoError(t, err)

	require.Len(t, f.Log, 2)
	own, work := f.Log[0], f.Log[1]
	require.Equal(t, "0", own.User)
	require.Equal(t, []string{"chown", "1000:1000", "/work"}, own.Entrypoint)
	require.Equal(t, own.Mounts[0]["Source"], work.Mounts[0]["Source"])
}

// c2: the same key gets the same answer until its keep has passed; then the
// key starts a new run in a new folder. A new key starts empty at once.
func TestAKeyAnswersTheSameUntilItsKeepEnds(t *testing.T) {
	f := enginetest.New("bash:5.2")
	f.Behave = echo
	r := newRunner(t, f)
	a1, err := r.Run(context.Background(), Request{Key: "c2", Script: "echo x"})
	require.NoError(t, err)
	a2, err := r.Run(context.Background(), Request{Key: "c2", Script: "echo something else"})
	require.NoError(t, err)
	require.Equal(t, a1, a2)
	require.Len(t, runs(f), 1)

	f.Age("jr-c2", r.MaxKeep+time.Second)
	r.Sweep(context.Background())
	require.Equal(t, 0, f.Containers())
	require.Empty(t, f.Volumes())
	a3, err := r.Run(context.Background(), Request{Key: "c2", Script: "echo fresh"})
	require.NoError(t, err)
	require.Equal(t, "fresh", a3.Output)
	rs := runs(f)
	require.Len(t, rs, 2)
	require.NotEqual(t, rs[0].Labels[labelVolume], rs[1].Labels[labelVolume])
}

// Found live: a hub took the answer for a caller that had died. The
// next call with the key gets the same answer, and nothing runs twice.
func TestAnAnswerTakenForADeadCallerIsStillThere(t *testing.T) {
	f := enginetest.New("bash:5.2")
	f.Behave = holdRuns
	r := newRunner(t, f)
	done := make(chan *Answer)
	go func() {
		a, err := r.Run(context.Background(), Request{Key: "k", Script: "sleep 60"})
		require.NoError(t, err)
		done <- a
	}()
	require.Eventually(t, func() bool { return f.Running() == 1 }, time.Second, 5*time.Millisecond)
	f.Release("jr-k", 0, "done\n")
	taken := <-done // the hub's call, whose caller is gone

	again, err := r.Run(context.Background(), Request{Key: "k", Script: "sleep 60"})
	require.NoError(t, err)
	require.Equal(t, taken, again)
	require.Len(t, runs(f), 1)
}

// c3: env reaches the script; its values are in no label, no log line of
// job_runner and not in the answer.
func TestEnvValuesStayOutOfLabelsLogsAndAnswer(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	f := enginetest.New("bash:5.2")
	r := newRunner(t, f)
	a, err := r.Run(context.Background(), Request{Key: "c3", Script: "echo done", Env: map[string]string{"T": "s3cret"}})
	require.NoError(t, err)

	c := runs(f)[0]
	require.Contains(t, c.Env, "T=s3cret")
	for k, v := range c.Labels {
		require.NotContains(t, k+v, "s3cret")
	}
	require.NotContains(t, logs.String(), "s3cret")
	require.NotContains(t, Render(a), "s3cret")
}

// c5: two calls with one key at once: one run, the same answer.
func TestConcurrentCallsShareOneRun(t *testing.T) {
	f := enginetest.New("bash:5.2")
	f.Behave = holdRuns
	r := newRunner(t, f)

	var wg sync.WaitGroup
	answers := make([]*Answer, 2)
	for i := range answers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a, err := r.Run(context.Background(), Request{Key: "c5", Script: "sleep 5; date"})
			require.NoError(t, err)
			answers[i] = a
		}(i)
	}
	require.Eventually(t, func() bool { return f.Running() == 1 }, time.Second, 5*time.Millisecond)
	time.Sleep(20 * time.Millisecond) // the second caller joins
	f.Release("jr-c5", 0, "1730000000\n")
	wg.Wait()

	require.Len(t, runs(f), 1)
	require.Equal(t, answers[0], answers[1])
	require.Equal(t, "1730000000", answers[0].Output)
}

// The caller leaving does not stop the run; the next call with the key gets
// its answer instead of starting another.
func TestAnAnswerWaitsForTheNextCallWithTheKey(t *testing.T) {
	f := enginetest.New("bash:5.2")
	f.Behave = holdRuns
	r := newRunner(t, f)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		require.Eventually(t, func() bool { return f.Running() == 1 }, time.Second, 5*time.Millisecond)
		cancel()
	}()
	_, err := r.Run(ctx, Request{Key: "k", Script: "long"})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, f.Running(), "the run goes on")

	f.Release("jr-k", 3, "late\n")
	require.Eventually(t, func() bool { return !r.followed("k") }, time.Second, 5*time.Millisecond)
	require.Equal(t, 1, f.Containers(), "kept for the next call")

	a, err := r.Run(context.Background(), Request{Key: "k", Script: "something else"})
	require.NoError(t, err)
	require.Equal(t, 3, a.ExitCode)
	require.Equal(t, "late", a.Output)
	require.Len(t, runs(f), 1)
}

// c4: a run past its limit is killed, and the answer says so.
func TestALimitStopsTheRun(t *testing.T) {
	f := enginetest.New("bash:5.2")
	f.Behave = holdRuns
	r := newRunner(t, f)

	a, err := r.Run(context.Background(), Request{Key: "c4", Script: "sleep 600", LimitMinutes: 1})
	require.NoError(t, err)
	require.True(t, a.Stopped)
	require.True(t, strings.HasPrefix(Render(a), "stopped by its limit"))
}

// A run nobody waits for is killed at its limit by the sweep, and its answer
// is dropped once its keep has passed.
func TestTheSweepKillsAndDropsWhatNobodyWaitsFor(t *testing.T) {
	f := enginetest.New("bash:5.2")
	f.Behave = holdRuns
	r := newRunner(t, f)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		require.Eventually(t, func() bool { return f.Running() == 1 }, time.Second, 5*time.Millisecond)
		cancel()
	}()
	_, _ = r.Run(ctx, Request{Key: "k", Script: "long", LimitMinutes: 1, KeepMinutes: 2})
	// The flight still follows the run and kills it at its deadline; the
	// sweep must do the same for a run no process follows.
	r.mu.Lock()
	r.flights = map[string]*flight{}
	r.mu.Unlock()

	time.Sleep(60 * time.Millisecond)
	r.Sweep(context.Background())
	require.Eventually(t, func() bool { return f.Running() == 0 }, time.Second, 5*time.Millisecond)
	require.Equal(t, 1, f.Containers(), "the answer waits its keep")

	f.Age("jr-k", 101*time.Millisecond)
	r.Sweep(context.Background())
	require.Equal(t, 0, f.Containers())
	require.Empty(t, f.Volumes())
}

// A volume no run holds is removed once it is older than a minute.
func TestTheSweepRemovesStrayVolumes(t *testing.T) {
	f := enginetest.New("bash:5.2")
	r := newRunner(t, f)
	require.NoError(t, r.Docker.CreateVolume(context.Background(), "jr-x-1", map[string]string{labelKey: "x"}))
	r.Sweep(context.Background())
	require.Len(t, f.Volumes(), 1, "young: may belong to a start in progress")
	f.AgeVolume("jr-x-1", 2*time.Minute)
	r.Sweep(context.Background())
	require.Empty(t, f.Volumes())
}

// Images by name only (section 3): the name given, else the first of
// IMAGES; a missing one is pulled; a name the server lacks, or a reference
// in place of a name, is refused before anything is made; a pull the
// registry refuses is an error and nothing is left.
func TestImages(t *testing.T) {
	f := enginetest.New("bash:5.2")
	r := newRunner(t, f)
	_, err := r.Run(context.Background(), Request{Key: "a", Script: "cat /etc/alpine-release", Image: "alp"})
	require.NoError(t, err)
	require.Equal(t, []string{"alpine:3.19"}, f.Pulled)
	require.Equal(t, "alpine:3.19", runs(f)[0].Image)

	_, err = r.Run(context.Background(), Request{Key: "b", Script: "true"})
	require.NoError(t, err)
	require.Equal(t, "bash:5.2", runs(f)[1].Image)

	before := len(f.Log)
	for _, img := range []string{"alpine:3.19", "python:3.12", "evil"} {
		_, err = r.Run(context.Background(), Request{Key: "x", Script: "true", Image: img})
		var ue *UsageError
		require.ErrorAs(t, err, &ue, img)
	}
	require.Len(t, f.Log, before)

	r.Images = append(r.Images, Image{"broken", "nope:1"})
	f.FailPull("nope:1", "manifest unknown")
	containers := f.Containers()
	_, err = r.Run(context.Background(), Request{Key: "c", Script: "true", Image: "broken"})
	require.ErrorContains(t, err, "manifest unknown")
	require.Equal(t, containers, f.Containers())
	require.Len(t, f.Volumes(), containers)
}

// The script runs under bash where the image has it and sh where it does not.
func TestTheEntrypointPicksBashOrSh(t *testing.T) {
	require.Equal(t, "sh", entrypoint[0])
	require.Contains(t, entrypoint[2], `command -v bash`)
	require.Contains(t, entrypoint[2], `exec sh -c "$1"`)
}

// A wrong call is refused before anything is created.
func TestWrongCallsAreRefused(t *testing.T) {
	f := enginetest.New("bash:5.2")
	r := newRunner(t, f)
	for _, req := range []Request{
		{Key: "", Script: "true"},
		{Key: "-x", Script: "true"},
		{Key: "a/b", Script: "true"},
		{Key: strings.Repeat("a", 129), Script: "true"},
		{Key: "k", Script: "  "},
		{Key: "k", Script: "true", LimitMinutes: 121},
		{Key: "k", Script: "true", LimitMinutes: -1},
		{Key: "k", Script: "true", KeepMinutes: -1},
		{Key: "k", Script: "true", Env: map[string]string{"1X": "v"}},
	} {
		_, err := r.Run(context.Background(), req)
		var ue *UsageError
		require.ErrorAs(t, err, &ue, "%+v", req)
	}
	require.Empty(t, f.Log)
}

// keep: the caller's, never longer than the server's maximum.
func TestKeepIsCappedByTheServer(t *testing.T) {
	r := &Runner{MaxKeep: 4 * time.Hour, Minute: time.Minute}
	require.Equal(t, 4*time.Hour, r.keep(Request{}))
	require.Equal(t, 10*time.Minute, r.keep(Request{KeepMinutes: 10}))
	require.Equal(t, 4*time.Hour, r.keep(Request{KeepMinutes: 600}))
}

func TestTailKeepsTheEnd(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 300; i++ {
		b.WriteString("line\r\n")
	}
	b.WriteString("last\r\n")
	out := tail(b.String())
	require.True(t, strings.HasSuffix(out, "line\nlast"))
	require.Equal(t, tailLines, strings.Count(out, "\n")+1)
}

// c7: a secret goes in by name, its value is in no label and reads *** in
// the answer; a name this instance does not hold is a wrong call.
func TestSecretsGoInByNameAndAreMasked(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	f := enginetest.New("bash:5.2")
	f.Behave = func(c enginetest.Created) enginetest.Outcome {
		if c.Labels[labelKey] == "" {
			return enginetest.Outcome{}
		}
		return enginetest.Outcome{Output: "token is tok-123456\nend\n"} // what `echo $T` would print
	}
	r := newRunner(t, f)
	r.Secrets = map[string]string{"T": "tok-123456", "OTHER": "unused"}

	a, err := r.Run(context.Background(), Request{Key: "c7", Script: "echo token is $T", Secrets: []string{"T"}})
	require.NoError(t, err)
	require.Equal(t, "token is ***\nend", a.Output)
	c := runs(f)[0]
	require.Contains(t, c.Env, "T=tok-123456")
	require.NotContains(t, c.Env, "OTHER=unused", "only the secrets asked for")
	for k, v := range c.Labels {
		require.NotContains(t, k+v, "tok-123456")
	}
	require.NotContains(t, logs.String(), "tok-123456")

	before := len(f.Log)
	_, err = r.Run(context.Background(), Request{Key: "c7b", Script: "true", Secrets: []string{"NO_SUCH"}})
	var ue *UsageError
	require.ErrorAs(t, err, &ue)
	require.Len(t, f.Log, before, "nothing runs")
}

// c8: the helpers directory is bound read-only at /opt/helpers and comes
// first on PATH; without one, nothing is bound.
func TestHelpersAreMountedAndFirstOnPath(t *testing.T) {
	f := enginetest.New("bash:5.2")
	r := newRunner(t, f)
	_, err := r.Run(context.Background(), Request{Key: "a", Script: "true"})
	require.NoError(t, err)
	require.Len(t, runs(f)[0].Mounts, 1, "only /work")

	r.HelpersDir = "/srv/helpers"
	_, err = r.Run(context.Background(), Request{Key: "b", Script: "command -v h"})
	require.NoError(t, err)
	m := runs(f)[1].Mounts
	require.Len(t, m, 2)
	require.Equal(t, map[string]any{"Type": "bind", "Source": "/srv/helpers", "Target": "/opt/helpers", "ReadOnly": true}, m[1])
	require.Contains(t, entrypoint[2], `export PATH="/opt/helpers:$PATH"`)
}
