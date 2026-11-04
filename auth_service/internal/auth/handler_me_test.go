package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jurasikasan/units/auth_service/internal/account"
	"github.com/jurasikasan/units/auth_service/internal/session"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func meRouter(h *Handler) *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/me", h.Me).Methods(http.MethodGet)
	return r
}

func TestMe_ReturnsPrincipalAndUsername(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	a := &account.Account{ID: uuid.New(), Username: "alice"}
	svc.On("Me", mock.Anything, "good-token").Return(a, nil)

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "good-token"})
	w := httptest.NewRecorder()
	meRouter(h).ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var got MeResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, a.ID.String(), got.PrincipalID)
	assert.Equal(t, "alice", got.Username)
	svc.AssertExpectations(t)
}

func TestMe_NoCookieIsUnauthorized(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	w := httptest.NewRecorder()
	meRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	svc.AssertNotCalled(t, "Me", mock.Anything, mock.Anything)
}

func TestMe_StaleSessionIsUnauthorized(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	svc.On("Me", mock.Anything, "stale-token").Return(nil, session.ErrNotFound)

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "stale-token"})
	w := httptest.NewRecorder()
	meRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMe_DoesNotLeakAccountFields(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	email := "alice@example.com"
	a := &account.Account{ID: uuid.New(), Username: "alice", Email: &email}
	svc.On("Me", mock.Anything, "good-token").Return(a, nil)

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "good-token"})
	w := httptest.NewRecorder()
	meRouter(h).ServeHTTP(w, req)

	assert.NotContains(t, w.Body.String(), email)
}
