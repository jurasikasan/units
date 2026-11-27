package sight

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	rtp "github.com/pion/rtp"
)

// fakeDecoder stands in for ffmpeg: an access unit's picture is grey at the
// level its last slice NAL unit's second byte says, one picture per unit.
type fakeDecoder struct {
	out chan *image.YCbCr
}

func newFakeDecoder() (Decoder, error) { return &fakeDecoder{out: make(chan *image.YCbCr, 1024)}, nil }

func (d *fakeDecoder) Write(au []byte) error {
	level := byte(0)
	for _, nal := range bytes.Split(au, []byte{0, 0, 0, 1}) {
		if len(nal) > 1 && (nal[0]&0x1f == 1 || nal[0]&0x1f == 5) {
			level = nal[1]
		}
	}
	img := image.NewYCbCr(image.Rect(0, 0, 64, 48), image.YCbCrSubsampleRatio420)
	for i := range img.Y {
		img.Y[i] = level
	}
	for i := range img.Cb {
		img.Cb[i], img.Cr[i] = 128, 128
	}
	d.out <- img
	return nil
}

func (d *fakeDecoder) CloseInput() error             { close(d.out); return nil }
func (d *fakeDecoder) Pictures() <-chan *image.YCbCr { return d.out }
func (d *fakeDecoder) Err() error                    { return nil }

// camera sends RTP H.264 to a video_in, one access unit per packet, 10
// pictures a second on the 90 kHz clock, and its sender reports to the port
// above.
type camera struct {
	t         *testing.T
	rtp, rtcp net.Conn
	ssrc      uint32
	seq       uint16
	ts0       uint32
	n         int // pictures sent, lost ones counted
	pace      time.Duration
	sps, pps  []byte
}

func newCamera(t *testing.T, videoIn string, ssrc uint32) *camera {
	t.Helper()
	host, port, _ := net.SplitHostPort(videoIn)
	p, _ := strconv.Atoi(port)
	c := &camera{t: t, ssrc: ssrc, seq: 4000, ts0: 1234567, pace: 15 * time.Millisecond,
		sps: []byte{0x67, 0x42, 0xc0, 0x1e}, pps: []byte{0x68, 0xce, 0x3c, 0x80}}
	var err error
	if c.rtp, err = net.Dial("udp", net.JoinHostPort(host, port)); err != nil {
		t.Fatal(err)
	}
	if c.rtcp, err = net.Dial("udp", net.JoinHostPort(host, strconv.Itoa(p+1))); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.rtp.Close(); _ = c.rtcp.Close() })
	return c
}

func (c *camera) ts(n int) uint32 { return c.ts0 + uint32(n*9000) }

// report sends a sender report: picture n was taken at wall.
func (c *camera) report(n int, wall time.Time) {
	b := make([]byte, 28)
	b[0], b[1] = 0x80, 200
	binary.BigEndian.PutUint16(b[2:], 6)
	binary.BigEndian.PutUint32(b[4:], c.ssrc)
	ntp := uint64(wall.Unix()+2208988800)<<32 | uint64(wall.Nanosecond())<<32/uint64(time.Second)
	binary.BigEndian.PutUint64(b[8:], ntp)
	binary.BigEndian.PutUint32(b[16:], c.ts(n))
	if _, err := c.rtcp.Write(b); err != nil {
		c.t.Fatal(err)
	}
	time.Sleep(c.pace) // the report is on its own socket: let it land first
}

// picture sends the next picture at a grey level: a key one (SPS, PPS and
// IDR in a STAP-A) or not.
func (c *camera) picture(level byte, key bool) {
	payload := []byte{0x41, level}
	if key {
		payload = []byte{0x18}
		for _, nal := range [][]byte{c.sps, c.pps, {0x65, level}} {
			payload = append(append(payload, byte(len(nal)>>8), byte(len(nal))), nal...)
		}
	}
	c.packet(payload)
}

func (c *camera) packet(payload []byte) {
	b, err := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: c.seq,
		Timestamp: c.ts(c.n), SSRC: c.ssrc, Marker: true}, Payload: payload}).Marshal()
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := c.rtp.Write(b); err != nil {
		c.t.Fatal(err)
	}
	c.seq++
	c.n++
	time.Sleep(c.pace)
}

