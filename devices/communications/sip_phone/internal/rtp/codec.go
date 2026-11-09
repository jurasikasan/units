package rtp

import "encoding/binary"

// G.711 mu-law codec — the ITU-T/Sun reference algorithm (public domain),
// operating on 16-bit linear PCM samples. No external library needed: this
// is a lookup-free segment-search transform, not a real compression codec.
const (
	muLawBias = 0x84
	muLawClip = 32635
)

var muLawSegEnd = [8]int32{0xFF, 0x1FF, 0x3FF, 0x7FF, 0xFFF, 0x1FFF, 0x3FFF, 0x7FFF}

// EncodeMuLaw converts one 16-bit linear PCM sample to an 8-bit G.711 mu-law byte.
func EncodeMuLaw(pcm int16) byte {
	sample := int32(pcm)
	var mask int32
	if sample < 0 {
		sample = muLawBias - sample
		mask = 0x7F
	} else {
		sample += muLawBias
		mask = 0xFF
	}
	if sample > muLawClip {
		sample = muLawClip
	}

	seg := 8
	for i, end := range muLawSegEnd {
		if sample <= end {
			seg = i
			break
		}
	}
	if seg >= 8 {
		return byte(0x7F ^ mask)
	}
	uval := byte(seg<<4) | byte((sample>>(int32(seg)+3))&0x0F)
	return uval ^ byte(mask)
}

// DecodeMuLaw converts one 8-bit G.711 mu-law byte back to a 16-bit linear PCM sample.
func DecodeMuLaw(u byte) int16 {
	u = ^u
	t := (int32(u&0x0F) << 3) + muLawBias
	t <<= (u & 0x70) >> 4
	if u&0x80 != 0 {
		return int16(muLawBias - t)
	}
	return int16(t - muLawBias)
}

// EncodePCMToMuLaw converts a buffer of 16-bit linear PCM samples to G.711 mu-law bytes.
func EncodePCMToMuLaw(pcm []int16) []byte {
	out := make([]byte, len(pcm))
	for i, s := range pcm {
		out[i] = EncodeMuLaw(s)
	}
	return out
}

// DecodeMuLawToPCM converts a buffer of G.711 mu-law bytes to 16-bit linear PCM samples.
func DecodeMuLawToPCM(mulaw []byte) []int16 {
	out := make([]int16, len(mulaw))
	for i, b := range mulaw {
		out[i] = DecodeMuLaw(b)
	}
	return out
}

// G.711 A-law — the other half of G.711 and the default on European trunks,
// where mu-law is the exception. Same ITU-T/Sun reference algorithm, different
// segment table and sign/mask convention.
const (
	aLawSignBit   = 0x80
	aLawQuantMask = 0x0F
	aLawSegMask   = 0x70
	aLawSegShift  = 4
)

var aLawSegEnd = [8]int32{0x1F, 0x3F, 0x7F, 0xFF, 0x1FF, 0x3FF, 0x7FF, 0xFFF}

// EncodeALaw converts one 16-bit linear PCM sample to an 8-bit G.711 A-law byte.
func EncodeALaw(pcm int16) byte {
	sample := int32(pcm) >> 3
	var mask int32
	if sample >= 0 {
		mask = 0xD5
	} else {
		mask = 0x55
		sample = -sample - 1
	}

	seg := 8
	for i, end := range aLawSegEnd {
		if sample <= end {
			seg = i
			break
		}
	}
	if seg >= 8 {
		return byte(0x7F ^ mask)
	}

	aval := int32(seg) << aLawSegShift
	if seg < 2 {
		aval |= (sample >> 1) & aLawQuantMask
	} else {
		aval |= (sample >> seg) & aLawQuantMask
	}
	return byte(aval ^ mask)
}

