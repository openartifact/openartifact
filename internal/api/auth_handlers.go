// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

func formatTime(t time.Time) string {
	return t.Format(time.RFC3339Nano)
}

func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}

	s := t.Format(time.RFC3339Nano)

	return &s
}

const (
	// loginIPLimit is the number of login requests a single client IP may
	// make per loginIPWindow. It throttles request volume only; guessing
	// passwords for one account from one IP is bounded separately by the
	// per-(username, IP) failure limiter.
	loginIPLimit  = 10
	loginIPWindow = time.Minute

	// loginUserFailureLimit failures within loginUserFailureWindow lock a
	// (username, client IP) pair out of logging in until the oldest failure
	// ages out. Keying by IP as well means an attacker can only lock out their
	// own address, never the legitimate user connecting from elsewhere. A
	// sliding window (rather than a permanent lock) keeps this from being a
	// durable denial of service.
	loginUserFailureLimit  = 10
	loginUserFailureWindow = 15 * time.Minute

	// loginDistributedFailureLimit bounds failures for one username across all
	// IPs in the same window, so guessing spread over many addresses is still
	// stopped. It is deliberately 10x the per-pair limit: holding an account
	// locked then takes a distributed effort, not one host.
	loginDistributedFailureLimit = 100

	// limiterMaxKeys bounds the memory a limiter may use. An attacker
	// rotating source IPs or usernames cannot grow the map past this.
	limiterMaxKeys = 10000

	// maxLoginFieldBytes caps username/password sizes accepted by login so a
	// huge value never reaches the hasher or the limiter key space.
	maxLoginFieldBytes = auth.MaxPasswordLength
)

// slidingWindowLimiter counts events per key inside a sliding time window.
// Stale keys are swept lazily (at most once per window) and the number of
// tracked keys is capped, so memory stays bounded without a background
// goroutine.
type slidingWindowLimiter struct {
	mu        sync.Mutex
	limit     int
	window    time.Duration
	maxKeys   int
	events    map[string][]time.Time
	lastSweep time.Time
	now       func() time.Time
}

func newSlidingWindowLimiter(limit int, window time.Duration) *slidingWindowLimiter {
	return &slidingWindowLimiter{
		limit:   limit,
		window:  window,
		maxKeys: limiterMaxKeys,
		events:  make(map[string][]time.Time),
		now:     time.Now,
	}
}

// live drops events older than the window and returns what is left.
func (l *slidingWindowLimiter) live(key string, now time.Time) []time.Time {
	hits := l.events[key]
	cutoff := now.Add(-l.window)

	i := 0
	for i < len(hits) && !hits[i].After(cutoff) {
		i++
	}

	if i > 0 {
		hits = hits[i:]
		if len(hits) == 0 {
			delete(l.events, key)
			return nil
		}

		l.events[key] = hits
	}

	return hits
}

// sweep removes every key whose events have all expired. It runs at most once
// per window.
func (l *slidingWindowLimiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < l.window {
		return
	}

	l.lastSweep = now

	for key := range l.events {
		l.live(key, now)
	}
}

// retryAfter reports whether key has exhausted its budget and, if so, how long
// until the oldest event leaves the window.
func (l *slidingWindowLimiter) retryAfter(key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweep(now)

	hits := l.live(key, now)
	if len(hits) < l.limit {
		return 0, false
	}

	return max(hits[0].Add(l.window).Sub(now), time.Second), true
}

// record adds one event for key.
func (l *slidingWindowLimiter) record(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.recordLocked(key, l.now())
}

func (l *slidingWindowLimiter) recordLocked(key string, now time.Time) {
	l.sweep(now)

	hits := l.live(key, now)
	if hits == nil && len(l.events) >= l.maxKeys && !l.evictUnlockedLocked() {
		// Full of keys that are all at their limit (locked out): never evict
		// those, or an attacker could flush a victim's lockout. Not tracking
		// the new key is the lesser evil.
		return
	}

	hits = append(hits, now)
	if len(hits) > l.limit {
		// Concurrent requests can overshoot the budget by a few events;
		// never retain more than the limit per key.
		hits = hits[len(hits)-l.limit:]
	}

	l.events[key] = hits
}

