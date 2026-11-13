package db

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

// The runner's whole job is to be boring, and every way it stops being boring is
// silent: a migration that never runs, two that run in the wrong order, one that
// is recorded as done after failing. None of those produce an error at the time —
// they produce a database shaped differently from what the code expects, later.
// So the tests here are about the refusals, not about the happy path, which the
// service's own boot exercises on every start.

func dir(files map[string]string) fstest.MapFS {
	out := fstest.MapFS{}
	for name, body := range files {
		out["migrations/"+name] = &fstest.MapFile{Data: []byte(body)}
	}
	return out
}

func TestLoadMigrationsOrdersByNumber(t *testing.T) {
	// 10 before 9 as strings, after 9 as numbers. The padding we use today hides
	// this; the test is what notices if someone drops it.
	got, err := loadMigrations(dir(map[string]string{
		"0010_tenth.sql":   "SELECT 10;",
		"0002_second.sql":  "SELECT 2;",
		"0009_ninth.sql":   "SELECT 9;",
		"0001_initial.sql": "SELECT 1;",
	}))
	require.NoError(t, err)

	var versions []int64
	for _, m := range got {
		versions = append(versions, m.version)
	}
	require.Equal(t, []int64{1, 2, 9, 10}, versions)
	require.Equal(t, "initial", got[0].name)
	require.Equal(t, "SELECT 1;", got[0].sql)
}

func TestLoadMigrationsRefusesDuplicateVersion(t *testing.T) {
	_, err := loadMigrations(dir(map[string]string{
		"0001_initial.sql": "SELECT 1;",
		"0002_mine.sql":    "SELECT 2;",
		"0002_theirs.sql":  "SELECT 2;",
	}))
	require.ErrorContains(t, err, "share version 0002")
}

func TestLoadMigrationsRefusesBadNames(t *testing.T) {
	for _, name := range []string{
		"0007.sql",        // number with nothing said about it
		"initial.sql",     // no number at all
		"_leading.sql",    // empty version
		"0000_zeroth.sql", // versions start at 1; 0 is the empty ledger
		"abcd_letters.sql",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadMigrations(dir(map[string]string{name: "SELECT 1;"}))
			require.Error(t, err, "%s should not be accepted", name)
		})
	}
}

func TestLoadMigrationsRefusesEmptyDir(t *testing.T) {
	// An embed directive that stops matching produces exactly this, and the
	// binary would otherwise come up and serve against no schema at all.
	_, err := loadMigrations(fstest.MapFS{"migrations/README.md": &fstest.MapFile{}})
	require.ErrorContains(t, err, "no migrations embedded")
}

func TestCheckOrderRefusesMigrationAddedBehindTheHead(t *testing.T) {
	all := []migration{
		{version: 1, name: "initial"},
		{version: 2, name: "mine"},
		{version: 3, name: "theirs"},
	}
	// 3 ran; 2 never did. Two branches, both merged, and 2 is the one that
	// would be silently skipped by a high-water-mark runner.
	err := checkOrder(all, map[int64]bool{1: true, 3: true})
	require.ErrorContains(t, err, "0002_mine has never run")
	require.ErrorContains(t, err, "renumber it above 0003")
}

func TestCheckOrderAllowsTheOrdinaryCases(t *testing.T) {
	all := []migration{
		{version: 1, name: "initial"},
		{version: 2, name: "second"},
		{version: 3, name: "third"},
	}
	t.Run("fresh database", func(t *testing.T) {
		require.NoError(t, checkOrder(all, map[int64]bool{}))
	})
	t.Run("partially applied, in order", func(t *testing.T) {
		require.NoError(t, checkOrder(all, map[int64]bool{1: true, 2: true}))
	})
	t.Run("fully applied", func(t *testing.T) {
		require.NoError(t, checkOrder(all, map[int64]bool{1: true, 2: true, 3: true}))
	})
	t.Run("database ahead of the binary", func(t *testing.T) {
		// An old image against a migrated database. Not this check's business:
		// 4 is not in `all`, and nothing in `all` is below the head unapplied.
		require.NoError(t, checkOrder(all, map[int64]bool{1: true, 2: true, 3: true, 4: true}))
	})
}

// TestEmbeddedMigrationsAreWellFormed runs the real rules against the real
// files. Everything above uses fixtures, which means none of it would notice a
// migration committed with a name the runner rejects — and that failure appears
// at boot, in the container, after the deploy.
func TestEmbeddedMigrationsAreWellFormed(t *testing.T) {
	got, err := loadMigrations(migrationFS)
	require.NoError(t, err)
	require.NotEmpty(t, got)
	require.Equal(t, int64(1), got[0].version, "numbering starts at 0001")

	for i, m := range got {
		require.NotEmpty(t, m.sql, "%04d_%s is empty", m.version, m.name)
		if i > 0 {
			require.Greater(t, m.version, got[i-1].version, "versions must be distinct and rising")
		}
	}
	require.NoError(t, checkOrder(got, map[int64]bool{}))
}
