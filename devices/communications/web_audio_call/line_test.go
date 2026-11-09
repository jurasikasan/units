package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"gopkg.in/hraban/opus.v2"
)

// startLine runs the full service against miniredis and returns it plus the
// base URL of an httptest server on its handler.
func startLine(t *testing.T, mut func(*Config)) (*Line, string) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := Config{
		MaxCalls:          4,
		UnattendedTimeout: 2 * time.Minute,
		PortMin:           45000,
		PortMax:           45099,
		AudioInHost:       "127.0.0.1",
		AudioInPortMin:    45100,
		AudioInPortMax:    45199,
	}
	if mut != nil {
		mut(&cfg)
	}
	line := NewLine(cfg, rdb, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(line.handler())
	t.Cleanup(ts.Close)
	return line, ts.URL
}

// standIn is a pion peer connection standing in for the browser.
type standIn struct {
	pc      *webrtc.PeerConnection
	out     *webrtc.TrackLocalStaticSample // line -> stand-in (browser mic)
	trackCh chan *webrtc.TrackRemote
	in      *webrtc.TrackRemote
	callID  string
}

func makeOffer(t *testing.T, withAudio bool) (*standIn, string) {
	t.Helper()
	se := webrtc.SettingEngine{}
	// Browsers fire ontrack as soon as the answer's m-lines are known; pion's
	// offerer only does so on the first packet by default.
	se.SetFireOnTrackBeforeFirstRTP(true)
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("stand-in: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	si := &standIn{pc: pc, trackCh: make(chan *webrtc.TrackRemote, 4)}
	if withAudio {
		tr, err := webrtc.NewTrackLocalStaticSample(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
			"mic", "standin")
		if err != nil {
			t.Fatalf("stand-in track: %v", err)
		}
		si.out = tr
		if _, err := pc.AddTrack(tr); err != nil {
			t.Fatalf("stand-in AddTrack: %v", err)
		}
	}
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { si.trackCh <- tr })
	gathered := make(chan struct{})
	var gatheredOnce sync.Once
	pc.OnICEGatheringStateChange(func(s webrtc.ICEGatheringState) {
		if s == webrtc.ICEGatheringStateComplete {
			gatheredOnce.Do(func() { close(gathered) })
		}
	})
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatalf("stand-in CreateOffer: %v", err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatalf("stand-in SetLocalDescription: %v", err)
	}
	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
		t.Fatal("stand-in ICE gathering timed out")
	}
	return si, pc.LocalDescription().SDP
}

func postOffer(t *testing.T, url, sdp, principal string) (int, []byte) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"sdp": sdp, "type": "offer"})
	req, err := http.NewRequest(http.MethodPost, url+"/offer", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("postOffer: %v", err)
	}
	if principal != "" {
		req.Header.Set("X-Principal-Id", principal)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("postOffer: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("postOffer read: %v", err)
	}
	return resp.StatusCode, b
}

// connectStandIn completes the offer/answer exchange and waits until the
// connection is up, then waits for the line's inbound track.
func connectStandIn(t *testing.T, base string) *standIn {
	t.Helper()
	si, sdp := makeOffer(t, true)
	code, body := postOffer(t, base, sdp, "test-user")
	if code != http.StatusOK {
		t.Fatalf("offer: got %d: %s", code, body)
	}
	var ans struct {
		SDP    string `json:"sdp"`
		Type   string `json:"type"`
		CallID string `json:"call_id"`
	}
	if err := json.Unmarshal(body, &ans); err != nil {
		t.Fatalf("bad answer json: %v", err)
	}
	if ans.Type != "answer" || !strings.HasPrefix(ans.CallID, "wcall-") {
		t.Fatalf("bad answer: %+v", ans)
	}
	si.callID = ans.CallID
	if err := si.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: ans.SDP}); err != nil {
		t.Fatalf("stand-in SetRemoteDescription: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if si.pc.ConnectionState() == webrtc.PeerConnectionStateConnected {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if si.pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
		t.Fatalf("stand-in never connected: %s", si.pc.ConnectionState())
	}
	select {
	case tr := <-si.trackCh:
		si.in = tr
	case <-time.After(5 * time.Second):
		t.Fatal("stand-in got no inbound track")
	}
	return si
}

