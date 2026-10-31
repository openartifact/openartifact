// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// exchangeIPLimit caps the exchanges one client address may attempt per
	// minute. A CI fleet behind one NAT address needs headroom; an attacker
	// guessing at tokens is stopped long before a JWT signature could be forged.
	exchangeIPLimit  = 60
	exchangeIPWindow = time.Minute
	// maxExchangeBodyBytes bounds the request body: an ID token is a few KiB.
	maxExchangeBodyBytes = 32 << 10
	maxAuditSubjectLen   = 200
)

// OIDCExchangeStore is the part of the database OIDCExchangeHandler needs.
type OIDCExchangeStore interface {
	auth.CIOIDCStore
	auth.CreateTokenStore
}

// OIDCExchangeHandler trades the ID token of a CI job for a short-lived token of
// the service account an administrator mapped it to (see auth.CIOIDC). The
// endpoint needs no credentials; the ID token is the credential.
type OIDCExchangeHandler struct {
	db             OIDCExchangeStore
	log            *logger.Logger
	verifier       *auth.CIOIDC
	limiter        *slidingWindowLimiter
	trustedProxies *middleware.TrustedProxies
}

// NewOIDCExchangeHandler returns the handler; trustedProxies resolves the client
// address behind a reverse proxy.
func NewOIDCExchangeHandler(db OIDCExchangeStore, log *logger.Logger, trustedProxies *middleware.TrustedProxies) *OIDCExchangeHandler {
	return &OIDCExchangeHandler{
		db:             db,
		log:            log,
		verifier:       auth.NewCIOIDC(db),
		limiter:        newSlidingWindowLimiter(exchangeIPLimit, exchangeIPWindow),
		trustedProxies: trustedProxies,
	}
}

// RegisterPublic mounts the exchange on the unauthenticated router.
func (h *OIDCExchangeHandler) RegisterPublic(r chi.Router) {
	r.Post("/api/v1/auth/oidc/exchange", h.handleExchange)
}

type oidcExchangeRequest struct {
	Token  string `json:"token"`
	Policy string `json:"policy"`
}

type oidcExchangeResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	ExpiresAt   string `json:"expires_at"`
}

func (h *OIDCExchangeHandler) handleExchange(w http.ResponseWriter, r *http.Request) {
	clientIP := middleware.ClientIP(r, h.trustedProxies)

	if wait, ok := h.limiter.take(middleware.LimiterKey(clientIP)); !ok {
		writeRateLimited(w, wait, "too many token exchanges")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxExchangeBodyBytes)

	var body oidcExchangeRequest
	if err := decodeJSON(r, &body); err != nil {
		writeBadJSON(w, err)
		return
	}

	body.Token = strings.TrimSpace(body.Token)
	if body.Token == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "token is required")
		return
	}

	ctx := r.Context()

	match, err := h.verifier.Authenticate(ctx, body.Token, body.Policy)
	if err != nil {
		h.refuse(ctx, w, err, clientIP)
		return
	}

	policy := match.Policy
	expiresAt := time.Now().Add(time.Duration(policy.TokenTTLSeconds) * time.Second)

	// Deploy tokens only: whatever the service account holds, a CI job never
	// reaches the management plane through an exchanged token.
	secret, token, err := auth.CreateToken(ctx, h.db, auth.TokenKindDeploy, "oidc:"+policy.Name, policy.ServiceAccountID, nil, &expiresAt)
	if err != nil {
		h.log.Errorw("oidc exchange: token creation failed", "policy", policy.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "token exchange failed")

		return
	}

	h.auditExchange(ctx, clientIP, audit.AuditEvent{
		Action:       audit.ActionAuthOIDCExchange,
		ActorType:    "token",
		ActorID:      &policy.ServiceAccountID,
		Actor:        policy.ServiceAccountName,
		ResourceType: new("oidc_policy"),
		ResourceID:   &policy.ID,
		TokenID:      token.ID,
		Details: "service account " + policy.ServiceAccountName + " via policy " + policy.Name +
			" for " + clip(match.Subject, maxAuditSubjectLen) + " from " + policy.Issuer,
	})

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, oidcExchangeResponse{
		AccessToken: secret,
		TokenType:   "Bearer",
		ExpiresIn:   policy.TokenTTLSeconds,
		ExpiresAt:   formatTime(expiresAt.UTC()),
	})
}

// refuse answers a failed exchange. Every refusal of the token itself (not
// verifiable, unknown issuer, no policy accepts it) reads the same to the caller,
// so the endpoint tells nothing about which issuers, policies or claims exist;
// the reason goes to the log, and to the audit trail when the token was
// genuine (so the trail shows real CI identities that were turned away, and
// noise cannot flood it).
func (h *OIDCExchangeHandler) refuse(ctx context.Context, w http.ResponseWriter, err error, clientIP string) {
	denial, ok := errors.AsType[*auth.CIOIDCDenialError](err)
	if !ok {
		h.log.Errorw("oidc exchange: policy lookup failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "token exchange failed")

		return
	}

	h.log.Infow("oidc exchange refused", "reason", denial.Reason, "issuer", clip(denial.Issuer, maxAuditSubjectLen), "error", denial.Err)

	if denial.Verified {
		h.auditExchange(ctx, clientIP, audit.AuditEvent{
			Action:       audit.ActionAuthOIDCExchangeFailed,
			ActorType:    "system",
			Outcome:      audit.OutcomeFailure,
			ResourceType: new("oidc_policy"),
			Details: "no policy accepted a token of " + clip(denial.Subject, maxAuditSubjectLen) + " from " + denial.Issuer +
				": " + denial.Reason,
		})
	}

	writeError(w, http.StatusUnauthorized, "invalid_token", "token not accepted")
}

func (h *OIDCExchangeHandler) auditExchange(ctx context.Context, clientIP string, ev audit.AuditEvent) {
	l := audit.Default()
	if l == nil {
		return
	}

	ev.IPAddress = &clientIP

	l.Log(ctx, ev)
}

// clip shortens s to at most n bytes for display.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "..."
}
