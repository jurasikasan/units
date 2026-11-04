package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

const schema = `
CREATE TABLE IF NOT EXISTS accounts (
    id            UUID PRIMARY KEY,
    username      TEXT        NOT NULL,
    email         TEXT,
    password_hash TEXT        NOT NULL,
    disabled      BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS accounts_username_lower_key ON accounts (lower(username));

CREATE TABLE IF NOT EXISTS api_tokens (
    id           UUID PRIMARY KEY,
    account_id   UUID        NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    token_hash   TEXT        NOT NULL UNIQUE,
    name         TEXT        NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS api_tokens_account_id_idx ON api_tokens (account_id);
`

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, schema); err != nil {
		return fmt.Errorf("apply auth schema: %w", err)
	}
	return nil
}
