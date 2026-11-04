package auth

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/jurasikasan/units/auth_service/internal/account"
	"github.com/jurasikasan/units/auth_service/internal/password"
	"github.com/jurasikasan/units/auth_service/internal/ratelimit"
	"github.com/jurasikasan/units/auth_service/internal/session"
	"github.com/jurasikasan/units/auth_service/internal/token"

	httpHelper "github.com/jurasikasan/units/shared/http"
)

const PrincipalHeader = "X-Principal-Id"

type Handler struct {
	svc         Authenticator
	cfg         Config
	userLimiter Throttle
	ipLimiter   Throttle
}

func NewHandler(svc Authenticator, cfg Config, userLimiter, ipLimiter Throttle) *Handler {
	return &Handler{svc: svc, cfg: cfg, userLimiter: userLimiter, ipLimiter: ipLimiter}
}

// @Summary Authenticate a user
// @Description Exchanges a username and password for a session cookie. The body deliberately carries no token.
// @Tags auth
// @Accept json
// @Produce json
// @Param body body LoginRequest true "credentials"
// @Success 200 {object} LoginResponse
// @Failure 400 {object} errorResponse "malformed body or password policy"
// @Failure 401 {object} errorResponse "invalid credentials"
// @Failure 403 {object} errorResponse "origin not allowed"
// @Failure 429 {object} errorResponse "throttled"
// @Failure 500 {object} errorResponse "internal error"
// @Router /login [post]
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {

	if !h.originAllowed(r) {
		log.Printf("login refused: disallowed origin %q", r.Header.Get("Origin"))
		httpHelper.RespondWithJSON(w, http.StatusForbidden, errorResponse{Error: "origin not allowed"})
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpHelper.RespondWithJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	ip := clientIP(r)
	userKey := ratelimit.Key("login", "user", strings.ToLower(strings.TrimSpace(req.Username)))
	ipKey := ratelimit.Key("login", "ip", ip)

	userOK := h.userLimiter.Allow(r.Context(), userKey)
	ipOK := h.ipLimiter.Allow(r.Context(), ipKey)
	if !userOK || !ipOK {
		log.Printf("login throttled: user=%q ip=%s (user_ok=%t ip_ok=%t)", req.Username, ip, userOK, ipOK)
		httpHelper.RespondWithJSON(w, http.StatusTooManyRequests, errorResponse{Error: "too many attempts, try later"})
		return
	}

	a, token, err := h.svc.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		log.Printf("login error for %q: %v", req.Username, err)
		if errors.Is(err, password.ErrMalformedHash) || errors.Is(err, password.ErrUnsupportedVersion) {

			httpHelper.RespondWithJSON(w, http.StatusUnauthorized, errorResponse{Error: invalidCredentials})
			return
		}
		httpHelper.RespondWithJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal error"})
		return
	}
	if a == nil {

		httpHelper.RespondWithJSON(w, http.StatusUnauthorized, errorResponse{Error: invalidCredentials})
		return
	}

	h.userLimiter.Reset(r.Context(), userKey)

	http.SetCookie(w, newCookie(h.cfg, token, h.svc.SessionTTL()))
	httpHelper.RespondWithJSON(w, http.StatusOK, LoginResponse{
		PrincipalID: a.ID.String(),
		Username:    a.Username,
	})
}

// @Summary Verify a session
// @Description The auth_request endpoint nginx calls before serving protected pages. On 200 the X-Principal-Id response header carries the principal.
// @Tags auth
// @Produce json
// @Success 200 {object} VerifyResponse
// @Failure 401 "no or unknown session, or unknown API token"
// @Failure 500 "internal error (Redis or Postgres outage)"
// @Router /verify [get]
func (h *Handler) Verify(w http.ResponseWriter, r *http.Request) {

	principalID, err := h.resolvePrincipal(r)
	if errors.Is(err, session.ErrNotFound) || errors.Is(err, token.ErrNotFound) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if err != nil {
		log.Printf("verify error: %v", err)

		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set(PrincipalHeader, principalID)
	httpHelper.RespondWithJSON(w, http.StatusOK, VerifyResponse{PrincipalID: principalID})
}

func (h *Handler) resolvePrincipal(r *http.Request) (string, error) {
	if cookie, err := r.Cookie(h.cfg.CookieName); err == nil {
		return h.svc.Verify(r.Context(), cookie.Value)
	}
	if bearer := bearerToken(r); bearer != "" {
		return h.svc.VerifyAPIToken(r.Context(), bearer)
	}
	return "", session.ErrNotFound
}

func bearerToken(r *http.Request) string {
	const scheme = "bearer "
	v := r.Header.Get("Authorization")
	if len(v) < len(scheme) || !strings.EqualFold(v[:len(scheme)], scheme) {
		return ""
	}
	return strings.TrimSpace(v[len(scheme):])
}

// @Summary Current principal
// @Description "Who am I", answered to the browser from the session cookie.
// @Tags auth
// @Produce json
// @Success 200 {object} MeResponse
// @Failure 401 {object} errorResponse "unauthenticated"
// @Failure 500 {object} errorResponse "internal error"
// @Router /me [get]
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(h.cfg.CookieName)
	if err != nil {
		httpHelper.RespondWithJSON(w, http.StatusUnauthorized, errorResponse{Error: "unauthenticated"})
		return
	}

	a, err := h.svc.Me(r.Context(), cookie.Value)
	if errors.Is(err, session.ErrNotFound) {
		httpHelper.RespondWithJSON(w, http.StatusUnauthorized, errorResponse{Error: "unauthenticated"})
		return
	}
	if err != nil {
		log.Printf("me error: %v", err)
		httpHelper.RespondWithJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal error"})
		return
	}

	httpHelper.RespondWithJSON(w, http.StatusOK, MeResponse{
		PrincipalID: a.ID.String(),
		Username:    a.Username,
	})
}