// evictUnlockedLocked removes the key below its limit whose latest event is
// oldest, reporting whether room was made. Keys at their limit (locked out)
// are never evicted.
func (l *slidingWindowLimiter) evictUnlockedLocked() bool {
	var (
		victim string
		oldest time.Time
		found  bool
	)

	for key, hits := range l.events {
		if len(hits) >= l.limit {
			continue
		}

		if last := hits[len(hits)-1]; !found || last.Before(oldest) {
			victim, oldest, found = key, last, true
		}
	}

	if found {
		delete(l.events, victim)
	}

	return found
}

// take counts one event for key and reports whether it fit in the budget. On
// refusal it returns how long the caller must wait.
func (l *slidingWindowLimiter) take(key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweep(now)

	hits := l.live(key, now)
	if len(hits) >= l.limit {
		return max(hits[0].Add(l.window).Sub(now), time.Second), false
	}

	l.recordLocked(key, now)

	return 0, true
}

// reset forgets key (used after a successful login).
func (l *slidingWindowLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.events, key)
}

func retryAfterSeconds(d time.Duration) string {
	return strconv.Itoa(int(math.Ceil(d.Seconds())))
}

// limiterKey normalises a user-supplied username for the failure limiter.
func limiterKey(prefix, username string) string {
	key := strings.ToLower(strings.TrimSpace(username))
	if len(key) > 256 {
		key = key[:256]
	}

	return prefix + key
}

type AuthHandler struct {
	db               database.Database
	log              *logger.Logger
	evaluator        *auth.Evaluator
	loginIPLimiter   *slidingWindowLimiter
	loginUserLimiter *slidingWindowLimiter
	// loginPairLimiter counts login failures per (username, IP);
	// loginAnyIPLimiter per username across all IPs.
	loginPairLimiter  *slidingWindowLimiter
	loginAnyIPLimiter *slidingWindowLimiter
	trustedProxies    *middleware.TrustedProxies
	baseURL           string

	proxyWarnOnce sync.Once
}

func NewAuthHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, trustedProxies *middleware.TrustedProxies) *AuthHandler {
	return &AuthHandler{
		db:                db,
		log:               log,
		evaluator:         evaluator,
		loginIPLimiter:    newSlidingWindowLimiter(loginIPLimit, loginIPWindow),
		loginUserLimiter:  newSlidingWindowLimiter(loginUserFailureLimit, loginUserFailureWindow),
		loginPairLimiter:  newSlidingWindowLimiter(loginUserFailureLimit, loginUserFailureWindow),
		loginAnyIPLimiter: newSlidingWindowLimiter(loginDistributedFailureLimit, loginUserFailureWindow),
		trustedProxies:    trustedProxies,
	}
}

// WithBaseURL sets the public base URL; an https value forces Secure cookies.
func (h *AuthHandler) WithBaseURL(baseURL string) *AuthHandler {
	h.baseURL = baseURL

	return h
}

// warnIfBehindUnconfiguredProxy logs, once per process, that forwarding headers
// are arriving although no trusted proxy is configured. In that state every
// client behind the proxy shares the proxy's address, so the per-IP login
// throttle (and the IP recorded in audit rows and sessions) is wrong.
func (h *AuthHandler) warnIfBehindUnconfiguredProxy(r *http.Request) {
	if h.trustedProxies.Configured() || h.log == nil {
		return
	}

	if r.Header.Get("X-Forwarded-For") == "" && r.Header.Get("X-Real-IP") == "" {
		return
	}

	h.proxyWarnOnce.Do(func() {
		h.log.Warnw("forwarding headers received but server.trusted_proxies is empty: "+
			"all clients behind the reverse proxy share one IP for login throttling, sessions and audit; "+
			"set server.trusted_proxies to the proxy's CIDR",
			"peer", r.RemoteAddr)
	})
}

func (h *AuthHandler) RegisterPublic(r chi.Router) {
	r.Post("/api/v1/auth/login", h.handleLogin)
	r.Post("/api/v1/auth/logout", h.handleLogout)
}

func (h *AuthHandler) RegisterAuthed(r chi.Router) {
	r.Get("/api/v1/auth/me", h.handleMe)
	r.Post("/api/v1/auth/change-password", h.handleChangePassword)
}