// DecodeALaw converts one 8-bit G.711 A-law byte back to a 16-bit linear PCM sample.
func DecodeALaw(a byte) int16 {
	a ^= 0x55
	t := int32(a&aLawQuantMask) << 4
	switch seg := (int32(a) & aLawSegMask) >> aLawSegShift; seg {
	case 0:
		t += 8
	case 1:
		t += 0x108
	default:
		t += 0x108
		t <<= seg - 1
	}
	if a&aLawSignBit != 0 {
		return int16(t)
	}
	return int16(-t)
}

// EncodePCMToALaw converts a buffer of 16-bit linear PCM samples to G.711 A-law bytes.
func EncodePCMToALaw(pcm []int16) []byte {
	out := make([]byte, len(pcm))
	for i, s := range pcm {
		out[i] = EncodeALaw(s)
	}
	return out
}

// DecodeALawToPCM converts a buffer of G.711 A-law bytes to 16-bit linear PCM samples.
func DecodeALawToPCM(alaw []byte) []int16 {
	out := make([]int16, len(alaw))
	for i, b := range alaw {
		out[i] = DecodeALaw(b)
	}
	return out
}

// PayloadType is a static RTP payload type (RFC 3551). This transport speaks
// the two G.711 flavours and nothing else — which one is used is decided per
// call by the SDP answer, not hardcoded.
type PayloadType uint8

const (
	// PayloadTypePCMU is G.711 mu-law — the North American default.
	PayloadTypePCMU PayloadType = 0
	// PayloadTypePCMA is G.711 A-law — the European default, and what most EU
	// trunks offer. Answering PCMU to an A-law-only offer violates RFC 3264 §6
	// (the answer's formats must be a subset of the offer) and gets the call
	// dropped by the far SBC.
	PayloadTypePCMA PayloadType = 8
)

// Name returns the SDP rtpmap encoding name for the payload type.
func (pt PayloadType) Name() string {
	if pt == PayloadTypePCMA {
		return "PCMA"
	}
	return "PCMU"
}

// EncodePCM converts 8 kHz linear PCM16 to this payload type's G.711 bytes.
func (pt PayloadType) EncodePCM(pcm []int16) []byte {
	if pt == PayloadTypePCMA {
		return EncodePCMToALaw(pcm)
	}
	return EncodePCMToMuLaw(pcm)
}

// DecodePCM converts this payload type's G.711 bytes to 8 kHz linear PCM16.
func (pt PayloadType) DecodePCM(b []byte) []int16 {
	if pt == PayloadTypePCMA {
		return DecodeALawToPCM(b)
	}
	return DecodeMuLawToPCM(b)
}

// UpsampleLinear6x converts 8kHz PCM16 to 48kHz PCM16 via linear interpolation.
// The ratio is an exact 1:6 (48000/8000), so no fractional-rate resampling is needed.
func UpsampleLinear6x(in []int16) []int16 {
	if len(in) == 0 {
		return nil
	}
	out := make([]int16, len(in)*6)
	for i, cur := range in {
		next := cur
		if i+1 < len(in) {
			next = in[i+1]
		}
		for j := 0; j < 6; j++ {
			out[i*6+j] = int16(int32(cur) + (int32(next)-int32(cur))*int32(j)/6)
		}
	}
	return out
}

// DownsampleAvg6x converts 48kHz PCM16 to 8kHz PCM16 by averaging each block of 6
// samples — a simple box-filter low-pass that also decimates, exact 1:6 ratio.
// Trailing samples that don't fill a full block of 6 are dropped.
func DownsampleAvg6x(in []int16) []int16 {
	n := len(in) / 6
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		var sum int32
		for j := 0; j < 6; j++ {
			sum += int32(in[i*6+j])
		}
		out[i] = int16(sum / 6)
	}
	return out
}

// Int16ToBytes little-endian encodes a PCM16 sample buffer to bytes.
func Int16ToBytes(samples []int16) []byte {
	b := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(s))
	}
	return b
}

// BytesToInt16 little-endian decodes a byte buffer to PCM16 samples.
func BytesToInt16(b []byte) []int16 {
	n := len(b) / 2
	s := make([]int16, n)
	for i := range s {
		s[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
	}
	return s
}