// sineFrames is n 20 ms frames of 440 Hz, amplitude 8000, 48 kHz, phase
// continuous across frames.
func sineFrames(t *testing.T, n int) [][]int16 {
	t.Helper()
	frames := make([][]int16, n)
	for i := range frames {
		f := make([]int16, samplesPerFrame)
		for s := range f {
			p := i*samplesPerFrame + s
			f[s] = int16(8000 * math.Sin(2*math.Pi*440*float64(p)/48000))
		}
		frames[i] = f
	}
	return frames
}

// zeroCrossRate is the zero-crossing rate (crossings/sec) of a 48 kHz signal.
// A 440 Hz sine has 880.
func zeroCrossRate(samples []int16) float64 {
	var base int8
	known := false
	zc := 0
	for _, s := range samples {
		switch {
		case s > 0:
			if known && base != 1 {
				zc++
			}
			base, known = 1, true
		case s < 0:
			if known && base != -1 {
				zc++
			}
			base, known = -1, true
		}
	}
	if len(samples) < 2 {
		return 0
	}
	return float64(zc) * 48000 / float64(len(samples)-1)
}

func fromBE(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.BigEndian.Uint16(b[2*i:]))
	}
	return out
}

func toBE(samples []int16) []byte {
	out := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.BigEndian.PutUint16(out[2*i:], uint16(s))
	}
	return out
}

// decodeStandInAudio reads and Opus-decodes wantPackets worth of the stand-in's
// inbound track.
func decodeStandInAudio(t *testing.T, si *standIn, wantPackets int, timeout time.Duration) []int16 {
	t.Helper()
	dec, err := opus.NewDecoder(48000, 1)
	if err != nil {
		t.Fatalf("stand-in decoder: %v", err)
	}
	var (
		mu  sync.Mutex
		out []int16
		got int
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		pcm := make([]int16, 1152)
		for {
			pkt, _, err := si.in.ReadRTP()
			if err != nil {
				return
			}
			// Read the codec per packet: with FireOnTrackBeforeFirstRTP the track
			// fires before its first packet, while its payload type is still 0.
			if pkt.PayloadType != uint8(si.in.Codec().PayloadType) {
				continue
			}
			n, err := dec.Decode(pkt.Payload, pcm)
			if err != nil {
				continue
			}
			mu.Lock()
			got++
			if n > 0 {
				out = append(out, pcm[:n]...)
			}
			stop := got >= wantPackets
			mu.Unlock()
			if stop {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		mu.Lock()
		g := got
		mu.Unlock()
		t.Fatalf("timed out decoding stand-in audio (want %d packets, got %d)", wantPackets, g)
	}
	mu.Lock()
	defer mu.Unlock()
	if got < wantPackets {
		t.Fatalf("got %d packets, want %d", got, wantPackets)
	}
	return out
}

// readRTP collects want RTP packets from conn.
func readRTP(t *testing.T, conn *net.UDPConn, want int, timeout time.Duration) []rtp.Packet {
	t.Helper()
	var pkts []rtp.Packet
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 65536)
		for len(pkts) < want {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			pkt := &rtp.Packet{}
			if err := pkt.Unmarshal(buf[:n]); err != nil {
				return
			}
			pkts = append(pkts, *pkt)
		}
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %d RTP packets", want)
	}
	if len(pkts) < want {
		t.Fatalf("got %d packets, want %d", len(pkts), want)
	}
	return pkts
}

func listenUDP(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listenUDP: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func mcpSession(t *testing.T, base string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "line-test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: base + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("callTool %s: %v", name, err)
	}
	return res
}

func toolText(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

type streamEvent struct {
	Typ  string
	Data map[string]any
}

func lineEvents(t *testing.T, line *Line) []streamEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	entries, err := line.rdb.XRange(ctx, eventsStream, "-", "+").Result()
	if err != nil {
		t.Fatalf("XRange: %v", err)
	}
	var out []streamEvent
	for _, e := range entries {
		var ev streamEvent
		ev.Typ, _ = e.Values["ce_type"].(string)
		if d, ok := e.Values["data"].(string); ok {
			if err := json.Unmarshal([]byte(d), &ev.Data); err != nil {
				t.Fatalf("bad event data %q: %v", d, err)
			}
		}
		out = append(out, ev)
	}
	return out
}

