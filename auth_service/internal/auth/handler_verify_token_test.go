package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jurasikasan/units/auth_service/internal/token"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestVerify_BearerTokenReturnsPrincipalHeader(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	principal := uuid.New().String()
	svc.On("VerifyAPIToken", mock.Anything, "sfa_good").Return(principal, nil)

	req := httptest.NewRequest(http.MethodGet, "/verify", nil)
	req.Header.Set("Authorization", "Bearer sfa_good")
	w := httptest.NewRecorder()
	verifyRouter(h).ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, principal, w.Header().Get(PrincipalHeader))
	svc.AssertExpectations(t)
}

func TestVerify_BearerSchemeIsCaseInsensitive(t *testing.T) {
	for _, header := range []string{"bearer sfa_good", "BEARER sfa_good", "BeArEr sfa_good"} {
		svc := new(MockAuthenticator)
		h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})
		svc.On("VerifyAPIToken", mock.Anything, "sfa_good").Return(uuid.New().String(), nil)

		req := httptest.NewRequest(http.MethodGet, "/verify", nil)
		req.Header.Set("Authorization", header)
		w := httptest.NewRecorder()
		verifyRouter(h).ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code, "header %q", header)
	}
}

func TestVerify_UnknownTokenIsUnauthorized(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	svc.On("VerifyAPIToken", mock.Anything, "sfa_revoked").Return("", token.ErrNotFound)

	req := httptest.NewRequest(http.MethodGet, "/verify", nil)
	req.Header.Set("Authorization", "Bearer sfa_revoked")
	w := httptest.NewRecorder()
	verifyRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Empty(t, w.Header().Get(PrincipalHeader))
}

func TestVerify_TokenStoreFailureIs500NotUnauthorized(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	svc.On("VerifyAPIToken", mock.Anything, "sfa_good").Return("", errors.New("postgres is down"))

	req := httptest.NewRequest(http.MethodGet, "/verify", nil)
	req.Header.Set("Authorization", "Bearer sfa_good")
	w := httptest.NewRecorder()
	verifyRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Empty(t, w.Header().Get(PrincipalHeader))
}

func TestVerify_NonBearerAuthorizationIsIgnored(t *testing.T) {
	for _, header := range []string{"Basic dXNlcjpwYXNz", "Bearer", "Token sfa_good", ""} {
		svc := new(MockAuthenticator)
		h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

		req := httptest.NewRequest(http.MethodGet, "/verify", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		w := httptest.NewRecorder()
		verifyRouter(h).ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code, "header %q", header)
		svc.AssertNotCalled(t, "VerifyAPIToken", mock.Anything, mock.Anything)
	}
}

func TestVerify_CookieWinsOverBearer(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	fromCookie := uuid.New().String()
	svc.On("Verify", mock.Anything, "session-token").Return(fromCookie, nil)

	req := httptest.NewRequest(http.MethodGet, "/verify", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "session-token"})
	req.Header.Set("Authorization", "Bearer sfa_other_principal")
	w := httptest.NewRecorder()
	verifyRouter(h).ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, fromCookie, w.Header().Get(PrincipalHeader))
	svc.AssertNotCalled(t, "VerifyAPIToken", mock.Anything, mock.Anything)
}

func TestVerify_BearerIgnoresClientSuppliedPrincipalHeader(t *testing.T) {
	svc := new(MockAuthenticator)
	h := NewHandler(svc, testConfig(), &MockThrottle{}, &MockThrottle{})

	real := uuid.New().String()
	attacker := uuid.New().String()
	svc.On("VerifyAPIToken", mock.Anything, "sfa_good").Return(real, nil)

	req := httptest.NewRequest(http.MethodGet, "/verify", nil)
	req.Header.Set("Authorization", "Bearer sfa_good")
	req.Header.Set(PrincipalHeader, attacker)
	w := httptest.NewRecorder()
	verifyRouter(h).ServeHTTP(w, req)

	assert.Equal(t, real, w.Header().Get(PrincipalHeader))
	assert.NotEqual(t, attacker, w.Header().Get(PrincipalHeader))
}
