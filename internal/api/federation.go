// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/federation"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

// peerNameRe bounds peer and token names: they appear in logs, metrics labels
// and the UI.
var peerNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

const (
	maxPEMBytes            = 64 << 10
	maxFederationTokenSize = 512
	deferredListLimit      = 200
)

// FederationPeerResponse is a federation peer with its replication state.
type FederationPeerResponse struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	BaseURL         string     `json:"base_url"`
	Enabled         bool       `json:"enabled"`
	State           string     `json:"state"`
	CursorSeq       int64      `json:"cursor_seq"`
	RemoteHeadSeq   int64      `json:"remote_head_seq"`
	BacklogUnits    int64      `json:"backlog_units"`
	LagSeconds      float64    `json:"lag_seconds"`
	LastAttemptAt   *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt   *time.Time `json:"last_success_at,omitempty"`
	NextAttemptAt   *time.Time `json:"next_attempt_at,omitempty"`
	LastError       string     `json:"last_error"`
	ErrorCount      int        `json:"error_count"`
	AppliedTotal    int64      `json:"applied_total"`
	ConflictsTotal  int64      `json:"conflicts_total"`
	DeferredUnits   int        `json:"deferred_units"`
	ResyncRequested bool       `json:"resync_requested"`
	HasCAPEM        bool       `json:"has_ca_pem"`
	HasClientCert   bool       `json:"has_client_cert"`
	CreatedAt       time.Time  `json:"created_at"`
}

// FederationTokenResponse is a peer token; the secret is shown once, at creation.
type FederationTokenResponse struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// FederationTokenCreatedResponse carries the new token's secret.
type FederationTokenCreatedResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
}

