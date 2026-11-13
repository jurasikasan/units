// Package enginetest is an in-process fake of the Docker Engine API calls
// job_runner makes (docs.docker.com/reference/api/engine). Containers do not
// run anything: a test decides, per started container, what it prints, how
// it exits and whether it keeps running until killed.
package enginetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Outcome is what a started container does.
type Outcome struct {
	Output string
	Exit   int
	Hold   bool // runs until killed, or until Release
}

// Created is a container as it was asked for.
type Created struct {
	Name       string
	Image      string
	Entrypoint []string
	Cmd        []string
	User       string
	WorkDir    string
	Env        []string
	Labels     map[string]string
	Mounts     []map[string]any
}

type container struct {
	id      string
	spec    Created
	running bool
	exit    int
	output  string
	started time.Time
	ended   time.Time
	stopped chan struct{}
}

// Fake is a daemon.
type Fake struct {
	*httptest.Server
	// Behave decides what a started container does. Nil: exit 0, no output.
	Behave func(c Created) Outcome
	Now    func() time.Time

	mu         sync.Mutex
	images     map[string]bool
	pullErr    map[string]string
	containers map[string]*container // by id
	names      map[string]string     // name → id
	volumes    map[string]map[string]string
	volCreated map[string]time.Time
	seq        int
	// Log is every container ever created, in order.
	Log []Created
	// Pulled is every image pulled, in order.
	Pulled []string
	// Requests is every request line, method and path with its query.
	Requests []string
}

// New starts a fake daemon that already has the images given.
func New(images ...string) *Fake {
	f := &Fake{Now: time.Now, images: map[string]bool{}, pullErr: map[string]string{},
		containers: map[string]*container{}, names: map[string]string{},
		volumes: map[string]map[string]string{}, volCreated: map[string]time.Time{}}
	for _, i := range images {
		f.images[i] = true
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// FailPull makes a pull of the image report msg in its progress stream.
func (f *Fake) FailPull(image, msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pullErr[image] = msg
}

// Running is the number of containers running now.
func (f *Fake) Running() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.containers {
		if c.running {
			n++
		}
	}
	return n
}

// Containers is the number of containers that exist now.
func (f *Fake) Containers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.containers)
}

// Volumes is the names of the volumes that exist now.
func (f *Fake) Volumes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n := range f.volumes {
		out = append(out, n)
	}
	return out
}

// AgeVolume makes a volume look created d ago.
func (f *Fake) AgeVolume(name string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volCreated[name] = f.Now().Add(-d)
}

// Release ends a held container by name with the exit code given.
func (f *Fake) Release(name string, exit int, output string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.containers[f.names[name]]; c != nil && c.running {
		c.output += output
		f.stop(c, exit)
	}
}

// Age makes a stopped container look stopped d ago.
func (f *Fake) Age(name string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.containers[f.names[name]]; c != nil {
		c.ended = f.Now().Add(-d)
	}
}

func (f *Fake) stop(c *container, exit int) {
	c.running = false
	c.exit = exit
	c.ended = f.Now()
	close(c.stopped)
}

func (f *Fake) lookup(ref string) *container {
	if id, ok := f.names[ref]; ok {
		return f.containers[id]
	}
	return f.containers[ref]
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func labelOf(q url.Values) string {
	var m map[string][]string
	_ = json.Unmarshal([]byte(q.Get("filters")), &m)
	if l := m["label"]; len(l) > 0 {
		return l[0]
	}
	return ""
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	f.mu.Lock()
	f.Requests = append(f.Requests, r.Method+" "+r.URL.RequestURI())
	f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && p[0] == "images" && len(p) >= 3:
		name, _ := url.PathUnescape(strings.Join(p[1:len(p)-1], "/"))
		f.mu.Lock()
		ok := f.images[name]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "no such image", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]string{"Id": name})
	case r.Method == http.MethodPost && r.URL.Path == "/images/create":
		img := r.URL.Query().Get("fromImage")
		f.mu.Lock()
		f.Pulled = append(f.Pulled, img)
		msg := f.pullErr[img]
		if msg == "" {
			f.images[img] = true
		}
		f.mu.Unlock()
		writeJSON(w, map[string]string{"status": "Pulling"})
		if msg != "" {
			writeJSON(w, map[string]string{"error": msg})
		}
	case r.Method == http.MethodPost && r.URL.Path == "/volumes/create":
		var b struct {
			Name   string
			Labels map[string]string
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.volumes[b.Name] = b.Labels
		f.volCreated[b.Name] = f.Now()
		f.mu.Unlock()
		writeJSON(w, map[string]string{"Name": b.Name})
	case r.Method == http.MethodGet && r.URL.Path == "/volumes":
		label := labelOf(r.URL.Query())
		f.mu.Lock()
		var out []map[string]any
		for n, l := range f.volumes {
			if _, ok := l[label]; ok {
				out = append(out, map[string]any{"Name": n, "Labels": l, "CreatedAt": f.volCreated[n]})
			}
		}
		f.mu.Unlock()
		writeJSON(w, map[string]any{"Volumes": out})
	case r.Method == http.MethodDelete && p[0] == "volumes" && len(p) == 2:
		f.mu.Lock()
		_, ok := f.volumes[p[1]]
		delete(f.volumes, p[1])
		f.mu.Unlock()
		if !ok {
			http.Error(w, "no such volume", http.StatusNotFound)
		}
	case r.Method == http.MethodPost && r.URL.Path == "/containers/create":
		f.create(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/containers/json":
		label := labelOf(r.URL.Query())
		f.mu.Lock()
		var out []map[string]any
		for _, c := range f.containers {
			if _, ok := c.spec.Labels[label]; ok {
				state := "exited"
				if c.running {
					state = "running"
				}
				out = append(out, map[string]any{"Id": c.id, "State": state, "Labels": c.spec.Labels})
			}
		}
		f.mu.Unlock()
		writeJSON(w, out)
	case p[0] == "containers" && len(p) >= 2:
		f.container(w, r, p)
	default:
		http.Error(w, "fake daemon: "+r.Method+" "+r.URL.Path+" not served", http.StatusNotImplemented)
	}
}

