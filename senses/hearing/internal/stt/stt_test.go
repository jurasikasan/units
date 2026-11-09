package stt

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEncodeWAV(t *testing.T) {
	pcm := []int16{1000, -1000, 42}
	buf := EncodeWAV(pcm)
	if len(buf) != 44+2*len(pcm) {
		t.Fatalf("len = %d, want %d", len(buf), 44+2*len(pcm))
	}
	if !strings.Contains(string(buf[0:4]), "RIFF") || !strings.Contains(string(buf[8:12]), "WAVE") {
		t.Fatalf("bad RIFF/WAVE magic: % x", buf[0:12])
	}
	if f := binary.LittleEndian.Uint16(buf[20:22]); f != 1 {
		t.Fatalf("audio format = %d, want 1 (PCM)", f)
	}
	if ch := binary.LittleEndian.Uint16(buf[22:24]); ch != 1 {
		t.Fatalf("channels = %d, want 1", ch)
	}
	if rate := binary.LittleEndian.Uint32(buf[24:28]); rate != 16000 {
		t.Fatalf("sample rate = %d, want 16000", rate)
	}
	if bits := binary.LittleEndian.Uint16(buf[34:36]); bits != 16 {
		t.Fatalf("bits per sample = %d, want 16", bits)
	}
	if dl := binary.LittleEndian.Uint32(buf[40:44]); dl != uint32(2*len(pcm)) {
		t.Fatalf("data chunk size = %d, want %d", dl, 2*len(pcm))
	}
	if s := int16(binary.LittleEndian.Uint16(buf[44:46])); s != 1000 {
		t.Fatalf("first sample = %d, want 1000", s)
	}
}

func TestTranscribeContract(t *testing.T) {
	var (
		path   string
		wav    []byte
		fields = map[string]string{}
		auth   string
		code   = http.StatusOK
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if code != http.StatusOK {
			http.Error(w, "boom", code)
			return
		}
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			http.Error(w, "bad multipart", http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "no file field", http.StatusBadRequest)
			return
		}
		wav, _ = io.ReadAll(file)
		_ = file.Close()
		for _, k := range []string{"model", "language", "response_format"} {
			fields[k] = r.FormValue(k)
		}
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"text":"hello","language":"en"}`)
	}))
	defer srv.Close()

	pcm := make([]int16, 512)
	door := Door{URL: srv.URL + "/v1/", Key: "sk-x", Model: "stt"}
	res, err := Transcribe(context.Background(), door, srv.Client(), pcm, "ru")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"model": "stt", "language": "ru", "response_format": "verbose_json"}
	for k, v := range want {
		if fields[k] != v {
			t.Fatalf("field %s = %q, want %q", k, fields[k], v)
		}
	}
	if auth != "Bearer sk-x" {
		t.Fatalf("Authorization = %q", auth)
	}
	if res.Text != "hello" || res.Language != "en" {
		t.Fatalf("res = %+v, want {hello en}", res)
	}
	if path != "/v1/audio/transcriptions" {
		t.Fatalf("path = %q", path)
	}
	if len(wav) != 44+2*len(pcm) {
		t.Fatalf("wav len = %d, want %d", len(wav), 44+2*len(pcm))
	}

	code = http.StatusInternalServerError
	res, err = Transcribe(context.Background(), door, srv.Client(), pcm, "")
	if err == nil {
		t.Fatalf("500 from STT must be an error, got %+v", res)
	}
}
