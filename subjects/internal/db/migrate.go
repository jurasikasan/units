// Package db owns the subjects schema and applies it on boot.
//
// Each unit carries its own runner, not a shared import, so it can move to
// another host or compose alone.
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Embedded rather than read from disk: the binary runs in a FROM alpine image
// that copies nothing but the executable (see Dockerfile), so a migration left
// on the filesystem would exist in development and be missing in the container —
// the one place where a missing migration is unrecoverable.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// ledger is the table recording what has run. IF NOT EXISTS here is not the
// exception 0001 makes for itself — this one has to be idempotent forever,
// because it is the bootstrap that answers "what has run" before anything has.
const ledger = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    BIGINT      PRIMARY KEY,
    name       TEXT        NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`

type migration struct {
	version int64
	name    string
	sql     string
}

// Migrate applies every migration not yet recorded, in order. Safe to run on
// every boot.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, ledger); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}

	all, err := loadMigrations(migrationFS)
	if err != nil {
		return err
	}
	applied, err := appliedVersions(ctx, pool)
	if err != nil {
		return err
	}
	if err := checkOrder(all, applied); err != nil {
		return err
	}

	// The database being ahead of the binary is an old image against a migrated
	// database — normal during a rolling deploy, and not this runner's business
	// to refuse. Said out loud because silence here reads as "nothing to do".
	known := map[int64]bool{}
	for _, m := range all {
		known[m.version] = true
	}
	for v := range applied {
		if !known[v] {
			log.Printf("migrate: database has %04d, this build does not — "+
				"running against a newer schema", v)
		}
	}

	highest := highestOf(applied)
	ran := 0
	for _, m := range all {
		if applied[m.version] {
			continue
		}
		if err := apply(ctx, pool, m); err != nil {
			return err
		}
		log.Printf("migrate: applied %04d_%s", m.version, m.name)
		ran++
	}
	if ran == 0 {
		log.Printf("migrate: schema up to date at %04d", highest)
	}
	return nil
}

func highestOf(applied map[int64]bool) int64 {
	var highest int64
	for v := range applied {
		if v > highest {
			highest = v
		}
	}
	return highest
}

// checkOrder refuses a migration numbered below something already applied.
//
// This is the failure the whole file exists to catch, and it is not exotic: two
// branches each add "the next one", both merge, and the lower number is either
// never run — because a runner that tracks a high-water mark thinks it is behind
// it — or run out of order against a table the other one already reshaped.
//
// Refused rather than warned. The database is in a state nobody described, and
// guessing which half of the disagreement is right is how a schema becomes
// folklore. The message says which file and what to do with it, because the
// person reading it is deploying and does not want to reason this out.
func checkOrder(all []migration, applied map[int64]bool) error {
	highest := highestOf(applied)
	for _, m := range all {
		if m.version < highest && !applied[m.version] {
			return fmt.Errorf("migration %04d_%s has never run, but %04d has — "+
				"it was added behind the head and would apply out of order; "+
				"renumber it above %04d", m.version, m.name, highest, highest)
		}
	}
	return nil
}

// apply runs one migration and records it in the same transaction. Postgres has
// transactional DDL, so a migration that fails half way leaves nothing behind —
// including no ledger row claiming it succeeded.
func apply(ctx context.Context, pool *pgxpool.Pool, m migration) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin %04d: %w", m.version, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, m.sql); err != nil {
		return fmt.Errorf("migration %04d_%s: %w", m.version, m.name, err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`,
		m.version, m.name); err != nil {
		return fmt.Errorf("record %04d: %w", m.version, err)
	}
	return tx.Commit(ctx)
}

func appliedVersions(ctx context.Context, pool *pgxpool.Pool) (map[int64]bool, error) {
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read migration ledger: %w", err)
	}
	defer rows.Close()

	out := map[int64]bool{}
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan ledger row: %w", err)
		}
		out[v] = true
	}
	return out, rows.Err()
}

// loadMigrations reads migrations/ and returns them in version order.
//
// Sorted by the parsed number, not by filename. Those agree today and stop
// agreeing at 0010 only if someone drops the zero padding — which is exactly the
// kind of thing that gets done once, in a hurry, and is not noticed because the
// damage is silent reordering rather than an error.
//
// Takes the filesystem rather than reaching for migrationFS directly, so the
// rules below can be tested against a directory built for the purpose. The
// alternative — committing a deliberately broken migration to exercise the
// refusal — is a file the next reader has to be told to ignore.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	var out []migration
	seen := map[int64]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, name, err := parseName(e.Name())
		if err != nil {
			return nil, err
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %s and %s share version %04d — "+
				"one of them would never run", other, e.Name(), version)
		}
		seen[version] = e.Name()

		body, err := fs.ReadFile(fsys, path.Join("migrations", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		out = append(out, migration{version: version, name: name, sql: string(body)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no migrations embedded — the binary cannot create " +
			"its own schema, which means the embed directive stopped matching")
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// parseName splits NNNN_some_name.sql. The number must be first and the name
// must not be empty: "0007.sql" tells a reader nothing at the moment they are
// reading a ledger to find out what went wrong.
func parseName(filename string) (int64, string, error) {
	base := strings.TrimSuffix(filename, ".sql")
	i := strings.Index(base, "_")
	if i <= 0 || i == len(base)-1 {
		return 0, "", fmt.Errorf("migration %q is not named NNNN_what_it_does.sql",
			filename)
	}
	version, err := strconv.ParseInt(base[:i], 10, 64)
	if err != nil || version < 1 {
		return 0, "", fmt.Errorf("migration %q does not start with a version "+
			"number", filename)
	}
	return version, base[i+1:], nil
}
