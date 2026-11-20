// Package driver is a model holding a subject's reins: woken by what others
// say in a thread its subject is in, it reads the thread back, has the mind
// answer, and says the answer there as the subject. While the answer is
// being written its pieces go on the bus, never into the journal.
package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"

	sharedevents "github.com/jurasikasan/units/shared/events"
)

const (
	// ThreadsStream is where threads announces each append.
	ThreadsStream = "threads_events"
	// Stream is where the driver says what it is writing: ephemeral, capped.
	Stream = "driver_events"
	Source = "/drivers/model"

	// ReplyDelta: a piece of a reply being written (part text or reasoning).
	ReplyDelta = "driver.reply.delta"
	// ReplyFailed: the reply ended with nothing said, and why.
	ReplyFailed = "driver.reply.failed"
	// ReplyFinished: the reply was said, the entry at seq.
	ReplyFinished = "driver.reply.finished"

	// streamMax is about how many events driver_events keeps: they matter
	// only while a reply is written.
	streamMax = 10000
)

// Hold is one subject the driver holds the reins of, and the model it
// thinks with.
type Hold struct {
	Subject Subject
	Model   string
}

// ParseHolds reads HOLDS: `<subject nick or id>=<chat model>,…`.
func ParseHolds(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		who, model, ok := strings.Cut(item, "=")
		who, model = strings.TrimSpace(who), strings.TrimSpace(model)
		if !ok || who == "" || model == "" {
			return nil, fmt.Errorf("HOLDS: %q is not <subject>=<model>", item)
		}
		out[who] = model
	}
	if len(out) == 0 {
		return nil, errors.New("HOLDS names no subject")
	}
	return out, nil
}

// Publisher is where pieces of a reply go.
type Publisher interface {
	Publish(ctx context.Context, e sharedevents.Event) error
}

// RedisPublisher writes to driver_events, capped: what is being written is
// worth nothing once it is said.
type RedisPublisher struct{ Client *redis.Client }

func (p RedisPublisher) Publish(ctx context.Context, e sharedevents.Event) error {
	f, err := sharedevents.Fields(Source, e)
	if err != nil {
		return err
	}
	return p.Client.XAdd(ctx, &redis.XAddArgs{Stream: Stream, Values: f, MaxLen: streamMax, Approx: true}).Err()
}

// Driver holds subjects' reins with models.
type Driver struct {
	Journal Journal
	Names   Registry
	Mind    Mind
	Bus     Publisher
	// Tail is how many of a thread's last entries the prompt is built from.
	Tail int
	// Catchup: on taking a subject's reins, what was said to it this long
	// ago or less and is unanswered is answered (it may have been said
	// while the subject was being looked for). Zero: nothing.
	Catchup time.Duration
	Log     *slog.Logger

	mu    sync.Mutex
	holds []Hold
	lanes map[lane]*laneState
}

// Holds is the subjects held now.
func (d *Driver) Holds() []Hold {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.holds)
}

