package task

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jurasikasan/units/task_board/internal/db"
)

// liveService is the service over a real, freshly migrated Postgres, or a
// skip: the schema's constraints and trigger are only exercised there.
func liveService(t *testing.T) *Service {
	t.Helper()
	dsn := os.Getenv("TASK_BOARD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TASK_BOARD_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`)
	require.NoError(t, err)
	require.NoError(t, db.Migrate(ctx, pool))
	return NewService(NewRepository(pool), nil)
}

// The flow against the real schema: handles count per project, the closing
// time is stamped by the trigger and cleared on reopening, and the database's
// refusals come back as the sentences the service promises.
func TestLiveFlowOnTheSchema(t *testing.T) {
	svc := liveService(t)
	ctx := context.Background()

	_, err := svc.CreateProject(ctx, CreateProjectRequest{Key: "ABC", Name: "a project"})
	require.NoError(t, err)

	newTask := func(goal string) *Task {
		t.Helper()
		got, err := svc.Create(ctx, CreateRequest{
			ProjectKey: "ABC", Goal: goal, AcceptanceCriteria: "it is done", Reporter: "ann",
		})
		require.NoError(t, err)
		return got
	}
	first, second := newTask("first"), newTask("second")
	assert.Equal(t, "ABC-1", first.Handle.String())
	assert.Equal(t, "ABC-2", second.Handle.String())

	closed, err := svc.SetStatus(ctx, first.ID, StatusRequest{Status: StatusCancelled})
	require.NoError(t, err)
	assert.NotNil(t, closed.ClosedAt, "entering CANCELLED stamps closed_at")
	reopened, err := svc.SetStatus(ctx, first.ID, StatusRequest{Status: StatusDraft})
	require.NoError(t, err)
	assert.Nil(t, reopened.ClosedAt, "leaving it clears closed_at")

	waiting, err := svc.SetDependencies(ctx, second.ID, DependenciesRequest{DependsOn: []string{first.ID.String()}})
	require.NoError(t, err)
	assert.Equal(t, []Handle{first.Handle}, waiting.BlockedBy)

	_, err = svc.SetDependencies(ctx, first.ID, DependenciesRequest{DependsOn: []string{first.ID.String()}})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalid), "a self-dependency is the caller's mistake: %v", err)

	_, err = svc.Create(ctx, CreateRequest{
		ProjectKey: "NOPE", Goal: "g", AcceptanceCriteria: "a", Reporter: "ann",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project_key refers to a project that does not exist")
}