func writeRateLimited(w http.ResponseWriter, wait time.Duration, message string) {
	w.Header().Set("Retry-After", retryAfterSeconds(wait))
	writeError(w, http.StatusTooManyRequests, "rate_limited", message)
}

// readLoginCredentials extracts username/password from a JSON body. Form
// encodings are refused: they are the only bodies a cross-site <form> can send
// without a CORS preflight, which would enable login CSRF.
func readLoginCredentials(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return "", "", false
	}

	return body.Username, body.Password, true
}

func (h *AuthHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !requireJSONSameOrigin(w, r) {
		return
	}

	// Real client IP, honoring X-Forwarded-For/X-Real-IP only from a
	// configured trusted proxy — never trust it from the client directly.
	clientIP := middleware.ClientIP(r, h.trustedProxies)
	ip := middleware.LimiterKey(clientIP)

	h.warnIfBehindUnconfiguredProxy(r)

	if wait, ok := h.loginIPLimiter.take(ip); !ok {
		writeRateLimited(w, wait, "too many login attempts")
		return
	}

	username, password, ok := readLoginCredentials(w, r)
	if !ok {
		return
	}

	// Per-(username, IP) throttle: an attacker can only lock themselves out,
	// never the legitimate user connecting from another address. It applies to every submitted name, existing or
	// not, so a 429 reveals nothing about which accounts exist.
	userKey := limiterKey("login:", username)
	pairKey := userKey + "|" + ip

	wait, blocked := h.loginPairLimiter.retryAfter(pairKey)
	if !blocked {
		wait, blocked = h.loginAnyIPLimiter.retryAfter(userKey)
	}

	if blocked {
		writeRateLimited(w, wait, "too many failed login attempts for this account")
		return
	}

	invalid := func() {
		h.loginPairLimiter.record(pairKey)
		h.loginAnyIPLimiter.record(userKey)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
	}

	if len(username) > maxLoginFieldBytes || len(password) > maxLoginFieldBytes {
		invalid()
		return
	}

	ctx := r.Context()

	user, err := h.db.GetUserByUsername(ctx, username)
	if err != nil {
		h.log.Errorw("login: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	// Every miss path still pays for one full Argon2 verification so response
	// time does not reveal whether the username exists, is disabled, or is
	// SSO-only.
	if user == nil || user.Disabled || user.IsService() || user.PasswordHash == nil {
		auth.BurnPasswordVerification(password)
		invalid()

		return
	}

	verified, err := auth.VerifyPassword(password, *user.PasswordHash)
	if err != nil || !verified {
		invalid()
		return
	}

	h.loginPairLimiter.reset(pairKey)
	h.loginAnyIPLimiter.reset(userKey)

	csrfToken, err := middleware.NewCSRFToken()
	if err != nil {
		h.log.Errorw("login: generate CSRF token", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "session creation failed")

		return
	}

	cookieValue, _, err := auth.CreateSession(ctx, h.db, user.ID, clientIP, r.UserAgent())
	if err != nil {
		h.log.Errorw("login: create session", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "session creation failed")

		return
	}

	secure := isSecureContext(r, h.baseURL)
	setSessionCookie(w, secure, cookieValue)
	setCSRFCookie(w, secure, csrfToken)

	now := time.Now()
	user.LastLoginAt = &now
	_ = h.db.UpdateUserLastLogin(ctx, user.ID, now)

	writeJSON(w, http.StatusOK, userToResponse(user))
}

// handleLogout ends the local session. For a session created by an OIDC
// provider whose IdP supports RP-initiated logout it answers 200 with
// {"end_session_url"} for the SPA to navigate to, else 204.
func (h *AuthHandler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !requireSameOrigin(w, r) {
		return
	}

	completeLogout(w, r, h.db, h.log, h.baseURL, "")
}

func (h *AuthHandler) handleMe(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r)
	if p == nil || p.IsAnonymous || p.UserID == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	ctx := r.Context()

	user, err := h.db.GetUserByID(ctx, *p.UserID)
	if err != nil {
		h.log.Errorw("me: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "user not found")
		return
	}

	isAdmin, _ := principalIsAdmin(ctx, p, h.evaluator)

	// Include group names for the frontend session store.
	var groupNames []string

	groups, err := h.db.ListGroupsByUser(ctx, user.ID)
	if err == nil {
		groupNames = make([]string, 0, len(groups))
		for _, g := range groups {
			groupNames = append(groupNames, g.Name)
		}
	}

	resp := responses.UserMeResponse{
		ID:                 user.ID,
		Username:           user.Username,
		Email:              user.Email,
		FullName:           user.FullName,
		Disabled:           user.Disabled,
		MustChangePassword: user.MustChangePassword,
		IsAdmin:            isAdmin,
		Groups:             groupNames,
		Projects:           h.projectRoles(ctx, p, user.ID),
		LastLoginAt:        formatTimePtr(user.LastLoginAt),
		CreatedAt:          formatTime(user.CreatedAt),
		UpdatedAt:          formatTime(user.UpdatedAt),
	}

	writeJSON(w, http.StatusOK, resp)
}

// projectRoles lists the caller's project roles for the session store. A
// failure leaves the list empty: /auth/me must keep working.
func (h *AuthHandler) projectRoles(ctx context.Context, p *auth.Principal, userID string) []responses.ProjectRoleResponse {
	roles, err := h.evaluator.ProjectRoles(ctx, *p)
	if err != nil || len(roles) == 0 {
		if err != nil {
			h.log.Errorw("me: project roles", "error", err)
		}

		return []responses.ProjectRoleResponse{}
	}

	projects, err := h.db.ListProjects(ctx, userID)
	if err != nil {
		h.log.Errorw("me: list projects", "error", err)
		return []responses.ProjectRoleResponse{}
	}

	out := make([]responses.ProjectRoleResponse, 0, len(roles))

	for _, project := range projects {
		if role := roles[project.ID]; role != "" {
			out = append(out, responses.ProjectRoleResponse{Name: project.Name, Role: role})
		}
	}

	return out
}

func (h *AuthHandler) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r)
	if p == nil || p.IsAnonymous || p.UserID == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	// A leaked API token must never be able to take over the account by
	// rotating its password: only an interactive session may do that.
	if p.IsToken() {
		writeError(w, http.StatusForbidden, "forbidden", "API tokens cannot change passwords")
		return
	}

	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	ctx := r.Context()

	user, err := h.db.GetUserByID(ctx, *p.UserID)
	if err != nil || user == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	if user.PasswordHash == nil {
		writeError(w, http.StatusBadRequest, "no_local_password", "user has no local password")
		return
	}

	// A stolen session must not be able to brute-force the current password
	// without bound, so wrong guesses share the failure budget.
	guessKey := limiterKey("change-password:", user.ID)
	if wait, blocked := h.loginUserLimiter.retryAfter(guessKey); blocked {
		writeRateLimited(w, wait, "too many failed attempts")
		return
	}

	if len(body.CurrentPassword) > maxLoginFieldBytes {
		h.loginUserLimiter.record(guessKey)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect")

		return
	}

	ok, err := auth.VerifyPassword(body.CurrentPassword, *user.PasswordHash)
	if err != nil || !ok {
		h.loginUserLimiter.record(guessKey)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect")

		return
	}

	h.loginUserLimiter.reset(guessKey)

	if !checkPasswordPolicy(w, body.NewPassword) {
		return
	}

	newHash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "password hashing failed")
		return
	}

	if err := h.db.UpdateUserPassword(ctx, user.ID, newHash, false); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	// The old password may be known to an attacker who holds another
	// session: drop every session except the one making this request.
	if err := auth.RevokeOtherSessions(ctx, h.db, user.ID, sessionCookieValue(r)); err != nil {
		h.log.Errorw("change password: revoke other sessions", "error", err, "user", user.ID)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to revoke other sessions")

		return
	}

	writeNoContent(w)
}

func userToResponse(u *models.User) responses.UserResponse {
	return responses.UserResponse{
		ID:                 u.ID,
		Username:           u.Username,
		Email:              u.Email,
		FullName:           u.FullName,
		Disabled:           u.Disabled,
		MustChangePassword: u.MustChangePassword,
		LastLoginAt:        formatTimePtr(u.LastLoginAt),
		CreatedAt:          formatTime(u.CreatedAt),
		UpdatedAt:          formatTime(u.UpdatedAt),
	}
}
