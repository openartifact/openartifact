// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

// PackageDeleter deletes a package row of any hosted format by its listing id.
// It is satisfied by *worker.CleanupWorker.
type PackageDeleter interface {
	DeletePackage(
		ctx context.Context,
		repo *models.Repository,
		id string,
		guard func(worker.DeletedPackage) error,
		actor string,
	) (*worker.DeletedPackage, error)
}

// SetPackageDeleter wires the deleter behind DELETE /repositories/{name}/packages/{id}
// for every format but deb and rpm, which have handlers of their own.
func (h *RPMUploadHandler) SetPackageDeleter(d PackageDeleter) {
	h.packageDeleter = d
}

// deleteOtherFormat deletes the package row packageID of a hosted repository
// of any format but deb and rpm: into the trash when the repository keeps one,
// else for good, and refuses a released version of an immutable repository
// unless an admin forces it (?force=true).
func (h *RPMUploadHandler) deleteOtherFormat(w http.ResponseWriter, r *http.Request, repo *models.Repository) {
	packageID := urlparams.Vars(r)["id"]

	if h.packageDeleter == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "package deletion is not available")
		return
	}

	res, err := h.packageDeleter.DeletePackage(r.Context(), repo, packageID, func(p worker.DeletedPackage) error {
		return trash.GuardRelease(r, repo, repo.Format, p.Version)
	}, trash.Actor(r.Context()))
	if err != nil {
		if errors.Is(err, worker.ErrPackageNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "package not found")
			return
		}

		writePackageDeleteError(w, h.log, err, packageID)

		return
	}

	trash.AuditDeleted(r.Context(), repo.ID, res.Kind, res.ID, res.Trashed, res.Name+" "+res.Version, res.Name, res.Version)

	webhook.Emit(
		r.Context(),
		h.publisher,
		repo.ID,
		webhook.EventArtifactDeleted,
		webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
			Repository: repo.Name,
			Format:     repo.Format,
			Name:       res.Name,
			Version:    res.Version,
		}),
	)

	h.log.Infow(
		"package deleted",
		"repository",
		repo.Name,
		"format",
		repo.Format,
		"package_id",
		packageID,
		"rows",
		res.Rows,
		"trashed",
		res.Trashed,
	)

	writeJSON(w, http.StatusOK, Response{
		Success: true,
		Data: map[string]any{
			"repository": repo.Name,
			"package_id": packageID,
			"trashed":    res.Trashed,
		},
	})
}

// writePackageDeleteError maps a deb/rpm/other-format package delete failure to
// its HTTP answer.
func writePackageDeleteError(w http.ResponseWriter, log *logger.Logger, err error, packageID string) {
	switch {
	case errors.Is(err, database.ErrTrashNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "package not found")
	case errors.Is(err, trash.ErrImmutableRelease):
		writeError(w, http.StatusConflict, "IMMUTABLE_RELEASE", "release versions are immutable in this repository")
	default:
		log.Errorw("failed to delete component", "package_id", packageID, "error", err)
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "failed to delete package")
	}
}
