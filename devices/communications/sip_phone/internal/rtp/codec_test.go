package rtp

import "testing"

func TestMuLawRoundTrip(t *testing.T) {
	// mu-law is lossy; a round trip should stay close to the original sample.
	samples := []int16{0, 100, -100, 1000, -1000, 16000, -16000, 32000, -32000, 32767, -32768}
	for _, s := range samples {
		encoded := EncodeMuLaw(s)
		decoded := DecodeMuLaw(encoded)
		diff := int32(s) - int32(decoded)
		if diff < 0 {
			diff = -diff
		}
		// mu-law quantization error grows with magnitude; allow up to ~4% of full scale.
		const maxErr = 1400
		if diff > maxErr {
			t.Errorf("EncodeMuLaw/DecodeMuLaw(%d) = %d, diff %d exceeds %d", s, decoded, diff, maxErr)
		}
	}
}

func TestMuLawSilence(t *testing.T) {
	// Silence (0) should round-trip to something very close to zero.
	encoded := EncodeMuLaw(0)
	decoded := DecodeMuLaw(encoded)
	if decoded < -10 || decoded > 10 {
		t.Errorf("EncodeMuLaw(0) round-tripped to %d, want near 0", decoded)
	}
}

func TestUpsampleLinear6x(t *testing.T) {
	in := []int16{100, 200, 300}
	out := UpsampleLinear6x(in)
	if len(out) != len(in)*6 {
		t.Fatalf("UpsampleLinear6x: got %d samples, want %d", len(out), len(in)*6)
	}
	if out[0] != 100 {
		t.Errorf("first output sample = %d, want 100", out[0])
	}
	// Interpolation should be monotonic between 100 and 200 for the first block.
	for i := 0; i < 5; i++ {
		if out[i] > out[i+1] {
			t.Errorf("expected monotonic increase at index %d: %d > %d", i, out[i], out[i+1])
		}
	}
}

func TestUpsampleLinear6xEmpty(t *testing.T) {
	if out := UpsampleLinear6x(nil); out != nil {
		t.Errorf("UpsampleLinear6x(nil) = %v, want nil", out)
	}
}

func TestDownsampleAvg6x(t *testing.T) {
	// Six identical samples should average back to the same value.
	in := make([]int16, 6)
	for i := range in {
		in[i] = 500
	}
	out := DownsampleAvg6x(in)
	if len(out) != 1 || out[0] != 500 {
		t.Errorf("DownsampleAvg6x(6x500) = %v, want [500]", out)
	}
}

func TestDownsampleAvg6xDropsRemainder(t *testing.T) {
	in := make([]int16, 8) // 1 full block of 6 + 2 leftover
	out := DownsampleAvg6x(in)
	if len(out) != 1 {
		t.Errorf("DownsampleAvg6x(8 samples) = %d blocks, want 1", len(out))
	}
}

func TestResampleRoundTrip(t *testing.T) {
	// 8kHz -> 48kHz -> 8kHz should approximately recover the original for a
	// constant signal (exact for DC, since averaging a constant gives it back).
	in := []int16{1000, 1000, 1000, 1000, 1000, 1000}
	up := UpsampleLinear6x(in)
	down := DownsampleAvg6x(up)
	if len(down) != len(in) {
		t.Fatalf("round trip length = %d, want %d", len(down), len(in))
	}
	for i, v := range down {
		if v != 1000 {
			t.Errorf("round trip[%d] = %d, want 1000", i, v)
		}
	}
}

func TestInt16BytesRoundTrip(t *testing.T) {
	samples := []int16{0, 1, -1, 32767, -32768, 12345, -12345}
	b := Int16ToBytes(samples)
	if len(b) != len(samples)*2 {
		t.Fatalf("Int16ToBytes length = %d, want %d", len(b), len(samples)*2)
	}
	back := BytesToInt16(b)
	if len(back) != len(samples) {
		t.Fatalf("BytesToInt16 length = %d, want %d", len(back), len(samples))
	}
	for i := range samples {
		if back[i] != samples[i] {
			t.Errorf("round trip[%d] = %d, want %d", i, back[i], samples[i])
		}
	}
}