func waitForEvent(t *testing.T, line *Line, typ string, timeout time.Duration) streamEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, ev := range lineEvents(t, line) {
			if ev.Typ == typ {
				return ev
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for event %s", typ)
	return streamEvent{}
}

func countEvent(t *testing.T, line *Line, typ string) int {
	t.Helper()
	n := 0
	for _, ev := range lineEvents(t, line) {
		if ev.Typ == typ {
			n++
		}
	}
	return n
}

func expectEventFields(t *testing.T, ev streamEvent, want map[string]any) {
	t.Helper()
	for k, v := range want {
		got, ok := ev.Data[k]
		if !ok {
			t.Fatalf("event %s missing field %q", ev.Typ, k)
		}
		if got != v {
			t.Fatalf("event %s field %s = %v, want %v", ev.Typ, k, got, v)
		}
	}
}

func TestConnectedAndAudioInToBrowser(t *testing.T) {
	line, base := startLine(t, nil)
	si := connectStandIn(t, base)

	ev := waitForEvent(t, line, eventConnected, 5*time.Second)
	expectEventFields(t, ev, map[string]any{
		"call_id":   si.callID,
		"principal": "test-user",
	})
	hostPort, _ := ev.Data["audio_in"].(string)
	host, portStr, err := net.SplitHostPort(hostPort)
	if err != nil {
		t.Fatalf("audio_in %q is not host:port: %v", hostPort, err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("audio_in host %q, want 127.0.0.1", host)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 45100 || port > 45199 {
		t.Fatalf("audio_in port %q outside configured 45100..45199", portStr)
	}

	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP(host), Port: port})
	if err != nil {
		t.Fatalf("dial audio_in: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// A wrong payload type is dropped and does not claim the socket.
	stray := rtp.Packet{Version: 2, PayloadType: 97, SequenceNumber: 1, SSRC: 0xBAD, Payload: make([]byte, 1920)}
	mStray, err := stray.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(mStray); err != nil {
		t.Fatal(err)
	}

	owner := uint32(0x12345678)
	snd := func(seq uint16, ts uint32, ssrc uint32, samples []int16) {
		t.Helper()
		p := rtp.Packet{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts, SSRC: ssrc, Payload: toBE(samples)}
		m, err := p.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write(m); err != nil {
			t.Fatal(err)
		}
		// The sender sets the pace (C6): the line has no queue, a burst is dropped.
		time.Sleep(20 * time.Millisecond)
	}

	frames := sineFrames(t, 55)
	// The first SSRC with PT 96 owns the socket: 0x12345678.
	for i := 0; i < 50; i++ {
		snd(uint16(i), uint32(i)*samplesPerFrame, owner, frames[i])
	}
	// 20 silent frames from a different SSRC must be dropped.
	silent := make([]int16, samplesPerFrame)
	for i := 0; i < 20; i++ {
		snd(uint16(50+i), uint32(50+i)*samplesPerFrame, 0xBAD, silent)
	}
	// The owner keeps flowing.
	for i := 50; i < 55; i++ {
		snd(uint16(70+i-50+20), uint32(i)*samplesPerFrame, owner, frames[i])
	}

	out := decodeStandInAudio(t, si, 55, 8*time.Second)
	rate := zeroCrossRate(out)
	if math.Abs(rate-880) > 44 {
		t.Fatalf("decoded stand-in audio zero-crossing rate %v, want 880±44 (got %d samples)", rate, len(out))
	}
}

func TestListenStream(t *testing.T) {
	line, base := startLine(t, nil)
	si := connectStandIn(t, base)
	waitForEvent(t, line, eventConnected, 5*time.Second)

	t1 := listenUDP(t)
	t2 := listenUDP(t)
	cs := mcpSession(t, base)

	res := callTool(t, cs, "listen", map[string]any{"call_id": si.callID, "to": t1.LocalAddr().String()})
	if res.IsError {
		t.Fatalf("listen: %s", toolText(res))
	}

	enc, err := opus.NewEncoder(48000, 1, opus.AppVoIP)
	if err != nil {
		t.Fatal(err)
	}
	frames := sineFrames(t, 50)
	snd := func(i int) {
		t.Helper()
		buf := make([]byte, 2048)
		nb, err := enc.Encode(frames[i], buf)
		if err != nil {
			t.Fatal(err)
		}
		if nb == 0 {
			t.Fatalf("opus encode produced no bytes at frame %d", i)
		}
		if err := si.out.WriteSample(media.Sample{Data: buf[:nb], Duration: 20 * time.Millisecond}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 30; i++ {
		snd(i)
	}

	pkts := readRTP(t, t1, 30, 8*time.Second)
	for i, p := range pkts {
		if p.PayloadType != 96 {
			t.Fatalf("pkt %d payload type %d, want %d", i, p.PayloadType, 96)
		}
		if len(p.Payload) != 2*samplesPerFrame {
			t.Fatalf("pkt %d payload %d bytes, want %d", i, len(p.Payload), 2*samplesPerFrame)
		}
		if i == 0 {
			continue
		}
		if p.SequenceNumber != pkts[i-1].SequenceNumber+1 {
			t.Fatalf("pkt %d seq %d after %d", i, p.SequenceNumber, pkts[i-1].SequenceNumber)
		}
		if p.Timestamp != pkts[i-1].Timestamp+samplesPerFrame {
			t.Fatalf("pkt %d ts %d after %d", i, p.Timestamp, pkts[i-1].Timestamp)
		}
		if p.SSRC != pkts[0].SSRC {
			t.Fatalf("pkt %d ssrc %d != %d (one SSRC per call)", i, p.SSRC, pkts[0].SSRC)
		}
	}
	var samples []int16
	for _, p := range pkts {
		samples = append(samples, fromBE(p.Payload)...)
	}
	if rate := zeroCrossRate(samples); math.Abs(rate-880) > 44 {
		t.Fatalf("listen output zero-crossing rate %v, want 880±44", rate)
	}

	// A second listen moves the stream to t2; t1 stops receiving.
	res = callTool(t, cs, "listen", map[string]any{"call_id": si.callID, "to": t2.LocalAddr().String()})
	if res.IsError {
		t.Fatalf("listen move: %s", toolText(res))
	}
	for i := 30; i < 40; i++ {
		snd(i)
	}
	readRTP(t, t2, 10, 8*time.Second)
	if err := t1.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := t1.ReadFromUDP(make([]byte, 65536)); err == nil {
		t.Fatal("t1 kept receiving after the stream moved to t2")
	}

	// to "" stops it; t2 stops receiving.
	res = callTool(t, cs, "listen", map[string]any{"call_id": si.callID, "to": ""})
	if res.IsError {
		t.Fatalf("listen stop: %s", toolText(res))
	}
	for i := 40; i < 50; i++ {
		snd(i)
	}
	if err := t2.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := t2.ReadFromUDP(make([]byte, 65536)); err == nil {
		t.Fatal("t2 kept receiving after listen with to \"\"")
	}
}

func TestHangupLocal(t *testing.T) {
	line, base := startLine(t, nil)
	si := connectStandIn(t, base)
	cs := mcpSession(t, base)

	res := callTool(t, cs, "hangup", map[string]any{"call_id": si.callID})
	if res.IsError {
		t.Fatalf("hangup: %s", toolText(res))
	}
	ev := waitForEvent(t, line, eventEnded, 5*time.Second)
	if ev.Data["reason"] != "local-hangup" {
		t.Fatalf("reason = %v, want local-hangup", ev.Data["reason"])
	}
	if _, ok := ev.Data["duration_ms"]; !ok {
		t.Fatal("ended event has no duration_ms")
	}
	if n := countEvent(t, line, eventEnded); n != 1 {
		t.Fatalf("ended events = %d, want 1", n)
	}

	// A second hangup is ok and does not produce a second event.
	res = callTool(t, cs, "hangup", map[string]any{"call_id": si.callID})
	if res.IsError {
		t.Fatalf("second hangup: %s", toolText(res))
	}
	if n := countEvent(t, line, eventEnded); n != 1 {
		t.Fatalf("ended events after second hangup = %d, want 1", n)
	}

	// Listening an ended call is unknown_call.
	res = callTool(t, cs, "listen", map[string]any{"call_id": si.callID, "to": "127.0.0.1:9999"})
	if !res.IsError || !strings.HasPrefix(toolText(res), "unknown_call") {
		t.Fatalf("listen on ended call: IsError=%v text=%q", res.IsError, toolText(res))
	}
}

func TestRemoteHangup(t *testing.T) {
	line, base := startLine(t, nil)
	si := connectStandIn(t, base)
	waitForEvent(t, line, eventConnected, 5*time.Second)

	if err := si.pc.Close(); err != nil {
		t.Fatalf("stand-in close: %v", err)
	}
	ev := waitForEvent(t, line, eventEnded, 8*time.Second)
	if ev.Data["reason"] != "remote-hangup" {
		t.Fatalf("reason = %v, want remote-hangup", ev.Data["reason"])
	}
}

func TestUnattended(t *testing.T) {
	line, base := startLine(t, func(c *Config) { c.UnattendedTimeout = time.Second })
	si := connectStandIn(t, base)
	waitForEvent(t, line, eventConnected, 5*time.Second)
	_ = si
	ev := waitForEvent(t, line, eventEnded, 5*time.Second)
	if ev.Data["reason"] != "unattended" {
		t.Fatalf("reason = %v, want unattended", ev.Data["reason"])
	}
	if _, ok := ev.Data["duration_ms"]; !ok {
		t.Fatal("ended event has no duration_ms")
	}
}

func TestOfferErrors(t *testing.T) {
	_, base := startLine(t, func(c *Config) { c.MaxCalls = 1 })

	// No X-Principal-Id -> 401.
	si0, sdp0 := makeOffer(t, true)
	code, _ := postOffer(t, base, sdp0, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("no principal: got %d, want 401", code)
	}
	_ = si0.pc.Close()

	// Offer without audio -> 400.
	si1, sdp1 := makeOffer(t, false)
	code, body := postOffer(t, base, sdp1, "test-user")
	if code != http.StatusBadRequest {
		t.Fatalf("no audio: got %d: %s", code, body)
	}
	_ = si1.pc.Close()

	// Occupy the single slot.
	si2 := connectStandIn(t, base)

	// Over the limit -> 503 {"error":"capacity"}, nothing opened.
	si3, sdp3 := makeOffer(t, true)
	code, body = postOffer(t, base, sdp3, "test-user")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("capacity: got %d: %s", code, body)
	}
	var capErr map[string]string
	if err := json.Unmarshal(body, &capErr); err != nil || capErr["error"] != "capacity" {
		t.Fatalf("capacity body = %s", body)
	}
	_ = si3.pc.Close()

	cs := mcpSession(t, base)
	// Unknown call id -> unknown_call.
	res := callTool(t, cs, "listen", map[string]any{"call_id": "wcall-deadbeef", "to": "127.0.0.1:9999"})
	if !res.IsError || !strings.HasPrefix(toolText(res), "unknown_call") {
		t.Fatalf("unknown_call: IsError=%v text=%q", res.IsError, toolText(res))
	}
	// Bad targets -> bad_target.
	for _, to := range []string{"not-a-hostport", "127.0.0.1:99999"} {
		res = callTool(t, cs, "listen", map[string]any{"call_id": si2.callID, "to": to})
		if !res.IsError || !strings.HasPrefix(toolText(res), "bad_target") {
			t.Fatalf("bad_target(%s): IsError=%v text=%q", to, res.IsError, toolText(res))
		}
	}
}

func TestHealth(t *testing.T) {
	_, base := startLine(t, nil)
	resp, err := http.Get(base + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: %d", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Fatalf("health body = %v", body)
	}
}

func TestEventEnvelope(t *testing.T) {
	line, base := startLine(t, nil)
	si := connectStandIn(t, base)
	ev := waitForEvent(t, line, eventConnected, 5*time.Second)
	_ = ev
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	entries, err := line.rdb.XRange(ctx, eventsStream, "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	var connEntry *redis.XMessage
	for i := range entries {
		if entries[i].Values["ce_type"] == eventConnected {
			connEntry = &entries[i]
		}
	}
	if connEntry == nil {
		t.Fatal("no connected entry in stream")
	}
	if connEntry.Values["ce_specversion"] != "1.0" {
		t.Fatalf("ce_specversion = %v", connEntry.Values["ce_specversion"])
	}
	if connEntry.Values["ce_source"] != "/devices/communications/web_audio_call" {
		t.Fatalf("ce_source = %v", connEntry.Values["ce_source"])
	}
	if connEntry.Values["ce_subject"] != si.callID {
		t.Fatalf("ce_subject = %v, want %s", connEntry.Values["ce_subject"], si.callID)
	}
	if connEntry.Values["ce_datacontenttype"] != "application/json" {
		t.Fatalf("ce_datacontenttype = %v", connEntry.Values["ce_datacontenttype"])
	}
	ceid, _ := connEntry.Values["ce_id"].(string)
	if _, err := uuid.Parse(ceid); err != nil {
		t.Fatalf("ce_id %q is not a UUID: %v", ceid, err)
	}
	ctime, _ := connEntry.Values["ce_time"].(string)
	if _, err := time.Parse(time.RFC3339Nano, ctime); err != nil {
		t.Fatalf("ce_time %q is not RFC3339: %v", ctime, err)
	}
}
