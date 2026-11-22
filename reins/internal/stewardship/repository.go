package stewardship

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store keeps the stewardship relation and which principals are settled.
type Store interface {
	// Steered is the subjects a principal holds the reins of, oldest first.
	Steered(ctx context.Context, principal string) ([]uuid.UUID, error)
	// Steers is whether a principal holds the reins of one subject.
	Steers(ctx context.Context, principal string, subject uuid.UUID) (bool, error)
	// Settled is whether the principal's first subjects were all made.
	Settled(ctx context.Context, principal string) (bool, error)
	// Provisioning runs fn while holding the principal's lock, so two first
	// visits of one principal do not both make its subjects.
	Provisioning(ctx context.Context, principal string, fn func(Locked) error) error
	// Steer records that a principal steers a subject: committed at once,
	// outside any provisioning.
	Steer(ctx context.Context, principal string, subject uuid.UUID) error
	// Handover moves the reins of a subject from one principal to another,
	// in one transaction. ErrNotSteward when `from` does not hold them.
	Handover(ctx context.Context, subject uuid.UUID, from, to string) error
}

// Locked is the store as seen while holding one principal's lock. Each write
// is committed as it is made: a subject created in `subjects` is recorded at
// once, so a run cut half way leaves no subject that nobody steers.
type Locked interface {
	Steered(ctx context.Context) ([]uuid.UUID, error)
	Settled(ctx context.Context) (bool, error)
	Record(ctx context.Context, subject uuid.UUID) error
	Settle(ctx context.Context) error
}

// querier is what both the pool and one of its connections can do.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type PGXRepository struct{ pool *pgxpool.Pool }

func NewPGXRepository(pool *pgxpool.Pool) *PGXRepository { return &PGXRepository{pool: pool} }

func (r *PGXRepository) Steered(ctx context.Context, principal string) ([]uuid.UUID, error) {
	return steered(ctx, r.pool, principal)
}

func (r *PGXRepository) Steers(ctx context.Context, principal string, subject uuid.UUID) (bool, error) {
	var ok bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM stewardship WHERE principal_id = $1 AND subject_id = $2)`,
		principal, subject).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("read stewardship: %w", err)
	}
	return ok, nil
}

func (r *PGXRepository) Settled(ctx context.Context, principal string) (bool, error) {
	return settled(ctx, r.pool, principal)
}

// Provisioning takes a session-level advisory lock on one connection and
// does every write of fn on that same connection, outside any transaction.
//
// Not pg_advisory_xact_lock with the writes in its transaction: a failure
// after the first subject was made would roll back its record while the
// subject stays in `subjects`, and its nick would then be taken by a subject
// nobody steers. Not the lock in a transaction with the writes on the pool
// either: under load every connection could be holding a lock and waiting for
// a second one to write.
func (r *PGXRepository) Provisioning(ctx context.Context, principal string, fn func(Locked) error) error {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, principal); err != nil {
		return fmt.Errorf("lock principal: %w", err)
	}
	defer func() {
		// The request's context may be gone by now; the lock must still go.
		if _, err := conn.Exec(context.Background(),
			`SELECT pg_advisory_unlock(hashtextextended($1, 0))`, principal); err != nil {
			// A connection that may still hold the lock is not given back:
			// closing it ends its session, and the lock with it.
			_ = conn.Conn().Close(context.Background())
		}
	}()

	if _, err := conn.Exec(ctx,
		`INSERT INTO principals (principal_id) VALUES ($1) ON CONFLICT DO NOTHING`, principal); err != nil {
		return fmt.Errorf("meet principal: %w", err)
	}
	return fn(&locked{q: conn, principal: principal})
}

func (r *PGXRepository) Steer(ctx context.Context, principal string, subject uuid.UUID) error {
	return record(ctx, r.pool, principal, subject)
}

func (r *PGXRepository) Handover(ctx context.Context, subject uuid.UUID, from, to string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`DELETE FROM stewardship WHERE principal_id = $1 AND subject_id = $2`, from, subject)
	if err != nil {
		return fmt.Errorf("release reins: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotSteward
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO stewardship (principal_id, subject_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		to, subject); err != nil {
		return fmt.Errorf("take reins: %w", err)
	}
	return tx.Commit(ctx)
}

type locked struct {
	q         querier
	principal string
}

func (l *locked) Steered(ctx context.Context) ([]uuid.UUID, error) {
	return steered(ctx, l.q, l.principal)
}

func (l *locked) Settled(ctx context.Context) (bool, error) {
	return settled(ctx, l.q, l.principal)
}

func (l *locked) Record(ctx context.Context, subject uuid.UUID) error {
	return record(ctx, l.q, l.principal, subject)
}

func record(ctx context.Context, q querier, principal string, subject uuid.UUID) error {
	_, err := q.Exec(ctx,
		`INSERT INTO stewardship (principal_id, subject_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		principal, subject)
	if err != nil {
		return fmt.Errorf("record stewardship: %w", err)
	}
	return nil
}

func (l *locked) Settle(ctx context.Context) error {
	_, err := l.q.Exec(ctx,
		`UPDATE principals SET settled_at = now() WHERE principal_id = $1 AND settled_at IS NULL`, l.principal)
	if err != nil {
		return fmt.Errorf("settle principal: %w", err)
	}
	return nil
}

func steered(ctx context.Context, q querier, principal string) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx,
		`SELECT subject_id FROM stewardship WHERE principal_id = $1 ORDER BY since, subject_id`, principal)
	if err != nil {
		return nil, fmt.Errorf("read stewardship: %w", err)
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan stewardship: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func settled(ctx context.Context, q querier, principal string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx,
		`SELECT settled_at IS NOT NULL FROM principals WHERE principal_id = $1`, principal).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read principal: %w", err)
	}
	return ok, nil
}
