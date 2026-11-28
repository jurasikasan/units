// Package sight is the sense the world reaches through as light, and as what
// was read (CONTRACT.md): it opens sessions that take a video stream in as
// RTP (H.264, decoded through ffmpeg) and already-worded observations as
// SenML, notices when the scene changes, keeps the frame that changed and
// the one described in artifact_store, has what is seen put into words, and
// reports it on the sight_events stream. It decides nothing about what it saw.
package sight

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jurasikasan/units/senses/sight/internal/artifacts"
	"github.com/jurasikasan/units/senses/sight/internal/change"
	"github.com/jurasikasan/units/senses/sight/internal/ffdec"
	"github.com/jurasikasan/units/senses/sight/internal/rtpvideo"
	"github.com/jurasikasan/units/senses/sight/internal/vision"
)

// Defaults (CONTRACT §5).
const (
	// DefaultVisionURL is an OpenAI-shaped chat base: a router such as
	// LiteLLM, its key in VisionKey, or one engine with none.
	DefaultVisionURL       = "http://litellm:4000/v1"
	DefaultVisionModel     = "vision"
	DefaultVisionTimeout   = 60 * time.Second
	DefaultChangeThreshold = 0.06
	DefaultDescribeEvery   = 10 * time.Second
	DefaultSampleEvery     = 500 * time.Millisecond
	DefaultMaxSessions     = 4
	DefaultIdleTimeout     = 2 * time.Minute
	DefaultInURL           = "http://sight:8080"
	DefaultVideoInHost     = "sight"
	DefaultPortMin         = 45000
	DefaultPortMax         = 45199
	DefaultFFmpeg          = "ffmpeg"
	DefaultArtifactURL     = "http://artifact_store:8094"
	DefaultArtifactSpace   = "sight"
	DefaultPort            = 8080
)

// RecognizerScene is the only recognizer this step has: the scene in words
// and the text read in it.
const RecognizerScene = "scene"

// queueDepth is how many packs a session holds while it is busy (a frame
// being described); beyond that a sender is told busy.
const queueDepth = 16

// artifactTimeout is how long one frame may take to be put.
const artifactTimeout = 15 * time.Second

// jpegQuality is what frames are kept and described at.
const jpegQuality = 85

// Decoder turns an H.264 Annex B stream into pictures in presentation
// order, one per access unit: ffmpeg (internal/ffdec), or a fake in tests.
type Decoder interface {
	Write(au []byte) error
	CloseInput() error
	Pictures() <-chan *image.YCbCr
	Err() error
}

// Config holds the runtime configuration (CONTRACT §5).
type Config struct {
	Sink            Sink
	VisionURL       string
	VisionKey       string // the router's bearer; empty for an engine
	VisionModel     string // OpenAI's model: the router's group, or the engine's name
	VisionTimeout   time.Duration
	ChangeThreshold float64       // mean grey difference, 0–1, that is a change
	DescribeEvery   time.Duration // on the input's timeline: at most one description per this much
	SampleEvery     time.Duration // on the stream's timeline: at most one decoded picture per this much goes to the detector
	MaxSessions     int
	IdleTimeout     time.Duration
	InURL           string // the base senders reach sight by; sight_in is {InURL}/in/{session_id}
	VideoInHost     string // the host video_in is given out with
	PortMin         int    // the video_in range; negative: an ephemeral port pair (the tests)
	PortMax         int
	FFmpeg          string // the ffmpeg binary
	NewDecoder      func() (Decoder, error)
	ArtifactURL     string // artifact_store's base
	ArtifactSpace   string
	HTTPClient      *http.Client
}

// Options is what an open may ask for beyond the deployment's config. Zero
// values keep the deployment's; IdleTimeout is a pointer, since 0 is a
// value of its own (never idle out).
type Options struct {
	Recognize       []string
	Language        string
	VisionModel     string
	VisionPrompt    string
	DescribeEvery   time.Duration
	ChangeThreshold float64
	IdleTimeout     *time.Duration
}