// lose skips a picture: its sequence number and its time.
func (c *camera) lose() { c.seq++; c.n++ }

func videoHarness(t *testing.T) *harness {
	return newHarness(t, func(c *Config) { c.NewDecoder = newFakeDecoder })
}

func byType(evs []Event, typ string) []Event {
	var out []Event
	for _, e := range evs {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// A stream of pictures over RTP: a dark scene, then a bright one. Each is a
// change, kept and described; their times are the stream's own, on the
// sender's clock from its report, not when they arrived.
func TestVideoOverRTP(t *testing.T) {
	h := videoHarness(t)
	id := h.open(t, Options{Language: "en"})
	cam := newCamera(t, h.videoIn, 0x5157)
	cam.report(0, t0)
	cam.picture(20, true)
	for range 9 {
		cam.picture(20, false)
	}
	for i := range 10 {
		cam.picture(220, i == 0)
	}
	evs := h.close(t, id)
	want := TypeStartOfInput + " " + TypeRecognitionComplete + " " + TypeStartOfInput + " " + TypeRecognitionComplete + " " + TypeSessionClosed
	if types(evs) != want {
		t.Fatalf("events: %s", types(evs))
	}
	for i, at := range []string{"2026-11-27T21:00:00.000Z", "2026-11-27T21:00:01.000Z"} {
		start, done := data(evs[2*i]), data(evs[2*i+1])
		if start["started_at"] != at || done["started_at"] != at || done["ended_at"] != at ||
			start["input_type"] != "video" || done["input_type"] != "video" ||
			start["clock"] != "rtcp-sr" || done["clock"] != "rtcp-sr" ||
			done["request_id"] != start["request_id"] || done["text"] != "A stone corridor lit by a torch." {
			t.Errorf("observation %d: %v / %v", i, start, done)
		}
		changed, described := h.frameOf(t, evs[2*i]), h.frameOf(t, evs[2*i+1])
		if changed["sha256"] != described["sha256"] || changed["at"] != at {
			t.Errorf("observation %d frames: %v / %v", i, changed, described)
		}
	}
	if h.frameOf(t, evs[0])["sha256"] == h.frameOf(t, evs[2])["sha256"] {
		t.Error("the dark and the bright scene are one frame")
	}
	if h.vision.count() != 2 {
		t.Errorf("vision asked %d times", h.vision.count())
	}
}

// Without a sender report the stream is placed at its first packet's
// arrival, then by the RTP clock; another sender on the port is not seen.
func TestVideoWithoutReportAndAnotherSender(t *testing.T) {
	h := videoHarness(t)
	id := h.open(t, Options{})
	cam, intruder := newCamera(t, h.videoIn, 1), newCamera(t, h.videoIn, 2)
	before := time.Now()
	cam.picture(20, true)
	after := time.Now()
	intruder.picture(250, true)
	for i := range 4 {
		cam.picture(20, i == 0)
	}
	cam.picture(220, true) // picture 5: 500 ms on the stream's clock
	evs := h.close(t, id)
	starts := byType(evs, TypeStartOfInput)
	if len(starts) != 2 {
		t.Fatalf("events: %s", types(evs))
	}
	first, err := time.Parse(time.RFC3339Nano, fmt.Sprint(data(starts[0])["started_at"]))
	if err != nil {
		t.Fatal(err)
	}
	if first.Before(before.Truncate(time.Millisecond)) || first.After(after) || data(starts[0])["clock"] != "first-packet" {
		t.Errorf("first picture at %s by %v, sent between %s and %s", first, data(starts[0])["clock"], before, after)
	}
	second, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(data(starts[1])["started_at"]))
	if d := second.Sub(first); d < 499*time.Millisecond || d > 501*time.Millisecond {
		t.Errorf("the change is %s after the first picture on the stream's clock, want 500ms", d)
	}
}

// After a lost packet nothing is decoded until the next key picture: the
// change is seen there, at its time.
func TestVideoWaitsForAKeyAfterALoss(t *testing.T) {
	h := videoHarness(t)
	id := h.open(t, Options{})
	cam := newCamera(t, h.videoIn, 3)
	cam.report(0, t0)
	cam.picture(20, true)
	cam.picture(20, false)
	cam.lose()
	cam.picture(220, false) // broken: what it refers to was lost
	cam.picture(220, false) // not a key: still waiting
	cam.picture(220, true)  // picture 5, at 500 ms
	evs := h.close(t, id)
	starts := byType(evs, TypeStartOfInput)
	if len(starts) != 2 || data(starts[1])["started_at"] != "2026-11-27T21:00:00.500Z" {
		t.Fatalf("events: %s %v", types(evs), evs)
	}
}

// Pictures are sampled on the stream's clock before the detector: a change
// between two samples is seen at the next sample.
func TestVideoSampling(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.NewDecoder = newFakeDecoder; c.SampleEvery = 300 * time.Millisecond })
	id := h.open(t, Options{})
	cam := newCamera(t, h.videoIn, 4)
	cam.report(0, t0)
	cam.picture(20, true)
	cam.picture(20, false)
	cam.picture(220, false) // 200 ms: not sampled
	cam.picture(220, false) // 300 ms: sampled, the change
	evs := h.close(t, id)
	starts := byType(evs, TypeStartOfInput)
	if len(starts) != 2 || data(starts[1])["started_at"] != "2026-11-27T21:00:00.300Z" {
		t.Fatalf("events: %s %v", types(evs), evs)
	}
}

