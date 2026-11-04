package token

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	cacheKeyPrefix = "api_token:"
	tokenBytes     = 32

	Prefix = "sfa_"

	cacheTTL = time.Minute
)

var ErrNotFound = errors.New("api token not found")

type Store struct {
	db  *pgxpool.Pool
	rdb *redis.Client
}

func NewStore(db *pgxpool.Pool, rdb *redis.Client) *Store {
	return &Store{db: db, rdb: rdb}
}

func (s *Store) Create(ctx context.Context, accountID uuid.UUID, name string, expiresAt time.Time) (string, error) {
	if name == "" {
		return "", errors.New("token name is required: it is the only thing that will say whether this row is safe to delete")
	}
	if !expiresAt.After(time.Now()) {
		return "", errors.New("token expiry must be in the future")
	}

	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate api token: %w", err)
	}
	plain := Prefix + base64.RawURLEncoding.EncodeToString(raw)

	_, err := s.db.Exec(ctx, `
		INSERT INTO api_tokens (id, account_id, token_hash, name, expires_at)
		VALUES ($1, $2, $3, $4, $5)`,
		uuid.New(), accountID, HashToken(plain), name, expiresAt,
	)
	if err != nil {
		return "", fmt.Errorf("store api token: %w", err)
	}

	return plain, nil
}

func (s *Store) Lookup(ctx context.Context, plain string) (uuid.UUID, error) {
	if plain == "" {
		return uuid.Nil, ErrNotFound
	}
	hash := HashToken(plain)

	if id, err := s.cached(ctx, hash); err == nil {
		return id, nil
	}

	var accountID uuid.UUID
	err := s.db.QueryRow(ctx, `
		SELECT t.account_id
		FROM api_tokens t
		JOIN accounts a ON a.id = t.account_id
		WHERE t.token_hash = $1
		  AND t.expires_at > now()
		  AND a.disabled = FALSE`, hash).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("read api token: %w", err)
	}

	if _, err := s.db.Exec(ctx,
		`UPDATE api_tokens SET last_used_at = now() WHERE token_hash = $1`, hash); err != nil {
		log.Printf("[token] record last_used_at: %v", err)
	}

	s.cache(ctx, hash, accountID)
	return accountID, nil
}

func (s *Store) cached(ctx context.Context, hash string) (uuid.UUID, error) {
	val, err := s.rdb.Get(ctx, cacheKeyPrefix+hash).Result()
	if err != nil {
		return uuid.Nil, err
	}
	return uuid.Parse(val)
}

func (s *Store) cache(ctx context.Context, hash string, accountID uuid.UUID) {
	if err := s.rdb.Set(ctx, cacheKeyPrefix+hash, accountID.String(), cacheTTL).Err(); err != nil {
		log.Printf("[token] cache write: %v", err)
	}
}

func HashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}
