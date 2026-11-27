package sight

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jurasikasan/units/senses/sight/internal/senml"
)

// fakeVision is the vision door in the tests: an OpenAI-shaped chat endpoint
// that answers what it is told to, and counts what it was asked.
type fakeVision struct {
	mu     sync.Mutex
	calls  int
	answer string
	status int
}

func (f *fakeVision) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls++
	answer, status := f.answer, f.status
	f.mu.Unlock()
	if r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	if status != 0 {
		http.Error(w, "down", status)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
		"message": map[string]string{"role": "assistant", "content": answer},
	}}})
}

func (f *fakeVision) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type harness struct {
	svc    *Service
	sink   *InMemorySink
	vision *fakeVision
	store  *fakeStore
	in     *httptest.Server
	// videoIn is the last open's video_in.
	videoIn string
}

// fakeStore is artifact_store in the tests: PUT /v1/{space}/{name} keeps the
// bytes, GET /v1/_sha256/{hex} gives them back; down makes every PUT a 503.
type fakeStore struct {
	mu    sync.Mutex
	down  bool
	names map[string]string // space/name → sha256
	blobs map[string][]byte // sha256 → bytes
	types map[string]string // sha256 → content type
}

func (f *fakeStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/"), "/")
	switch {
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "_sha256":
		b, ok := f.blobs[parts[1]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", f.types[parts[1]])
		_, _ = w.Write(b)
	case r.Method == http.MethodPut && len(parts) == 2:
		if f.down {
			http.Error(w, "minio unavailable", http.StatusServiceUnavailable)
			return
		}
		b, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(b)
		sha := hex.EncodeToString(sum[:])
		f.names[parts[0]+"/"+parts[1]], f.blobs[sha], f.types[sha] = sha, b, r.Header.Get("Content-Type")
		_ = json.NewEncoder(w).Encode(map[string]any{"name": parts[1], "sha256": sha, "content_type": r.Header.Get("Content-Type")})
	default:
		http.NotFound(w, r)
	}
}

// get reads a file back by its sha256, as a consumer of the events would.
func (f *fakeStore) get(sha string) ([]byte, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.blobs[sha]
	return b, f.types[sha], ok
}

func newHarness(t *testing.T, tune func(*Config)) *harness {
	t.Helper()
	fv := &fakeVision{answer: "A stone corridor lit by a torch.\nRead: KEEP OUT"}
	door := httptest.NewServer(fv)
	t.Cleanup(door.Close)
	fs := &fakeStore{names: map[string]string{}, blobs: map[string][]byte{}, types: map[string]string{}}
	store := httptest.NewServer(fs)
	t.Cleanup(store.Close)
	sink := &InMemorySink{}
	cfg := Config{Sink: sink, VisionURL: door.URL + "/v1", InURL: "http://sight.test",
		VideoInHost: "127.0.0.1", PortMin: -1, ArtifactURL: store.URL}
	if tune != nil {
		tune(&cfg)
	}
	svc := NewService(cfg)
	mux := http.NewServeMux()
	mux.Handle("POST /in/{session_id}", svc.InputHandler())
	in := httptest.NewServer(mux)
	t.Cleanup(in.Close)
	return &harness{svc: svc, sink: sink, vision: fv, store: fs, in: in}
}

func (h *harness) open(t *testing.T, o Options) string {
	t.Helper()
	got, err := h.svc.Open(context.Background(), o, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.SightIn != "http://sight.test/in/"+got.SessionID {
		t.Fatalf("sight_in %q", got.SightIn)
	}
	if !strings.HasPrefix(got.VideoIn, "127.0.0.1:") {
		t.Fatalf("video_in %q", got.VideoIn)
	}
	h.videoIn = got.VideoIn
	t.Cleanup(func() { _ = h.svc.Close(got.SessionID) })
	return got.SessionID
}

// frameOf checks an event's frame reference and that its bytes resolve in
// the store by sha256 as a JPEG, and returns the reference.
func (h *harness) frameOf(t *testing.T, ev Event) map[string]any {
	t.Helper()
	ref, ok := ev.Data["frame"].(map[string]any)
	if !ok {
		t.Fatalf("%s carries no frame: %v", ev.Type, ev.Data)
	}
	b, ct, ok := h.store.get(fmt.Sprint(ref["sha256"]))
	if !ok || ct != "image/jpeg" || ref["content_type"] != "image/jpeg" || ref["space"] != DefaultArtifactSpace ||
		!bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}) {
		t.Fatalf("%s: frame %v does not resolve to a JPEG (%v, %q)", ev.Type, ref, ok, ct)
	}
	if !strings.HasPrefix(fmt.Sprint(ref["name"]), ev.Subject+".") {
		t.Errorf("frame name %v is not under the session", ref["name"])
	}
	return ref
}