// Take finds each wanted subject (nick or id → model) in subjects and holds
// it, trying again every `every` until all are held or ctx ends: a subject
// may not exist yet (alice-assistant is made at alice's first login).
func (d *Driver) Take(ctx context.Context, wanted map[string]string, every time.Duration) {
	left := maps.Clone(wanted)
	for {
		for who, model := range left {
			sub, err := d.Names.Find(ctx, who)
			if err != nil {
				d.Log.Info("not holding yet", "subject", who, "why", err)
				continue
			}
			d.mu.Lock()
			d.holds = append(d.holds, Hold{Subject: sub, Model: model})
			d.mu.Unlock()
			delete(left, who)
			d.Log.Info("holding the reins", "subject", sub.Nick, "id", sub.ID, "model", model)
			d.catchUp(ctx, sub.ID)
		}
		if len(left) == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// lane is one subject in one thread: its answers come one at a time.
type lane struct{ subject, thread uuid.UUID }

type laneState struct {
	again bool
	done  []func() // called once the lane has answered what woke it
}

// Woken is one threads.entry.appended: the driver answers when the entry is
// a create, its sender is not the subject, and the subject is a member.
// done is called once whatever it woke has been answered (or found nothing
// to answer), so the wake-up can be acknowledged.
func (d *Driver) Woken(ctx context.Context, thread, sender uuid.UUID, kind string, done func()) {
	if kind != "create" {
		done()
		return
	}
	var woke []lane
	var members []uuid.UUID
	for _, h := range d.Holds() {
		if h.Subject.ID == sender {
			continue
		}
		if members == nil {
			var err error
			members, err = d.Journal.Members(ctx, thread)
			if err != nil {
				d.Log.Warn("who is in the thread", "thread", thread, "err", err)
				return // not acknowledged: tried again on restart
			}
		}
		if slices.Contains(members, h.Subject.ID) {
			woke = append(woke, lane{h.Subject.ID, thread})
		}
	}
	if len(woke) == 0 {
		done()
		return
	}
	var wg sync.WaitGroup
	wg.Add(len(woke))
	go func() { wg.Wait(); done() }()
	for _, l := range woke {
		d.wake(ctx, l, wg.Done)
	}
}

// catchUp answers, in each thread the subject is in, what someone else said
// there last within Catchup: a wake-up that came before the subject was
// held was acknowledged with nobody to answer it.
func (d *Driver) catchUp(ctx context.Context, subject uuid.UUID) {
	if d.Catchup <= 0 {
		return
	}
	threads, err := d.Journal.Threads(ctx, subject)
	if err != nil {
		d.Log.Warn("catch up", "err", err)
		return
	}
	for _, th := range threads {
		tail, err := d.Journal.Tail(ctx, th, 1)
		if err != nil || len(tail) == 0 {
			continue
		}
		last := tail[0]
		if last.Kind == "create" && last.Sender != subject && time.Since(last.At) <= d.Catchup {
			d.wake(ctx, lane{subject, th}, func() {})
		}
	}
}

// wake answers in a lane, or, while it is answering, has it look again once
// it is done: whatever came meanwhile is in the tail it reads then.
func (d *Driver) wake(ctx context.Context, l lane, done func()) {
	d.mu.Lock()
	if d.lanes == nil {
		d.lanes = map[lane]*laneState{}
	}
	if st, busy := d.lanes[l]; busy {
		st.again = true
		st.done = append(st.done, done)
		d.mu.Unlock()
		return
	}
	st := &laneState{done: []func(){done}}
	d.lanes[l] = st
	d.mu.Unlock()
	go func() {
		for {
			d.Answer(ctx, l.subject, l.thread)
			d.mu.Lock()
			if st.again {
				st.again = false
				d.mu.Unlock()
				continue
			}
			delete(d.lanes, l)
			finished := st.done
			d.mu.Unlock()
			for _, f := range finished {
				f()
			}
			return
		}
	}()
}

func (d *Driver) hold(subject uuid.UUID) (Hold, bool) {
	for _, h := range d.Holds() {
		if h.Subject.ID == subject {
			return h, true
		}
	}
	return Hold{}, false
}

// Answer reads the thread's tail and, when the last thing said there is
// not the subject's own, has the mind answer and says the answer as the
// subject. Its pieces go on the bus as they come; a failure is said there
// and logged, never journaled: the journal holds what was said.
func (d *Driver) Answer(ctx context.Context, subject, thread uuid.UUID) {
	h, ok := d.hold(subject)
	if !ok {
		return
	}
	tail, err := d.Journal.Tail(ctx, thread, d.Tail)
	if err != nil {
		d.Log.Warn("read the thread", "thread", thread, "err", err)
		return
	}
	messages, ok := Prompt(ctx, subject, tail, d.Names)
	if !ok {
		return
	}
	reply := uuid.NewString()
	base := map[string]any{"thread": thread, "sender": subject, "reply": reply}
	with := func(more map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range more {
			out[k] = v
		}
		return out
	}
	publish := func(typ string, data map[string]any) {
		if err := d.Bus.Publish(ctx, sharedevents.Event{Type: typ, Subject: thread.String(), Data: data}); err != nil {
			d.Log.Warn("publish", "type", typ, "err", err)
		}
	}
	start := time.Now()
	words, err := d.Mind.Run(ctx, thread.String(), reply, messages, h.Model, func(part, text string) {
		publish(ReplyDelta, with(map[string]any{"part": part, "text": text}))
	})
	if err == nil && len(words) == 0 {
		err = &RunError{Code: "said_nothing", Message: "The model said nothing."}
	}
	if err != nil {
		var re *RunError
		code, msg := "", err.Error()
		if errors.As(err, &re) {
			code, msg = re.Code, re.Message
		}
		d.Log.Warn("reply failed", "subject", h.Subject.Nick, "thread", thread, "model", h.Model, "code", code, "err", msg)
		publish(ReplyFailed, with(map[string]any{"code": code, "message": msg}))
		return
	}
	content := make([]Block, 0, len(words))
	for _, w := range words {
		content = append(content, Block{Type: "text", Text: w})
	}
	e, err := d.Journal.Say(ctx, thread, subject, content)
	if err != nil {
		d.Log.Warn("say the reply", "subject", h.Subject.Nick, "thread", thread, "err", err)
		publish(ReplyFailed, with(map[string]any{"code": "not_said", "message": "The reply could not be said: " + err.Error()}))
		return
	}
	d.Log.Info("replied", "subject", h.Subject.Nick, "thread", thread, "model", h.Model, "seq", e.Seq, "context_entries", len(messages), "took", time.Since(start).Round(time.Millisecond))
	publish(ReplyFinished, with(map[string]any{"seq": e.Seq}))
}

// wakeup is threads.entry.appended's data.
type wakeup struct {
	Thread uuid.UUID `json:"thread"`
	Seq    int64     `json:"seq"`
	Sender uuid.UUID `json:"sender"`
	Kind   string    `json:"kind"`
}

// Follow reads threads_events through the consumer group, from what it was
// handed before and never acknowledged (a restart mid-answer), then on.
// Each wake-up is acknowledged once what it woke is answered; answering is
// idempotent (nothing is said when the last word is the subject's own), so
// a wake-up handled twice costs a read.
func (d *Driver) Follow(ctx context.Context, rdb *redis.Client, group, consumer string) error {
	err := rdb.XGroupCreateMkStream(ctx, ThreadsStream, group, "$").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("consumer group %s: %w", group, err)
	}
	ack := func(id string) func() {
		return func() {
			if err := rdb.XAck(context.Background(), ThreadsStream, group, id).Err(); err != nil {
				d.Log.Warn("acknowledge", "id", id, "err", err)
			}
		}
	}
	handle := func(m redis.XMessage) {
		if t, _ := m.Values["ce_type"].(string); t != "threads.entry.appended" {
			ack(m.ID)()
			return
		}
		var w wakeup
		data, _ := m.Values["data"].(string)
		if json.Unmarshal([]byte(data), &w) != nil || w.Thread == uuid.Nil {
			ack(m.ID)()
			return
		}
		d.Woken(ctx, w.Thread, w.Sender, w.Kind, ack(m.ID))
	}
	// Handed before and not acknowledged: woken again.
	pending, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: group, Consumer: consumer, Streams: []string{ThreadsStream, "0"}, Count: 1000}).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("read pending: %w", err)
	}
	for _, s := range pending {
		for _, m := range s.Messages {
			handle(m)
		}
	}
	for ctx.Err() == nil {
		res, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: group, Consumer: consumer, Streams: []string{ThreadsStream, ">"}, Count: 100, Block: 5 * time.Second}).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if strings.Contains(err.Error(), "NOGROUP") {
				// The bus came back empty: the group with it is gone.
				if err := rdb.XGroupCreateMkStream(ctx, ThreadsStream, group, "$").Err(); err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
					d.Log.Warn("consumer group again", "err", err)
				}
				continue
			}
			d.Log.Warn("read the bus", "err", err)
			time.Sleep(time.Second)
			continue
		}
		for _, s := range res {
			for _, m := range s.Messages {
				handle(m)
			}
		}
	}
	return ctx.Err()
}
