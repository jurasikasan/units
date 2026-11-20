package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sharedevents "github.com/jurasikasan/units/shared/events"
)

var (
	helper = uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	alice  = uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	bob    = uuid.MustParse("00000000-0000-0000-0000-00000000000b")
)

type names map[uuid.UUID]string

func (n names) Find(_ context.Context, who string) (Subject, error) {
	for id, nick := range n {
		if nick == who || id.String() == who {
			return Subject{ID: id, Nick: nick}, nil
		}
	}
	return Subject{}, fmt.Errorf("no subject %q", who)
}
func (n names) Name(_ context.Context, id uuid.UUID) string { return n[id] }

var people = names{helper: "alice-assistant", alice: "alice", bob: "bob"}

func text(sender uuid.UUID, seq int64, words string) Entry {
	return Entry{ID: uuid.New(), Seq: seq, Sender: sender, Kind: "create", Content: []Block{{Type: "text", Text: words}}}
}

func TestPrompt_RolesAreComputed(t *testing.T) {
	tail := []Entry{
		{Seq: 1, Sender: alice, Kind: "join"},
		{Seq: 2, Sender: helper, Kind: "join"},
		text(alice, 3, "hi"),
		text(helper, 4, "hello, alice"),
		text(bob, 5, "and me"),
		{ID: uuid.New(), Seq: 6, Sender: alice, Kind: "create", Content: []Block{
			{Type: "image", URL: "data:image/png;base64,AAAA", MediaType: "image/png", SHA256: strings.Repeat("a", 64)},
			{Type: "text", Text: "<attachment name=\"n.txt\">\nZ\n</attachment>"},
			{Type: "file", URL: "data:application/pdf;base64,JVBE", MediaType: "application/pdf", SHA256: strings.Repeat("b", 64), Name: "d.pdf"},
			{Type: "text", Text: "look"},
		}},
	}
	msgs, ok := Prompt(context.Background(), helper, tail, people)
	require.True(t, ok)
	require.Len(t, msgs, 4, "joins are not said")
	assert.Equal(t, types.RoleUser, msgs[0].Role)
	assert.Equal(t, "alice: hi", msgs[0].Content, "who said it, in the words")
	assert.Equal(t, types.RoleAssistant, msgs[1].Role, "the subject's own")
	assert.Equal(t, "hello, alice", msgs[1].Content)
	assert.Equal(t, "bob: and me", msgs[2].Content)
	parts := msgs[3].Content.([]types.InputContent)
	require.Len(t, parts, 4)
	assert.Equal(t, types.InputContentTypeImage, parts[0].Type)
	assert.Equal(t, &types.InputContentSource{Type: "data", Value: "AAAA", MimeType: "image/png"}, parts[0].Source)
	assert.True(t, strings.HasPrefix(parts[1].Text, "<attachment"), "an attached file's text as it is")
	assert.Equal(t, types.InputContentTypeDocument, parts[2].Type)
	assert.Equal(t, "d.pdf", parts[2].Filename)
	assert.Equal(t, "alice: look", parts[3].Text)

	_, ok = Prompt(context.Background(), helper, append(tail, text(helper, 7, "seen")), people)
	assert.False(t, ok, "the last word is the subject's own: nothing to answer")
	_, ok = Prompt(context.Background(), helper, tail[:2], people)
	assert.False(t, ok, "nothing said yet")
}

