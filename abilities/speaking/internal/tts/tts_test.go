package tts

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wav builds a canonical RIFF/WAVE body (the layout from the WAVE spec), with
// an extra LIST chunk before data the way many encoders write one.
func wav(rate uint32, channels uint16, samples []int16) []byte {
	var data []byte
	for _, s := range samples {
		data = binary.LittleEndian.AppendUint16(data, uint16(s))
	}
	b := []byte("RIFF")
	b = binary.LittleEndian.AppendUint32(b, uint32(4+8+16+8+4+8+len(data)))
	b = append(b, "WAVE"...)
	b = append(b, "fmt "...)
	b = binary.LittleEndian.AppendUint32(b, 16)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint16(b, channels)
	b = binary.LittleEndian.AppendUint32(b, rate)
	b = binary.LittleEndian.AppendUint32(b, rate*uint32(channels)*2)
	b = binary.LittleEndian.AppendUint16(b, channels*2)
	b = binary.LittleEndian.AppendUint16(b, 16)
	b = append(b, "LIST"...)
	b = binary.LittleEndian.AppendUint32(b, 4)
	b = append(b, "INFO"...)
	b = append(b, "data"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(data)))
	return append(b, data...)
}

func TestSynth_AsksOpenAISpeechAndDecodes(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/audio/speech", r.URL.Path)
		auth = r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(wav(48000, 1, []int16{1, -2, 300}))
	}))
	defer srv.Close()

	pcm, err := New(srv.URL+"/v1/", "sk-x").Synth(context.Background(), "Привет.", "voxcpm_design_Elena")
	require.NoError(t, err)
	assert.Equal(t, []int16{1, -2, 300}, pcm, "48 kHz passes through untouched")
	assert.Equal(t, map[string]any{"model": "voxcpm", "voice": "design_Elena", "input": "Привет.", "response_format": "wav"}, got,
		"the engine prefix is the model, the rest — underscores and all — the voice")
	assert.Equal(t, "Bearer sk-x", auth)
}

// An engine answers at its own rate; speech brings it to 48 kHz. 24 kHz doubles
// the samples, each new one halfway between its neighbours.
func TestSynth_ResamplesTo48k(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(wav(24000, 1, []int16{0, 100, 200}))
	}))
	defer srv.Close()
	pcm, err := New(srv.URL, "").Synth(context.Background(), "x", "piper_irina")
	require.NoError(t, err)
	assert.Equal(t, []int16{0, 50, 100, 150, 200, 200}, pcm)
}

func TestSynth_VoiceWithoutEngineIsAnError(t *testing.T) {
	_, err := New("http://unused", "").Synth(context.Background(), "x", "denis")
	assert.ErrorContains(t, err, "<engine>_<voice>")
}

func TestSynth_EngineErrorIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unknown voice", http.StatusBadRequest)
	}))
	defer srv.Close()
	_, err := New(srv.URL, "").Synth(context.Background(), "x", "piper_nobody")
	assert.ErrorContains(t, err, "400")
}
