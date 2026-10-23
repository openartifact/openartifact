// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/recovery"
	"github.com/openartifact/openartifact/pkg/logger"
)

const recoveryStatusTimeout = 15 * time.Second

// RecoveryStatusSource reports the disaster-recovery journal state.
type RecoveryStatusSource interface {
	Status(ctx context.Context) (recovery.Status, error)
}

// AdminRecoveryHandler serves /api/v1/admin/recovery: the state of the row
// journal that makes `openartifact rebuild` possible.
type AdminRecoveryHandler struct {
	src       RecoveryStatusSource
	log       *logger.Logger
	evaluator *auth.Evaluator
}

// NewAdminRecoveryHandler creates the handler.
func NewAdminRecoveryHandler(src RecoveryStatusSource, log *logger.Logger, evaluator *auth.Evaluator) *AdminRecoveryHandler {
	return &AdminRecoveryHandler{src: src, log: log, evaluator: evaluator}
}

// Register mounts the routes.
func (h *AdminRecoveryHandler) Register(r chi.Router) {
	r.Get("/api/v1/admin/recovery/status", h.handleStatus)
}

func (h *AdminRecoveryHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), recoveryStatusTimeout)
	defer cancel()

	st, err := h.src.Status(ctx)
	if err != nil {
		h.log.Errorw("failed to read recovery journal status", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to read recovery journal status")

		return
	}

	writeJSON(w, http.StatusOK, st)
}
