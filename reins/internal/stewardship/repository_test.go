package stewardship

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jurasikasan/units/reins/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The lock and the SQL are where stewardship goes wrong, and a fake cannot
// show either. These run against a real Postgres when REINS_TEST_DATABASE_URL
// names one, and are skipped otherwise; check.sh starts one when docker is
// there.

func liveRepo(t *testing.T) *PGXRepository {
	t.Helper()
	dsn := os.Getenv("REINS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("REINS_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	// Fewer connections than concurrent first visits: the lock must not need
	// a second connection to make progress.
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, db.Migrate(ctx, pool))
	return NewPGXRepository(pool)
}

func TestLiveConcurrentFirstVisitsMakeEachPersonaOnce(t *testing.T) {
	repo := liveRepo(t)
	reg := newFakeRegistry()
	svc := NewService(repo, reg)
	login := "p" + uuid.NewString()[:8]

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, err := svc.MySubjects(ctx, login)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	assert.Equal(t, 2, reg.created)

	ids, err := repo.Steered(context.Background(), login)
	require.NoError(t, err)
	assert.Len(t, ids, 2)
	ok, err := repo.Settled(context.Background(), login)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestLiveRecordIsKeptWhenTheRunFails(t *testing.T) {
	repo := liveRepo(t)
	ctx := context.Background()
	login := "p" + uuid.NewString()[:8]
	s := uuid.New()

	err := repo.Provisioning(ctx, login, func(l Locked) error {
		require.NoError(t, l.Record(ctx, s))
		return assert.AnError
	})
	require.ErrorIs(t, err, assert.AnError)

	ids, err := repo.Steered(ctx, login)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{s}, ids, "recorded at once, not rolled back with the run")
	ok, _ := repo.Settled(ctx, login)
	assert.False(t, ok)

	// The lock was let go: a second run is not stuck behind the first.
	done := make(chan error, 1)
	go func() { done <- repo.Provisioning(ctx, login, func(Locked) error { return nil }) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the principal's lock was never released")
	}
}

func TestLiveHandover(t *testing.T) {
	repo := liveRepo(t)
	ctx := context.Background()
	a, b := "p"+uuid.NewString()[:8], "p"+uuid.NewString()[:8]
	s := uuid.New()
	require.NoError(t, repo.Provisioning(ctx, a, func(l Locked) error { return l.Record(ctx, s) }))

	require.ErrorIs(t, repo.Handover(ctx, s, b, a), ErrNotSteward)
	require.NoError(t, repo.Handover(ctx, s, a, b))

	ids, _ := repo.Steered(ctx, a)
	assert.Empty(t, ids)
	ids, _ = repo.Steered(ctx, b)
	assert.Equal(t, []uuid.UUID{s}, ids)
	require.ErrorIs(t, repo.Handover(ctx, s, a, b), ErrNotSteward, "a former steward may not")
}
