package thread

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jurasikasan/units/threads/internal/events"
)

type fakePub struct {
	got []events.Event
	err error
}

func (f *fakePub) Publish(_ context.Context, e events.Event) error {
	f.got = append(f.got, e)
	return f.err
}

var th = uuid.MustParse("11111111-1111-1111-1111-111111111111")

func saying(text string) NewEntry {
	return NewEntry{Sender: alice, Kind: "create", Content: []Block{{Type: "text", Text: text}}}
}

// The event names the entry and wakes; the content stays in the journal.
func TestAppendAnnounces(t *testing.T) {
	repo := &fakeRepo{}
	pub := &fakePub{}
	svc := NewService(repo).Announcing(pub)

	e, err := svc.Append(WithTraceparent(context.Background(), "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"), th, saying("secret"))
	require.NoError(t, err)

	require.Len(t, pub.got, 1)
	ev := pub.got[0]
	assert.Equal(t, events.EntryAppended, ev.Type)
	assert.Equal(t, th.String(), ev.Subject)
	assert.Equal(t, map[string]any{"thread": th.String(), "seq": e.Seq, "sender": alice.String(), "kind": "create"}, ev.Data)
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", ev.Traceparent)
}

func TestRefusedOrFailedAppendAnnouncesNothing(t *testing.T) {
	pub := &fakePub{}
	_, err := NewService(&fakeRepo{}).Announcing(pub).Append(context.Background(), th, NewEntry{Sender: alice, Kind: "like"})
	require.Error(t, err)
	_, err = NewService(&fakeRepo{err: ErrNotFound}).Announcing(pub).Append(context.Background(), th, saying("hi"))
	require.Error(t, err)
	assert.Empty(t, pub.got)
}

func TestPublishFailureDoesNotFailAppend(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo).Announcing(&fakePub{err: errors.New("redis gone")})
	e, err := svc.Append(context.Background(), th, saying("hi"))
	require.NoError(t, err)
	assert.Equal(t, []*Entry{e}, repo.appended)
}

// A Redis that is down, through the real publisher and over HTTP: the append
// is 201 and stored, and it does not wait past the publish timeout.
func TestRedisDownDoesNotFailAppend(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}) // nothing listens there
	t.Cleanup(func() { _ = client.Close() })
	repo := &fakeRepo{}
	r := mux.NewRouter()
	RegisterRoutes(r, NewHandler(NewService(repo).Announcing(events.Redis(client))))

	start := time.Now()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/threads/"+th.String()+"/entries",
		strings.NewReader(`{"sender":"`+alice.String()+`","kind":"join"}`)))

	assert.Equal(t, 201, rec.Code, rec.Body.String())
	assert.Len(t, repo.appended, 1)
	assert.Less(t, time.Since(start), publishTimeout+time.Second)
}

// Against a real Redis (THREADS_TEST_REDIS_ADDR, which check.sh starts): the
// stream entry is the contract's envelope.
func TestAnnouncedOnStream(t *testing.T) {
	addr := os.Getenv("THREADS_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("THREADS_TEST_REDIS_ADDR not set")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Del(ctx, events.Stream).Err())

	e, err := NewService(&fakeRepo{}).Announcing(events.Redis(client)).Append(ctx, th, saying("hi"))
	require.NoError(t, err)

	msgs, err := client.XRange(ctx, events.Stream, "-", "+").Result()
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	v := msgs[0].Values
	assert.Equal(t, "1.0", v["ce_specversion"])
	assert.Equal(t, "/threads", v["ce_source"])
	assert.Equal(t, "threads.entry.appended", v["ce_type"])
	assert.Equal(t, th.String(), v["ce_subject"])
	var data map[string]any
	require.NoError(t, json.Unmarshal([]byte(v["data"].(string)), &data))
	assert.Equal(t, map[string]any{"thread": th.String(), "seq": float64(e.Seq), "sender": alice.String(), "kind": "create"}, data)
}