func (f *Fake) create(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Image      string
		Entrypoint []string
		Cmd        []string
		User       string
		WorkingDir string
		Env        []string
		Labels     map[string]string
		HostConfig struct{ Mounts []map[string]any }
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	name := r.URL.Query().Get("name")
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.images[b.Image] {
		http.Error(w, "no such image: "+b.Image, http.StatusNotFound)
		return
	}
	if _, taken := f.names[name]; name != "" && taken {
		http.Error(w, "name in use", http.StatusConflict)
		return
	}
	f.seq++
	id := fmt.Sprintf("c%04d", f.seq)
	spec := Created{Name: name, Image: b.Image, Entrypoint: b.Entrypoint, Cmd: b.Cmd, User: b.User,
		WorkDir: b.WorkingDir, Env: b.Env, Labels: b.Labels, Mounts: b.HostConfig.Mounts}
	f.containers[id] = &container{id: id, spec: spec, stopped: make(chan struct{})}
	if name != "" {
		f.names[name] = id
	}
	f.Log = append(f.Log, spec)
	writeJSON(w, map[string]string{"Id": id})
}

func (f *Fake) container(w http.ResponseWriter, r *http.Request, p []string) {
	f.mu.Lock()
	c := f.lookup(p[1])
	f.mu.Unlock()
	if c == nil {
		http.Error(w, "no such container", http.StatusNotFound)
		return
	}
	op := ""
	if len(p) > 2 {
		op = p[2]
	}
	switch {
	case r.Method == http.MethodPost && op == "start":
		o := Outcome{}
		if f.Behave != nil {
			o = f.Behave(c.spec)
		}
		f.mu.Lock()
		c.running, c.started, c.output = true, f.Now(), o.Output
		if !o.Hold {
			f.stop(c, o.Exit)
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && op == "wait":
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-c.stopped:
			f.mu.Lock()
			code := c.exit
			f.mu.Unlock()
			writeJSON(w, map[string]int{"StatusCode": code})
		case <-r.Context().Done():
		}
	case r.Method == http.MethodGet && op == "json":
		f.mu.Lock()
		state := map[string]any{"Running": c.running, "ExitCode": c.exit, "StartedAt": c.started, "FinishedAt": c.ended}
		labels := c.spec.Labels
		f.mu.Unlock()
		writeJSON(w, map[string]any{"Id": c.id, "State": state, "Config": map[string]any{"Labels": labels}})
	case r.Method == http.MethodGet && op == "logs":
		f.mu.Lock()
		out := c.output
		f.mu.Unlock()
		// No TTY: the daemon frames the log (8-byte header per frame).
		if out != "" {
			n := len(out)
			_, _ = w.Write([]byte{1, 0, 0, 0, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
			_, _ = w.Write([]byte(out))
		}
	case r.Method == http.MethodPost && op == "kill":
		f.mu.Lock()
		defer f.mu.Unlock()
		if !c.running {
			http.Error(w, "is not running", http.StatusConflict)
			return
		}
		f.stop(c, 137)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && op == "":
		f.mu.Lock()
		if c.running {
			f.stop(c, 137)
		}
		delete(f.containers, c.id)
		if c.spec.Name != "" {
			delete(f.names, c.spec.Name)
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "fake daemon: "+r.Method+" "+r.URL.Path+" not served", http.StatusNotImplemented)
	}
}