// FederationDeferredResponse is a unit set aside after failing to apply.
type FederationDeferredResponse struct {
	UnitKey       string    `json:"unit_key"`
	Seq           int64     `json:"seq"`
	Attempts      int       `json:"attempts"`
	Reason        string    `json:"reason"`
	LastError     string    `json:"last_error"`
	FirstSeenAt   time.Time `json:"first_seen_at"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
}

// FederationStatusResponse is the federation state of a repository.
type FederationStatusResponse struct {
	Enabled    bool                      `json:"enabled"`
	Supported  bool                      `json:"supported"`
	InstanceID string                    `json:"instance_id"`
	LogID      string                    `json:"log_id"`
	HeadSeq    int64                     `json:"head_seq"`
	Units      int64                     `json:"units"`
	Tombstones int64                     `json:"tombstones"`
	Peers      []FederationPeerResponse  `json:"peers"`
	Tokens     []FederationTokenResponse `json:"tokens"`
}

// FederationHandler serves the federation admin API of a repository.
type FederationHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	store     *federation.Store
	// serverEnabled is federation.enabled: without it no pull worker or peer
	// endpoint runs, so a repository cannot be federated.
	serverEnabled bool
	// kick wakes the pull scheduler after a peer change (nil: nothing to wake).
	kick func()
}

// NewFederationHandler creates the handler.
func NewFederationHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, store *federation.Store, serverEnabled bool, kick func()) *FederationHandler {
	return &FederationHandler{db: db, log: log, evaluator: evaluator, store: store, serverEnabled: serverEnabled, kick: kick}
}

// RegisterAuthed mounts the routes on the authenticated API router.
func (h *FederationHandler) RegisterAuthed(r chi.Router) {
	r.Get("/api/v1/repositories/{name}/federation", h.handleStatus)
	r.Put("/api/v1/repositories/{name}/federation", h.handleSetEnabled)
	r.Post("/api/v1/repositories/{name}/federation/peers", h.handleCreatePeer)
	r.Patch("/api/v1/repositories/{name}/federation/peers/{id}", h.handleUpdatePeer)
	r.Delete("/api/v1/repositories/{name}/federation/peers/{id}", h.handleDeletePeer)
	r.Post("/api/v1/repositories/{name}/federation/peers/{id}/resync", h.handleResync)
	r.Get("/api/v1/repositories/{name}/federation/peers/{id}/deferred", h.handleDeferred)
	r.Post("/api/v1/repositories/{name}/federation/tokens", h.handleCreateToken)
	r.Delete("/api/v1/repositories/{name}/federation/tokens/{id}", h.handleRevokeToken)
}

// repo authorizes the caller (repository `manage`, or project maintainer) and
// loads the repository; it answers the error itself and reports false.
func (h *FederationHandler) repo(w http.ResponseWriter, r *http.Request) (*models.Repository, bool) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	p := currentPrincipal(r)
	if p == nil || p.IsAnonymous {
		writeError(w, http.StatusForbidden, "forbidden", "access denied")

		return nil, false
	}

	allowed, err := h.evaluator.Evaluate(ctx, *p, name, "", "manage")
	if err == nil && !allowed {
		allowed, err = h.evaluator.EvaluateRepoAdmin(ctx, *p, name)
	}

	if err != nil {
		h.log.Errorw("federation: auth evaluation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")

		return nil, false
	}

	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "access denied")

		return nil, false
	}

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("federation: load repository", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return nil, false
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")

		return nil, false
	}

	return repo, true
}

func (h *FederationHandler) supported(repo *models.Repository) bool {
	return h.serverEnabled && repo.RepoType == repotype.Hosted && repotype.FederationSupported(repo.Format)
}

func (h *FederationHandler) internalError(w http.ResponseWriter, what string, err error) {
	h.log.Errorw("federation: "+what, "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "database error")
}

// enabledRepo is repo for the routes that need a federated repository: 409
// otherwise.
func (h *FederationHandler) enabledRepo(w http.ResponseWriter, r *http.Request) (*models.Repository, bool) {
	repo, ok := h.repo(w, r)
	if !ok {
		return nil, false
	}

	if _, err := h.store.RepoState(r.Context(), repo.ID); err != nil {
		if errors.Is(err, federation.ErrNotFederated) {
			writeError(w, http.StatusConflict, "federation_not_enabled", "federation is not enabled for this repository")

			return nil, false
		}

		h.internalError(w, "read state", err)

		return nil, false
	}

	return repo, true
}

func peerResponse(p *federation.Peer, now time.Time) FederationPeerResponse {
	return FederationPeerResponse{
		ID: p.ID, Name: p.Name, BaseURL: p.BaseURL, Enabled: p.Enabled, State: string(p.State()),
		CursorSeq: p.CursorSeq, RemoteHeadSeq: p.RemoteHeadSeq, BacklogUnits: p.Backlog(),
		LagSeconds:    p.Lag(now).Seconds(),
		LastAttemptAt: p.LastAttemptAt, LastSuccessAt: p.LastSuccessAt, NextAttemptAt: p.NextAttemptAt,
		LastError: p.LastError, ErrorCount: p.ErrorCount, AppliedTotal: p.AppliedTotal, ConflictsTotal: p.ConflictsTotal,
		DeferredUnits: p.DeferredUnits, ResyncRequested: p.ResyncRequested,
		HasCAPEM: p.CAPEM != "", HasClientCert: p.ClientCertEnc != "", CreatedAt: p.CreatedAt,
	}
}

func tokenResponse(t *federation.Token) FederationTokenResponse {
	return FederationTokenResponse{ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, RevokedAt: t.RevokedAt}
}

// status builds the federation status of a repository.
func (h *FederationHandler) status(ctx context.Context, repo *models.Repository) (*FederationStatusResponse, error) {
	out := &FederationStatusResponse{
		Supported: h.supported(repo),
		Peers:     []FederationPeerResponse{},
		Tokens:    []FederationTokenResponse{},
	}

	instance, err := h.store.InstanceID(ctx)
	if err != nil {
		return nil, err //nolint:wrapcheck // logged by the caller
	}

	out.InstanceID = instance.String()

	st, err := h.store.RepoState(ctx, repo.ID)
	if errors.Is(err, federation.ErrNotFederated) {
		return out, nil
	}

	if err != nil {
		return nil, err //nolint:wrapcheck // logged by the caller
	}

	out.Enabled, out.LogID, out.HeadSeq = true, st.LogID.String(), st.HeadSeq

	if out.Units, out.Tombstones, err = h.store.Counts(ctx, repo.ID); err != nil {
		return nil, err //nolint:wrapcheck // logged by the caller
	}

	peers, err := h.store.ListPeers(ctx, repo.ID)
	if err != nil {
		return nil, err //nolint:wrapcheck // logged by the caller
	}

	now := time.Now()

	for _, p := range peers {
		out.Peers = append(out.Peers, peerResponse(p, now))
	}

	tokens, err := h.store.ListTokens(ctx, repo.ID)
	if err != nil {
		return nil, err //nolint:wrapcheck // logged by the caller
	}

	for _, t := range tokens {
		out.Tokens = append(out.Tokens, tokenResponse(t))
	}

	return out, nil
}

func (h *FederationHandler) writeStatus(ctx context.Context, w http.ResponseWriter, repo *models.Repository, code int) {
	st, err := h.status(ctx, repo)
	if err != nil {
		h.internalError(w, "build status", err)

		return
	}

	writeJSON(w, code, st)
}

func (h *FederationHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.repo(w, r)
	if !ok {
		return
	}

	h.writeStatus(r.Context(), w, repo, http.StatusOK)
}

func (h *FederationHandler) handleSetEnabled(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.repo(w, r)
	if !ok {
		return
	}

	var body struct {
		Enabled *bool `json:"enabled"`
	}

	if err := decodeJSON(r, &body); err != nil || body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: enabled is required")

		return
	}

	if *body.Enabled {
		if !h.serverEnabled {
			writeError(w, http.StatusConflict, "federation_disabled",
				"federation is turned off on this server (federation.enabled / OPENARTIFACT_FEDERATION_ENABLED)")

			return
		}

		if !h.supported(repo) {
			writeError(w, http.StatusBadRequest, "validation_failed", "only hosted repositories of formats with federation support can be federated")

			return
		}

		if err := h.store.Enable(r.Context(), repo.ID, repo.Format); err != nil {
			h.internalError(w, "enable", err)

			return
		}
	} else if err := h.store.Disable(r.Context(), repo.ID); err != nil {
		h.internalError(w, "disable", err)

		return
	}

	h.writeStatus(r.Context(), w, repo, http.StatusOK)
}

type peerBody struct {
	Name       *string `json:"name"`
	BaseURL    *string `json:"base_url"`
	Token      *string `json:"token"`
	CAPEM      *string `json:"ca_pem"`
	ClientCert *string `json:"client_cert"`
	ClientKey  *string `json:"client_key"`
	Enabled    *bool   `json:"enabled"`
}

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

// validate checks the fields that are present; a message is returned for the
// first invalid one.
func (b *peerBody) validate(ctx context.Context) string {
	if b.Name != nil && !peerNameRe.MatchString(*b.Name) {
		return "name must be 1-63 characters: letters, digits, '.', '_' or '-', starting with a letter or digit"
	}

	if b.BaseURL != nil {
		if err := federation.ValidatePeerURL(ctx, *b.BaseURL); err != nil {
			return err.Error()
		}
	}

	if b.Token != nil && (*b.Token == "" || len(*b.Token) > maxFederationTokenSize) {
		return "token must be the token the peer issued for this repository"
	}

	for _, pem := range []*string{b.CAPEM, b.ClientCert, b.ClientKey} {
		if pem != nil && len(*pem) > maxPEMBytes {
			return "PEM values are limited to 64 KiB"
		}
	}

	if b.CAPEM != nil || b.ClientCert != nil || b.ClientKey != nil {
		if err := federation.ValidatePeerTLS(strOrEmpty(b.CAPEM), strOrEmpty(b.ClientCert), strOrEmpty(b.ClientKey)); err != nil {
			return err.Error()
		}
	}

	return ""
}

func (h *FederationHandler) handleCreatePeer(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.enabledRepo(w, r)
	if !ok {
		return
	}

	var body peerBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")

		return
	}

	if body.Name == nil || body.BaseURL == nil || body.Token == nil {
		writeError(w, http.StatusBadRequest, "validation_failed", "name, base_url and token are required")

		return
	}

	if msg := body.validate(r.Context()); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)

		return
	}

	peer, err := h.store.CreatePeer(r.Context(), federation.NewPeer{
		RepoID: repo.ID, Name: *body.Name, BaseURL: *body.BaseURL, Token: *body.Token,
		CAPEM: strOrEmpty(body.CAPEM), ClientCert: strOrEmpty(body.ClientCert), ClientKey: strOrEmpty(body.ClientKey),
	})
	if errors.Is(err, federation.ErrConflict) {
		writeError(w, http.StatusConflict, "conflict", "a peer with this name already exists")

		return
	}

	if err != nil {
		h.internalError(w, "create peer", err)

		return
	}

	h.wake()
	writeJSON(w, http.StatusCreated, peerResponse(peer, time.Now()))
}

func (h *FederationHandler) wake() {
	if h.kick != nil {
		h.kick()
	}
}

func (h *FederationHandler) handleUpdatePeer(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.enabledRepo(w, r)
	if !ok {
		return
	}

	var body peerBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")

		return
	}

	if msg := body.validate(r.Context()); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)

		return
	}

	peer, err := h.store.UpdatePeer(r.Context(), repo.ID, urlparams.Vars(r)["id"], federation.PeerPatch{
		Name: body.Name, BaseURL: body.BaseURL, Token: body.Token, CAPEM: body.CAPEM,
		ClientCert: body.ClientCert, ClientKey: body.ClientKey, Enabled: body.Enabled,
	})

	switch {
	case errors.Is(err, federation.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "peer not found")
	case errors.Is(err, federation.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "a peer with this name already exists")
	case err != nil:
		h.internalError(w, "update peer", err)
	default:
		h.wake()
		writeJSON(w, http.StatusOK, peerResponse(peer, time.Now()))
	}
}

func (h *FederationHandler) handleDeletePeer(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.enabledRepo(w, r)
	if !ok {
		return
	}

	err := h.store.DeletePeer(r.Context(), repo.ID, urlparams.Vars(r)["id"])

	switch {
	case errors.Is(err, federation.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "peer not found")
	case err != nil:
		h.internalError(w, "delete peer", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *FederationHandler) handleResync(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.enabledRepo(w, r)
	if !ok {
		return
	}

	id := urlparams.Vars(r)["id"]

	err := h.store.RequestResync(r.Context(), repo.ID, id)
	if errors.Is(err, federation.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "peer not found")

		return
	}

	if err != nil {
		h.internalError(w, "request resync", err)

		return
	}

	peer, err := h.store.GetPeer(r.Context(), repo.ID, id)
	if err != nil {
		h.internalError(w, "read peer", err)

		return
	}

	h.wake()
	writeJSON(w, http.StatusAccepted, peerResponse(peer, time.Now()))
}

func (h *FederationHandler) handleDeferred(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.enabledRepo(w, r)
	if !ok {
		return
	}

	id := urlparams.Vars(r)["id"]

	if _, err := h.store.GetPeer(r.Context(), repo.ID, id); err != nil {
		if errors.Is(err, federation.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "peer not found")

			return
		}

		h.internalError(w, "read peer", err)

		return
	}

	rows, err := h.store.ListDeferred(r.Context(), id, false, deferredListLimit)
	if err != nil {
		h.internalError(w, "list deferred units", err)

		return
	}

	items := make([]FederationDeferredResponse, 0, len(rows))
	for _, d := range rows {
		items = append(items, FederationDeferredResponse{
			UnitKey: d.UnitKey, Seq: d.Seq, Attempts: d.Attempts, Reason: d.Reason, LastError: d.LastError,
			FirstSeenAt: d.FirstSeenAt, NextAttemptAt: d.NextAttemptAt,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *FederationHandler) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.enabledRepo(w, r)
	if !ok {
		return
	}

	var body struct {
		Name string `json:"name"`
	}

	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")

		return
	}

	if !peerNameRe.MatchString(body.Name) {
		writeError(w, http.StatusBadRequest, "validation_failed", "name must be 1-63 characters: letters, digits, '.', '_' or '-', starting with a letter or digit")

		return
	}

	createdBy := ""
	if p := currentPrincipal(r); p != nil {
		createdBy = p.Username
	}

	tok, secret, err := h.store.CreateToken(r.Context(), repo.ID, body.Name, createdBy)
	if err != nil {
		h.internalError(w, "create token", err)

		return
	}

	writeJSON(w, http.StatusCreated, FederationTokenCreatedResponse{ID: tok.ID, Name: tok.Name, Token: secret, CreatedAt: tok.CreatedAt})
}

func (h *FederationHandler) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.enabledRepo(w, r)
	if !ok {
		return
	}

	err := h.store.RevokeToken(r.Context(), repo.ID, urlparams.Vars(r)["id"])

	switch {
	case errors.Is(err, federation.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "token not found")
	case err != nil:
		h.internalError(w, "revoke token", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