// fakeMind answers in AG-UI as minds/assistant does, from a script by the
// last user message.
func fakeMind(t *testing.T, seen chan<- types.RunAgentInput) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Principal-Id") == "" {
			http.Error(w, "who", http.StatusUnauthorized)
			return
		}
		var in types.RunAgentInput
		require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
		if seen != nil {
			seen <- in
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(ev map[string]any) {
			b, _ := json.Marshal(ev)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			w.(http.Flusher).Flush()
		}
		last, _ := in.Messages[len(in.Messages)-1].ContentString()
		send(map[string]any{"type": "RUN_STARTED", "threadId": in.ThreadID, "runId": in.RunID})
		if strings.Contains(last, "#fail") {
			send(map[string]any{"type": "RUN_ERROR", "code": "model_rate_limited", "message": "The model's endpoint answered 429"})
			return
		}
		// #hang: nothing comes until the run is cut; #slow: half a sentence,
		// then nothing.
		if strings.Contains(last, "#slow") {
			send(map[string]any{"type": "TEXT_MESSAGE_START", "messageId": "m1", "role": "assistant"})
			send(map[string]any{"type": "TEXT_MESSAGE_CONTENT", "messageId": "m1", "delta": "Half a sen"})
		}
		if strings.Contains(last, "#slow") || strings.Contains(last, "#hang") {
			<-r.Context().Done()
			return
		}
		send(map[string]any{"type": "REASONING_MESSAGE_CONTENT", "messageId": "r", "delta": "hm"})
		send(map[string]any{"type": "TEXT_MESSAGE_START", "messageId": "m1", "role": "assistant"})
		send(map[string]any{"type": "TEXT_MESSAGE_CONTENT", "messageId": "m1", "delta": "Hel"})
		send(map[string]any{"type": "TEXT_MESSAGE_CONTENT", "messageId": "m1", "delta": "lo."})
		send(map[string]any{"type": "TEXT_MESSAGE_END", "messageId": "m1"})
		send(map[string]any{"type": "TOOL_CALL_START", "toolCallId": "c", "toolCallName": "x"})
		send(map[string]any{"type": "TEXT_MESSAGE_START", "messageId": "m2", "role": "assistant"})
		send(map[string]any{"type": "TEXT_MESSAGE_CONTENT", "messageId": "m2", "delta": "Done."})
		send(map[string]any{"type": "TEXT_MESSAGE_END", "messageId": "m2"})
		send(map[string]any{"type": "RUN_FINISHED", "threadId": in.ThreadID, "runId": in.RunID})
	}))
}

// fakeJournal is one thread, the0, whoever asks.
type fakeJournal struct {
	mu       sync.Mutex
	members  []uuid.UUID
	openedBy *uuid.UUID
	entries  []Entry
}

var the0 = uuid.MustParse("00000000-0000-0000-0000-0000000000cc")

func (j *fakeJournal) Threads(context.Context, uuid.UUID) ([]ThreadInfo, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return []ThreadInfo{{ID: the0, OpenedBy: j.openedBy}}, nil
}
func (j *fakeJournal) Thread(context.Context, uuid.UUID) (ThreadInfo, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return ThreadInfo{ID: the0, OpenedBy: j.openedBy, Members: slices.Clone(j.members)}, nil
}
func (j *fakeJournal) said() []Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.Clone(j.entries)
}
func (j *fakeJournal) Tail(context.Context, uuid.UUID, int) ([]Entry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.Clone(j.entries), nil
}
func (j *fakeJournal) Say(_ context.Context, _, sender uuid.UUID, content []Block) (Entry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	e := Entry{ID: uuid.New(), Seq: int64(len(j.entries) + 1), Sender: sender, Kind: "create", Content: content}
	j.entries = append(j.entries, e)
	return e, nil
}

type fakeBus struct {
	mu     sync.Mutex
	events []sharedevents.Event
}

func (b *fakeBus) Publish(_ context.Context, e sharedevents.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, e)
	return nil
}

func (b *fakeBus) types() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, e := range b.events {
		out = append(out, e.Type)
	}
	return out
}

// fakeReins: which subject each brain profile holds.
type fakeReins struct {
	mu    sync.Mutex
	holds []BrainHold
}

func (r *fakeReins) BrainHolds(context.Context) ([]BrainHold, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.holds), nil
}

func (r *fakeReins) set(hs ...BrainHold) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.holds = hs
}

var profiles = []Profile{{Name: "fake", Model: "fake-1"}, {Name: "glm", Model: "glm-4"}}

