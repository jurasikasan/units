package thread

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jurasikasan/units/threads/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The SQL is where a journal goes wrong (gaps, order, membership), and a mock
// cannot show any of it. These run against a real Postgres when
// THREADS_TEST_DATABASE_URL names one, and are skipped otherwise; check.sh
// starts one when docker is there.

func liveService(t *testing.T) (*DefaultService, *PGXRepository) {
	t.Helper()
	dsn := os.Getenv("THREADS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("THREADS_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, db.Migrate(ctx, pool))
	repo := NewPGXRepository(pool)
	return NewService(repo), repo
}

func say(text string) NewEntry {
	return NewEntry{Sender: alice, Kind: KindCreate, Content: []Block{{Type: BlockText, Text: text}}}
}

func TestLiveSeqHasNoGapsUnderConcurrentAppends(t *testing.T) {
	svc, _ := liveService(t)
	ctx := context.Background()
	th, err := svc.Open(ctx, NewThread{})
	require.NoError(t, err)

	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Append(ctx, th.ID, say("x"))
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	limit := int64(1000)
	es, err := svc.Entries(ctx, th.ID, PageQuery{Limit: &limit})
	require.NoError(t, err)
	require.Len(t, es, n)
	for i, e := range es {
		require.Equal(t, int64(i+1), e.Seq)
	}
}

func TestLiveAppendToNoThreadLeavesNothing(t *testing.T) {
	svc, _ := liveService(t)
	_, err := svc.Append(context.Background(), uuid.New(), say("x"))
	require.ErrorIs(t, err, ErrNotFound)
}

func TestLivePages(t *testing.T) {
	svc, _ := liveService(t)
	ctx := context.Background()
	th, err := svc.Open(ctx, NewThread{})
	require.NoError(t, err)
	for _, s := range []string{"1", "2", "3", "4", "5"} {
		_, err := svc.Append(ctx, th.ID, say(s))
		require.NoError(t, err)
	}
	seqs := func(q PageQuery) []int64 {
		es, err := svc.Entries(ctx, th.ID, q)
		require.NoError(t, err)
		out := []int64{}
		for _, e := range es {
			out = append(out, e.Seq)
		}
		return out
	}
	two, three, four := int64(2), int64(3), int64(4)
	require.Equal(t, []int64{1, 2, 3, 4, 5}, seqs(PageQuery{}))
	require.Equal(t, []int64{3, 4}, seqs(PageQuery{After: &two, Limit: &two}))
	require.Equal(t, []int64{1, 2, 3}, seqs(PageQuery{Before: &four}))
	require.Equal(t, []int64{2, 3}, seqs(PageQuery{Before: &four, Limit: &two}))
	require.Equal(t, []int64{3, 4, 5}, seqs(PageQuery{Tail: true, Limit: &three}))

	es, err := svc.Entries(ctx, th.ID, PageQuery{Tail: true, Limit: &two})
	require.NoError(t, err)
	require.Equal(t, "5", es[1].Content[0].Text)

	_, err = svc.Entries(ctx, uuid.New(), PageQuery{})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestLiveMembersAndName(t *testing.T) {
	svc, _ := liveService(t)
	ctx := context.Background()
	bob, carol := uuid.New(), uuid.New()
	th, err := svc.Open(ctx, NewThread{OpenedBy: &bob})
	require.NoError(t, err)

	steps := []NewEntry{
		{Sender: bob, Kind: KindJoin},
		{Sender: carol, Kind: KindJoin},
		{Sender: bob, Kind: KindUpdate, Name: "Kitchen"},
		{Sender: bob, Kind: KindLeave},
		{Sender: carol, Kind: KindUpdate, Name: "Garden"},
	}
	for _, e := range steps {
		_, err := svc.Append(ctx, th.ID, e)
		require.NoError(t, err)
	}
	got, err := svc.Get(ctx, th.ID)
	require.NoError(t, err)
	require.Equal(t, "Garden", got.Name)
	require.Equal(t, []uuid.UUID{carol}, got.Members)
	require.Equal(t, &bob, got.OpenedBy)

	// bob comes back: a member again, after carol.
	_, err = svc.Append(ctx, th.ID, NewEntry{Sender: bob, Kind: KindJoin})
	require.NoError(t, err)
	got, err = svc.Get(ctx, th.ID)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{carol, bob}, got.Members)

	// carol's threads: this one; bob's after carol leaves: still this one.
	_, err = svc.Append(ctx, th.ID, NewEntry{Sender: carol, Kind: KindLeave})
	require.NoError(t, err)
	carolsThreads, err := svc.List(ctx, Filter{Member: &carol})
	require.NoError(t, err)
	require.Empty(t, carolsThreads)
	bobsThreads, err := svc.List(ctx, Filter{Member: &bob})
	require.NoError(t, err)
	require.Len(t, bobsThreads, 1)
	require.Equal(t, "Garden", bobsThreads[0].Name)
}

func TestLiveChildrenGoWithTheParent(t *testing.T) {
	svc, _ := liveService(t)
	ctx := context.Background()
	parent, err := svc.Open(ctx, NewThread{})
	require.NoError(t, err)
	child, err := svc.Open(ctx, NewThread{Parent: &parent.ID})
	require.NoError(t, err)
	grandchild, err := svc.Open(ctx, NewThread{Parent: &child.ID})
	require.NoError(t, err)
	_, err = svc.Append(ctx, grandchild.ID, say("deep"))
	require.NoError(t, err)

	children, err := svc.List(ctx, Filter{Parent: &parent.ID})
	require.NoError(t, err)
	require.Len(t, children, 1)
	require.Equal(t, child.ID, children[0].ID)

	require.NoError(t, svc.Delete(ctx, parent.ID))
	for _, id := range []uuid.UUID{parent.ID, child.ID, grandchild.ID} {
		_, err := svc.Get(ctx, id)
		require.ErrorIs(t, err, ErrNotFound)
	}
	require.ErrorIs(t, svc.Delete(ctx, parent.ID), ErrNotFound)

	noParent := uuid.New()
	_, err = svc.Open(ctx, NewThread{Parent: &noParent})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestLiveBlocksRoundTrip(t *testing.T) {
	svc, _ := liveService(t)
	ctx := context.Background()
	th, err := svc.Open(ctx, NewThread{})
	require.NoError(t, err)
	in := []Block{
		{Type: BlockText, Text: "looking"},
		{Type: BlockToolCall, ID: "c1", Name: "look", Args: []byte(`{"q": "rain"}`)},
	}
	_, err = svc.Append(ctx, th.ID, NewEntry{Sender: alice, Kind: KindCreate, Content: in})
	require.NoError(t, err)
	_, err = svc.Append(ctx, th.ID, NewEntry{Sender: alice, Kind: KindCreate, Content: []Block{
		{Type: BlockToolResult, CallID: "c1", Text: "wet", IsError: true},
		{Type: BlockAudio, URL: "u", MediaType: "audio/wav", SHA256: digest, Transcript: "wet"},
	}})
	require.NoError(t, err)

	es, err := svc.Entries(ctx, th.ID, PageQuery{})
	require.NoError(t, err)
	require.Len(t, es, 2)
	require.Equal(t, "look", es[0].Content[1].Name)
	require.JSONEq(t, `{"q":"rain"}`, string(es[0].Content[1].Args))
	require.True(t, es[1].Content[0].IsError)
	require.Equal(t, digest, es[1].Content[1].SHA256)
	require.False(t, es[1].At.IsZero())
}
