package rtp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A-law is lossy by design (13-bit → 8-bit companding), so a round trip is
// checked for bounded error, not equality. The bound scales with amplitude:
// G.711's whole point is a roughly constant signal-to-quantisation-noise ratio.
func TestALawRoundTrip(t *testing.T) {
	for _, sample := range []int16{0, 1, -1, 100, -100, 1000, -1000, 8000, -8000, 32000, -32000, 32767, -32768} {
		got := DecodeALaw(EncodeALaw(sample))
		tolerance := int(float64(abs(int(sample)))*0.07) + 16
		assert.InDeltaf(t, float64(sample), float64(got), float64(tolerance),
			"A-law round trip for %d gave %d", sample, got)
	}
}

// Known-good vectors from the ITU-T G.711 reference: digital silence and the
// extremes. Guards against a sign/mask inversion that a round-trip test would
// happily accept (encode and decode would cancel out).
func TestALawKnownValues(t *testing.T) {
	assert.Equal(t, byte(0xD5), EncodeALaw(0), "A-law silence byte")
	assert.Equal(t, byte(0x55), EncodeALaw(-1))
	// Positive input must decode back positive, negative back negative —
	// the failure mode that makes a call sound like white noise.
	assert.Positive(t, DecodeALaw(EncodeALaw(5000)))
	assert.Negative(t, DecodeALaw(EncodeALaw(-5000)))
}

func TestPayloadTypeDispatch(t *testing.T) {
	pcm := []int16{0, 1000, -1000, 20000}

	require.Equal(t, "PCMU", PayloadTypePCMU.Name())
	require.Equal(t, "PCMA", PayloadTypePCMA.Name())

	// Each flavour must round-trip through itself...
	for _, pt := range []PayloadType{PayloadTypePCMU, PayloadTypePCMA} {
		out := pt.DecodePCM(pt.EncodePCM(pcm))
		require.Len(t, out, len(pcm))
		for i := range pcm {
			assert.InDeltaf(t, float64(pcm[i]), float64(out[i]), float64(abs(int(pcm[i])))*0.08+64,
				"%s round trip sample %d", pt.Name(), i)
		}
	}

	// ...and the two must NOT be interchangeable. If they were, a codec
	// mismatch would go unnoticed until someone listened to the call.
	assert.NotEqual(t, PayloadTypePCMU.EncodePCM(pcm), PayloadTypePCMA.EncodePCM(pcm))
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
