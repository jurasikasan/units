package ffdec

import (
	"bytes"
	"fmt"
	"image"
	"os/exec"
	"strings"
	"testing"
)

func TestReadY4M(t *testing.T) {
	// 3×3: chroma planes are 2×2.
	var b bytes.Buffer
	b.WriteString("YUV4MPEG2 W3 H3 F25:1 Ip A1:1 C420jpeg XYSCSS=420JPEG XCOLORRANGE=FULL\n")
	for f := byte(0); f < 2; f++ {
		b.WriteString("FRAME\n")
		b.Write(bytes.Repeat([]byte{10 + f}, 9))
		b.Write(bytes.Repeat([]byte{128}, 8))
	}
	var got []*image.YCbCr
	if err := ReadY4M(&b, func(p *image.YCbCr) { got = append(got, p) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Rect.Dx() != 3 || got[1].Y[8] != 11 || got[0].Y[0] != 10 || len(got[1].Cr) != 4 {
		t.Fatalf("pictures: %d", len(got))
	}
	if err := ReadY4M(strings.NewReader(""), func(*image.YCbCr) {}); err != nil {
		t.Errorf("an empty stream: %v", err)
	}
	for _, bad := range []string{"RIFF\n", "YUV4MPEG2 W3 H3 C444\n", "YUV4MPEG2 H3\n", "YUV4MPEG2 W2 H2\nFRAME\n\x01"} {
		if err := ReadY4M(strings.NewReader(bad), func(*image.YCbCr) {}); err == nil {
			t.Errorf("%q read", bad)
		}
	}
}

// h264 makes an Annex B stream with ffmpeg: frames pictures, a dark scene
// then a bright one. The test is skipped where ffmpeg or its H.264 encoder
// is not (check.sh says so); the decoding logic around it is tested with a
// fake decoder in package sight.
func h264(t *testing.T, frames int) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not on PATH")
	}
	half := frames / 2
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=0x202020:s=64x48:r=10:d=%g[a];color=c=0xe0e0e0:s=64x48:r=10:d=%g[b];[a][b]concat=n=2:v=1:a=0",
			float64(half)/10, float64(frames-half)/10),
		"-c:v", "libx264", "-profile:v", "baseline", "-g", "5", "-f", "h264", "pipe:1").Output()
	if err != nil {
		t.Skipf("ffmpeg cannot encode H.264 here: %v", err)
	}
	return out
}

func TestDecodesThroughFFmpeg(t *testing.T) {
	stream := h264(t, 10)
	d, err := Start("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = d.Write(stream)
		_ = d.CloseInput()
	}()
	var pics []*image.YCbCr
	for p := range d.Pictures() {
		pics = append(pics, p)
	}
	if err := d.Err(); err != nil {
		t.Fatal(err)
	}
	if len(pics) != 10 {
		t.Fatalf("%d pictures of 10", len(pics))
	}
	if pics[0].Rect.Dx() != 64 || pics[0].Y[0] > 60 || pics[9].Y[0] < 200 {
		t.Errorf("pictures: %dx%d, first luma %d, last %d", pics[0].Rect.Dx(), pics[0].Rect.Dy(), pics[0].Y[0], pics[9].Y[0])
	}
}

func TestNoFFmpeg(t *testing.T) {
	if _, err := Start("/nonexistent/ffmpeg"); err == nil {
		t.Error("started without a binary")
	}
}
