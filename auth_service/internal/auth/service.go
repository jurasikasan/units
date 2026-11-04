package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jurasikasan/units/auth_service/internal/account"
	"github.com/jurasikasan/units/auth_service/internal/session"
	"github.com/jurasikasan/units/auth_service/internal/token"
)

const CookieName = "auth_session"

type Config struct {
	CookieName string

	CookieSecure bool

	AllowedOrigins []string
}

type Authenticator interface {
	Login(ctx context.Context, username, plain string) (*account.Account, string, error)
	Verify(ctx context.Context, token string) (string, error)
	VerifyAPIToken(ctx context.Context, token string) (string, error)
	Me(ctx context.Context, token string) (*account.Account, error)
	ChangePassword(ctx context.Context, token, current, next string) (string, error)
	Logout(ctx context.Context, token string) error
	SessionTTL() time.Duration
}

type Throttle interface {
	Allow(ctx context.Context, key string) bool
	Reset(ctx context.Context, key string)
}

type Service struct {
	accounts *account.Service
	sessions *session.Store
	tokens   *token.Store
}

var _ Authenticator = (*Service)(nil)

func NewService(accounts *account.Service, sessions *session.Store, tokens *token.Store) *Service {
	return &Service{accounts: accounts, sessions: sessions, tokens: tokens}
}

func (s *Service) Login(ctx context.Context, username, plain string) (*account.Account, string, error) {
	a, err := s.accounts.Authenticate(ctx, username, plain)
	if err != nil {
		return nil, "", err
	}
	if a == nil {
		return nil, "", nil
	}

	token, err := s.sessions.Create(ctx, a.ID)
	if err != nil {
		return nil, "", err
	}

	return a, token, nil
}

func (s *Service) Verify(ctx context.Context, token string) (string, error) {
	principalID, err := s.sessions.Lookup(ctx, token)
	if err != nil {
		return "", err
	}
	return principalID.String(), nil
}

func (s *Service) VerifyAPIToken(ctx context.Context, plain string) (string, error) {
	principalID, err := s.tokens.Lookup(ctx, plain)
	if err != nil {
		return "", err
	}
	return principalID.String(), nil
}

func (s *Service) Me(ctx context.Context, token string) (*account.Account, error) {
	principalID, err := s.sessions.Lookup(ctx, token)
	if err != nil {
		return nil, err
	}

	a, err := s.accounts.FindByID(ctx, principalID)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, session.ErrNotFound
	}

	return a, nil
}

func (s *Service) ChangePassword(ctx context.Context, token, current, next string) (string, error) {
	principalID, err := s.sessions.Lookup(ctx, token)
	if err != nil {
		return "", err
	}

	if err := s.accounts.ChangePassword(ctx, principalID, current, next); err != nil {
		return "", err
	}

	if err := s.sessions.DeleteAllForPrincipal(ctx, principalID); err != nil {

		return "", err
	}

	return s.sessions.Create(ctx, principalID)
}

func (s *Service) Logout(ctx context.Context, token string) error {
	err := s.sessions.Delete(ctx, token)
	if errors.Is(err, session.ErrNotFound) {
		return nil
	}
	return err
}

func (s *Service) SessionTTL() time.Duration { return s.sessions.TTL() }

func newCookie(cfg Config, value string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     cfg.CookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

func expiredCookie(cfg Config) *http.Cookie {
	return &http.Cookie{
		Name:     cfg.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}
