package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"math/rand/v2"
	"net/http"
	"strings"
	"testing"
)

// UNIT-44: one photo sent from the face got HTTP 500. It was a JPEG of
// 1800×1800, about 1.7 MB (2.3 MB as base64), JFIF with EXIF whose Software
// tag holds CP1251 bytes, not UTF-8, and its file name began with a space.
// The 500 was the site's nginx (the gate's auth subrequest held the body to
// 1 MB), not the mind's; these tests hold the mind to its part: such a photo
// reaches the model as it is, and a provider that will not take it is heard
// in its own words.

// photo is a JPEG with the reported photo's traits, made here: noise, so it
// does not compress below a megabyte, and an EXIF segment whose Software is
// CP1251 text.
func photo(t *testing.T) []byte {
	t.Helper()
	const side = 1800
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	r := rand.New(rand.NewPCG(44, 44))
	for i := range img.Pix {
		img.Pix[i] = byte(r.UintN(256))
	}
	var enc bytes.Buffer
	if err := jpeg.Encode(&enc, img, &jpeg.Options{Quality: 50}); err != nil {
		t.Fatal(err)
	}
	// "Фото ACD Systems" in CP1251: not valid UTF-8.
	software := append([]byte{0xD4, 0xEE, 0xF2, 0xEE, ' '}, "ACD Systems\x00"...)
	var tiff bytes.Buffer
	tiff.WriteString("MM\x00\x2a")
	_ = binary.Write(&tiff, binary.BigEndian, uint32(8))                                       // IFD0 at 8
	_ = binary.Write(&tiff, binary.BigEndian, uint16(1))                                       // one entry
	_ = binary.Write(&tiff, binary.BigEndian, []uint16{0x0131, 2})                             // Software, ASCII
	_ = binary.Write(&tiff, binary.BigEndian, []uint32{uint32(len(software)), 8 + 2 + 12 + 4}) // count, offset
	_ = binary.Write(&tiff, binary.BigEndian, uint32(0))                                       // no next IFD
	tiff.Write(software)
	app1 := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	b := enc.Bytes()
	var out bytes.Buffer
	out.Write(b[:2]) // SOI
	out.Write([]byte{0xFF, 0xE1})
	_ = binary.Write(&out, binary.BigEndian, uint16(len(app1)+2))
	out.Write(app1)
	out.Write(b[2:])
	if out.Len() < 1<<20 {
		t.Fatalf("the photo is %d bytes; it must be over a megabyte to be like the reported one", out.Len())
	}
	return out.Bytes()
}

// photoRun is a run carrying the photo as the face sends it.
func photoRun(data string) string {
	return fmt.Sprintf(`{"messages":[{"role":"user","content":[
		{"type":"text","text":"what is in the picture?"},
		{"type":"image","source":{"type":"data","value":%q,"mimeType":"image/jpeg"},"metadata":{"filename":" img.jpg"}}]}]}`, data)
}

func TestALargePhotoWithOddEXIFReachesTheModelAsItIs(t *testing.T) {
	data := base64.StdEncoding.EncodeToString(photo(t))
	var parts []struct {
		Type     string `json:"type"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(lastContent(t, photoRun(data)), &parts); err != nil {
		t.Fatalf("content is not parts: %v", err)
	}
	if len(parts) != 2 || parts[1].Type != "image_url" {
		t.Fatalf("parts %+v", parts)
	}
	if want := "data:image/jpeg;base64," + data; parts[1].ImageURL.URL != want {
		t.Errorf("the photo reached the model changed: %d bytes of URL, want %d", len(parts[1].ImageURL.URL), len(want))
	}
}

func TestAProviderRefusingThePhotoIsHeardInItsOwnWords(t *testing.T) {
	data := base64.StdEncoding.EncodeToString(photo(t))
	const words = "Provided image is not valid (fake provider)"
	_, url := mindOn(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"error":{"message":%q,"type":"invalid_request_error","code":"400"}}`, words)
	}))
	resp := post(t, context.Background(), url, "alice", photoRun(data))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want the run's 200 with RUN_ERROR in it", resp.StatusCode)
	}
	evs := read(t, resp.Body)
	if got := kinds(evs); got != "RUN_STARTED RUN_ERROR" {
		t.Fatalf("events %s", got)
	}
	last := evs[len(evs)-1]
	if last.Code != CodeModelRefused || !strings.Contains(last.Message, words) || !strings.Contains(last.Message, "400") {
		t.Errorf("RUN_ERROR %q %q, want %s with the provider's words", last.Code, last.Message, CodeModelRefused)
	}
}
