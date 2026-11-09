// Package vad detects speech in 32 ms windows (512 samples at 16 kHz).
package vad

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
)

// Detector decides whether a 16 kHz window is speech.
type Detector interface {
	// Speech reports whether window is speech.
	Speech(ctx context.Context, window []int16) (bool, error)
	// Forget drops any per-session state the detector keeps.
	Forget(ctx context.Context)
}

// Energy is an RMS-based detector: a window is speech when its RMS is
// above Threshold.
type Energy struct {
	Threshold float64
}

// Speech implements Detector.
func (e *Energy) Speech(_ context.Context, window []int16) (bool, error) {
	if len(window) == 0 {
		return false, nil
	}
	var sum float64
	for _, s := range window {
		v := float64(s)
		sum += v * v
	}
	rms := math.Sqrt(sum / float64(len(window)))
	return rms > e.Threshold, nil
}

// Forget implements Detector (energy VAD keeps no state).
func (e *Energy) Forget(context.Context) {}

// Silero is a client for engines/vad/silero: POST /vad/{id} with a
// body of exactly 1024 bytes (512 int16 little-endian samples at 16 kHz)
// and {"prob" in [0,1]} as the answer; a window is speech when
// prob >= 0.5. DELETE /vad/{id} forgets the state.
type Silero struct {
	base       string
	id         string
	httpClient *http.Client
}

// NewSilero makes a Silero client for one session id.
func NewSilero(base string, id string, httpClient *http.Client) *Silero {
	return &Silero{base: strings.TrimRight(base, "/"), id: id, httpClient: httpClient}
}

// Speech implements Detector.
func (s *Silero) Speech(ctx context.Context, window []int16) (bool, error) {
	body := make([]byte, 2*len(window))
	for i, v := range window {
		binary.LittleEndian.PutUint16(body[2*i:], uint16(v))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/vad/"+s.id, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := s.do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	if resp.StatusCode/100 != 2 {
		return false, fmt.Errorf("vad: status %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out struct {
		Prob float64 `json:"prob"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, fmt.Errorf("vad: bad body: %w", err)
	}
	return out.Prob >= 0.5, nil
}

// Forget implements Detector.
func (s *Silero) Forget(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.base+"/vad/"+s.id, nil)
	if err != nil {
		return
	}
	resp, err := s.do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

func (s *Silero) do(req *http.Request) (*http.Response, error) {
	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
