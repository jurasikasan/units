// Package stt transcribes 16 kHz PCM through OpenAI's
// POST /v1/audio/transcriptions: a router (LiteLLM) or one engine.
package stt

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

// Result is the transcription outcome.
type Result struct {
	Text     string
	Language string
}

// Door is where transcriptions go: an OpenAI-shaped base (…/v1), its bearer
// (a router's key; empty for an engine) and the model to ask for.
type Door struct {
	URL, Key, Model string
}

// Transcribe POSTs the samples as a 16 kHz 16-bit mono WAV file to
// {URL}/audio/transcriptions with language as a hint (empty: STT guesses) and
// returns the text and the language STT reports. Any transport or HTTP failure
// is an error; the caller reports the utterance with recognizer-error.
func Transcribe(ctx context.Context, d Door, httpClient *http.Client, pcm16k []int16, language string) (Result, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "audio.wav")
	if err != nil {
		return Result{}, err
	}
	if _, err := part.Write(EncodeWAV(pcm16k)); err != nil {
		return Result{}, err
	}
	fields := [][2]string{{"model", d.Model}, {"response_format", "verbose_json"}}
	if language != "" {
		fields = append(fields, [2]string{"language", language})
	}
	for _, f := range fields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return Result{}, err
		}
	}
	if err := mw.Close(); err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(d.URL, "/")+"/audio/transcriptions", &body)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if d.Key != "" {
		req.Header.Set("Authorization", "Bearer "+d.Key)
	}
	client := httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode/100 != 2 {
		return Result{}, fmt.Errorf("stt %s: status %d: %s", d.Model, resp.StatusCode, truncate(string(raw), 200))
	}
	var out struct {
		Text     string `json:"text"`
		Language string `json:"language"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, fmt.Errorf("stt %s: bad body: %w", d.Model, err)
	}
	return Result{Text: out.Text, Language: out.Language}, nil
}

// EncodeWAV encodes 16 kHz mono 16-bit little-endian PCM in a 44-byte
// RIFF/WAV container.
func EncodeWAV(pcm []int16) []byte {
	dataSize := uint32(len(pcm) * 2)
	buf := make([]byte, 44+int(dataSize))
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], 36+dataSize)
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)    // PCM fmt chunk size
	binary.LittleEndian.PutUint16(buf[20:22], 1)     // PCM
	binary.LittleEndian.PutUint16(buf[22:24], 1)     // channels
	binary.LittleEndian.PutUint32(buf[24:28], 16000) // sample rate
	binary.LittleEndian.PutUint32(buf[28:32], 32000) // byte rate
	binary.LittleEndian.PutUint16(buf[32:34], 2)     // block align
	binary.LittleEndian.PutUint16(buf[34:36], 16)    // bits per sample
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], dataSize)
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(buf[44+2*i:], uint16(s))
	}
	return buf
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
