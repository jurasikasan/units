package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
)

const (
	sessionKeyPrefix   = "session:"
	principalKeyPrefix = "principal_sessions:"
	tokenBytes         = 32
)

var ErrNotFound = errors.New("session not found")

type Store struct {
	rdb *redis.Client
	ttl time.Duration
}

func NewStore(rdb *redis.Client, ttl time.Duration) *Store {
	return &Store{rdb: rdb, ttl: ttl}
}

func (s *Store) TTL() time.Duration { return s.ttl }

func (s *Store) Create(ctx context.Context, principalID uuid.UUID) (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, sessionKey(token), principalID.String(), s.ttl)

	pipe.SAdd(ctx, principalKey(principalID), hashToken(token))
	pipe.Expire(ctx, principalKey(principalID), s.ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", fmt.Errorf("store session: %w", err)
	}

	return token, nil
}

func (s *Store) Lookup(ctx context.Context, token string) (uuid.UUID, error) {
	if token == "" {
		return uuid.Nil, ErrNotFound
	}

	val, err := s.rdb.Get(ctx, sessionKey(token)).Result()
	if errors.Is(err, redis.Nil) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("read session: %w", err)
	}

	principalID, err := uuid.Parse(val)
	if err != nil {
		return uuid.Nil, fmt.Errorf("corrupt session value %q: %w", val, err)
	}

	if err := s.rdb.Expire(ctx, sessionKey(token), s.ttl).Err(); err != nil {
		return principalID, nil
	}
	s.rdb.Expire(ctx, principalKey(principalID), s.ttl)

	return principalID, nil
}

func (s *Store) Delete(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}

	principalID, err := s.Lookup(ctx, token)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, sessionKey(token))
	pipe.SRem(ctx, principalKey(principalID), hashToken(token))
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}

	return nil
}

func (s *Store) DeleteAllForPrincipal(ctx context.Context, principalID uuid.UUID) error {
	hashes, err := s.rdb.SMembers(ctx, principalKey(principalID)).Result()
	if err != nil {
		return fmt.Errorf("list sessions of %s: %w", principalID, err)
	}

	pipe := s.rdb.TxPipeline()
	for _, h := range hashes {
		pipe.Del(ctx, sessionKeyPrefix+h)
	}
	pipe.Del(ctx, principalKey(principalID))
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("delete sessions of %s: %w", principalID, err)
	}

	return nil
}

func sessionKey(token string) string {
	return sessionKeyPrefix + hashToken(token)
}

func principalKey(principalID uuid.UUID) string {
	return principalKeyPrefix + principalID.String()
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
