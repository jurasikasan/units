package main

import (
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// realAnswer is what pion gives the line to send: a gathered, non-trickle
// answer with host candidates, as newCall takes it.
func realAnswer(t *testing.T) string {
	t.Helper()
	offerer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = offerer.Close() }()
	if _, err := offerer.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
		t.Fatal(err)
	}
	offer, err := offerer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	answerer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = answerer.Close() }()
	if err := answerer.SetRemoteDescription(offer); err != nil {
		t.Fatal(err)
	}
	answer, err := answerer.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(answerer)
	if err := answerer.SetLocalDescription(answer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
		t.Fatal("gathering timed out")
	}
	return answerer.LocalDescription().SDP
}

// With WEBRTC_PUBLIC_IP set, every host UDP candidate carries it, and the
// answer stays well-formed SDP: CRLF lines, no empty line. An empty line at
// the end is what Chrome refused ("Invalid SDP line") on the first call
// from a browser behind the box's NAT.
func TestRewriteCandidateIPs_RealAnswer(t *testing.T) {
	in := realAnswer(t)
	if !strings.Contains(in, "typ host") {
		t.Fatalf("no host candidates to rewrite in:\n%s", in)
	}
	out := rewriteCandidateIPs(in, "203.0.113.7")

	if !strings.HasSuffix(out, "\r\n") || strings.HasSuffix(out, "\r\n\r\n") {
		t.Errorf("answer must end in exactly one CRLF: %q", out[max(0, len(out)-12):])
	}
	lines := strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n")
	hosts := 0
	for _, l := range lines {
		if len(l) < 2 || l[1] != '=' || strings.ContainsAny(l, "\r\n") {
			t.Errorf("not an SDP line: %q", l)
		}
		if strings.HasPrefix(l, "a=candidate:") && strings.Contains(l, " typ host") {
			f := strings.Split(l, " ")
			if strings.EqualFold(f[2], "udp") {
				hosts++
				if f[4] != "203.0.113.7" {
					t.Errorf("host candidate kept its address: %q", l)
				}
			}
		}
	}
	if hosts == 0 {
		t.Error("no UDP host candidate was found to check")
	}
	// The browser's parser is the judge; pion's is the nearest at hand.
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
		t.Fatal(err)
	}
	offer, _ := pc.CreateOffer(nil)
	_ = pc.SetLocalDescription(offer)
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: out}); err != nil {
		t.Errorf("rewritten answer does not parse: %v", err)
	}
}

func TestRewriteCandidateIPs_NoIPUnchanged(t *testing.T) {
	in := "v=0\r\na=candidate:1 1 udp 2130706431 10.0.0.2 45000 typ host\r\n"
	if got := rewriteCandidateIPs(in, ""); got != in {
		t.Errorf("got %q", got)
	}
}