func driverOn(j *fakeJournal, mindURL string, catchup time.Duration) (*Driver, *fakeReins, *fakeBus) {
	bus := &fakeBus{}
	reins := &fakeReins{}
	d := &Driver{Journal: j, Names: people, Mind: NewAGUI(mindURL, "drivers/model"), Bus: bus, Reins: reins, Profiles: profiles,
		Tail: 50, Catchup: catchup, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return d, reins, bus
}

// newDriver holds alice-assistant as the fake profile.
func newDriver(t *testing.T, mindURL string) (*Driver, *fakeJournal, *fakeBus) {
	j := &fakeJournal{members: []uuid.UUID{alice, helper}}
	d, reins, bus := driverOn(j, mindURL, 0)
	reins.set(BrainHold{Subject: helper, Profile: "fake"})
	require.NoError(t, d.Sync(context.Background()))
	return d, j, bus
}

func TestAnswer_SaysTheReplyAsTheSubject_PiecesOnTheBus(t *testing.T) {
	seen := make(chan types.RunAgentInput, 1)
	mind := fakeMind(t, seen)
	defer mind.Close()
	d, j, bus := newDriver(t, mind.URL)
	thread := uuid.New()
	j.entries = []Entry{text(alice, 1, "hi")}

	d.Answer(context.Background(), helper, thread)

	in := <-seen
	assert.Equal(t, map[string]any{"modelName": "fake-1"}, in.ForwardedProps, "the model of the profile it holds the subject as")
	assert.Equal(t, thread.String(), in.ThreadID)
	require.Len(t, j.entries, 2)
	said := j.entries[1]
	assert.Equal(t, helper, said.Sender)
	assert.Equal(t, []Block{{Type: "text", Text: "Hello."}, {Type: "text", Text: "Done."}}, said.Content, "each text message a block")
	assert.Equal(t, []string{ReplyStarted, ReplyDelta, ReplyDelta, ReplyDelta, ReplyDelta, ReplyFinished}, bus.types())
	assert.Equal(t, "reasoning", bus.events[1].Data["part"])
	assert.Equal(t, "Hel", bus.events[2].Data["text"])
	assert.Equal(t, thread.String(), bus.events[2].Subject)
	assert.EqualValues(t, 2, bus.events[5].Data["seq"])

	d.Answer(context.Background(), helper, thread)
	assert.Len(t, j.entries, 2, "the last word is its own: nothing more said")
}

func TestAnswer_AFailureIsSaidOnTheBusOnly(t *testing.T) {
	mind := fakeMind(t, nil)
	defer mind.Close()
	d, j, bus := newDriver(t, mind.URL)
	j.entries = []Entry{text(alice, 1, "please #fail")}

	d.Answer(context.Background(), helper, uuid.New())

	assert.Len(t, j.entries, 1, "nothing journaled")
	require.Equal(t, []string{ReplyStarted, ReplyFailed}, bus.types())
	assert.Equal(t, "model_rate_limited", bus.events[1].Data["code"])
	assert.Contains(t, bus.events[1].Data["message"], "429")

	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	d.Mind = NewAGUI(gone.URL, "drivers/model")
	d.Answer(context.Background(), helper, uuid.New())
	assert.Equal(t, "mind_unreachable", bus.events[3].Data["code"])
}

func TestWoken_OnlyBySomeoneElse_InItsThreads(t *testing.T) {
	mind := fakeMind(t, nil)
	defer mind.Close()
	d, j, _ := newDriver(t, mind.URL)
	j.entries = []Entry{text(alice, 1, "hi")}
	wait := func(sender uuid.UUID, kind string) {
		done := make(chan struct{})
		d.Woken(context.Background(), uuid.New(), sender, kind, func() { close(done) })
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("never done")
		}
	}
	wait(helper, "create")
	assert.Len(t, j.entries, 1, "its own words wake nothing")
	wait(alice, "join")
	assert.Len(t, j.entries, 1, "a join is nothing to answer where something is said")
	j.members = []uuid.UUID{alice}
	wait(alice, "create")
	assert.Len(t, j.entries, 1, "not its thread")
	j.members = []uuid.UUID{alice, helper}
	wait(alice, "create")
	assert.Len(t, j.entries, 2, "answered")
}

func TestSync_CatchesUpWhatWasSaidMeanwhile(t *testing.T) {
	mind := fakeMind(t, nil)
	defer mind.Close()
	j := &fakeJournal{members: []uuid.UUID{alice, helper}}
	j.entries = []Entry{text(alice, 1, "said before it held the reins")}
	j.entries[0].At = time.Now()
	d, reins, _ := driverOn(j, mind.URL, time.Minute)
	require.NoError(t, d.Sync(context.Background()))
	assert.Empty(t, d.Holds(), "reins says it holds nobody")
	reins.set(BrainHold{Subject: helper, Profile: "fake"}, BrainHold{Subject: bob, Profile: "another-drivers"})
	require.NoError(t, d.Sync(context.Background()))
	require.Len(t, d.Holds(), 1, "only its own profiles")
	assert.Equal(t, "fake-1", d.Holds()[0].Model)
	require.Eventually(t, func() bool { return len(j.said()) == 2 }, 5*time.Second, 10*time.Millisecond, "answered on taking the reins")

	old := &fakeJournal{members: []uuid.UUID{alice, helper}, entries: []Entry{text(alice, 1, "long ago")}}
	old.entries[0].At = time.Now().Add(-time.Hour)
	d2, reins2, _ := driverOn(old, mind.URL, time.Minute)
	reins2.set(BrainHold{Subject: helper, Profile: "glm"})
	require.NoError(t, d2.Sync(context.Background()))
	time.Sleep(200 * time.Millisecond)
	assert.Len(t, old.said(), 1, "an old word is left as it is")
}

