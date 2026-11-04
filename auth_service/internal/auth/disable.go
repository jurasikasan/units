package auth

import (
	"context"
	"fmt"

	"github.com/jurasikasan/units/auth_service/internal/account"
	"github.com/jurasikasan/units/auth_service/internal/session"

	"github.com/google/uuid"
)

type AccountDisabler interface {
	DisableByUsername(ctx context.Context, username string) (uuid.UUID, error)
}

type SessionKiller interface {
	DeleteAllForPrincipal(ctx context.Context, principalID uuid.UUID) error
}

var (
	_ AccountDisabler = (*account.Service)(nil)
	_ SessionKiller   = (*session.Store)(nil)
)

func Disable(ctx context.Context, accounts AccountDisabler, sessions SessionKiller, username string) (uuid.UUID, error) {
	id, err := accounts.DisableByUsername(ctx, username)
	if err != nil {

		return uuid.Nil, err
	}

	if err := sessions.DeleteAllForPrincipal(ctx, id); err != nil {
		return id, fmt.Errorf("delete sessions of %s: %w", id, err)
	}

	return id, nil
}
