// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/internal/worker"
)

// PackageDeleter deletes a package row of any hosted format by its listing id.
// It is satisfied by *worker.CleanupWorker.
type PackageDeleter interface {
	DeletePackage(ctx context.Context, repo *models.Repository, id string, guard func(worker.DeletedPackage) error) (*worker.DeletedPackage, error)
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
	})
	if err != nil {
		if errors.Is(err, worker.ErrPackageNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "package not found")
			return
		}

		h.writeDeleteError(w, err, packageID)

		return
	}

	trash.AuditDeleted(r.Context(), repo.ID, res.Kind, res.ID, res.Trashed, res.Name+" "+res.Version)

	webhook.Emit(r.Context(), h.publisher, repo.ID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repo.Name,
		Format:     repo.Format,
		Name:       res.Name,
		Version:    res.Version,
	}))

	h.log.Infow("package deleted", "repository", repo.Name, "format", repo.Format, "package_id", packageID, "rows", res.Rows, "trashed", res.Trashed)

	writeJSON(w, http.StatusOK, Response{
		Success: true,
		Data: map[string]any{
			"repository": repo.Name,
			"package_id": packageID,
			"trashed":    res.Trashed,
		},
	})
}
