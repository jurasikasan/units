package run

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jurasikasan/units/job_runner/internal/engine"
)

// Input is a file fetched into /work before the script starts.
type Input struct {
	URL    string `json:"url" jsonschema:"fetched with GET"`
	To     string `json:"to" jsonschema:"the path under /work it lands at, e.g. in/input.bundle"`
	Secret string `json:"secret,omitempty" jsonschema:"a secret of this server sent as Authorization: Bearer; its value never enters the run"`
}

// Output is a file of /work sent with PUT after the script has ended.
type Output struct {
	From   string `json:"from" jsonschema:"the path under /work, e.g. out/result.bundle"`
	URL    string `json:"url" jsonschema:"sent with PUT"`
	Secret string `json:"secret,omitempty" jsonschema:"a secret of this server sent as Authorization: Bearer; its value never enters the run"`
}

// LogTo is where the whole log of a run goes once it has ended.
type LogTo struct {
	URL    string `json:"url" jsonschema:"the whole output and the answer, sent with PUT however the run ended"`
	Secret string `json:"secret,omitempty" jsonschema:"a secret of this server sent as Authorization: Bearer"`
}

// Sent is how one output went.
type Sent struct {
	From   string `json:"from"`
	Status string `json:"status"`
}

// Changes is what the run did to /work: files added, changed (size or
// modification time) and removed, against the inputs it started with.
type Changes struct {
	Added   []Changed `json:"added"`
	Changed []Changed `json:"changed"`
	Removed []Changed `json:"removed"`
}

// Changed is one file of a change report.
type Changed struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// transfers is what a run's label keeps for the moment it ends: the inputs
// as they went in, and where its outputs and log go. Secret names only.
type transfers struct {
	Inputs  []entry  `json:"inputs,omitempty"`
	Outputs []Output `json:"outputs,omitempty"`
	Log     *LogTo   `json:"log,omitempty"`
}

type entry struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"` // unix seconds
}

func (r *Runner) validateTransfers(req *Request) error {
	if len(req.Inputs)+len(req.Outputs) > 0 || req.Log != nil {
		if len(r.TransferURLs) == 0 {
			return usage("inputs, outputs and log: this instance allows no transfers")
		}
	}
	urlOK := func(what, u, secret string) error {
		ok := false
		for _, p := range r.TransferURLs {
			if strings.HasPrefix(u, p) {
				ok = true
				break
			}
		}
		if !ok {
			return usage("%s url %q: not under %s", what, u, strings.Join(r.TransferURLs, ", "))
		}
		if secret != "" {
			if _, has := r.Secrets[secret]; !has {
				return usage("%s secret %q: this instance has none by that name", what, secret)
			}
		}
		return nil
	}
	seen := map[string]bool{}
	for i, in := range req.Inputs {
		p, err := workPath(in.To)
		if err != nil {
			return usage("input to %q: %v", in.To, err)
		}
		if seen[p] {
			return usage("input to %q: twice", in.To)
		}
		seen[p] = true
		req.Inputs[i].To = p
		if err := urlOK("input", in.URL, in.Secret); err != nil {
			return err
		}
	}
	for i, o := range req.Outputs {
		p, err := workPath(o.From)
		if err != nil {
			return usage("output from %q: %v", o.From, err)
		}
		req.Outputs[i].From = p
		if err := urlOK("output", o.URL, o.Secret); err != nil {
			return err
		}
	}
	if req.Log != nil {
		if err := urlOK("log", req.Log.URL, req.Log.Secret); err != nil {
			return err
		}
	}
	return nil
}

// workPath is p as a clean path under /work, or why it is not one.
func workPath(p string) (string, error) {
	if p == "" || strings.HasPrefix(p, "/") {
		return "", errors.New("a path under /work, relative")
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", errors.New("a path under /work, relative")
	}
	return c, nil
}

// fetched is a run's inputs as a tar for /work, in a file of this process.
type fetched struct {
	tar      *os.File
	manifest []entry
}

func (f *fetched) close() {
	if f.tar != nil {
		_ = f.tar.Close()
		_ = os.Remove(f.tar.Name())
	}
}

func (r *Runner) request(ctx context.Context, method, url, secret string, body io.Reader, size int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+r.Secrets[secret])
	}
	hc := r.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	return hc.Do(req)
}

