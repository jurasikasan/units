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
	// ReinsStream is where reins announces who holds what, and stops.
	ReinsStream = "reins_events"
	// Stream is where the driver says what it is writing: ephemeral, capped.
	Stream = "driver_events"
	Source = "/drivers/model"

	// ReplyStarted: a reply is being written; nothing of it yet.
	ReplyStarted = "driver.reply.started"
	// ReplyDelta: a piece of a reply being written (part text or reasoning).
	ReplyDelta = "driver.reply.delta"
	// ReplyFailed: the reply ended with nothing said, and why.
	ReplyFailed = "driver.reply.failed"
	// ReplyFinished: the reply was said, the entry at seq.
	ReplyFinished = "driver.reply.finished"
	// ReplyStopped: the reply was stopped (a steward asked, or the reins
	// were taken); what was written so far was said at seq when there was
	// any text and the reins are still held.
	ReplyStopped = "driver.reply.stopped"

	// reins' events the driver reads.
	holdChanged = "reins.hold.changed"
	replyStop   = "reins.reply.stop"

	// streamMax is about how many events driver_events keeps: they matter
	// only while a reply is written.
	streamMax = 10000
)

// Hold is one subject the driver holds the reins of: the profile that
// moved into it, and the chat model it thinks with.
type Hold struct {
	Subject Subject
	Profile string
	Model   string
}

// Profile is one brain the driver can be: a name, and what it thinks
// with. Each carries only its chat model now; per-context choices (a
// realtime model in a call, vision or text only, agent work) are room
// left in the shape.
type Profile struct {
	Name  string `json:"name"`
	Model string `json:"model"`
}

// ParseProfiles reads PROFILES: `<name>=<chat model>,…`, in order.
func ParseProfiles(s string) ([]Profile, error) {
	var out []Profile
	seen := map[string]bool{}
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name, model, ok := strings.Cut(item, "=")
		name, model = strings.TrimSpace(name), strings.TrimSpace(model)
		if !ok || name == "" || model == "" {
			return nil, fmt.Errorf("PROFILES: %q is not <name>=<chat model>", item)
		}
		if seen[name] {
			return nil, fmt.Errorf("PROFILES: %q twice", name)
		}
		seen[name] = true
		out = append(out, Profile{Name: name, Model: model})
	}
	if len(out) == 0 {
		return nil, errors.New("PROFILES names no profile")
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

// Driver holds subjects' reins with its profiles, as reins says.
type Driver struct {
	Journal Journal
	Names   Registry
	Mind    Mind
	Bus     Publisher
	// Reins says which subjects the brains hold (its inner GET /holds).
	Reins Reins
	// Profiles are the brains it can be.
	Profiles []Profile
	// Tail is how many of a thread's last entries the prompt is built from.
	Tail int
	// Catchup: on taking a subject's reins, what was said to it this long
	// ago or less and is unanswered is answered (it may have been said
	// while nobody held it). Zero: nothing.
	Catchup time.Duration
	Log     *slog.Logger

	syncing sync.Mutex
	mu      sync.Mutex
	holds   []Hold
	lanes   map[lane]*laneState
}

// Holds is the subjects held now.
func (d *Driver) Holds() []Hold {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.holds)
}

func (d *Driver) profile(name string) (Profile, bool) {
	for _, p := range d.Profiles {
		if p.Name == name {
			return p, true
		}
	}
	return Profile{}, false
}

// Sync reads from reins which subjects the brains hold, and holds those
// of its own profiles: a subject gained is caught up with (what was said
// to it lately and is unanswered, a greeting where it opened a thread
// and nothing is said); one lost is let go, and a reply being written as
// it is stopped, nothing said.
func (d *Driver) Sync(ctx context.Context) error {
	d.syncing.Lock()
	defer d.syncing.Unlock()
	hs, err := d.Reins.BrainHolds(ctx)
	if err != nil {
		return err
	}
	current := d.Holds()
	var next []Hold
	var gained []uuid.UUID
	for _, h := range hs {
		p, ok := d.profile(h.Profile)
		if !ok {
			continue // another driver's profile
		}
		i := slices.IndexFunc(current, func(c Hold) bool { return c.Subject.ID == h.Subject })
		if i >= 0 {
			held := current[i]
			if held.Profile != p.Name {
				d.Log.Info("another profile in it", "subject", held.Subject.Nick, "profile", p.Name, "model", p.Model)
			}
			held.Profile, held.Model = p.Name, p.Model
			next = append(next, held)
			continue
		}
		sub, err := d.Names.Find(ctx, h.Subject.String())
		if err != nil {
			d.Log.Warn("a held subject not found yet", "subject", h.Subject, "err", err)
			continue
		}
		next = append(next, Hold{Subject: sub, Profile: p.Name, Model: p.Model})
		gained = append(gained, sub.ID)
		d.Log.Info("holding the reins", "subject", sub.Nick, "id", sub.ID, "profile", p.Name, "model", p.Model)
	}
	d.mu.Lock()
	d.holds = next
	d.mu.Unlock()
	for _, c := range current {
		if !slices.ContainsFunc(next, func(n Hold) bool { return n.Subject.ID == c.Subject.ID }) {
			d.Log.Info("the reins taken", "subject", c.Subject.Nick, "id", c.Subject.ID)
			d.cancel(func(l lane) bool { return l.subject == c.Subject.ID }, stopTaken)
		}
	}
	for _, id := range gained {
		d.arrive(ctx, id)
	}
	return nil
}

