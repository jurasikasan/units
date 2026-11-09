// Package resample converts G.711 8 kHz streams to 16 kHz by linear
// interpolation and L16 48 kHz streams to 16 kHz by averaging every 3
// samples, keeping state across packets so no sample is lost at packet
// boundaries.
package resample

// Resampler is a streaming 8 kHz / 48 kHz -> 16 kHz resampler.
type Resampler struct {
	rate    int
	prev    int16
	hasPrev bool
	rem     [3]int16
	remN    int
	p16     int
}

// SetRate selects the source sample rate (8000 or 48000).
func (r *Resampler) SetRate(rate int) {
	r.rate = rate
	r.prev = 0
	r.hasPrev = false
	r.remN = 0
	r.p16 = 0
}

// Emitted is the number of 16 kHz samples produced so far.
func (r *Resampler) Emitted() int { return r.p16 }

// Push feeds one packet of source-rate samples and returns the 16 kHz
// samples it produces.
func (r *Resampler) Push(src []int16) []int16 {
	var out []int16
	switch r.rate {
	case 8000:
		for _, s := range src {
			if r.hasPrev {
				out = append(out, (r.prev+s)/2)
			}
			out = append(out, s)
			r.prev = s
			r.hasPrev = true
		}
	case 48000:
		for _, s := range src {
			r.rem[r.remN] = s
			r.remN++
			if r.remN == 3 {
				out = append(out, (r.rem[0]+r.rem[1]+r.rem[2])/3)
				r.remN = 0
			}
		}
	}
	r.p16 += len(out)
	return out
}