// fetchInputs fetches every input into one tar, owned by the run's user,
// with the directories on the way. A failure is the call's: nothing runs.
func (r *Runner) fetchInputs(ctx context.Context, inputs []Input) (*fetched, error) {
	out := &fetched{}
	if len(inputs) == 0 {
		return out, nil
	}
	f, err := os.CreateTemp("", "inputs-*.tar")
	if err != nil {
		return nil, err
	}
	out.tar = f
	tw := tar.NewWriter(f)
	now := r.Now().Truncate(time.Second)
	dirs := map[string]bool{}
	for _, in := range inputs {
		for d := path.Dir(in.To); d != "."; d = path.Dir(d) {
			if dirs[d] {
				continue
			}
			dirs[d] = true
			if err := tw.WriteHeader(&tar.Header{Name: d + "/", Typeflag: tar.TypeDir, Mode: 0o755, Uid: 1000, Gid: 1000, ModTime: now}); err != nil {
				out.close()
				return nil, err
			}
		}
		e, err := r.fetchOne(ctx, tw, in, now)
		if err != nil {
			out.close()
			return nil, &UsageError{msg: r.mask(fmt.Sprintf("input %s: %v", in.To, err))}
		}
		out.manifest = append(out.manifest, e)
	}
	if err := tw.Close(); err != nil {
		out.close()
		return nil, err
	}
	return out, nil
}

func (r *Runner) fetchOne(ctx context.Context, tw *tar.Writer, in Input, now time.Time) (entry, error) {
	resp, err := r.request(ctx, http.MethodGet, in.URL, in.Secret, nil, 0)
	if err != nil {
		return entry{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return entry{}, fmt.Errorf("GET answered %d", resp.StatusCode)
	}
	// The tar header needs the size first; a body without a length is
	// spooled to a file.
	body, size := io.Reader(resp.Body), resp.ContentLength
	if size < 0 {
		tmp, err := os.CreateTemp("", "input-*")
		if err != nil {
			return entry{}, err
		}
		defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
		if size, err = io.Copy(tmp, resp.Body); err != nil {
			return entry{}, err
		}
		if _, err := tmp.Seek(0, 0); err != nil {
			return entry{}, err
		}
		body = tmp
	}
	if err := tw.WriteHeader(&tar.Header{Name: in.To, Typeflag: tar.TypeReg, Mode: 0o644, Uid: 1000, Gid: 1000, Size: size, ModTime: now}); err != nil {
		return entry{}, err
	}
	if _, err := io.CopyN(tw, body, size); err != nil {
		return entry{}, err
	}
	return entry{Path: in.To, Size: size, Mtime: now.Unix()}, nil
}

// changes reads /work of a stopped run and compares it with what went in.
func (r *Runner) changes(ctx context.Context, id string, before []entry) (*Changes, error) {
	rc, err := r.Docker.GetArchive(ctx, id, workDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	was := map[string]entry{}
	for _, e := range before {
		was[e.Path] = e
	}
	ch := &Changes{Added: []Changed{}, Changed: []Changed{}, Removed: []Changed{}}
	tr := tar.NewReader(rc)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		_, p, ok := strings.Cut(h.Name, "/") // the first component is "work"
		if !ok || p == "" {
			continue
		}
		e, had := was[p]
		delete(was, p)
		switch {
		case !had:
			ch.Added = append(ch.Added, Changed{p, h.Size})
		case e.Size != h.Size || e.Mtime != h.ModTime.Unix():
			ch.Changed = append(ch.Changed, Changed{p, h.Size})
		}
	}
	for p, e := range was {
		ch.Removed = append(ch.Removed, Changed{p, e.Size})
	}
	for _, l := range [][]Changed{ch.Added, ch.Changed, ch.Removed} {
		sort.Slice(l, func(i, j int) bool { return l[i].Path < l[j].Path })
	}
	return ch, nil
}

// sendOutput sends one file of a stopped run's /work; the result is a line
// of the answer, never an error of the run.
func (r *Runner) sendOutput(ctx context.Context, id string, o Output) string {
	rc, err := r.Docker.GetArchive(ctx, id, workDir+"/"+o.From)
	if errors.Is(err, engine.ErrNotFound) {
		return "missing"
	}
	if err != nil {
		return "not read: " + err.Error()
	}
	defer func() { _ = rc.Close() }()
	tr := tar.NewReader(rc)
	h, err := tr.Next()
	if err != nil {
		return "not read: " + err.Error()
	}
	if h.Typeflag != tar.TypeReg {
		return "not a file, not sent"
	}
	return r.put(ctx, o.URL, o.Secret, io.LimitReader(tr, h.Size), h.Size)
}

// sendLog sends the whole output of a stopped run, then its answer.
func (r *Runner) sendLog(ctx context.Context, id string, to LogTo, a *Answer) string {
	out, err := r.Docker.Logs(ctx, id)
	if err != nil {
		return "not read: " + err.Error()
	}
	body := r.mask(string(out)) + "\n\n---\n" + Render(a)
	return r.put(ctx, to.URL, to.Secret, strings.NewReader(body), int64(len(body)))
}

func (r *Runner) put(ctx context.Context, url, secret string, body io.Reader, size int64) string {
	resp, err := r.request(ctx, http.MethodPut, url, secret, body, size)
	if err != nil {
		return "failed: " + err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Sprintf("failed: PUT %d %s", resp.StatusCode, strings.TrimSpace(string(bytes.ToValidUTF8(msg, nil))))
	}
	return fmt.Sprintf("PUT %d", resp.StatusCode)
}