// lane is one subject in one thread: its answers come one at a time.
type lane struct{ subject, thread uuid.UUID }

// Why a run was cancelled.
const (
	stopAsked = "stopped" // a steward asked: what was written is said
	stopTaken = "taken"   // the reins went to another: nothing is said
)

type laneState struct {
	again  bool
	done   []func() // called once the lane has answered what woke it
	cancel context.CancelFunc
	why    string
}

// cancel stops the runs of every lane that matches, for why.
func (d *Driver) cancel(match func(lane) bool, why string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for l, st := range d.lanes {
		if !match(l) || st.cancel == nil {
			continue
		}
		st.why, st.again = why, false
		st.cancel()
		n++
	}
	return n
}

// Stop stops the reply the subject is writing in the thread: what was
// written so far is said, when there is any text.
func (d *Driver) Stop(thread, subject uuid.UUID) {
	if d.cancel(func(l lane) bool { return l == lane{subject, thread} }, stopAsked) > 0 {
		d.Log.Info("stopped", "subject", subject, "thread", thread)
	}
}

// Woken is one threads.entry.appended. A create by someone else wakes each
// subject it holds that is a member: it answers. A join by someone else
// wakes it only where nothing is said yet: it greets, in a thread its
// subject opened. done is called once whatever it woke has been answered
// (or found nothing to answer), so the wake-up can be acknowledged.
func (d *Driver) Woken(ctx context.Context, thread, sender uuid.UUID, kind string, done func()) {
	if kind != "create" && kind != "join" {
		done()
		return
	}
	var woke []lane
	var th *ThreadInfo
	for _, h := range d.Holds() {
		if h.Subject.ID == sender {
			continue
		}
		if th == nil {
			info, err := d.Journal.Thread(ctx, thread)
			if err != nil {
				d.Log.Warn("who is in the thread", "thread", thread, "err", err)
				return // not acknowledged: tried again on restart
			}
			th = &info
		}
		if !slices.Contains(th.Members, h.Subject.ID) {
			continue
		}
		if kind == "join" && !d.toGreet(ctx, h.Subject.ID, thread, *th) {
			continue
		}
		woke = append(woke, lane{h.Subject.ID, thread})
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

// toGreet is whether the subject opened the thread and nothing is said
// there yet.
func (d *Driver) toGreet(ctx context.Context, subject, thread uuid.UUID, th ThreadInfo) bool {
	if th.OpenedBy == nil || *th.OpenedBy != subject {
		return false
	}
	tail, err := d.Journal.Tail(ctx, thread, d.Tail)
	if err != nil {
		d.Log.Warn("read the thread", "thread", thread, "err", err)
		return false
	}
	return nothingSaid(tail, d.Tail)
}

// nothingSaid: no create in a tail that is the whole thread.
func nothingSaid(tail []Entry, limit int) bool {
	if len(tail) >= limit {
		return false
	}
	return !slices.ContainsFunc(tail, func(e Entry) bool { return e.Kind == "create" })
}

// arrive is a subject just taken: in each thread it is in, what someone
// else said there last within Catchup is answered (said while nobody held
// it, or before this driver came), and where it opened the thread and
// nothing is said, it greets.
func (d *Driver) arrive(ctx context.Context, subject uuid.UUID) {
	threads, err := d.Journal.Threads(ctx, subject)
	if err != nil {
		d.Log.Warn("catch up", "err", err)
		return
	}
	for _, th := range threads {
		tail, err := d.Journal.Tail(ctx, th.ID, d.Tail)
		if err != nil || len(tail) == 0 {
			continue
		}
		if th.OpenedBy != nil && *th.OpenedBy == subject && nothingSaid(tail, d.Tail) {
			d.wake(ctx, lane{subject, th.ID}, func() {})
			continue
		}
		if d.Catchup <= 0 {
			continue
		}
		last := tail[len(tail)-1]
		if last.Kind == "create" && last.Sender != subject && time.Since(last.At) <= d.Catchup {
			d.wake(ctx, lane{subject, th.ID}, func() {})
		}
	}
}

// wake answers in a lane, or, while it is answering, has it look again once
// it is done: whatever came meanwhile is in the tail it reads then. Each
// run can be cancelled: Stop, or the reins taken.
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
			run, cancel := context.WithCancel(ctx)
			d.mu.Lock()
			st.cancel, st.why = cancel, ""
			d.mu.Unlock()
			d.answer(run, l.subject, l.thread, func() string {
				d.mu.Lock()
				defer d.mu.Unlock()
				return st.why
			})
			cancel()
			d.mu.Lock()
			st.cancel = nil
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
// subject; where the subject opened the thread and nothing is said yet,
// it greets. Its pieces go on the bus as they come; a failure is said
// there and logged, never journaled: the journal holds what was said.
func (d *Driver) Answer(ctx context.Context, subject, thread uuid.UUID) {
	d.answer(ctx, subject, thread, func() string { return "" })
}

func (d *Driver) answer(ctx context.Context, subject, thread uuid.UUID, why func() string) {
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
	if !ok && nothingSaid(tail, d.Tail) {
		th, err := d.Journal.Thread(ctx, thread)
		if err != nil {
			d.Log.Warn("who is in the thread", "thread", thread, "err", err)
			return
		}
		if th.OpenedBy != nil && *th.OpenedBy == subject {
			messages, ok = Greeting(ctx, subject, th.Members, d.Names), true
		}
	}
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
	// Published even when the run's context is gone: a stopped reply says so.
	publish := func(typ string, data map[string]any) {
		if err := d.Bus.Publish(context.WithoutCancel(ctx), sharedevents.Event{Type: typ, Subject: thread.String(), Data: data}); err != nil {
			d.Log.Warn("publish", "type", typ, "err", err)
		}
	}
	start := time.Now()
	publish(ReplyStarted, with(nil))
	var written strings.Builder
	words, err := d.Mind.Run(ctx, thread.String(), reply, messages, h.Model, func(part, text string) {
		if part == "text" {
			written.WriteString(text)
		}
		publish(ReplyDelta, with(map[string]any{"part": part, "text": text}))
	})
	if ctx.Err() != nil {
		d.stopped(context.WithoutCancel(ctx), h, thread, why(), written.String(), with, publish)
		return
	}
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
	if _, still := d.hold(subject); !still {
		d.Log.Info("the reins taken while it wrote: nothing said", "subject", h.Subject.Nick, "thread", thread)
		publish(ReplyStopped, with(map[string]any{"why": stopTaken}))
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

// stopped ends a cancelled run. Asked to stop, what was written so far is
// said as it stands, when there is any text (those watching saw it: a
// sentence cut off), and nothing when there is none. Taken, nothing is
// said: the subject is another's now. A run cut by shutdown says nothing.
func (d *Driver) stopped(ctx context.Context, h Hold, thread uuid.UUID, why, written string, with func(map[string]any) map[string]any, publish func(string, map[string]any)) {
	switch why {
	case stopAsked:
		if _, still := d.hold(h.Subject.ID); still && strings.TrimSpace(written) != "" {
			e, err := d.Journal.Say(ctx, thread, h.Subject.ID, []Block{{Type: "text", Text: written}})
			if err != nil {
				d.Log.Warn("say what was written", "subject", h.Subject.Nick, "thread", thread, "err", err)
				publish(ReplyFailed, with(map[string]any{"code": "not_said", "message": "The reply could not be said: " + err.Error()}))
				return
			}
			d.Log.Info("stopped, said as far as it went", "subject", h.Subject.Nick, "thread", thread, "seq", e.Seq)
			publish(ReplyStopped, with(map[string]any{"why": why, "seq": e.Seq}))
			return
		}
		d.Log.Info("stopped, nothing written", "subject", h.Subject.Nick, "thread", thread)
		publish(ReplyStopped, with(map[string]any{"why": why}))
	case stopTaken:
		publish(ReplyStopped, with(map[string]any{"why": why}))
	}
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

// FollowReins keeps the holds as reins says them: read at boot, again on
// each reins.hold.changed on reins_events (the event only wakes), and
// every `every` in case one was missed. A reins.reply.stop for a subject
// it holds stops the reply there. Read from where the stream was when it
// started: each driver sees every event, none is acknowledged.
func (d *Driver) FollowReins(ctx context.Context, rdb *redis.Client, every time.Duration) {
	last := "0-0"
	if ms, err := rdb.XRevRangeN(ctx, ReinsStream, "+", "-", 1).Result(); err == nil && len(ms) > 0 {
		last = ms[0].ID
	}
	sync := func() {
		if err := d.Sync(ctx); err != nil {
			d.Log.Warn("whom it holds", "err", err)
		}
	}
	sync()
	synced := time.Now()
	for ctx.Err() == nil {
		res, err := rdb.XRead(ctx, &redis.XReadArgs{Streams: []string{ReinsStream, last}, Count: 100, Block: 5 * time.Second}).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			if ctx.Err() != nil {
				return
			}
			d.Log.Warn("read reins' events", "err", err)
			time.Sleep(time.Second)
		}
		changed := false
		for _, s := range res {
			for _, m := range s.Messages {
				last = m.ID
				typ, _ := m.Values["ce_type"].(string)
				data, _ := m.Values["data"].(string)
				switch typ {
				case holdChanged:
					changed = true
				case replyStop:
					var stop struct {
						Thread  uuid.UUID `json:"thread"`
						Subject uuid.UUID `json:"subject"`
					}
					if json.Unmarshal([]byte(data), &stop) == nil {
						d.Stop(stop.Thread, stop.Subject)
					}
				}
			}
		}
		if changed || time.Since(synced) > every {
			sync()
			synced = time.Now()
		}
	}
}
