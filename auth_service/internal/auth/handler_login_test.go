package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jurasikasan/units/auth_service/internal/account"
	"github.com/jurasikasan/units/auth_service/internal/password"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func loginRouter(h *Handler) *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/login", h.Login).Methods(http.MethodPost)
	return r
}

func TestLogin_Success(t *testing.T) {
	svc := new(MockAuthenticator)
	throttle := &MockThrottle{}
	h := NewHandler(svc, testConfig(), throttle, &MockThrottle{})

	a := &account.Account{ID: uuid.New(), Username: "ivan"}
	svc.On("Login", mock.Anything, "ivan", "s3cret").Return(a, "token-value", nil)

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"ivan","password":"s3cret"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	loginRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), a.ID.String())

	cookie := findCookie(w.Result(), CookieName)
	require.NotNil(t, cookie, "a successful login must set the session cookie")
	assert.Equal(t, "token-value", cookie.Value)

	assert.True(t, cookie.HttpOnly, "cookie must be HttpOnly so XSS cannot read it")
	assert.True(t, cookie.Secure, "cookie must be Secure so it never crosses plain HTTP")
	assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite, "cookie must be SameSite to blunt CSRF")

	assert.True(t, throttle.reset, "a successful login should clear the attempt counter")
	svc.AssertExpectations(t)
}

func TestLogin_DoesNotLeakTokenInBody(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	a := &account.Account{ID: uuid.New(), Username: "ivan"}
	svc.On("Login", mock.Anything, "ivan", "s3cret").Return(a, "super-secret-token", nil)

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"ivan","password":"s3cret"}`))
	w := httptest.NewRecorder()
	loginRouter(h).ServeHTTP(w, req)

	assert.NotContains(t, w.Body.String(), "super-secret-token")
}

func TestLogin_InvalidCredentialsIsOpaque(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	svc.On("Login", mock.Anything, "ghost", "whatever").Return(nil, "", nil)

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"ghost","password":"whatever"}`))
	w := httptest.NewRecorder()
	loginRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), invalidCredentials)
	assert.Nil(t, findCookie(w.Result(), CookieName), "a failed login must not set a session cookie")
}

func TestLogin_MalformedHashIsInvalidCredentials(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	svc.On("Login", mock.Anything, "billing-integration", "whatever").
		Return(nil, "", fmt.Errorf("verify password for %s: %w", "acct-id", password.ErrMalformedHash))

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"billing-integration","password":"whatever"}`))
	w := httptest.NewRecorder()
	loginRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), invalidCredentials)
	assert.Nil(t, findCookie(w.Result(), CookieName), "a failed login must not set a session cookie")
}

func TestLogin_Throttled(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{deny: true}, &MockThrottle{})

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"ivan","password":"s3cret"}`))
	w := httptest.NewRecorder()
	loginRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusTooManyRequests, w.Code)

	svc.AssertNotCalled(t, "Login", mock.Anything, mock.Anything, mock.Anything)
}

func TestLogin_ThrottledByAddressAlone(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{deny: true})

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"ivan","password":"s3cret"}`))
	w := httptest.NewRecorder()
	loginRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	svc.AssertNotCalled(t, "Login", mock.Anything, mock.Anything, mock.Anything)
}

func TestLogin_BothCountersChargedWhenThrottled(t *testing.T) {
	svc := new(MockAuthenticator)
	userThrottle := &countingThrottle{deny: true}
	ipThrottle := &countingThrottle{}
	h := NewHandler(svc, testConfig(), userThrottle, ipThrottle)

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"ivan","password":"s3cret"}`))
	w := httptest.NewRecorder()
	loginRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, 1, userThrottle.allowCalls)
	assert.Equal(t, 1, ipThrottle.allowCalls, "the address counter must still be charged")
}

func TestLogin_ForeignOriginRejected(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"ivan","password":"s3cret"}`))
	req.Header.Set("Origin", foreignOrigin)
	w := httptest.NewRecorder()
	loginRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	svc.AssertNotCalled(t, "Login", mock.Anything, mock.Anything, mock.Anything)
}

func TestLogin_AllowedOriginAccepted(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	a := &account.Account{ID: uuid.New(), Username: "ivan"}
	svc.On("Login", mock.Anything, "ivan", "s3cret").Return(a, "token-value", nil)

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"ivan","password":"s3cret"}`))
	req.Header.Set("Origin", allowedOrigin)
	w := httptest.NewRecorder()
	loginRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestLogin_SameOriginAllowedWithoutConfiguredList(t *testing.T) {
	svc := new(MockAuthenticator)
	cfg := testConfig()
	cfg.AllowedOrigins = nil
	h := NewHandler(svc, cfg, &MockThrottle{}, &MockThrottle{})

	a := &account.Account{ID: uuid.New(), Username: "ivan"}
	svc.On("Login", mock.Anything, "ivan", "s3cret").Return(a, "token-value", nil)

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"ivan","password":"s3cret"}`))
	req.Host = "app.example.com"

	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Origin", "https://app.example.com")
	w := httptest.NewRecorder()
	loginRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestLogin_ForeignOriginRejectedWithoutConfiguredList(t *testing.T) {
	svc := new(MockAuthenticator)
	cfg := testConfig()
	cfg.AllowedOrigins = nil
	h := NewHandler(svc, cfg, &MockThrottle{}, &MockThrottle{})

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"ivan","password":"s3cret"}`))
	req.Host = "app.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Origin", foreignOrigin)
	w := httptest.NewRecorder()
	loginRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	svc.AssertNotCalled(t, "Login", mock.Anything, mock.Anything, mock.Anything)
}

func TestLogin_ServiceErrorIs500(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	svc.On("Login", mock.Anything, "ivan", "s3cret").
		Return(nil, "", errors.New("postgres is down"))

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"ivan","password":"s3cret"}`))
	w := httptest.NewRecorder()
	loginRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	assert.NotContains(t, w.Body.String(), invalidCredentials)

	assert.NotContains(t, w.Body.String(), "postgres")
}

func TestLogin_MalformedBody(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`not json`))
	w := httptest.NewRecorder()
	loginRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}
