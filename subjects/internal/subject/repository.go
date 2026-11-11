package subject

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// prefsValue is preferences for a JSONB column (absent or null → SQL NULL).
func prefsValue(p json.RawMessage) any {
	if len(p) == 0 || string(p) == "null" {
		return nil
	}
	return string(p)
}

// scanPrefs is a JSONB column's bytes (SQL NULL → absent).
func scanPrefs(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return json.RawMessage(raw)
}

// SubjectRepository defines the interface for database operations on subjects.
type SubjectRepository interface {
	Create(ctx context.Context, subject *Subject) error
	GetAll(ctx context.Context) ([]Subject, error)
	GetByID(ctx context.Context, id string) (*Subject, error)
	Update(ctx context.Context, subject *Subject) error
	Delete(ctx context.Context, id string) error
}

// PGXSubjectRepository is the pgx implementation of SubjectRepository.
type PGXSubjectRepository struct {
	dbpool *pgxpool.Pool
}

// NewPGXSubjectRepository creates a new PGXSubjectRepository.
func NewPGXSubjectRepository(dbpool *pgxpool.Pool) *PGXSubjectRepository {
	return &PGXSubjectRepository{dbpool: dbpool}
}

// Create inserts a new subject record into the database.
func (r *PGXSubjectRepository) Create(ctx context.Context, subject *Subject) error {
	pv := prefsValue(subject.Preferences)
	query := `INSERT INTO subjects (id, name, nick, bio, subject_type, preferences) VALUES ($1, $2, $3, $4, $5, $6::jsonb)`
	_, err := r.dbpool.Exec(ctx, query, subject.ID, subject.Name, subject.Nick, subject.Bio, subject.SubjectType, pv)
	// 23505 is unique_violation: the only unique column a client chooses is nick.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %q", ErrNickTaken, subject.Nick)
	}
	return err
}

// GetAll retrieves all non-deleted subjects from the database.
func (r *PGXSubjectRepository) GetAll(ctx context.Context) ([]Subject, error) {
	query := "SELECT id, name, nick, bio, subject_type, preferences FROM subjects WHERE deleted_at IS NULL ORDER BY created_at DESC"
	rows, err := r.dbpool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subjects []Subject
	for rows.Next() {
		var s Subject
		var prefs []byte
		if err := rows.Scan(&s.ID, &s.Name, &s.Nick, &s.Bio, &s.SubjectType, &prefs); err != nil {
			return nil, err
		}
		s.Preferences = scanPrefs(prefs)
		subjects = append(subjects, s)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if subjects == nil {
		subjects = []Subject{}
	}
	return subjects, nil
}

// GetByID retrieves a non-deleted subject by its ID.
func (r *PGXSubjectRepository) GetByID(ctx context.Context, id string) (*Subject, error) {
	query := "SELECT id, name, nick, bio, subject_type, preferences FROM subjects WHERE id = $1 AND deleted_at IS NULL"
	var subject Subject
	var prefs []byte
	err := r.dbpool.QueryRow(ctx, query, id).Scan(&subject.ID, &subject.Name, &subject.Nick, &subject.Bio, &subject.SubjectType, &prefs)
	if err != nil {
		return nil, err
	}
	subject.Preferences = scanPrefs(prefs)
	return &subject, nil
}

// Update updates an existing subject in the database. The nick is not
// written: it is fixed at creation.
func (r *PGXSubjectRepository) Update(ctx context.Context, subject *Subject) error {
	pv := prefsValue(subject.Preferences)
	query := `UPDATE subjects SET name = $1, bio = $2, subject_type = $3, preferences = $4::jsonb WHERE id = $5`
	_, err := r.dbpool.Exec(ctx, query, subject.Name, subject.Bio, subject.SubjectType, pv, subject.ID)
	return err
}

// Delete soft-deletes a subject by setting deleted_at.
func (r *PGXSubjectRepository) Delete(ctx context.Context, id string) error {
	query := `UPDATE subjects SET deleted_at = NOW() WHERE id = $1 AND deleted_at IS NULL`
	_, err := r.dbpool.Exec(ctx, query, id)
	return err
}
