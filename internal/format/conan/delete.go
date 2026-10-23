// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/webhook"
)

// handleDeleteRecipe removes every revision of a recipe, with its packages.
func (h *HTTPHandler) handleDeleteRecipe(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scopeOf(w, r)
	if !ok {
		return
	}

	h.deleteScope(w, r, s, false, func(*models.ConanPackage) bool { return true })
}

// handleDeleteRecipeRevision removes one recipe revision and its packages.
func (h *HTTPHandler) handleDeleteRecipeRevision(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scopeOf(w, r)
	if !ok {
		return
	}

	h.deleteScope(w, r, s, false, func(f *models.ConanPackage) bool { return f.RecipeRevision == s.rrev })
}

// handleDeletePackages removes every package of a recipe revision.
func (h *HTTPHandler) handleDeletePackages(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scopeOf(w, r)
	if !ok {
		return
	}

	h.deleteScope(w, r, s, false, func(f *models.ConanPackage) bool {
		return f.RecipeRevision == s.rrev && f.PackageID != ""
	})
}

// handleDeletePackageRevision removes one package revision.
func (h *HTTPHandler) handleDeletePackageRevision(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scopeOf(w, r)
	if !ok {
		return
	}

	h.deleteScope(w, r, s, false, func(f *models.ConanPackage) bool {
		return f.RecipeRevision == s.rrev && f.PackageID == s.pid && f.PackageRevision == s.prev
	})
}

// deleteScope deletes the files of the scope's recipe that match: into the
// trash when the repository keeps one, else for good together with their blobs.
// Deleting nothing is a 404 (a 200 with allowEmpty); a repository with immutable releases refuses. The
// conan client treats any status but 200 as failure.
func (h *HTTPHandler) deleteScope(w http.ResponseWriter, r *http.Request, s *scope, allowEmpty bool, match func(*models.ConanPackage) bool) {
	files, ok := h.recipeFiles(w, r, s)
	if !ok {
		return
	}

	var doomed []*models.ConanPackage

	for _, f := range files {
		if match(f) {
			doomed = append(doomed, f)
		}
	}

	if len(doomed) == 0 {
		if allowEmpty {
			w.WriteHeader(http.StatusOK)
			return
		}

		http.NotFound(w, r)

		return
	}

	if gErr := trash.GuardReleaseByName(r, h.DB, s.repoName, "conan", s.recipe.Version); gErr != nil {
		h.rejectImmutable(w, s, gErr)
		return
	}

	ids := make([]string, 0, len(doomed))
	for _, f := range doomed {
		ids = append(ids, f.ID)
	}

	err := trash.Delete(r.Context(), h.DB, trash.Request{
		RepoID:   s.repoID,
		Kind:     models.TrashKindConan,
		IDs:      ids,
		Resource: s.resource(),
	}, func() error { return h.hardDelete(r, doomed) })
	if err != nil {
		if errors.Is(err, database.ErrTrashNotFound) {
			http.NotFound(w, r)
			return
		}

		h.fail(w, "failed to delete conan files", err, "path", s.resource())

		return
	}

	h.Emit(r.Context(), s.repoID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: s.repoName,
		Format:     "conan",
		Name:       s.recipe.Name,
		Version:    s.recipe.Version,
		Path:       s.resource(),
	}))

	w.WriteHeader(http.StatusOK)
}

// hardDelete removes the rows, then every blob no other row references. Blobs
// are content-addressed and may be shared, so each goes through SafeDeleteBlob.
func (h *HTTPHandler) hardDelete(r *http.Request, files []*models.ConanPackage) error {
	if len(files) == 0 {
		return nil
	}

	shas := make(map[string]struct{}, len(files))

	// Every file row is one quota artifact of its blob's size; return what was
	// actually deleted, even when a later row fails.
	var (
		freed int64
		count int
	)

	defer func() {
		if err := quota.ReleaseN(r.Context(), h.DB, files[0].RepositoryID, freed, count); err != nil {
			h.Log.Warnw("failed to release quota", "repo", files[0].RepositoryID, "error", err)
		}
	}()

	for _, f := range files {
		if err := h.DB.DeleteConanPackage(r.Context(), f.ID); err != nil {
			return fmt.Errorf("delete conan package: %w", err)
		}

		freed += f.SizeBytes
		count++

		if f.SHA256 != "" {
			shas[f.SHA256] = struct{}{}
		}
	}

	for sha := range shas {
		if err := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, sha); err != nil {
			h.Log.Warnw("failed to clean up blob after conan delete", "sha256", sha, "error", err)
		}
	}

	return nil
}