// send POSTs a pack to the session's sight_in and returns the status.
func (h *harness) send(t *testing.T, id string, pack []map[string]any) int {
	t.Helper()
	body, _ := json.Marshal(pack)
	resp, err := http.Post(h.in.URL+"/in/"+id, senml.ContentType, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func (h *harness) close(t *testing.T, id string) []Event {
	t.Helper()
	if err := h.svc.Close(id); err != nil {
		t.Fatal(err)
	}
	return h.sink.Events()
}

// snapshot is a JPEG frame: dark, with a bright band on the left when someone
// is there.
func snapshot(t *testing.T, someone bool) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 160, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 160; x++ {
			v := uint8(25)
			if someone && x < 60 {
				v = 210
			}
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The world's clock in the tests: 2026-11-27T21:00:00Z, as SenML seconds.
var t0 = time.Date(2026, 11, 27, 21, 0, 0, 0, time.UTC)

func at(d time.Duration) float64 { return float64(t0.Add(d).UnixMilli()) / 1e3 }

func frameRec(t *testing.T, d time.Duration, someone bool) map[string]any {
	return map[string]any{"n": "frame", "t": at(d), "vd": senml.EncodeData(snapshot(t, someone))}
}

func data(ev Event) map[string]any { return ev.Data }

func types(evs []Event) string {
	var s []string
	for _, e := range evs {
		s = append(s, e.Type)
	}
	return strings.Join(s, " ")
}

// A frame opens an observation at its own time and is put into words; words
// that come as words go straight to recognition-complete, at their time.
func TestFrameAndWordsCarryTheInputsTimes(t *testing.T) {
	h := newHarness(t, nil)
	id := h.open(t, Options{Language: "en"})
	frame := frameRec(t, 2*time.Second, false)
	frame["bn"] = "urn:dev:dungeon:" // the base name before every record's own
	pack := []map[string]any{
		frame,
		{"n": "description", "t": at(1500 * time.Millisecond), "vs": "torchlight flickers on wet walls"},
		{"n": "read", "t": at(4 * time.Second), "vs": "BEWARE THE STAIRS"},
	}
	if code := h.send(t, id, pack); code != http.StatusAccepted {
		t.Fatalf("send: %d", code)
	}
	evs := h.close(t, id)
	want := TypeStartOfInput + " " + TypeRecognitionComplete + " " + TypeRecognitionComplete + " " +
		TypeRecognitionComplete + " " + TypeSessionClosed
	if types(evs) != want {
		t.Fatalf("events: %s", types(evs))
	}
	start, seen, said, read := data(evs[0]), data(evs[1]), data(evs[2]), data(evs[3])
	if start["started_at"] != "2026-11-27T21:00:02.000Z" {
		t.Errorf("start-of-input at %v", start["started_at"])
	}
	if seen["request_id"] != start["request_id"] || seen["completion_cause"] != CauseSuccess ||
		seen["text"] != "A stone corridor lit by a torch." || seen["read"] != "KEEP OUT" ||
		seen["input_type"] != "frame" || seen["ended_at"] != "2026-11-27T21:00:02.000Z" || seen["language"] != "en" {
		t.Errorf("frame recognised: %v", seen)
	}
	if said["input_type"] != "description" || said["text"] != "torchlight flickers on wet walls" || said["read"] != "" ||
		said["started_at"] != "2026-11-27T21:00:01.500Z" || said["ended_at"] != "2026-11-27T21:00:01.500Z" {
		t.Errorf("description: %v", said)
	}
	if read["input_type"] != "read" || read["read"] != "BEWARE THE STAIRS" || read["text"] != "" ||
		read["started_at"] != "2026-11-27T21:00:04.000Z" {
		t.Errorf("read: %v", read)
	}
	if said["request_id"] == read["request_id"] || said["request_id"] == seen["request_id"] {
		t.Error("request ids repeat")
	}
	if data(evs[4])["reason"] != "closed" {
		t.Errorf("closed: %v", data(evs[4]))
	}
	for _, e := range evs {
		if e.Subject != id || data(e)["session_id"] != id {
			t.Errorf("%s: subject %q", e.Type, e.Subject)
		}
	}
	if h.vision.count() != 1 {
		t.Errorf("vision asked %d times", h.vision.count())
	}
	// The change and the described frame are the same picture here: kept
	// once, named by both, as the sender's own JPEG, at the frame's time.
	changed, described := h.frameOf(t, evs[0]), h.frameOf(t, evs[1])
	if changed["sha256"] != described["sha256"] || changed["at"] != "2026-11-27T21:00:02.000Z" ||
		start["clock"] != ClockSenML {
		t.Errorf("frame refs: %v %v (clock %v)", changed, described, start["clock"])
	}
	if b, _, _ := h.store.get(fmt.Sprint(changed["sha256"])); !bytes.Equal(b, snapshot(t, false)) {
		t.Error("the stored frame is not the one sent")
	}
	if _, ok := said["frame"]; ok {
		t.Error("words carry a frame")
	}
}

// A frame artifact_store does not take is missing from the event, with why;
// the event itself goes out.
func TestAFailedPutDropsTheFrameNotTheEvent(t *testing.T) {
	h := newHarness(t, nil)
	h.store.down = true
	id := h.open(t, Options{})
	h.send(t, id, []map[string]any{frameRec(t, 0, true)})
	evs := h.close(t, id)
	if types(evs) != TypeStartOfInput+" "+TypeRecognitionComplete+" "+TypeSessionClosed {
		t.Fatalf("events: %s", types(evs))
	}
	for _, e := range evs[:2] {
		if _, ok := e.Data["frame"]; ok || !strings.Contains(fmt.Sprint(e.Data["frame_error"]), "503") {
			t.Errorf("%s: frame %v, frame_error %v", e.Type, e.Data["frame"], e.Data["frame_error"])
		}
	}
	if evs[1].Data["completion_cause"] != CauseSuccess {
		t.Errorf("described anyway: %v", evs[1].Data)
	}
}

// The same scene again is no change; a changed one is.
func TestOnlyAChangeOpensAnObservation(t *testing.T) {
	h := newHarness(t, nil)
	id := h.open(t, Options{})
	h.send(t, id, []map[string]any{
		frameRec(t, 0, false),
		frameRec(t, time.Second, false),
		frameRec(t, 2*time.Second, true),
		frameRec(t, 3*time.Second, true),
	})
	evs := h.close(t, id)
	n := 0
	for _, e := range evs {
		if e.Type == TypeStartOfInput {
			n++
		}
	}
	if n != 2 || h.vision.count() != 2 {
		t.Fatalf("%d observations, %d descriptions: %s", n, h.vision.count(), types(evs))
	}
	if data(evs[2])["started_at"] != "2026-11-27T21:00:02.000Z" {
		t.Errorf("second observation at %v", data(evs[2])["started_at"])
	}
}

// Within describe_every of the last description a change waits: it is
// described at the first frame past it, or at close, and spans the frames
// between.
func TestDescriptionsAreRateLimitedOnTheInputsTimeline(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.DescribeEvery = 10 * time.Second })
	id := h.open(t, Options{})
	h.send(t, id, []map[string]any{
		frameRec(t, 0, false),
		frameRec(t, 3*time.Second, true),
		frameRec(t, 5*time.Second, true),
	})
	// Packs are taken in order; wait until the worker is past them.
	deadline := time.Now().Add(2 * time.Second)
	for len(h.sink.Events()) < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := types(h.sink.Events()); got != TypeStartOfInput+" "+TypeRecognitionComplete+" "+TypeStartOfInput {
		t.Fatalf("before close: %s", got)
	}
	evs := h.close(t, id)
	last := data(evs[3])
	if evs[3].Type != TypeRecognitionComplete || last["started_at"] != "2026-11-27T21:00:03.000Z" ||
		last["ended_at"] != "2026-11-27T21:00:05.000Z" || last["request_id"] != data(evs[2])["request_id"] {
		t.Errorf("held observation: %s %v", evs[3].Type, last)
	}
	if h.vision.count() != 2 {
		t.Errorf("vision asked %d times", h.vision.count())
	}

	// Past the interval, a change is described as it comes.
	id = h.open(t, Options{DescribeEvery: time.Second})
	h.send(t, id, []map[string]any{frameRec(t, 0, false), frameRec(t, 3*time.Second, true)})
	evs = h.close(t, id)[5:]
	if types(evs) != TypeStartOfInput+" "+TypeRecognitionComplete+" "+TypeStartOfInput+" "+TypeRecognitionComplete+" "+TypeSessionClosed {
		t.Errorf("with the session's own interval: %s", types(evs))
	}
}

func TestVisionFailureAndSilence(t *testing.T) {
	h := newHarness(t, nil)
	h.vision.status = http.StatusBadGateway
	id := h.open(t, Options{})
	h.send(t, id, []map[string]any{frameRec(t, 0, true)})
	evs := h.close(t, id)
	if d := data(evs[1]); d["completion_cause"] != CauseRecognizerError || d["text"] != "" {
		t.Errorf("door down: %v", d)
	}

	h.vision.status, h.vision.answer = 0, "  \n"
	id = h.open(t, Options{})
	h.send(t, id, []map[string]any{frameRec(t, 0, true)})
	evs = h.close(t, id)
	if d := data(evs[4]); d["completion_cause"] != CauseNoMatch {
		t.Errorf("nothing said: %v", d)
	}
}

func TestSightInRefuses(t *testing.T) {
	h := newHarness(t, nil)
	id := h.open(t, Options{})
	for name, c := range map[string]struct {
		pack []map[string]any
		code int
	}{
		"unknown kind":   {[]map[string]any{{"n": "smell", "vs": "sulfur"}}, http.StatusBadRequest},
		"no words":       {[]map[string]any{{"n": "description", "vs": ""}}, http.StatusBadRequest},
		"not a picture":  {[]map[string]any{{"n": "frame", "vd": senml.EncodeData([]byte("GIF89a"))}}, http.StatusBadRequest},
		"an empty pack":  {[]map[string]any{}, http.StatusBadRequest},
		"one bad record": {[]map[string]any{{"n": "read", "vs": "ok"}, {"n": "frame"}}, http.StatusBadRequest},
	} {
		if got := h.send(t, id, c.pack); got != c.code {
			t.Errorf("%s: %d, want %d", name, got, c.code)
		}
	}
	if got := h.send(t, "sg-00000000", []map[string]any{{"n": "read", "vs": "x"}}); got != http.StatusNotFound {
		t.Errorf("unknown session: %d", got)
	}
	resp, err := http.Post(h.in.URL+"/in/"+id, "image/jpeg", bytes.NewReader(snapshot(t, false)))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("a bare JPEG: %d", resp.StatusCode)
	}
	evs := h.close(t, id)
	if types(evs) != TypeSessionClosed {
		t.Errorf("a refused pack made events: %s", types(evs))
	}
	if got := h.send(t, id, []map[string]any{{"n": "read", "vs": "x"}}); got != http.StatusGone {
		t.Errorf("closed session: %d", got)
	}
}

