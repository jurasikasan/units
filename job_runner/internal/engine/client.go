// Package engine is the few Docker Engine API calls job_runner needs, over
// plain HTTP to its own daemon (DOCKER_HOST=unix:///var/run/dind/docker.sock).
// The official SDK would bring a large dependency tree for a dozen endpoints;
// the Engine API is the documented interface either way.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotFound is a container, image or volume the daemon does not have.
var ErrNotFound = errors.New("not found")

// ErrConflict is a name already taken.
var ErrConflict = errors.New("conflict")

// Client talks to one Docker daemon.
type Client struct {
	base string
	http *http.Client
}

// New takes unix:///path or tcp://host:port.
func New(host string) *Client {
	// No client timeout: wait blocks for as long as a run lasts. Every call is
	// bounded by its context instead.
	if path, ok := strings.CutPrefix(host, "unix://"); ok {
		var d net.Dialer
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.DialContext(ctx, "unix", path)
		}}
		return &Client{base: "http://docker", http: &http.Client{Transport: tr}}
	}
	return &Client{base: "http://" + strings.TrimPrefix(host, "tcp://"), http: &http.Client{}}
}

// NewHTTP is a client for a daemon at a plain http:// address (tests).
func NewHTTP(base string, hc *http.Client) *Client {
	return &Client{base: base, http: hc}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%s %s: %w", method, path, ErrNotFound)
	case resp.StatusCode == http.StatusConflict:
		return fmt.Errorf("%s %s: %w", method, path, ErrConflict)
	case resp.StatusCode >= 300:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		// /wait answers 200 at once and sends its body only when the
		// container stops, so a deadline that ends a wait surfaces here.
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	if w, ok := out.(io.Writer); ok {
		_, err = io.Copy(w, resp.Body)
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func labelFilter(label string) string {
	f, _ := json.Marshal(map[string][]string{"label": {label}})
	return url.QueryEscape(string(f))
}

// HasImage is whether the daemon already has the image.
func (c *Client) HasImage(ctx context.Context, image string) (bool, error) {
	err := c.do(ctx, http.MethodGet, "/images/"+image+"/json", nil, nil)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Pull fetches an image. The daemon answers 200 and reports a failure inside
// the progress stream, so the stream is read to its end.
func (c *Client) Pull(ctx context.Context, image string) error {
	var buf bytes.Buffer
	if err := c.do(ctx, http.MethodPost, "/images/create?fromImage="+url.QueryEscape(image), nil, &buf); err != nil {
		return err
	}
	dec := json.NewDecoder(&buf)
	for {
		var m struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&m); err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("pull %s: unreadable progress: %w", image, err)
		}
		if m.Error != "" {
			return fmt.Errorf("pull %s: %s", image, m.Error)
		}
	}
}

// CreateVolume makes a named volume.
func (c *Client) CreateVolume(ctx context.Context, name string, labels map[string]string) error {
	return c.do(ctx, http.MethodPost, "/volumes/create", map[string]any{"Name": name, "Labels": labels}, nil)
}

// RemoveVolume deletes a volume; one already gone is not an error.
func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	err := c.do(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name)+"?force=1", nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Volume is the part of a volume listing job_runner reads.
type Volume struct {
	Name      string            `json:"Name"`
	CreatedAt time.Time         `json:"CreatedAt"`
	Labels    map[string]string `json:"Labels"`
}

// Volumes lists the volumes carrying the label.
func (c *Client) Volumes(ctx context.Context, label string) ([]Volume, error) {
	var out struct {
		Volumes []Volume `json:"Volumes"`
	}
	err := c.do(ctx, http.MethodGet, "/volumes?filters="+labelFilter(label), nil, &out)
	return out.Volumes, err
}

// Spec is what a container is made from.
type Spec struct {
	Name       string // "" lets the daemon choose
	Image      string
	Entrypoint []string
	Cmd        []string
	User       string
	WorkDir    string
	Env        []string
	Labels     map[string]string
	Volume     string // mounted at VolumeAt
	VolumeAt   string
	// ReadOnly are host paths, as the daemon sees them, bound read-only:
	// source → target.
	ReadOnly map[string]string
}

// Create makes a container and returns its id. A taken name is ErrConflict.
func (c *Client) Create(ctx context.Context, s Spec) (string, error) {
	host := map[string]any{}
	var mounts []map[string]any
	if s.Volume != "" {
		// NoCopy: an empty volume mounted where the container's own
		// filesystem has a directory — WorkingDir creates one, as root — takes
		// that directory's owner, and the folder handed to uid 1000 goes back
		// to root (found live, BOT-434).
		mounts = append(mounts, map[string]any{"Type": "volume", "Source": s.Volume, "Target": s.VolumeAt,
			"VolumeOptions": map[string]any{"NoCopy": true}})
	}
	for src, dst := range s.ReadOnly {
		mounts = append(mounts, map[string]any{"Type": "bind", "Source": src, "Target": dst, "ReadOnly": true})
	}
	if mounts != nil {
		host["Mounts"] = mounts
	}
	body := map[string]any{
		"Image": s.Image, "Entrypoint": s.Entrypoint, "Cmd": s.Cmd, "User": s.User,
		"WorkingDir": s.WorkDir, "Env": s.Env, "Labels": s.Labels,
		// No TTY: a script sees a pipe, as in CI, so tools print no colours
		// and no \r\n into the answer (found live, BOT-434). The log then
		// comes in frames, which Logs takes apart.
		"Tty":        false,
		"HostConfig": host,
	}
	path := "/containers/create"
	if s.Name != "" {
		path += "?name=" + url.QueryEscape(s.Name)
	}
	var out struct {
		ID string `json:"Id"`
	}
	err := c.do(ctx, http.MethodPost, path, body, &out)
	return out.ID, err
}

// Start starts a created container.
func (c *Client) Start(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil)
}

