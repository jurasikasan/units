package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jurasikasan/units/auth_service/internal/session"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func verifyRouter(h *Handler) *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/verify", h.Verify).Methods(http.MethodGet)
	r.HandleFunc("/logout", h.Logout).Methods(http.MethodPost)
	return r
}

func TestVerify_ValidSessionReturnsPrincipalHeader(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	principal := uuid.New().String()
	svc.On("Verify", mock.Anything, "good-token").Return(principal, nil)

	req := httptest.NewRequest(http.MethodGet, "/verify", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "good-token"})
	w := httptest.NewRecorder()
	verifyRouter(h).ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	assert.Equal(t, principal, w.Header().Get(PrincipalHeader))
	svc.AssertExpectations(t)
}

func TestVerify_IgnoresClientSuppliedPrincipalHeader(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	victim := uuid.New().String()
	attacker := uuid.New().String()
	svc.On("Verify", mock.Anything, "good-token").Return(victim, nil)

	req := httptest.NewRequest(http.MethodGet, "/verify", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "good-token"})
	req.Header.Set(PrincipalHeader, attacker)
	w := httptest.NewRecorder()
	verifyRouter(h).ServeHTTP(w, req)

	assert.Equal(t, victim, w.Header().Get(PrincipalHeader))
	assert.NotEqual(t, attacker, w.Header().Get(PrincipalHeader))
}

func TestVerify_NoCookieIsUnauthorized(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	req := httptest.NewRequest(http.MethodGet, "/verify", nil)
	w := httptest.NewRecorder()
	verifyRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Empty(t, w.Header().Get(PrincipalHeader))
	svc.AssertNotCalled(t, "Verify", mock.Anything, mock.Anything)
}

func TestVerify_UnknownSessionIsUnauthorized(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	svc.On("Verify", mock.Anything, "stale-token").Return("", session.ErrNotFound)

	req := httptest.NewRequest(http.MethodGet, "/verify", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "stale-token"})
	w := httptest.NewRecorder()
	verifyRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Empty(t, w.Header().Get(PrincipalHeader))
}

func TestVerify_StoreFailureIs500NotUnauthorized(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	svc.On("Verify", mock.Anything, "some-token").Return("", errors.New("redis is down"))

	req := httptest.NewRequest(http.MethodGet, "/verify", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "some-token"})
	w := httptest.NewRecorder()
	verifyRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Empty(t, w.Header().Get(PrincipalHeader))
}

func TestLogout_ClearsCookieAndSession(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	svc.On("Logout", mock.Anything, "good-token").Return(nil)

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "good-token"})
	w := httptest.NewRecorder()
	verifyRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)

	cookie := findCookie(w.Result(), CookieName)
	require.NotNil(t, cookie)
	assert.Empty(t, cookie.Value)
	assert.Less(t, cookie.MaxAge, 0, "logout must expire the cookie")
	svc.AssertExpectations(t)
}

func TestLogout_WithoutCookieStillSucceeds(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	w := httptest.NewRecorder()
	verifyRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	svc.AssertNotCalled(t, "Logout", mock.Anything, mock.Anything)
}
