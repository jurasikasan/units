package brain

import (
	"sort"
	"sync/atomic"
	"time"
)

// Counts is what the brain dropped or did, by reason; read on GET /health.
type Counts struct {
	Read         atomic.Int64 // stream entries read
	Ignored      atomic.Int64 // types the brain does not read
	Malformed    atomic.Int64 // entries it cannot read as an event
	Duplicates   atomic.Int64 // an event seen before (source + id)
	Gated        atomic.Int64 // dropped at a closed gate
	Empty        atomic.Int64 // recognitions with no words and no frame
	Expired      atomic.Int64 // outlived their channel's lifetime in the buffer
	Overflow     atomic.Int64 // pushed out of a full buffer
	Thoughts     atomic.Int64 // thoughts made
	ModelErrors  atomic.Int64 // ticks the model did not answer
	FrameErrors  atomic.Int64 // frames that could not be fetched
	MemoryErrs   atomic.Int64 // reads or writes of the thread that failed
	Acts         atomic.Int64 // acts done on the body (one per thought at most)
	ActErrors    atomic.Int64 // acts the body refused or that did not reach it
	IgnoredCalls atomic.Int64 // tool calls past the first in one answer
	Skips        atomic.Int64 // answers with tools offered and no call
	BodyErrors   atomic.Int64 // tool lists the body did not give
	Unthought    atomic.Int64 // percepts let go at a tick with no policy to think them
}

// Snapshot is the counts as a map, for the health answer.
func (c *Counts) Snapshot() map[string]int64 {
	return map[string]int64{
		"read": c.Read.Load(), "ignored": c.Ignored.Load(), "malformed": c.Malformed.Load(),
		"duplicates": c.Duplicates.Load(), "gated": c.Gated.Load(), "empty": c.Empty.Load(),
		"expired": c.Expired.Load(), "overflow": c.Overflow.Load(), "thoughts": c.Thoughts.Load(),
		"model_errors": c.ModelErrors.Load(), "frame_errors": c.FrameErrors.Load(),
		"memory_errors": c.MemoryErrs.Load(), "acts": c.Acts.Load(), "act_errors": c.ActErrors.Load(),
		"ignored_calls": c.IgnoredCalls.Load(), "skips": c.Skips.Load(), "body_errors": c.BodyErrors.Load(),
		"unthought": c.Unthought.Load(),
	}
}

// Buffers are sensory memory: one per channel, ordered by perception time,
// each percept living for its channel's lifetime in perception time.
//
// A percept's age is measured on its own channel's clock — the latest
// ended_at that channel has reported — not on the brain's: a sense whose
// words come seconds after what they tell of (a vision model's latency) is
// not aged by that lag, and a backlog read after a restart keeps only the
// last lifetime of each channel. Bounded twice: by lifetime, and by Max
// percepts per channel, the oldest pushed out.
type Buffers struct {
	Keep        map[string]time.Duration // per channel
	KeepDefault time.Duration            // any other channel
	Max         int                      // percepts per channel
	Counts      *Counts

	bufs  map[string][]Percept
	clock map[string]time.Time
}

func (b *Buffers) keep(ch string) time.Duration {
	if d, ok := b.Keep[ch]; ok {
		return d
	}
	return b.KeepDefault
}

// Add keeps p in its channel's buffer, in perception order, and drops what
// is now older than the channel's lifetime.
func (b *Buffers) Add(p Percept) {
	if b.bufs == nil {
		b.bufs, b.clock = map[string][]Percept{}, map[string]time.Time{}
	}
	ch := p.Channel
	if p.Ended.After(b.clock[ch]) {
		b.clock[ch] = p.Ended
	}
	buf := append(b.bufs[ch], p)
	sort.SliceStable(buf, func(i, j int) bool { return buf[i].Started.Before(buf[j].Started) })
	b.bufs[ch] = buf
	b.expire(ch)
	if max := b.Max; max > 0 && len(b.bufs[ch]) > max {
		over := len(b.bufs[ch]) - max
		b.Counts.Overflow.Add(int64(over))
		b.bufs[ch] = append([]Percept(nil), b.bufs[ch][over:]...)
	}
}

func (b *Buffers) expire(ch string) {
	keep, now := b.keep(ch), b.clock[ch]
	kept := b.bufs[ch][:0]
	for _, p := range b.bufs[ch] {
		if now.Sub(p.Ended) > keep {
			b.Counts.Expired.Add(1)
			continue
		}
		kept = append(kept, p)
	}
	b.bufs[ch] = kept
}

// Len is how many percepts wait, over all channels.
func (b *Buffers) Len() int {
	n := 0
	for _, buf := range b.bufs {
		n += len(buf)
	}
	return n
}

// Take empties every buffer and gives what was in them, ordered by
// perception time (started_at; channel and then arrival break ties).
func (b *Buffers) Take() []Percept {
	var out []Percept
	for ch, buf := range b.bufs {
		out = append(out, buf...)
		delete(b.bufs, ch)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Started.Equal(out[j].Started) {
			return out[i].Started.Before(out[j].Started)
		}
		if out[i].Channel != out[j].Channel {
			return out[i].Channel < out[j].Channel
		}
		return out[i].Received.Before(out[j].Received)
	})
	return out
}

// Bind groups percepts (ordered by perception time) into moments: a percept
// joins the moment before it when it starts within bind of that moment's
// end (overlapping is within) and the moment holds another channel than
// its own. Two percepts of one channel alone are two moments; a percept of
// another channel between them may bind all three.
func Bind(ps []Percept, bind time.Duration) [][]Percept {
	var out [][]Percept
	var end time.Time
	for _, p := range ps {
		if n := len(out); n > 0 && !p.Started.After(end.Add(bind)) && hasOther(out[n-1], p.Channel) {
			out[n-1] = append(out[n-1], p)
			if p.Ended.After(end) {
				end = p.Ended
			}
			continue
		}
		out = append(out, []Percept{p})
		end = p.Ended
	}
	return out
}

func hasOther(moment []Percept, ch string) bool {
	for _, p := range moment {
		if p.Channel != ch {
			return true
		}
	}
	return false
}

// Span is the perception time a set of percepts covers.
func Span(ps []Percept) (start, end time.Time) {
	for i, p := range ps {
		if i == 0 || p.Started.Before(start) {
			start = p.Started
		}
		if i == 0 || p.Ended.After(end) {
			end = p.Ended
		}
	}
	return start, end
}
