// Package tts is speech's client of a TTS engine behind OpenAI's audio API
// (POST /v1/audio/speech): text in, 48 kHz mono PCM out. Behind URL is
// a router (LiteLLM) or one engine; speech does not care which.
//
// A voice is named the way subjects store it, <engine>_<voice> (piper_irina,
// voxcpm_design_Elena): the engine is OpenAI's model, the rest its voice.
// Bringing the engine's own rate to 48 kHz is speech's job (C4 §4).
package tts

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const sampleRate = 48000

type Client struct {
	URL  string // OpenAI-shaped base, e.g. http://litellm:4000/v1
	Key  string // bearer for a router; empty for an engine
	HTTP *http.Client
}

func New(url, key string) *Client {
	return &Client{URL: strings.TrimRight(url, "/"), Key: key, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// ModelVoice splits a stored voice name into OpenAI's model and voice.
func ModelVoice(name string) (model, voice string, ok bool) {
	model, voice, ok = strings.Cut(name, "_")
	return model, voice, ok && model != "" && voice != ""
}

// Synth speaks one sentence and returns it as 48 kHz mono PCM16.
func (c *Client) Synth(ctx context.Context, text, voiceName string) ([]int16, error) {
	model, voice, ok := ModelVoice(voiceName)
	if !ok {
		return nil, fmt.Errorf("tts: voice %q is not <engine>_<voice>", voiceName)
	}
	body, _ := json.Marshal(map[string]any{"model": model, "voice": voice, "input": text, "response_format": "wav"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/audio/speech", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tts %s/%s %d: %s", model, voice, res.StatusCode, bytes.TrimSpace(data))
	}
	pcm, rate, err := decodeWAV(data)
	if err != nil {
		return nil, err
	}
	return resample(pcm, rate, sampleRate), nil
}

// resample linearly interpolates mono PCM16 from src to dst Hz. Linear is adequate for speech that
// a phone narrows to 8 kHz or a browser line Opus-encodes.
func resample(in []int16, src, dst int) []int16 {
	n := len(in)
	if src == dst || n == 0 {
		return in
	}
	outN := int(int64(n) * int64(dst) / int64(src))
	out := make([]int16, outN)
	ratio := float64(src) / float64(dst)
	for j := range out {
		pos := float64(j) * ratio
		i0 := int(pos)
		frac := pos - float64(i0)
		s0 := float64(in[i0])
		s1 := s0
		if i0+1 < n {
			s1 = float64(in[i0+1])
		}
		out[j] = int16(s0 + (s1-s0)*frac)
	}
	return out
}

// decodeWAV reads 16-bit mono PCM and its rate out of a RIFF/WAVE body,
// walking the chunks rather than assuming a 44-byte header.
func decodeWAV(b []byte) ([]int16, int, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, 0, errors.New("tts: not a WAV")
	}
	var fmtOK bool
	var rate int
	for p := 12; p+8 <= len(b); {
		id := string(b[p : p+4])
		size := int(binary.LittleEndian.Uint32(b[p+4 : p+8]))
		body := b[p+8:]
		if size > len(body) {
			size = len(body)
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, 0, errors.New("tts: short fmt chunk")
			}
			format := binary.LittleEndian.Uint16(body[0:2])
			channels := binary.LittleEndian.Uint16(body[2:4])
			rate = int(binary.LittleEndian.Uint32(body[4:8]))
			bits := binary.LittleEndian.Uint16(body[14:16])
			if format != 1 || channels != 1 || bits != 16 || rate <= 0 {
				return nil, 0, fmt.Errorf("tts: want PCM16 mono, got format %d, %d ch, %d bit, %d Hz", format, channels, bits, rate)
			}
			fmtOK = true
		case "data":
			if !fmtOK {
				return nil, 0, errors.New("tts: data before fmt")
			}
			pcm := make([]int16, size/2)
			for i := range pcm {
				pcm[i] = int16(binary.LittleEndian.Uint16(body[2*i:]))
			}
			return pcm, rate, nil
		}
		p += 8 + size + size%2
	}
	return nil, 0, errors.New("tts: no data chunk")
}