// Wait blocks until the container stops, or ctx ends.
func (c *Client) Wait(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/wait", nil, nil)
}

// Container is the part of an inspect job_runner reads.
type Container struct {
	ID     string `json:"Id"`
	State  State  `json:"State"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

// State is a container's state.
type State struct {
	Running    bool      `json:"Running"`
	ExitCode   int       `json:"ExitCode"`
	StartedAt  time.Time `json:"StartedAt"`
	FinishedAt time.Time `json:"FinishedAt"`
}

// Inspect reads a container by id or name.
func (c *Client) Inspect(ctx context.Context, idOrName string) (*Container, error) {
	var out Container
	if err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(idOrName)+"/json", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Listed is the part of a container listing job_runner reads.
type Listed struct {
	ID     string            `json:"Id"`
	State  string            `json:"State"` // created, running, exited, …
	Labels map[string]string `json:"Labels"`
}

// List returns every container carrying the label, running or not.
func (c *Client) List(ctx context.Context, label string) ([]Listed, error) {
	var out []Listed
	err := c.do(ctx, http.MethodGet, "/containers/json?all=true&filters="+labelFilter(label), nil, &out)
	return out, err
}

// Logs returns the container's whole output, stdout and stderr in the order
// they were written.
func (c *Client) Logs(ctx context.Context, id string) ([]byte, error) {
	var buf bytes.Buffer
	if err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/logs?stdout=1&stderr=1", nil, &buf); err != nil {
		return nil, err
	}
	return Demux(buf.Bytes())
}

// Demux joins the frames of a container's log without a TTY: each frame is an
// 8-byte header — the stream (1 stdout, 2 stderr), three zero bytes, the
// payload's length big-endian — then the payload (docs.docker.com,
// ContainerAttach, "Stream format").
func Demux(raw []byte) ([]byte, error) {
	var out bytes.Buffer
	for len(raw) > 0 {
		if len(raw) < 8 {
			return out.Bytes(), fmt.Errorf("log: a frame header cut short (%d bytes)", len(raw))
		}
		n := int(raw[4])<<24 | int(raw[5])<<16 | int(raw[6])<<8 | int(raw[7])
		raw = raw[8:]
		if n > len(raw) {
			return out.Bytes(), fmt.Errorf("log: a frame of %d bytes with %d left", n, len(raw))
		}
		out.Write(raw[:n])
		raw = raw[n:]
	}
	return out.Bytes(), nil
}

// Kill stops a running container.
func (c *Client) Kill(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/kill", nil, nil)
}

// Remove deletes a container, running or not; one already gone is not an error.
func (c *Client) Remove(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id)+"?force=1", nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}
