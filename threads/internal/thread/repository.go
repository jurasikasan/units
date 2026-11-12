package thread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound: no thread with that id (or no parent with that id on create).
var ErrNotFound = errors.New("thread not found")

// Filter narrows a thread list; zero fields do not narrow.
type Filter struct {
	Member *uuid.UUID
	Parent *uuid.UUID
}

// Page picks entries by seq: Before > 0 reads the Limit entries before it,
// Tail the last Limit entries, otherwise Limit entries after After.
type Page struct {
	After, Before int64
	Tail          bool
	Limit         int
}

// Repository is the storage the service needs.
type Repository interface {
	CreateThread(ctx context.Context, t *Thread) error
	GetThread(ctx context.Context, id uuid.UUID) (*Thread, error)
	ListThreads(ctx context.Context, f Filter) ([]Thread, error)
	DeleteThread(ctx context.Context, id uuid.UUID) error
	Append(ctx context.Context, e *Entry) error
	Entries(ctx context.Context, thread uuid.UUID, p Page) ([]Entry, error)
}

// PGXRepository is Repository on Postgres.
type PGXRepository struct {
	pool *pgxpool.Pool
}

// NewPGXRepository wraps a pool.
func NewPGXRepository(pool *pgxpool.Pool) *PGXRepository {
	return &PGXRepository{pool: pool}
}

// nameOf: the name from the thread's last update entry.
const nameOf = `COALESCE((SELECT e.name FROM entries e
  WHERE e.thread = t.id AND e.kind = 'update' ORDER BY e.seq DESC LIMIT 1), '')`

// CreateThread inserts the thread; an unknown parent is ErrNotFound.
func (r *PGXRepository) CreateThread(ctx context.Context, t *Thread) error {
	err := r.pool.QueryRow(ctx,
		`INSERT INTO threads (id, parent, opened_by) VALUES ($1, $2, $3) RETURNING created_at`,
		t.ID, t.Parent, t.OpenedBy).Scan(&t.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation
		return fmt.Errorf("%w: parent %s", ErrNotFound, t.Parent)
	}
	return err
}

// GetThread reads one thread with its name and members.
func (r *PGXRepository) GetThread(ctx context.Context, id uuid.UUID) (*Thread, error) {
	var t Thread
	err := r.pool.QueryRow(ctx,
		`SELECT t.id, t.parent, t.opened_by, t.created_at, `+nameOf+` FROM threads t WHERE t.id = $1`,
		id).Scan(&t.ID, &t.Parent, &t.OpenedBy, &t.CreatedAt, &t.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	// The last join/leave of each sender; members are those whose last one
	// is a join, in the order they (last) joined.
	rows, err := r.pool.Query(ctx, `
		SELECT sender FROM (
		  SELECT DISTINCT ON (sender) sender, kind, seq FROM entries
		  WHERE thread = $1 AND kind IN ('join', 'leave')
		  ORDER BY sender, seq DESC
		) last WHERE kind = 'join' ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	members, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	t.Members = members
	return &t, nil
}

// ListThreads: newest first, narrowed by the filter.
func (r *PGXRepository) ListThreads(ctx context.Context, f Filter) ([]Thread, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT t.id, t.parent, t.opened_by, t.created_at, `+nameOf+`
		FROM threads t
		WHERE ($1::uuid IS NULL OR t.parent = $1)
		  AND ($2::uuid IS NULL OR (
		    SELECT e.kind FROM entries e
		    WHERE e.thread = t.id AND e.sender = $2 AND e.kind IN ('join', 'leave')
		    ORDER BY e.seq DESC LIMIT 1) = 'join')
		ORDER BY t.created_at DESC, t.id`, f.Parent, f.Member)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Thread{}
	for rows.Next() {
		var t Thread
		if err := rows.Scan(&t.ID, &t.Parent, &t.OpenedBy, &t.CreatedAt, &t.Name); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteThread removes the thread; its entries and children go by cascade.
func (r *PGXRepository) DeleteThread(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM threads WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Append gives the entry the thread's next seq and stores it, in one
// transaction: the UPDATE's row lock queues concurrent appends to the same
// thread, and a failed insert rolls the counter back, so seq has no gaps.
func (r *PGXRepository) Append(ctx context.Context, e *Entry) error {
	content, err := json.Marshal(e.Content)
	if err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	err = tx.QueryRow(ctx,
		`UPDATE threads SET last_seq = last_seq + 1 WHERE id = $1 RETURNING last_seq`,
		e.Thread).Scan(&e.Seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var name *string
	if e.Name != "" {
		name = &e.Name
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO entries (id, thread, seq, sender, kind, content, name)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING at`,
		e.ID, e.Thread, e.Seq, e.Sender, e.Kind, content, name).Scan(&e.At); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Entries reads a page, always oldest first; an unknown thread is ErrNotFound.
func (r *PGXRepository) Entries(ctx context.Context, thread uuid.UUID, p Page) ([]Entry, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM threads WHERE id = $1)`, thread).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}

	const cols = `id, thread, seq, sender, kind, at, content, COALESCE(name, '')`
	var rows pgx.Rows
	var err error
	if p.Tail || p.Before > 0 {
		before := p.Before
		if p.Tail {
			before = 1<<63 - 1
		}
		rows, err = r.pool.Query(ctx, `SELECT * FROM (
		  SELECT `+cols+` FROM entries WHERE thread = $1 AND seq < $2
		  ORDER BY seq DESC LIMIT $3) tail ORDER BY seq`, thread, before, p.Limit)
	} else {
		rows, err = r.pool.Query(ctx, `SELECT `+cols+` FROM entries
		  WHERE thread = $1 AND seq > $2 ORDER BY seq LIMIT $3`, thread, p.After, p.Limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var content []byte
		var at time.Time
		if err := rows.Scan(&e.ID, &e.Thread, &e.Seq, &e.Sender, &e.Kind, &at, &content, &e.Name); err != nil {
			return nil, err
		}
		e.At = at
		if err := json.Unmarshal(content, &e.Content); err != nil {
			return nil, fmt.Errorf("entry %s content: %w", e.ID, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
