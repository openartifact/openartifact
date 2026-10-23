// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/replication"
	"github.com/openartifact/openartifact/pkg/logger"
)

// ReplicationService is blob replication as the admin API sees it;
// *replication.Service implements it.
type ReplicationService interface {
	Status(ctx context.Context) (replication.Status, error)
	// RequestVerify asks for a verify pass; false when one is running or requested.
	RequestVerify(ctx context.Context) (bool, error)
}

// AdminReplicationHandler serves the replication status and verify endpoints.
type AdminReplicationHandler struct {
	svc       ReplicationService
	log       *logger.Logger
	evaluator *auth.Evaluator
}

// NewAdminReplicationHandler creates the handler. svc is nil when no replica
// is configured: the status endpoint then reports enabled=false.
func NewAdminReplicationHandler(svc ReplicationService, log *logger.Logger, evaluator *auth.Evaluator) *AdminReplicationHandler {
	return &AdminReplicationHandler{svc: svc, log: log, evaluator: evaluator}
}

// Register mounts the routes. They are static paths, so they win over the
// parameterized /api/v1/admin/storage/{name}: a storage backend that is named
// "replication" is reached through the list instead.
func (h *AdminReplicationHandler) Register(r chi.Router) {
	r.Get("/api/v1/admin/storage/replication", h.handleStatus)
	r.Post("/api/v1/admin/storage/replication/verify", h.handleVerify)
}

func (h *AdminReplicationHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	if h.svc == nil {
		writeJSON(w, http.StatusOK, responses.ReplicationStatusResponse{})

		return
	}

	st, err := h.svc.Status(r.Context())
	if err != nil {
		h.log.Errorw("failed to read replication status", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to read replication status")

		return
	}

	writeJSON(w, http.StatusOK, replicationStatusResponse(st))
}

func (h *AdminReplicationHandler) handleVerify(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	if h.svc == nil {
		writeError(w, http.StatusNotFound, "not_found", "blob replication is not configured (storage.replica.type is empty)")

		return
	}

	requested, err := h.svc.RequestVerify(r.Context())
	if err != nil {
		h.log.Errorw("failed to request a replication verify pass", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to request a verify pass")

		return
	}

	if !requested {
		writeError(w, http.StatusConflict, "conflict", "a verify pass is already running or requested")

		return
	}

	auditAdmin(r, audit.ActionSystemStorageBackendChanged, "storage_replication", "", "replication verify requested")
	writeJSON(w, http.StatusAccepted, responses.ReplicationVerifyRequestedResponse{Requested: true})
}

func replicationStatusResponse(st replication.Status) responses.ReplicationStatusResponse {
	return responses.ReplicationStatusResponse{
		Enabled:                 st.Enabled,
		Replica:                 &responses.ReplicaInfoResponse{Type: st.Replica.Type, Location: st.Replica.Location},
		ReadFallback:            st.ReadFallback,
		BacklogCopy:             st.BacklogCopy,
		BacklogDelete:           st.BacklogDelete,
		OldestPendingAgeSeconds: st.OldestPendingSeconds,
		Failing:                 st.Failing,
		ReplicatedTotal:         st.ReplicatedTotal,
		ReplicatedBytesTotal:    st.ReplicatedBytes,
		LastError:               st.LastError,
		LastErrorAt:             st.LastErrorAt,
		VerifyRequested:         st.VerifyRequested,
		VerifyRunning:           replicationVerifyResponse(st.VerifyRunning),
		LastVerify:              replicationVerifyResponse(st.LastVerify),
	}
}

func replicationVerifyResponse(v *replication.VerifyResult) *responses.ReplicationVerifyResponse {
	if v == nil {
		return nil
	}

	return &responses.ReplicationVerifyResponse{
		StartedAt: v.StartedAt, FinishedAt: v.FinishedAt, Deep: v.Deep, Listing: v.Listing, ReplicaListed: v.ReplicaListed,
		Checked: v.Checked, MissingReplica: v.MissingReplica, MissingPrimary: v.MissingPrimary, Mismatched: v.Mismatched,
		Repaired: v.Repaired, Unreferenced: v.Unreferenced, Lost: v.Lost, Errors: v.Errors,
	}
}
