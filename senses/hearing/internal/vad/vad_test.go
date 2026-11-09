package vad

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnergyThreshold(t *testing.T) {
	d := &Energy{Threshold: 500}
	speech := make([]int16, 512)
	for i := range speech {
		speech[i] = 8000
	}
	ok, err := d.Speech(context.Background(), speech)
	if err != nil || !ok {
		t.Fatalf("amp 8000: ok=%v err=%v, want speech", ok, err)
	}
	quiet := make([]int16, 512)
	ok, err = d.Speech(context.Background(), quiet)
	if err != nil || ok {
		t.Fatalf("silence: ok=%v err=%v, want silence", ok, err)
	}
	// a window with rms just below 500 is silence, just above is speech
	below := make([]int16, 512)
	for i := range below {
		below[i] = 499
	}
	ok, _ = d.Speech(context.Background(), below)
	if ok {
		t.Fatalf("rms 499 should be silence")
	}
	above := make([]int16, 512)
	for i := range above {
		above[i] = 501
	}
	ok, _ = d.Speech(context.Background(), above)
	if !ok {
		t.Fatalf("rms 501 should be speech")
	}
}

func TestSileroSendsExactly1024Bytes(t *testing.T) {
	var (
		bodyLens []int
		deletes  int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		body, _ := io.ReadAll(r.Body)
		bodyLens = append(bodyLens, len(body))
		if len(body) != 1024 {
			http.Error(w, "body must be exactly 1024 bytes", http.StatusBadRequest)
			return
		}
		var sum float64
		for i := 0; i+1 < len(body); i += 2 {
			v := float64(int16(binary.LittleEndian.Uint16(body[i:])))
			sum += v * v
		}
		rms := math.Sqrt(sum / float64(len(body)/2))
		prob := 0.0
		if rms >= 500 {
			prob = 1.0
		}
		_, _ = fmt.Fprintf(w, `{"prob":%v}`, prob)
	}))
	defer srv.Close()

	d := NewSilero(srv.URL, "hr-00000000", srv.Client())
	speech := make([]int16, 512)
	for i := range speech {
		speech[i] = 8000
	}
	ok, err := d.Speech(context.Background(), speech)
	if err != nil || !ok {
		t.Fatalf("speech window: ok=%v err=%v", ok, err)
	}
	quiet := make([]int16, 512)
	ok, err = d.Speech(context.Background(), quiet)
	if err != nil || ok {
		t.Fatalf("silence window: ok=%v err=%v", ok, err)
	}
	d.Forget(context.Background())

	if len(bodyLens) != 2 {
		t.Fatalf("expected 2 vad requests, got %d", len(bodyLens))
	}
	for i, n := range bodyLens {
		if n != 1024 {
			t.Fatalf("request %d body is %d bytes, want exactly 1024", i, n)
		}
	}
	if deletes == 0 {
		t.Fatal("Forget did not DELETE the vad state")
	}
}