// In a thread its subject opened where nothing is said, it greets: on
// taking the reins, and on a join; once.
func TestGreets_WhereItOpenedAndNothingIsSaid(t *testing.T) {
	seen := make(chan types.RunAgentInput, 4)
	mind := fakeMind(t, seen)
	defer mind.Close()
	j := &fakeJournal{members: []uuid.UUID{helper, alice}, openedBy: &helper,
		entries: []Entry{{Seq: 1, Sender: helper, Kind: "join"}, {Seq: 2, Sender: alice, Kind: "join"}}}
	d, reins, _ := driverOn(j, mind.URL, time.Minute)
	reins.set(BrainHold{Subject: helper, Profile: "fake"})
	require.NoError(t, d.Sync(context.Background()))
	require.Eventually(t, func() bool { return len(j.said()) == 3 }, 5*time.Second, 10*time.Millisecond, "greeted")
	in := <-seen
	cue, _ := in.Messages[0].ContentString()
	assert.Contains(t, cue, "alice is here")
	assert.Equal(t, helper, j.said()[2].Sender)

	done := make(chan struct{})
	d.Woken(context.Background(), the0, alice, "join", func() { close(done) })
	<-done
	assert.Len(t, j.said(), 3, "said once: something is said now")

	// Not where another opened it.
	other := &fakeJournal{members: []uuid.UUID{helper, alice}, openedBy: &alice,
		entries: []Entry{{Seq: 1, Sender: alice, Kind: "join"}, {Seq: 2, Sender: helper, Kind: "join"}}}
	d2, reins2, _ := driverOn(other, mind.URL, time.Minute)
	reins2.set(BrainHold{Subject: helper, Profile: "fake"})
	require.NoError(t, d2.Sync(context.Background()))
	done = make(chan struct{})
	d2.Woken(context.Background(), the0, alice, "join", func() { close(done) })
	<-done
	time.Sleep(100 * time.Millisecond)
	assert.Len(t, other.said(), 2)
}

// Stopped, what was written is said as it stands; nothing written,
// nothing said. Taken back while it writes, nothing is said.
func TestStop_SaysWhatWasWritten(t *testing.T) {
	mind := fakeMind(t, nil)
	defer mind.Close()
	d, j, bus := newDriver(t, mind.URL)
	run := func(words string, cut func()) {
		j.mu.Lock()
		j.entries = append(j.entries, text(alice, int64(len(j.entries)+1), words))
		j.mu.Unlock()
		done := make(chan struct{})
		d.Woken(context.Background(), the0, alice, "create", func() { close(done) })
		require.Eventually(t, func() bool {
			for _, ty := range bus.types() {
				if ty == ReplyStarted {
					return true
				}
			}
			return false
		}, 5*time.Second, 10*time.Millisecond)
		time.Sleep(200 * time.Millisecond)
		cut()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the run was not cut")
		}
	}

	run("go on #slow", func() { d.Stop(the0, helper) })
	said := j.said()
	require.Len(t, said, 2)
	assert.Equal(t, helper, said[1].Sender)
	assert.Equal(t, []Block{{Type: "text", Text: "Half a sen"}}, said[1].Content, "as far as it went")
	last := bus.events[len(bus.events)-1]
	assert.Equal(t, ReplyStopped, last.Type)
	assert.EqualValues(t, 2, last.Data["seq"])

	bus.mu.Lock()
	bus.events = nil
	bus.mu.Unlock()
	run("wait #hang", func() { d.Stop(the0, helper) })
	assert.Len(t, j.said(), 3, "nothing written: nothing said")
	assert.Equal(t, []string{ReplyStarted, ReplyStopped}, bus.types())

	reins := d.Reins.(*fakeReins)
	run("again #slow", func() {
		reins.set()
		require.NoError(t, d.Sync(context.Background()))
	})
	assert.Len(t, j.said(), 4, "taken back: nothing said as it")
	assert.Empty(t, d.Holds())
}

