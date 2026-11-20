package holds

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store keeps the holds and when each principal's device was last seen.
type Store interface {
	// Get is one subject's hold; false when nobody holds it.
	Get(ctx context.Context, subject uuid.UUID) (Hold, bool, error)
	// Of is the holds of these subjects; one held by nobody is not there.
	Of(ctx context.Context, subjects []uuid.UUID) (map[uuid.UUID]Hold, error)
	// Set gives the subject to h's holder, and says whether that changed
	// anything.
	Set(ctx context.Context, h Hold) (bool, error)
	// SetIfNone gives it only when nobody holds it.
	SetIfNone(ctx context.Context, h Hold) (bool, error)
	// Clear gives it to nobody; false when nobody held it.
	Clear(ctx context.Context, subject uuid.UUID) (bool, error)
	// Brains is every hold of a brain.
	Brains(ctx context.Context) ([]Hold, error)
	// Seen marks a principal's device as there now.
	Seen(ctx context.Context, principal, device string) error
	// Lapse clears every principal's hold whose device has been gone
	// longer than grace, and answers what it cleared.
	Lapse(ctx context.Context, grace time.Duration) ([]Hold, error)
}

type PGXStore struct{ pool *pgxpool.Pool }

func NewPGXStore(pool *pgxpool.Pool) *PGXStore { return &PGXStore{pool: pool} }

const columns = `subject_id, holder, device, since`

func scan(row pgx.Row) (Hold, error) {
	var h Hold
	err := row.Scan(&h.Subject, &h.Holder, &h.Device, &h.Since)
	return h, err
}

func (s *PGXStore) Get(ctx context.Context, subject uuid.UUID) (Hold, bool, error) {
	h, err := scan(s.pool.QueryRow(ctx, `SELECT `+columns+` FROM holds WHERE subject_id = $1`, subject))
	if errors.Is(err, pgx.ErrNoRows) {
		return Hold{}, false, nil
	}
	if err != nil {
		return Hold{}, false, fmt.Errorf("read hold: %w", err)
	}
	return h, true, nil
}

func (s *PGXStore) list(ctx context.Context, sql string, args ...any) ([]Hold, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("read holds: %w", err)
	}
	defer rows.Close()
	out := []Hold{}
	for rows.Next() {
		h, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan hold: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *PGXStore) Of(ctx context.Context, subjects []uuid.UUID) (map[uuid.UUID]Hold, error) {
	hs, err := s.list(ctx, `SELECT `+columns+` FROM holds WHERE subject_id = ANY($1)`, subjects)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]Hold{}
	for _, h := range hs {
		out[h.Subject] = h
	}
	return out, nil
}

// Set writes the holder; `since` moves only when the holder or its device
// changed. Whether anything changed is read under the row's lock, in the
// same transaction as the write.
func (s *PGXStore) Set(ctx context.Context, h Hold) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var holder, device string
	err = tx.QueryRow(ctx, `SELECT holder, device FROM holds WHERE subject_id = $1 FOR UPDATE`, h.Subject).Scan(&holder, &device)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return false, fmt.Errorf("read hold: %w", err)
	case holder == h.Holder && device == h.Device:
		return false, nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO holds (subject_id, holder, device) VALUES ($1, $2, $3)
		ON CONFLICT (subject_id) DO UPDATE SET holder = EXCLUDED.holder, device = EXCLUDED.device, since = now()`,
		h.Subject, h.Holder, h.Device); err != nil {
		return false, fmt.Errorf("write hold: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("write hold: %w", err)
	}
	return true, nil
}

func (s *PGXStore) SetIfNone(ctx context.Context, h Hold) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO holds (subject_id, holder, device) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		h.Subject, h.Holder, h.Device)
	if err != nil {
		return false, fmt.Errorf("write hold: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PGXStore) Clear(ctx context.Context, subject uuid.UUID) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM holds WHERE subject_id = $1`, subject)
	if err != nil {
		return false, fmt.Errorf("clear hold: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PGXStore) Brains(ctx context.Context) ([]Hold, error) {
	return s.list(ctx, `SELECT `+columns+` FROM holds WHERE holder LIKE 'brain:%' ORDER BY since, subject_id`)
}

func (s *PGXStore) Seen(ctx context.Context, principal, device string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO presence (principal_id, device) VALUES ($1, $2)
		ON CONFLICT (principal_id, device) DO UPDATE SET seen_at = now()`, principal, device)
	if err != nil {
		return fmt.Errorf("mark device seen: %w", err)
	}
	return nil
}

// Lapse counts the grace from the later of when the device was last seen
// and when the hold was taken: a device that takes the reins before its
// stream opens has the grace to open it.
func (s *PGXStore) Lapse(ctx context.Context, grace time.Duration) ([]Hold, error) {
	return s.list(ctx, `
		DELETE FROM holds h
		WHERE h.holder LIKE 'principal:%'
		  AND greatest(h.since, coalesce((SELECT p.seen_at FROM presence p
		        WHERE p.principal_id = substr(h.holder, 11) AND p.device = h.device), h.since))
		      < now() - make_interval(secs => $1)
		RETURNING `+columns, grace.Seconds())
}