// Opened is what an open hands out.
type Opened struct {
	SessionID string
	// SightIn is the URL SenML packs (words, single frames) are POSTed to.
	SightIn string
	// VideoIn is "host:port": RTP video goes there, RTCP to the port above.
	VideoIn string
}

// Service runs sight sessions.
type Service struct {
	cfg      Config
	mu       sync.Mutex
	sessions map[string]*session
	active   int
}

// NewService applies the defaults to cfg and returns a Service.
func NewService(cfg Config) *Service {
	if cfg.Sink == nil {
		cfg.Sink = &InMemorySink{}
	}
	if cfg.VisionURL == "" {
		cfg.VisionURL = DefaultVisionURL
	}
	if cfg.VisionModel == "" {
		cfg.VisionModel = DefaultVisionModel
	}
	if cfg.VisionTimeout <= 0 {
		cfg.VisionTimeout = DefaultVisionTimeout
	}
	if cfg.ChangeThreshold <= 0 {
		cfg.ChangeThreshold = DefaultChangeThreshold
	}
	// DescribeEvery 0 is no limit: every change is described at once.
	if cfg.DescribeEvery < 0 {
		cfg.DescribeEvery = DefaultDescribeEvery
	}
	// SampleEvery 0 is every picture.
	if cfg.SampleEvery < 0 {
		cfg.SampleEvery = DefaultSampleEvery
	}
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = DefaultMaxSessions
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	if cfg.InURL == "" {
		cfg.InURL = DefaultInURL
	}
	if cfg.VideoInHost == "" {
		cfg.VideoInHost = DefaultVideoInHost
	}
	if cfg.PortMin == 0 {
		cfg.PortMin = DefaultPortMin
	}
	if cfg.PortMax == 0 {
		cfg.PortMax = DefaultPortMax
	}
	if cfg.FFmpeg == "" {
		cfg.FFmpeg = DefaultFFmpeg
	}
	if cfg.NewDecoder == nil {
		bin := cfg.FFmpeg
		cfg.NewDecoder = func() (Decoder, error) { return ffdec.Start(bin) }
	}
	if cfg.ArtifactURL == "" {
		cfg.ArtifactURL = DefaultArtifactURL
	}
	if cfg.ArtifactSpace == "" {
		cfg.ArtifactSpace = DefaultArtifactSpace
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: cfg.VisionTimeout}
	}
	return &Service{cfg: cfg, sessions: map[string]*session{}}
}

// Open starts a session (CONTRACT §2.1): its video_in, an RTP port pair, and
// its sight_in, the URL senders POST SenML packs to. traceparent is the W3C
// traceparent of the MCP open call; every event of the session carries
// exactly that value.
func (s *Service) Open(ctx context.Context, o Options, traceparent string) (Opened, error) {
	if o.DescribeEvery < 0 || o.ChangeThreshold < 0 || o.ChangeThreshold > 1 || o.IdleTimeout != nil && *o.IdleTimeout < 0 {
		return Opened{}, fmt.Errorf("bad_argument: describe_every_ms or idle_timeout_ms below zero, or change_threshold outside 0–1")
	}
	recognize := o.Recognize
	if len(recognize) == 0 {
		recognize = []string{RecognizerScene}
	}
	for _, r := range recognize {
		if r != RecognizerScene {
			return Opened{}, fmt.Errorf("unsupported_recognizer: %q is not a recognizer in this step", r)
		}
	}
	cfg := s.cfg
	if o.DescribeEvery > 0 {
		cfg.DescribeEvery = o.DescribeEvery
	}
	if o.ChangeThreshold > 0 {
		cfg.ChangeThreshold = o.ChangeThreshold
	}
	if o.IdleTimeout != nil {
		cfg.IdleTimeout = *o.IdleTimeout // 0: the session never idles out
	}
	door := vision.Door{URL: cfg.VisionURL, Key: cfg.VisionKey, Model: cfg.VisionModel, Prompt: o.VisionPrompt}
	if o.VisionModel != "" {
		door.Model = o.VisionModel
	}

	s.mu.Lock()
	if s.active >= s.cfg.MaxSessions {
		s.mu.Unlock()
		return Opened{}, fmt.Errorf("capacity: %d sessions already open (limit %d)", s.active, s.cfg.MaxSessions)
	}
	s.active++
	s.mu.Unlock()

	in, err := s.allocateIn()
	if err != nil {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
		return Opened{}, err
	}

	id := newID("sg")
	se := &session{
		id:          id,
		svc:         s,
		cfg:         cfg,
		door:        door,
		store:       artifacts.Store{URL: cfg.ArtifactURL, Space: cfg.ArtifactSpace, Client: cfg.HTTPClient},
		language:    o.Language,
		traceparent: traceparent,
		in:          in,
		opened:      time.Now(),
		queue:       make(chan []item, queueDepth),
		frames:      make(chan item, 1),
		done:        make(chan struct{}),
	}
	s.mu.Lock()
	s.sessions[id] = se
	s.mu.Unlock()
	// The session outlives the open call, whose context ends when it answers.
	go se.video()
	go se.worker(context.WithoutCancel(ctx))
	return Opened{
		SessionID: id,
		SightIn:   strings.TrimRight(cfg.InURL, "/") + "/in/" + id,
		VideoIn:   net.JoinHostPort(cfg.VideoInHost, strconv.Itoa(in.Port())),
	}, nil
}