func TestParseProfiles(t *testing.T) {
	p, err := ParseProfiles(" fake=fake , glm=glm-4.6")
	require.NoError(t, err)
	assert.Equal(t, []Profile{{"fake", "fake"}, {"glm", "glm-4.6"}}, p)
	for _, bad := range []string{"fake", "", "a=b,a=c"} {
		_, err = ParseProfiles(bad)
		assert.Error(t, err, bad)
	}
}

// Follow on a real Redis: a wake-up on threads_events is answered and
// acknowledged; one not acknowledged before a restart is answered after.
func TestFollow_Live(t *testing.T) {
	addr := os.Getenv("DRIVER_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("DRIVER_TEST_REDIS_ADDR not set; check.sh starts a Redis")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	require.NoError(t, rdb.Del(ctx, ThreadsStream, Stream).Err())

	mind := fakeMind(t, nil)
	defer mind.Close()
	d, j, _ := newDriver(t, mind.URL)
	d.Bus = RedisPublisher{Client: rdb}
	thread := uuid.New()
	announce := func(e Entry) {
		data, _ := json.Marshal(map[string]any{"thread": thread, "seq": e.Seq, "sender": e.Sender, "kind": e.Kind})
		require.NoError(t, rdb.XAdd(ctx, &redis.XAddArgs{Stream: ThreadsStream, Values: map[string]any{
			"ce_type": "threads.entry.appended", "data": string(data)}}).Err())
	}
	go func() { _ = d.Follow(ctx, rdb, "drivers-model", "test") }()
	require.Eventually(t, func() bool {
		_, err := rdb.XPending(ctx, ThreadsStream, "drivers-model").Result()
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "the group made")

	e := text(alice, 1, "hi")
	j.Say(ctx, thread, alice, e.Content) //nolint:errcheck
	announce(e)
	require.Eventually(t, func() bool { return len(j.said()) == 2 }, 5*time.Second, 20*time.Millisecond, "answered")
	require.Eventually(t, func() bool {
		p, err := rdb.XPending(ctx, ThreadsStream, "drivers-model").Result()
		return err == nil && p.Count == 0
	}, 5*time.Second, 20*time.Millisecond, "acknowledged")
	pieces, err := rdb.XRange(ctx, Stream, "-", "+").Result()
	require.NoError(t, err)
	assert.Equal(t, "driver.reply.finished", pieces[len(pieces)-1].Values["ce_type"])
	assert.Equal(t, "/drivers/model", pieces[0].Values["ce_source"])
}

// FollowReins on a real Redis: a reins.hold.changed has it read the holds
// again; a reins.reply.stop stops the reply there.
func TestFollowReins_Live(t *testing.T) {
	addr := os.Getenv("DRIVER_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("DRIVER_TEST_REDIS_ADDR not set; check.sh starts a Redis")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	require.NoError(t, rdb.Del(ctx, ReinsStream).Err())
	mind := fakeMind(t, nil)
	defer mind.Close()
	j := &fakeJournal{members: []uuid.UUID{alice, helper}, entries: []Entry{text(alice, 1, "go on #slow")}}
	d, reins, bus := driverOn(j, mind.URL, 0)
	go d.FollowReins(ctx, rdb, time.Hour)
	reinsSays := func(typ string, data map[string]any) {
		b, _ := json.Marshal(data)
		require.NoError(t, rdb.XAdd(ctx, &redis.XAddArgs{Stream: ReinsStream, Values: map[string]any{"ce_type": typ, "data": string(b)}}).Err())
	}
	time.Sleep(100 * time.Millisecond)
	reins.set(BrainHold{Subject: helper, Profile: "fake"})
	reinsSays("reins.hold.changed", map[string]any{"subject": helper, "holder": "brain:fake"})
	require.Eventually(t, func() bool { return len(d.Holds()) == 1 }, 10*time.Second, 20*time.Millisecond, "held on the event")

	done := make(chan struct{})
	d.Woken(ctx, the0, alice, "create", func() { close(done) })
	require.Eventually(t, func() bool { return slices.Contains(bus.types(), ReplyDelta) }, 5*time.Second, 10*time.Millisecond)
	reinsSays("reins.reply.stop", map[string]any{"thread": the0, "subject": helper})
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("not stopped")
	}
	assert.Len(t, j.said(), 2, "what was written, said")
}
