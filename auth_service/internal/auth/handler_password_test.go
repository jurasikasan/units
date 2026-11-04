package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jurasikasan/units/auth_service/internal/account"
	"github.com/jurasikasan/units/auth_service/internal/password"
	"github.com/jurasikasan/units/auth_service/internal/session"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func passwordRouter(h *Handler) *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/password", h.ChangePassword).Methods(http.MethodPost)
	return r
}

func passwordRequest(body string, cookieValue string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookieValue != "" {
		req.AddCookie(&http.Cookie{Name: CookieName, Value: cookieValue})
	}
	return req
}

const validPasswordBody = `{"current_password":"correct-horse-battery","new_password":"brand-new-ten-plus-runes"}`

func TestChangePassword_NoSessionIsUnauthorized(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	w := httptest.NewRecorder()
	passwordRouter(h).ServeHTTP(w, passwordRequest(validPasswordBody, ""))

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Nil(t, findCookie(w.Result(), CookieName), "a refused change must not set a session cookie")
	svc.AssertNotCalled(t, "ChangePassword", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestChangePassword_StaleSessionIsUnauthorized(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	svc.On("ChangePassword", mock.Anything, "stale", "correct-horse-battery", "brand-new-ten-plus-runes").
		Return("", session.ErrNotFound)

	w := httptest.NewRecorder()
	passwordRouter(h).ServeHTTP(w, passwordRequest(validPasswordBody, "stale"))

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Nil(t, findCookie(w.Result(), CookieName))
	svc.AssertExpectations(t)
}

func TestChangePassword_WrongCurrentPasswordIsForbidden(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	svc.On("ChangePassword", mock.Anything, "good", "wrong-current", "brand-new-ten-plus-runes").
		Return("", account.ErrWrongPassword)

	req := passwordRequest(`{"current_password":"wrong-current","new_password":"brand-new-ten-plus-runes"}`, "good")
	w := httptest.NewRecorder()
	passwordRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), account.ErrWrongPassword.Error())
	assert.Nil(t, findCookie(w.Result(), CookieName))
	svc.AssertExpectations(t)
}

func TestChangePassword_AccountGoneIsUnauthorized(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"not found", account.ErrAccountNotFound},
		{"disabled", account.ErrAccountDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := new(MockAuthenticator)
			h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

			svc.On("ChangePassword", mock.Anything, "good", "correct-horse-battery", "brand-new-ten-plus-runes").
				Return("", tc.err)

			w := httptest.NewRecorder()
			passwordRouter(h).ServeHTTP(w, passwordRequest(validPasswordBody, "good"))

			assert.Equal(t, http.StatusUnauthorized, w.Code)
			assert.Nil(t, findCookie(w.Result(), CookieName))
			svc.AssertExpectations(t)
		})
	}
}

func TestChangePassword_SamePasswordIsBadRequest(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	svc.On("ChangePassword", mock.Anything, "good", "correct-horse-battery", "correct-horse-battery").
		Return("", account.ErrSamePassword)

	req := passwordRequest(`{"current_password":"correct-horse-battery","new_password":"correct-horse-battery"}`, "good")
	w := httptest.NewRecorder()
	passwordRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), account.ErrSamePassword.Error())
	assert.Nil(t, findCookie(w.Result(), CookieName))
	svc.AssertExpectations(t)
}

func TestChangePassword_TooShortNewPasswordIsBadRequest(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	req := passwordRequest(`{"current_password":"correct-horse-battery","new_password":"short123"}`, "good")
	w := httptest.NewRecorder()
	passwordRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), password.ErrTooShort.Error())
	assert.Nil(t, findCookie(w.Result(), CookieName))
	svc.AssertNotCalled(t, "ChangePassword", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestChangePassword_TooLongNewPasswordIsBadRequest(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	long := strings.Repeat("a", 129)
	req := passwordRequest(`{"current_password":"correct-horse-battery","new_password":"`+long+`"}`, "good")
	w := httptest.NewRecorder()
	passwordRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), password.ErrTooLong.Error())
	svc.AssertNotCalled(t, "ChangePassword", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestChangePassword_ForeignOriginIsForbidden(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	req := passwordRequest(validPasswordBody, "good")
	req.Header.Set("Origin", foreignOrigin)
	w := httptest.NewRecorder()
	passwordRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "origin not allowed")
	svc.AssertNotCalled(t, "ChangePassword", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestChangePassword_AllowedOriginPasses(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	svc.On("ChangePassword", mock.Anything, "good", "correct-horse-battery", "brand-new-ten-plus-runes").
		Return("fresh-token", nil)

	req := passwordRequest(validPasswordBody, "good")
	req.Header.Set("Origin", allowedOrigin)
	w := httptest.NewRecorder()
	passwordRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	svc.AssertExpectations(t)
}

func TestChangePassword_SuccessSetsFreshSessionCookie(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	svc.On("ChangePassword", mock.Anything, "old-session", "correct-horse-battery", "brand-new-ten-plus-runes").
		Return("fresh-token", nil)

	w := httptest.NewRecorder()
	passwordRouter(h).ServeHTTP(w, passwordRequest(validPasswordBody, "old-session"))

	assert.Equal(t, http.StatusOK, w.Code)

	cookie := findCookie(w.Result(), CookieName)
	require.NotNil(t, cookie, "a successful change must hand back a fresh session cookie")
	assert.Equal(t, "fresh-token", cookie.Value)
	assert.True(t, cookie.HttpOnly)
	assert.True(t, cookie.Secure)
	assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
	svc.AssertExpectations(t)
}

func TestChangePassword_ThrottledIsTooManyRequests(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{deny: true})

	w := httptest.NewRecorder()
	passwordRouter(h).ServeHTTP(w, passwordRequest(validPasswordBody, "live"))

	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Nil(t, findCookie(w.Result(), CookieName))
	svc.AssertNotCalled(t, "ChangePassword", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}
