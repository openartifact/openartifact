// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
)

// handleDeleteModule answers DELETE /_api/modules/{mod}/{system}/{version}.
func (h *HTTPHandler) handleDeleteModule(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	name, system, ok := moduleCoords(w, r)
	if !ok {
		return
	}

	version := urlparams.Vars(r)["version"]
	if !validVersion(version) {
		writeErrors(w, http.StatusNotFound, "module version not found")
		return
	}

	m, err := h.DB.GetTerraformModule(r.Context(), repo.ID, name, system, version)
	if err != nil {
		h.Log.Errorw("failed to get terraform module", "repo", repo.Name, "module", name, "error", err)
		writeErrors(w, http.StatusInternalServerError, "internal error")

		return
	}

	if m == nil {
		writeErrors(w, http.StatusNotFound, "module version not found")
		return
	}

	h.deleteRows(w, r, repo, deletion{
		kind: models.TrashKindTerraformModule, name: "modules/" + name + "/" + system, version: version,
		path: "modules/" + name + "/" + system + "/" + version,
		rows: []row{{id: m.ID, sha256: m.Checksum, size: m.SizeBytes}},
		hard: func(ctx context.Context) error { return h.DB.DeleteTerraformModule(ctx, repo.ID, m.ID) },
	})
}

// handleDeleteProviderPlatform answers
// DELETE /_api/providers/{type}/{version}/{os}/{arch}.
func (h *HTTPHandler) handleDeleteProviderPlatform(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	typ, version, osName, arch, ok := providerPlatform(w, r)
	if !ok {
		return
	}

	p, err := h.DB.GetTerraformProvider(r.Context(), repo.ID, typ, version, osName, arch)
	if err != nil {
		h.Log.Errorw("failed to get terraform provider", "repo", repo.Name, "type", typ, "error", err)
		writeErrors(w, http.StatusInternalServerError, "internal error")

		return
	}

	if p == nil {
		writeErrors(w, http.StatusNotFound, "provider package not found")
		return
	}

	h.deleteProviders(w, r, repo, typ, version, "/"+osName+"/"+arch, []*models.TerraformProvider{p})
}

// handleDeleteProviderVersion answers DELETE /_api/providers/{type}/{version}:
// every platform of the version.
func (h *HTTPHandler) handleDeleteProviderVersion(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	vars := urlparams.Vars(r)
	typ, version := vars["type"], vars["version"]

	if !validName(typ) || !validVersion(version) {
		writeErrors(w, http.StatusNotFound, "provider version not found")
		return
	}

	rows, err := h.DB.ListTerraformProviderVersionPlatforms(r.Context(), repo.ID, typ, version)
	if err != nil {
		h.Log.Errorw("failed to list terraform provider platforms", "repo", repo.Name, "type", typ, "error", err)
		writeErrors(w, http.StatusInternalServerError, "internal error")

		return
	}

	if len(rows) == 0 {
		writeErrors(w, http.StatusNotFound, "provider version not found")
		return
	}

	h.deleteProviders(w, r, repo, typ, version, "", rows)
}

func (h *HTTPHandler) deleteProviders(
	w http.ResponseWriter, r *http.Request, repo *models.Repository, typ, version, platform string, rows []*models.TerraformProvider,
) {
	items := make([]row, 0, len(rows))
	for _, p := range rows {
		items = append(items, row{id: p.ID, sha256: p.Checksum, size: p.SizeBytes})
	}

	h.deleteRows(w, r, repo, deletion{
		kind: models.TrashKindTerraformProvider, name: "providers/" + typ, version: version,
		path: "providers/" + typ + "/" + version + platform,
		rows: items,
		hard: func(ctx context.Context) error {
			for _, p := range rows {
				if err := h.DB.DeleteTerraformProvider(ctx, repo.ID, p.ID); err != nil {
					return fmt.Errorf("delete terraform provider: %w", err)
				}
			}

			return nil
		},
	})
}

// row is one deleted row with what its blob and quota need.
type row struct {
	id     string
	sha256 string
	size   int64
}

// deletion describes a delete of one or more rows of one kind.
type deletion struct {
	kind    models.TrashKind
	name    string // catalog name: modules/NAME/SYSTEM or providers/TYPE
	version string
	path    string
	rows    []row
	hard    func(ctx context.Context) error // removes the rows for good
}

// deleteRows moves the rows to the trash or removes them for good, as the
// repository's retention decides, and answers 200 {"deleted": n}.
func (h *HTTPHandler) deleteRows(w http.ResponseWriter, r *http.Request, repo *models.Repository, d deletion) {
	if err := trash.GuardRelease(r, repo, formatName, d.version); err != nil {
		if errors.Is(err, trash.ErrImmutableRelease) {
			writeErrors(w, http.StatusConflict, "repository has immutable releases")
			return
		}

		h.Log.Errorw("failed to check release immutability", "repo", repo.Name, "error", err)
		writeErrors(w, http.StatusInternalServerError, "internal error")

		return
	}

	ids := make([]string, 0, len(d.rows))

	var size int64

	for _, it := range d.rows {
		ids = append(ids, it.id)
		size += it.size
	}

	err := trash.Delete(r.Context(), h.DB, trash.Request{
		RepoID: repo.ID, Kind: d.kind, IDs: ids, Resource: d.path, Package: d.name, Version: d.version,
	}, func() error {
		if err := d.hard(r.Context()); err != nil {
			return err
		}

		if relErr := quota.ReleaseN(r.Context(), h.DB, repo.ID, size, len(d.rows)); relErr != nil {
			h.Log.Warnw("failed to release quota", "repo", repo.Name, "error", relErr)
		}

		// Blobs are content-addressed and may be shared; each goes only once
		// no row references it any more.
		seen := make([]string, 0, len(d.rows))

		for _, it := range d.rows {
			if slices.Contains(seen, it.sha256) {
				continue
			}

			seen = append(seen, it.sha256)

			if delErr := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, it.sha256); delErr != nil {
				h.Log.Warnw("failed to clean up blob after terraform delete", "sha256", it.sha256, "error", delErr)
			}
		}

		return nil
	})
	if err != nil {
		if errors.Is(err, database.ErrTrashNotFound) {
			writeErrors(w, http.StatusNotFound, "not found")
			return
		}

		h.Log.Errorw("failed to delete terraform rows", "repo", repo.Name, "path", d.path, "error", err)
		writeErrors(w, http.StatusInternalServerError, "internal error")

		return
	}

	for _, it := range d.rows {
		h.Emit(r.Context(), repo.ID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
			Repository: repo.Name, Format: formatName, Name: d.name, Version: d.version, Path: d.path, SHA256: it.sha256, Size: it.size,
		}))
	}

	writeJSON(w, http.StatusOK, map[string]int{"deleted": len(d.rows)})
}
