package account

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/jurasikasan/units/auth_service/internal/password"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrUsernameTaken = errors.New("username already taken")

var (
	ErrAccountNotFound = errors.New("account not found")
	ErrAccountDisabled = errors.New("account is disabled")
	ErrWrongPassword   = errors.New("current password does not match")
	ErrSamePassword    = errors.New("new password is the same as the current one")
)

type Repository interface {
	FindByUsername(ctx context.Context, username string) (*Account, error)
	FindByID(ctx context.Context, id uuid.UUID) (*Account, error)
}

type Service struct {
	pool *pgxpool.Pool
	repo Repository

	dummyVerify func(plain string)
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, repo: &pgRepository{pool: pool}, dummyVerify: password.DummyVerify}
}

func (s *Service) Authenticate(ctx context.Context, username, plain string) (*Account, error) {
	a, err := s.repo.FindByUsername(ctx, username)
	if err != nil {
		return nil, err
	}

	if a == nil {

		s.dummyVerify(plain)
		return nil, nil
	}

	ok, err := password.Verify(plain, a.passwordHash)
	if err != nil {
		if errors.Is(err, password.ErrMalformedHash) || errors.Is(err, password.ErrUnsupportedVersion) {

			s.dummyVerify(plain)
		}

		return nil, fmt.Errorf("verify password for %s: %w", a.ID, err)
	}
	if !ok {
		return nil, nil
	}

	if a.Disabled {
		log.Printf("login refused: account %s is disabled", a.ID)
		return nil, nil
	}

	return a, nil
}

func (s *Service) Create(ctx context.Context, username, email, plain string) (*Account, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("username is required")
	}
	if plain == "" {
		return nil, errors.New("password is required")
	}

	hash, err := password.Hash(plain)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	var emailArg *string
	if e := strings.TrimSpace(email); e != "" {
		emailArg = &e
	}

	a := &Account{ID: uuid.New(), Username: username, Email: emailArg}

	err = s.pool.QueryRow(ctx,
		`INSERT INTO accounts (id, username, email, password_hash)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT DO NOTHING
		 RETURNING created_at`,
		a.ID, a.Username, emailArg, hash,
	).Scan(&a.CreatedAt)

	if errors.Is(err, pgx.ErrNoRows) {

		return nil, ErrUsernameTaken
	}
	if err != nil {
		return nil, fmt.Errorf("insert account: %w", err)
	}

	return a, nil
}

func (s *Service) ChangePassword(ctx context.Context, id uuid.UUID, current, next string) error {
	a, err := s.FindByID(ctx, id)
	if err != nil {
		return err
	}
	if a == nil {

		return ErrAccountNotFound
	}

	if a.Disabled {
		return ErrAccountDisabled
	}

	ok, err := password.Verify(current, a.passwordHash)
	if err != nil {

		return fmt.Errorf("verify current password for %s: %w", a.ID, err)
	}
	if !ok {
		return ErrWrongPassword
	}

	if next == current {
		return ErrSamePassword
	}

	hash, err := password.Hash(next)
	if err != nil {
		return fmt.Errorf("hash new password: %w", err)
	}

	if _, err := s.pool.Exec(ctx,
		`UPDATE accounts SET password_hash = $2 WHERE id = $1`,
		a.ID, hash,
	); err != nil {
		return fmt.Errorf("update password for %s: %w", a.ID, err)
	}

	return nil
}

func (s *Service) Count(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM accounts`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count accounts: %w", err)
	}
	return n, nil
}

func (s *Service) DisableByUsername(ctx context.Context, username string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx,
		`UPDATE accounts SET disabled = true
		 WHERE lower(username) = lower($1)
		 RETURNING id`,
		strings.TrimSpace(username),
	).Scan(&id)

	if errors.Is(err, pgx.ErrNoRows) {

		return uuid.Nil, ErrAccountNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("disable account %q: %w", username, err)
	}

	return id, nil
}

func (s *Service) FindByID(ctx context.Context, id uuid.UUID) (*Account, error) {
	return s.repo.FindByID(ctx, id)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func (r *pgRepository) FindByID(ctx context.Context, id uuid.UUID) (*Account, error) {
	a := &Account{}
	err := r.pool.QueryRow(ctx,
		`SELECT id, username, email, password_hash, disabled, created_at
		 FROM accounts WHERE id = $1`,
		id,
	).Scan(&a.ID, &a.Username, &a.Email, &a.passwordHash, &a.Disabled, &a.CreatedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select account %s: %w", id, err)
	}

	return a, nil
}

func (r *pgRepository) FindByUsername(ctx context.Context, username string) (*Account, error) {
	a := &Account{}
	err := r.pool.QueryRow(ctx,
		`SELECT id, username, email, password_hash, disabled, created_at
		 FROM accounts WHERE lower(username) = lower($1)`,
		strings.TrimSpace(username),
	).Scan(&a.ID, &a.Username, &a.Email, &a.passwordHash, &a.Disabled, &a.CreatedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select account: %w", err)
	}

	return a, nil
}
