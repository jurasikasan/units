package sight

import (
	"log"
	"sort"
	"sync"
	"time"

	"github.com/jurasikasan/units/senses/sight/internal/change"
)

// video is a session's video path (CONTRACT §4.1): access units from
// video_in go to a decoder from the first key frame on, its pictures are
// placed on the stream's timeline, sampled, and offered to the worker.
// After a loss it waits for the next key frame. It ends when video_in has
// stopped and the decoder has given its last picture, and closes frames.
func (se *session) video() {
	defer close(se.frames)
	var (
		dec     Decoder
		pending *stamps
		reading sync.WaitGroup
		started bool
		failed  bool // the decoder could not start: said once, not for every key frame
	)
	stop := func() {
		if dec != nil {
			_ = dec.CloseInput()
			reading.Wait()
			dec = nil
		}
		started = false
	}
	var sample sampler
	for au := range se.in.Out() {
		if au.Broken {
			started = false
			continue
		}
		if !started {
			if !au.Key {
				continue
			}
			if dec == nil {
				d, err := se.cfg.NewDecoder()
				if err != nil {
					if !failed {
						log.Printf("sight: %s: no decoder, video_in is not seen: %v", se.id, err)
						failed = true
					}
					continue
				}
				dec, pending = d, &stamps{}
				reading.Add(1)
				go func() {
					defer reading.Done()
					se.pictures(d, pending, &sample)
				}()
			}
			started = true
		}
		pending.push(au.TS)
		if err := dec.Write(au.Data); err != nil {
			log.Printf("sight: %s: decoder: %v", se.id, err)
			stop()
		}
	}
	stop()
}

// pictures takes a decoder's pictures, gives each the timestamp it was fed
// with, and offers the sampled ones to the worker.
func (se *session) pictures(d Decoder, pending *stamps, sample *sampler) {
	for img := range d.Pictures() {
		ts, ok := pending.pop()
		if !ok {
			continue // a picture the decoder made of nothing fed: no time to give it
		}
		at, clock := se.in.At(ts)
		if !sample.take(at, se.cfg.SampleEvery) {
			continue
		}
		grid, err := change.OfImage(img)
		if err != nil {
			continue
		}
		se.offer(item{kind: "video", at: at, pic: &picture{img: img, grid: grid, at: at, clock: clock}})
	}
	if err := d.Err(); err != nil {
		log.Printf("sight: %s: decoder stopped: %v", se.id, err)
	}
}

// offer hands a picture to the worker without waiting for it: when the
// worker is busy (a description running), the picture waiting gives way to
// this newer one. The detector looks at the latest of the scene.
func (se *session) offer(it item) {
	for {
		select {
		case se.frames <- it:
			return
		default:
		}
		select {
		case <-se.frames:
		default:
		}
	}
}

// sampler lets through at most one picture per interval of the stream's
// time; it is the session's, across decoders.
type sampler struct {
	mu   sync.Mutex
	have bool
	last time.Time
}

func (s *sampler) take(at time.Time, every time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.have && at.Sub(s.last) < every {
		return false
	}
	s.have, s.last = true, at
	return true
}

// stamps are the timestamps of the access units fed to a decoder and not yet
// given back as pictures. A decoder gives pictures in presentation order,
// which RTP timestamps are, so each picture is the earliest one waiting,
// B-frames or none.
type stamps struct {
	mu sync.Mutex
	ts []int64
}

func (s *stamps) push(ts int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := sort.Search(len(s.ts), func(i int) bool { return s.ts[i] > ts })
	s.ts = append(s.ts, 0)
	copy(s.ts[i+1:], s.ts[i:])
	s.ts[i] = ts
}

func (s *stamps) pop() (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ts) == 0 {
		return 0, false
	}
	ts := s.ts[0]
	s.ts = s.ts[1:]
	return ts, true
}