// A session without a decoder (no ffmpeg) still takes words.
func TestNoDecoderStillTakesWords(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.FFmpeg = "/nonexistent/ffmpeg" })
	id := h.open(t, Options{})
	cam := newCamera(t, h.videoIn, 5)
	cam.picture(20, true)
	h.send(t, id, []map[string]any{{"n": "description", "t": at(0), "vs": "a quiet room"}})
	evs := h.close(t, id)
	if types(evs) != TypeRecognitionComplete+" "+TypeSessionClosed {
		t.Fatalf("events: %s", types(evs))
	}
}

// The real thing, where ffmpeg is: ffmpeg sends a generated video over RTP
// (with its RTCP sender reports) to video_in, and ffmpeg decodes it in sight.
// Skipped where ffmpeg or its H.264 encoder is not; check.sh says so.
func TestVideoThroughFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not on PATH")
	}
	if out, _ := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output(); !strings.Contains(string(out), "libx264") {
		t.Skip("ffmpeg has no libx264 to send H.264 with")
	}
	h := newHarness(t, nil)
	id := h.open(t, Options{})
	before := time.Now()
	send := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "color=c=0x202020:s=160x120:r=10:d=1[a];smptebars=s=160x120:r=10:d=1[b];[a][b]concat=n=2:v=1:a=0",
		"-c:v", "libx264", "-profile:v", "baseline", "-tune", "zerolatency", "-g", "10",
		"-f", "rtp", "rtp://"+h.videoIn)
	if out, err := send.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg sending: %v\n%s", err, out)
	}
	after := time.Now()
	evs := h.close(t, id)
	starts := byType(evs, TypeStartOfInput)
	if len(starts) != 2 || len(byType(evs, TypeRecognitionComplete)) != 2 {
		t.Fatalf("events: %s", types(evs))
	}
	var times [2]time.Time
	for i, s := range starts {
		times[i], _ = time.Parse(time.RFC3339Nano, fmt.Sprint(data(s)["started_at"]))
		if times[i].Before(before.Add(-time.Second)) || times[i].After(after) {
			t.Errorf("picture %d at %s, sent between %s and %s", i, times[i], before, after)
		}
		h.frameOf(t, s)
	}
	if d := times[1].Sub(times[0]); d < 900*time.Millisecond || d > 1100*time.Millisecond {
		t.Errorf("the scene changed %s after it began, the stream says 1s", d)
	}
	if c := data(starts[1])["clock"]; c != "rtcp-sr" {
		t.Errorf("placed by %v; ffmpeg sends sender reports", c)
	}
}