// @Summary Change password
// @Description Changes the password for the session's principal and rotates the session. Requires the current password.
// @Tags auth
// @Accept json
// @Produce json
// @Param body body ChangePasswordRequest true "current and new password"
// @Success 200 {object} map[string]string
// @Failure 400 {object} errorResponse "malformed body, policy violation, or same password"
// @Failure 401 {object} errorResponse "unauthenticated or wrong current password context"
// @Failure 403 {object} errorResponse "origin not allowed or current password mismatch"
// @Failure 429 {object} errorResponse "throttled"
// @Failure 500 {object} errorResponse "internal error"
// @Router /password [post]
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {

	if !h.originAllowed(r) {
		log.Printf("password change refused: disallowed origin %q", r.Header.Get("Origin"))
		httpHelper.RespondWithJSON(w, http.StatusForbidden, errorResponse{Error: "origin not allowed"})
		return
	}

	var req ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpHelper.RespondWithJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	cookie, err := r.Cookie(h.cfg.CookieName)
	if err != nil {
		httpHelper.RespondWithJSON(w, http.StatusUnauthorized, errorResponse{Error: "unauthenticated"})
		return
	}

	if !h.ipLimiter.Allow(r.Context(), ratelimit.Key("password", "ip", clientIP(r))) {
		log.Printf("password change throttled: ip=%s", clientIP(r))
		httpHelper.RespondWithJSON(w, http.StatusTooManyRequests, errorResponse{Error: "too many attempts, try later"})
		return
	}

	if verr := password.Validate(req.NewPassword); verr != nil {
		httpHelper.RespondWithJSON(w, http.StatusBadRequest, errorResponse{Error: verr.Error()})
		return
	}

	token, err := h.svc.ChangePassword(r.Context(), cookie.Value, req.CurrentPassword, req.NewPassword)
	if err != nil {

		switch {
		case errors.Is(err, session.ErrNotFound):
			httpHelper.RespondWithJSON(w, http.StatusUnauthorized, errorResponse{Error: "unauthenticated"})
		case errors.Is(err, account.ErrAccountNotFound) || errors.Is(err, account.ErrAccountDisabled):
			httpHelper.RespondWithJSON(w, http.StatusUnauthorized, errorResponse{Error: "unauthenticated"})
		case errors.Is(err, account.ErrWrongPassword):
			log.Printf("password change: current password does not match")
			httpHelper.RespondWithJSON(w, http.StatusForbidden, errorResponse{Error: err.Error()})
		case errors.Is(err, account.ErrSamePassword):
			httpHelper.RespondWithJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		default:
			log.Printf("password change error: %v", err)
			httpHelper.RespondWithJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal error"})
		}
		return
	}

	http.SetCookie(w, newCookie(h.cfg, token, h.svc.SessionTTL()))
	httpHelper.RespondWithJSON(w, http.StatusOK, map[string]string{})
}

// @Summary Log out
// @Description Invalidates the session and clears the cookie. Always 204, whether or not there was a session.
// @Tags auth
// @Success 204 "no content"
// @Failure 403 {object} errorResponse "origin not allowed"
// @Failure 500 {object} errorResponse "internal error"
// @Router /logout [post]
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if !h.originAllowed(r) {
		httpHelper.RespondWithJSON(w, http.StatusForbidden, errorResponse{Error: "origin not allowed"})
		return
	}

	if cookie, err := r.Cookie(h.cfg.CookieName); err == nil {
		if err := h.svc.Logout(r.Context(), cookie.Value); err != nil {
			log.Printf("logout error: %v", err)
			httpHelper.RespondWithJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal error"})
			return
		}
	}

	http.SetCookie(w, expiredCookie(h.cfg))
	w.WriteHeader(http.StatusNoContent)
}

// @Summary Health check
// @Description Liveness probe; always answers 200 while the process runs.
// @Tags auth
// @Produce json
// @Success 200 {object} map[string]string
// @Router /health [get]
func (h *Handler) Health(w http.ResponseWriter, _ *http.Request) {
	httpHelper.RespondWithJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if len(h.cfg.AllowedOrigins) > 0 {
		return slices.Contains(h.cfg.AllowedOrigins, origin)
	}
	return origin == selfOrigin(r)
}

func selfOrigin(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
		if r.TLS != nil {
			scheme = "https"
		}
	}

	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}

	return scheme + "://" + host
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
