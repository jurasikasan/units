package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/jurasikasan/units/auth_service/internal/account"

	"github.com/stretchr/testify/mock"
)

type MockAuthenticator struct {
	mock.Mock
}

func (m *MockAuthenticator) Login(ctx context.Context, username, plain string) (*account.Account, string, error) {
	args := m.Called(ctx, username, plain)
	a, _ := args.Get(0).(*account.Account)
	return a, args.String(1), args.Error(2)
}

func (m *MockAuthenticator) Verify(ctx context.Context, token string) (string, error) {
	args := m.Called(ctx, token)
	return args.String(0), args.Error(1)
}

func (m *MockAuthenticator) VerifyAPIToken(ctx context.Context, token string) (string, error) {
	args := m.Called(ctx, token)
	return args.String(0), args.Error(1)
}

func (m *MockAuthenticator) Me(ctx context.Context, token string) (*account.Account, error) {
	args := m.Called(ctx, token)
	a, _ := args.Get(0).(*account.Account)
	return a, args.Error(1)
}

func (m *MockAuthenticator) ChangePassword(ctx context.Context, token, current, next string) (string, error) {
	args := m.Called(ctx, token, current, next)
	return args.String(0), args.Error(1)
}

func (m *MockAuthenticator) Logout(ctx context.Context, token string) error {
	args := m.Called(ctx, token)
	return args.Error(0)
}

func (m *MockAuthenticator) SessionTTL() time.Duration {
	return time.Hour
}

type MockThrottle struct {
	deny bool

	reset bool
}

func (m *MockThrottle) Allow(_ context.Context, _ string) bool { return !m.deny }
func (m *MockThrottle) Reset(_ context.Context, _ string)      { m.reset = true }

type countingThrottle struct {
	deny       bool
	allowCalls int
	resetCalls int
}

func (c *countingThrottle) Allow(_ context.Context, _ string) bool {
	c.allowCalls++
	return !c.deny
}

func (c *countingThrottle) Reset(_ context.Context, _ string) { c.resetCalls++ }

func findCookie(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

const (
	allowedOrigin = "https://app.example.com"
	foreignOrigin = "https://evil.example.com"
)

func testConfig() Config {
	return Config{
		CookieName:     CookieName,
		CookieSecure:   true,
		AllowedOrigins: []string{allowedOrigin},
	}
}