func TestSessionsLimitCloseTwiceAndIdle(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxSessions = 1; c.IdleTimeout = 50 * time.Millisecond })
	id := h.open(t, Options{})
	if _, err := h.svc.Open(context.Background(), Options{}, ""); err == nil || !strings.HasPrefix(err.Error(), "capacity") {
		t.Fatalf("over the limit: %v", err)
	}
	h.send(t, id, []map[string]any{{"n": "description", "vs": "a quiet room"}})
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(types(h.sink.Events()), TypeSessionClosed) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	evs := h.sink.Events()
	if types(evs) != TypeRecognitionComplete+" "+TypeSessionClosed || data(evs[1])["reason"] != "idle" {
		t.Fatalf("idle: %s %v", types(evs), evs)
	}
	if err := h.svc.Close(id); err != nil {
		t.Errorf("close after idle: %v", err)
	}
	if len(h.sink.Events()) != 2 {
		t.Error("a second session-closed")
	}
	if _, err := h.svc.Open(context.Background(), Options{}, ""); err != nil {
		t.Errorf("the place was not freed: %v", err)
	}
	if err := h.svc.Close("sg-ffffffff"); err == nil || !strings.HasPrefix(err.Error(), "unknown_session") {
		t.Errorf("unknown: %v", err)
	}
	for _, o := range []Options{{Recognize: []string{"faces"}}, {ChangeThreshold: 2}, {DescribeEvery: -1}} {
		if _, err := h.svc.Open(context.Background(), o, ""); err == nil {
			t.Errorf("%+v opened", o)
		}
	}
}

// A relative SenML time is the sender's "this long before I sent it": it is
// placed against the pack's arrival, not against when the worker got to it.
func TestRelativeTimeIsAgainstArrival(t *testing.T) {
	h := newHarness(t, nil)
	id := h.open(t, Options{})
	before := time.Now()
	h.send(t, id, []map[string]any{{"n": "description", "t": -2, "vs": "a door creaks open"}})
	evs := h.close(t, id)
	got, err := time.Parse(time.RFC3339Nano, fmt.Sprint(data(evs[0])["started_at"]))
	if err != nil {
		t.Fatal(err)
	}
	if d := before.Add(-2 * time.Second).Sub(got); d > 50*time.Millisecond || d < -500*time.Millisecond {
		t.Errorf("relative time placed at %s, sent at %s", got, before)
	}
}
