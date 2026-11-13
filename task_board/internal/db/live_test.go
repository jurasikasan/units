package db

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// livePool connects to TASK_BOARD_TEST_DATABASE_URL and empties its schema,
// or skips: check.sh starts a throwaway Postgres and sets the variable.
func livePool(t *testing.T) *pgxpool.Pool {
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
	return pool
}

func relations(t *testing.T, pool *pgxpool.Pool, pattern string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT relname FROM pg_class
		WHERE relnamespace = 'public'::regnamespace AND relname ~ $1
		UNION ALL
		SELECT conname FROM pg_constraint
		WHERE connamespace = 'public'::regnamespace AND conname ~ $1
		UNION ALL
		SELECT tgname FROM pg_trigger WHERE NOT tgisinternal AND tgname ~ $1
		UNION ALL
		SELECT proname FROM pg_proc
		WHERE pronamespace = 'public'::regnamespace AND proname ~ $1
		ORDER BY 1`, pattern)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		out = append(out, n)
	}
	require.NoError(t, rows.Err())
	return out
}

func headVersion(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var v int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT max(version) FROM schema_migrations`).Scan(&v))
	return v
}

// A fresh database ends with the current names, whatever names the history
// passed through, and a second boot applies nothing.
func TestLiveMigrateFreshDatabase(t *testing.T) {
	pool := livePool(t)
	ctx := context.Background()

	require.NoError(t, Migrate(ctx, pool))
	all, err := loadMigrations(migrationFS)
	require.NoError(t, err)
	head := all[len(all)-1].version
	assert.Equal(t, head, headVersion(t, pool))

	assert.Empty(t, relations(t, pool, `_v2`), "no object keeps the old suffix")
	for _, table := range []string{"projects", "tasks", "task_dependencies", "schema_migrations"} {
		var reg *string
		require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, table).Scan(&reg))
		assert.NotNil(t, reg, table)
	}
	assert.Contains(t, relations(t, pool, `^tasks_closed_at$`), "tasks_closed_at")

	require.NoError(t, Migrate(ctx, pool), "a second boot is a no-op")
	assert.Equal(t, head, headVersion(t, pool))
}

// A database whose ledger still has its old name is adopted on boot rather
// than migrated again from the start, which would fail on tables that exist.
func TestLiveMigrateAdoptsTheOldLedger(t *testing.T) {
	pool := livePool(t)
	ctx := context.Background()

	require.NoError(t, Migrate(ctx, pool))
	head := headVersion(t, pool)
	_, err := pool.Exec(ctx, `
		ALTER TABLE schema_migrations RENAME TO schema_migrations_v2;
		ALTER INDEX schema_migrations_pkey RENAME TO schema_migrations_v2_pkey;`)
	require.NoError(t, err)

	require.NoError(t, Migrate(ctx, pool))
	assert.Equal(t, head, headVersion(t, pool))
	assert.Empty(t, relations(t, pool, `^schema_migrations_v2`))
}
