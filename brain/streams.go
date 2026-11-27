package brain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
)

// Streams are the paths in: the senses' Redis streams named in BRAIN_SENSES,
// read by one consumer group so a restart resumes where the brain left off.
type Streams struct {
	Client   *redis.Client
	Names    []string
	Group    string
	Consumer string
}

// Ensure creates the group on each stream (and the stream, if no sense has
// written yet). A new group starts at the stream's end: on its first start
// the brain perceives from now, not the stream's history.
func (s Streams) Ensure(ctx context.Context) error {
	for _, name := range s.Names {
		err := s.Client.XGroupCreateMkStream(ctx, name, s.Group, "$").Err()
		if err != nil && !strings.HasPrefix(err.Error(), "BUSYGROUP") {
			return fmt.Errorf("group %s on %s: %w", s.Group, name, err)
		}
	}
	return nil
}

func pendingEmpty(res []redis.XStream) bool {
	for _, st := range res {
		if len(st.Messages) > 0 {
			return false
		}
	}
	return true
}

// Run reads the streams until ctx ends, gives each entry to out as an Event
// and acknowledges it once out took it. An entry it cannot read is counted,
// logged and acknowledged: a bad entry is never read again. out is bounded,
// so a brain that is behind slows the reading, never grows a queue.
func (s Streams) Run(ctx context.Context, out chan<- Event, counts *Counts, log *slog.Logger) {
	// First what was delivered to this consumer and never acknowledged (a
	// brain that stopped between the two), until none is left; then what is new.
	cursor := "0"
	for ctx.Err() == nil {
		streams := make([]string, 0, 2*len(s.Names))
		streams = append(streams, s.Names...)
		for range s.Names {
			streams = append(streams, cursor)
		}
		args := &redis.XReadGroupArgs{Group: s.Group, Consumer: s.Consumer, Streams: streams, Count: 64, Block: time.Second}
		if cursor != ">" {
			args.Block = -1 // the pending history answers at once: no BLOCK
		}
		res, err := s.Client.XReadGroup(ctx, args).Result()
		if errors.Is(err, redis.Nil) {
			cursor = ">"
			continue
		}
		if err == nil && cursor != ">" && pendingEmpty(res) {
			cursor = ">"
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("streams: read failed, trying again", "err", err.Error())
			if strings.HasPrefix(err.Error(), "NOGROUP") {
				_ = s.Ensure(ctx)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		for _, st := range res {
			for _, msg := range st.Messages {
				counts.Read.Add(1)
				ev, err := ParseEvent(msg.Values, time.Now())
				if err != nil {
					counts.Malformed.Add(1)
					log.Warn("streams: entry dropped", "stream", st.Stream, "entry", msg.ID, "err", err.Error())
				} else {
					select {
					case out <- ev:
					case <-ctx.Done():
						return
					}
				}
				// Acknowledged even as the brain stops: the event was taken.
				actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
				err = s.Client.XAck(actx, st.Stream, s.Group, msg.ID).Err()
				cancel()
				if err != nil {
					log.Warn("streams: ack failed", "stream", st.Stream, "entry", msg.ID, "err", err.Error())
				}
			}
		}
	}
}
