package change

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func frame(t *testing.T, enc string, paint func(x, y int) uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			img.SetGray(x, y, color.Gray{Y: paint(x, y)})
		}
	}
	var b bytes.Buffer
	var err error
	if enc == "png" {
		err = png.Encode(&b, img)
	} else {
		err = jpeg.Encode(&b, img, &jpeg.Options{Quality: 80})
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestSameSceneIsStill_ChangedSceneIsNot(t *testing.T) {
	dark := frame(t, "jpeg", func(int, int) uint8 { return 30 })
	darkAgain := frame(t, "png", func(int, int) uint8 { return 31 })
	// someone steps into the left third of the view
	someone := frame(t, "jpeg", func(x, _ int) uint8 {
		if x < 110 {
			return 200
		}
		return 30
	})
	g1, err := Of(dark)
	if err != nil {
		t.Fatal(err)
	}
	g2, _ := Of(darkAgain)
	g3, _ := Of(someone)
	if d := Diff(g1, g2); d > 0.02 {
		t.Errorf("same scene differs by %.3f", d)
	}
	if d := Diff(g1, g3); d < 0.2 {
		t.Errorf("changed scene differs by only %.3f", d)
	}
}

func TestMediaType(t *testing.T) {
	if got := MediaType(frame(t, "jpeg", func(int, int) uint8 { return 0 })); got != "image/jpeg" {
		t.Errorf("jpeg: %q", got)
	}
	if got := MediaType(frame(t, "png", func(int, int) uint8 { return 0 })); got != "image/png" {
		t.Errorf("png: %q", got)
	}
	if got := MediaType([]byte("GIF89a")); got != "" {
		t.Errorf("gif: %q", got)
	}
	if _, err := Of([]byte("not a picture")); err == nil {
		t.Error("garbage decoded")
	}
}
