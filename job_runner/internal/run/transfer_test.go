package run

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jurasikasan/units/job_runner/internal/engine/enginetest"

	"github.com/stretchr/testify/require"
)

// The expected values below come from CONTRACT.md, sections 3 and 5.

// store is a file server: GET answers what is there, PUT keeps the body.
type store struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string]string
	auth  map[string]string // path → Authorization of its last request
	puts  int
}

func newStore(t *testing.T, files map[string]string) *store {
	s := &store{files: files, auth: map[string]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.auth[r.URL.Path] = r.Header.Get("Authorization")
		switch r.Method {
		case http.MethodGet:
			d, ok := s.files[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = io.WriteString(w, d)
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			s.files[r.URL.Path] = string(b)
			s.puts++
			w.WriteHeader(http.StatusCreated)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *store) get(p string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.files[p]
	return d, ok
}

func transferRunner(t *testing.T, f *enginetest.Fake, s *store) *Runner {
	r := newRunner(t, f)
	r.TransferURLs = []string{s.URL + "/v1/"}
	r.Secrets = map[string]string{"STORE_TOKEN": "tok-123"}
	r.HTTP = s.Client()
	return r
}

// Inputs land in /work at their paths, owned by the run's user, fetched with
// the named secret as a bearer token; the secret's value is nowhere in the run.
func TestInputsLandInWork(t *testing.T) {
	f := enginetest.New("bash:5.2")
	s := newStore(t, map[string]string{"/v1/files/a.bundle": "AAAA", "/v1/files/b.txt": "bee"})
	r := transferRunner(t, f, s)

	_, err := r.Run(context.Background(), Request{Key: "k", Script: "ls", Inputs: []Input{
		{URL: s.URL + "/v1/files/a.bundle", To: "in/input.bundle", Secret: "STORE_TOKEN"},
		{URL: s.URL + "/v1/files/b.txt", To: "b.txt"},
	}})
	require.NoError(t, err)

	files := f.Files(f.VolumeOf("jr-k"))
	require.Equal(t, "AAAA", files["in/input.bundle"].Data)
	require.Equal(t, 1000, files["in/input.bundle"].Uid)
	require.Equal(t, "bee", files["b.txt"].Data)
	require.Equal(t, "Bearer tok-123", s.auth["/v1/files/a.bundle"])
	require.Empty(t, s.auth["/v1/files/b.txt"], "no secret named, none sent")

	c := runs(f)[0]
	for _, e := range c.Env {
		require.NotContains(t, e, "tok-123")
	}
	for _, v := range c.Labels {
		require.NotContains(t, v, "tok-123")
	}
}

// A failed fetch fails the call: nothing is created, nothing runs.
func TestAFailedInputRunsNothing(t *testing.T) {
	f := enginetest.New("bash:5.2")
	s := newStore(t, map[string]string{})
	r := transferRunner(t, f, s)
	_, err := r.Run(context.Background(), Request{Key: "k", Script: "true",
		Inputs: []Input{{URL: s.URL + "/v1/files/none", To: "x"}}})
	var ue *UsageError
	require.ErrorAs(t, err, &ue)
	require.Contains(t, err.Error(), "404")
	require.Empty(t, f.Log)
	require.Empty(t, f.Volumes())
}

// A transfer goes only under TRANSFER_URLS — the only places a secret is
// sent — to a path under /work; none allowed when none are configured.
func TestTransfersAreHeldToTheirPlaces(t *testing.T) {
	f := enginetest.New("bash:5.2")
	s := newStore(t, map[string]string{})
	r := transferRunner(t, f, s)
	ok := s.URL + "/v1/files/x"
	for _, req := range []Request{
		{Inputs: []Input{{URL: "https://elsewhere.example/v1/x", To: "x"}}},
		{Inputs: []Input{{URL: s.URL + "/other/x", To: "x"}}},
		{Inputs: []Input{{URL: ok, To: "../x"}}},
		{Inputs: []Input{{URL: ok, To: "/etc/x"}}},
		{Inputs: []Input{{URL: ok, To: "."}}},
		{Inputs: []Input{{URL: ok, To: "x"}, {URL: ok, To: "./x"}}},
		{Inputs: []Input{{URL: ok, To: "x", Secret: "NO_SUCH"}}},
		{Outputs: []Output{{From: "x", URL: "https://elsewhere.example/v1/x", Secret: "STORE_TOKEN"}}},
		{Outputs: []Output{{From: "../../etc/passwd", URL: ok}}},
		{Log: &LogTo{URL: "https://elsewhere.example/log"}},
	} {
		req.Key, req.Script = "k", "true"
		_, err := r.Run(context.Background(), req)
		var ue *UsageError
		require.ErrorAs(t, err, &ue, "%+v", req)
	}
	r.TransferURLs = nil
	_, err := r.Run(context.Background(), Request{Key: "k", Script: "true", Outputs: []Output{{From: "x", URL: ok}}})
	require.ErrorContains(t, err, "allows no transfers")
	require.Empty(t, f.Log)
}

// Outputs go out after the script with the named secret; a missing file is
// a line of the answer, not an error. The change report compares /work with
// the inputs: added, changed, removed.
func TestOutputsAndChanges(t *testing.T) {
	f := enginetest.New("bash:5.2")
	s := newStore(t, map[string]string{"/v1/files/keep": "k", "/v1/files/edit": "e", "/v1/files/gone": "g"})
	r := transferRunner(t, f, s)
	f.Behave = func(c enginetest.Created) enginetest.Outcome {
		if c.Labels[labelKey] == "" {
			return enginetest.Outcome{}
		}
		return enginetest.Outcome{Output: "done\n", Exit: 1,
			Write:  map[string]string{"out/result.bundle": "RESULT", "edit": "edited"},
			Remove: []string{"gone"}}
	}
	a, err := r.Run(context.Background(), Request{Key: "k", Script: "work",
		Inputs: []Input{
			{URL: s.URL + "/v1/files/keep", To: "keep"},
			{URL: s.URL + "/v1/files/edit", To: "edit"},
			{URL: s.URL + "/v1/files/gone", To: "gone"},
		},
		Outputs: []Output{
			{From: "out/result.bundle", URL: s.URL + "/v1/files/T.result.bundle", Secret: "STORE_TOKEN"},
			{From: "out/change.patch", URL: s.URL + "/v1/files/T.change.patch"},
		}})
	require.NoError(t, err)
	require.Equal(t, 1, a.ExitCode, "outputs go however the run ended")

	got, ok := s.get("/v1/files/T.result.bundle")
	require.True(t, ok)
	require.Equal(t, "RESULT", got)
	require.Equal(t, "Bearer tok-123", s.auth["/v1/files/T.result.bundle"])
	_, ok = s.get("/v1/files/T.change.patch")
	require.False(t, ok)
	require.Equal(t, []Sent{{"out/result.bundle", "PUT 201"}, {"out/change.patch", "missing"}}, a.Outputs)

	require.Equal(t, &Changes{
		Added:   []Changed{{"out/result.bundle", 6}},
		Changed: []Changed{{"edit", 6}},
		Removed: []Changed{{"gone", 1}},
	}, a.Changes)
	text := Render(a)
	require.Regexp(t, `^exit code 1 after \S+\n\nChanges: \+1 ~1 -1\n  \+ out/result.bundle 6\n  ~ edit 6\n  - gone 1\n\nOutputs:\n  out/result.bundle -> PUT 201\n  out/change.patch -> missing\n\nOutput:\ndone$`, text)
}

// log: the whole output and the answer, sent however the run ended — a run
// stopped by its limit included.
func TestTheLogGoesOutHoweverTheRunEnded(t *testing.T) {
	f := enginetest.New("bash:5.2")
	s := newStore(t, map[string]string{})
	r := transferRunner(t, f, s)
	f.Behave = func(c enginetest.Created) enginetest.Outcome {
		if c.Labels[labelKey] == "" {
			return enginetest.Outcome{}
		}
		return enginetest.Outcome{Output: "line one\nsecret tok-123\n", Hold: true}
	}
	a, err := r.Run(context.Background(), Request{Key: "k", Script: "sleep 600", LimitMinutes: 1,
		Log: &LogTo{URL: s.URL + "/v1/logs/k.log", Secret: "STORE_TOKEN"}})
	require.NoError(t, err)
	require.True(t, a.Stopped)
	require.Equal(t, "PUT 201", a.Log)

	got, ok := s.get("/v1/logs/k.log")
	require.True(t, ok)
	require.Contains(t, got, "line one\nsecret ***\n")
	require.NotContains(t, got, "tok-123")
	require.Contains(t, got, "stopped by its limit after")
}

// The caller leaving does not stop the outputs: they go when the run ends.
func TestOutputsGoWithNobodyWaiting(t *testing.T) {
	f := enginetest.New("bash:5.2")
	f.Behave = holdRuns
	s := newStore(t, map[string]string{})
	r := transferRunner(t, f, s)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		require.Eventually(t, func() bool { return f.Running() == 1 }, 2*time.Second, 5*time.Millisecond)
		cancel()
	}()
	_, err := r.Run(ctx, Request{Key: "k", Script: "work",
		Log: &LogTo{URL: s.URL + "/v1/logs/k.log"}})
	require.ErrorIs(t, err, context.Canceled)

	f.Release("jr-k", 0, "finished alone\n")
	require.Eventually(t, func() bool { _, ok := s.get("/v1/logs/k.log"); return ok }, 2*time.Second, 5*time.Millisecond)
	got, _ := s.get("/v1/logs/k.log")
	require.True(t, strings.HasPrefix(got, "finished alone\n"))
}

// After a restart nobody follows a run that ended meanwhile: the sweep sends
// its outputs, once.
func TestTheSweepSendsWhatARestartLeftBehind(t *testing.T) {
	f := enginetest.New("bash:5.2")
	f.Behave = holdRuns
	s := newStore(t, map[string]string{})
	// The process that started the run is gone: nothing follows it.
	old := transferRunner(t, f, s)
	req := Request{Key: "k", Script: "work", Log: &LogTo{URL: s.URL + "/v1/logs/k.log"}}
	require.NoError(t, old.validate(&req))
	_, err := old.start(context.Background(), req)
	require.NoError(t, err)
	f.Release("jr-k", 0, "ended during the restart\n")

	r2 := transferRunner(t, f, s)
	r2.Sweep(context.Background())
	got, ok := s.get("/v1/logs/k.log")
	require.True(t, ok)
	require.Contains(t, got, "ended during the restart")
	r2.Sweep(context.Background())
	require.Equal(t, 1, s.puts, "once")
}