// allocateIn binds the first free even port of the video_in range with the
// one above it for RTCP, or an ephemeral pair when PortMin is negative (the
// tests).
func (s *Service) allocateIn() (*rtpvideo.In, error) {
	var lastErr error
	if s.cfg.PortMin < 0 {
		for range 20 {
			in, err := rtpvideo.Listen("", 0)
			if err == nil {
				return in, nil
			}
			lastErr = err
		}
		return nil, fmt.Errorf("capacity: cannot bind video_in: %w", lastErr)
	}
	for p := s.cfg.PortMin + s.cfg.PortMin%2; p+1 <= s.cfg.PortMax; p += 2 {
		in, err := rtpvideo.Listen("", p)
		if err == nil {
			return in, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("capacity: no free video_in port pair in %d-%d (last: %v)", s.cfg.PortMin, s.cfg.PortMax, lastErr)
}

// Close closes a session: video_in stops, what was received is decoded and
// looked at, an observation still waiting for its description is described
// now, then session-closed goes out; Close returns once it has. Closing a
// closed session is a no-op with no second event.
func (s *Service) Close(sessionID string) error {
	se := s.session(sessionID)
	if se == nil {
		return fmt.Errorf("unknown_session: no such session %q", sessionID)
	}
	if se.beginClose() {
		se.queue <- []item{{close: "closed"}}
	}
	<-se.done
	return nil
}

func (s *Service) session(id string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

// item is one thing a session's worker does: a picture (from the video
// stream or a single frame), an already-worded observation, or the end.
type item struct {
	kind  string // "video", "frame", "description", "read"
	at    time.Time
	words string
	pic   *picture
	close string // the reason, on the last item
}

// picture is a frame the detector looks at, and what became of it: kept in
// artifact_store at most once, however many events name it.
type picture struct {
	img   image.Image
	raw   []byte // the sender's own bytes, when they are a JPEG
	grid  *change.Grid
	at    time.Time
	clock string // what placed it in time: rtcp-sr, first-packet, senml

	jpg    []byte
	ref    map[string]any // the reference once put
	refErr string         // why it could not be put
}

// jpeg is the picture as a JPEG: the sender's own when it was one.
func (p *picture) jpeg() ([]byte, error) {
	if p.raw != nil {
		return p.raw, nil
	}
	if p.jpg == nil {
		var b bytes.Buffer
		if err := jpeg.Encode(&b, p.img, &jpeg.Options{Quality: jpegQuality}); err != nil {
			return nil, err
		}
		p.jpg = b.Bytes()
	}
	return p.jpg, nil
}

// observation is a change seen and not yet put into words.
type observation struct {
	id      string
	kind    string // "video" or "frame"
	started time.Time
	last    *picture // the latest picture of it: the one described
}

// session is one open sight session. After Open it is driven by one worker
// goroutine, the only place its events are emitted, so their order is total.
type session struct {
	id          string
	svc         *Service
	cfg         Config
	door        vision.Door
	store       artifacts.Store
	language    string
	traceparent string
	in          *rtpvideo.In
	opened      time.Time
	queue       chan []item
	frames      chan item // sampled pictures from video_in: the latest waits, an older one gives way
	done        chan struct{}
	lastPack    atomic.Int64 // UnixNano of the last pack taken

	mu      sync.Mutex
	closing bool

	// the worker's own
	ref           *change.Grid // the frame last described: what the scene was said to be
	lastDescribed time.Time
	described     bool
	pending       *observation
}

// beginClose marks the session closing and stops video_in; false if it
// already was.
func (se *session) beginClose() bool {
	se.mu.Lock()
	if se.closing {
		se.mu.Unlock()
		return false
	}
	se.closing = true
	se.mu.Unlock()
	se.in.Stop()
	return true
}

// errClosed and errBusy are what a sender is told when a pack cannot be taken.
var (
	errClosed = fmt.Errorf("closed")
	errBusy   = fmt.Errorf("busy")
)

// take queues a pack's items without waiting: a closed session refuses it, a
// full queue is busy.
func (se *session) take(items []item) error {
	se.mu.Lock()
	defer se.mu.Unlock()
	if se.closing {
		return errClosed
	}
	select {
	case se.queue <- items:
		se.lastPack.Store(time.Now().UnixNano())
		return nil
	default:
		return errBusy
	}
}

// lastInput is when the session last got anything: a pack, a packet, or
// its open.
func (se *session) lastInput() time.Time {
	t := se.opened
	if p := se.in.LastPacket(); p.After(t) {
		t = p
	}
	if n := se.lastPack.Load(); n != 0 && time.Unix(0, n).After(t) {
		t = time.Unix(0, n)
	}
	return t
}

// worker takes packs and sampled pictures as they come. It ends once the
// session is closing and video_in has given its last picture: what was
// queued is handled, a waiting observation described, then session-closed.
func (se *session) worker(ctx context.Context) {
	defer close(se.done)
	frames := se.frames
	idle := se.cfg.IdleTimeout
	if idle <= 0 {
		idle = time.Second // never idles out: the tick only has to come round
	}
	tick := time.NewTicker(min(max(idle/4, 10*time.Millisecond), time.Second))
	defer tick.Stop()
	reason := ""
	for reason == "" || frames != nil {
		select {
		case items := <-se.queue:
			for _, it := range items {
				if it.close != "" {
					reason = it.close
					continue
				}
				se.handle(ctx, it)
			}
		case it, ok := <-frames:
			if !ok {
				frames = nil
				continue
			}
			se.handle(ctx, it)
		case <-tick.C:
			if reason == "" && se.cfg.IdleTimeout > 0 && time.Since(se.lastInput()) >= se.cfg.IdleTimeout && se.beginClose() {
				reason = "idle"
			}
		}
	}
	// What was queued before the end is still the session's.
	for drained := false; !drained; {
		select {
		case items := <-se.queue:
			for _, it := range items {
				if it.close == "" {
					se.handle(ctx, it)
				}
			}
		default:
			drained = true
		}
	}
	se.finish(ctx, reason)
}

func (se *session) handle(ctx context.Context, it item) {
	switch it.kind {
	case "video", "frame":
		se.frame(ctx, it)
	default:
		se.worded(ctx, it)
	}
}

// frame is the detector: a picture that differs from the one last described
// by the change threshold opens an observation (start-of-input, at the
// picture's time, the picture kept); the observation is described at once,
// or, within DescribeEvery of the last description, at the first later
// picture past it or at the end.
func (se *session) frame(ctx context.Context, it item) {
	if se.pending == nil {
		if se.ref != nil && change.Diff(se.ref, it.pic.grid) < se.cfg.ChangeThreshold {
			return
		}
		se.pending = &observation{id: newID("obs"), kind: it.kind, started: it.at}
		data := map[string]any{
			"request_id": se.pending.id,
			"input_type": it.kind,
			"started_at": stamp(it.at),
			"clock":      it.pic.clock,
		}
		se.attach(ctx, data, it.pic, se.pending.id+".change.jpg")
		se.emit(ctx, TypeStartOfInput, data)
	}
	se.pending.last = it.pic
	if !se.described || it.at.Sub(se.lastDescribed) >= se.cfg.DescribeEvery {
		se.describe(ctx)
	}
}

// attach puts the picture to artifact_store, once, and names it in data as
// frame; when it cannot be put, frame_error says why and the event goes
// out without it.
func (se *session) attach(ctx context.Context, data map[string]any, p *picture, name string) {
	if p.ref == nil && p.refErr == "" {
		b, err := p.jpeg()
		if err == nil {
			cctx, cancel := context.WithTimeout(ctx, artifactTimeout)
			var r artifacts.Ref
			r, err = se.store.Put(cctx, se.id+"."+name, "image/jpeg", b)
			cancel()
			if err == nil {
				p.ref = map[string]any{
					"sha256":       r.SHA256,
					"space":        r.Space,
					"name":         r.Name,
					"content_type": r.ContentType,
					"at":           stamp(p.at),
				}
			}
		}
		if err != nil {
			log.Printf("sight: %s: frame not kept: %v", se.id, err)
			p.refErr = "artifact_store: " + err.Error()
		}
	}
	if p.ref != nil {
		data["frame"] = p.ref
	} else {
		data["frame_error"] = p.refErr
	}
}

// describe puts the waiting observation into words and completes it.
func (se *session) describe(ctx context.Context) {
	o := se.pending
	se.pending = nil
	var res vision.Result
	b, err := o.last.jpeg()
	if err == nil {
		cctx, cancel := context.WithTimeout(ctx, se.cfg.VisionTimeout)
		res, err = vision.Describe(cctx, se.door, se.cfg.HTTPClient, b, "image/jpeg", se.language)
		cancel()
	}
	cause := CauseSuccess
	switch {
	case err != nil:
		log.Printf("sight: %s %s: %v", se.id, o.id, err)
		cause, res = CauseRecognizerError, vision.Result{}
	case res.Text == "" && res.Read == "":
		cause = CauseNoMatch
	}
	// The scene is now what was said of it, even when saying failed: a door
	// that is down is not asked again for every frame.
	se.ref, se.lastDescribed, se.described = o.last.grid, o.last.at, true
	data := map[string]any{
		"request_id":       o.id,
		"completion_cause": cause,
		"input_type":       o.kind,
		"text":             res.Text,
		"read":             res.Read,
		"language":         se.language,
		"started_at":       stamp(o.started),
		"ended_at":         stamp(o.last.at),
		"clock":            o.last.clock,
	}
	se.attach(ctx, data, o.last, o.id+".described.jpg")
	se.emit(ctx, TypeRecognitionComplete, data)
}

// worded is an observation that came as words: no detector, no model, its
// recognition-complete at its own time.
func (se *session) worded(ctx context.Context, it item) {
	text, read := it.words, ""
	if it.kind == "read" {
		text, read = "", it.words
	}
	se.emit(ctx, TypeRecognitionComplete, map[string]any{
		"request_id":       newID("obs"),
		"completion_cause": CauseSuccess,
		"input_type":       it.kind,
		"text":             text,
		"read":             read,
		"language":         se.language,
		"started_at":       stamp(it.at),
		"ended_at":         stamp(it.at),
	})
}

// finish completes a waiting observation, says the session is closed and
// frees its place.
func (se *session) finish(ctx context.Context, reason string) {
	if se.pending != nil {
		se.describe(ctx)
	}
	se.emit(ctx, TypeSessionClosed, map[string]any{"reason": reason})
	se.svc.mu.Lock()
	se.svc.active--
	se.svc.mu.Unlock()
}

func (se *session) emit(ctx context.Context, typ string, data map[string]any) {
	data["session_id"] = se.id
	se.cfg.Sink.Emit(ctx, Event{Type: typ, Subject: se.id, Data: data, Traceparent: se.traceparent})
}

// stamp is a time as the events carry it: RFC 3339, UTC, milliseconds.
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func newID(prefix string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}
