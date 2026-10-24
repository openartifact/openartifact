// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/gpgkeys"
	"github.com/openartifact/openartifact/pkg/gpg"
)

// bundleManifestSchema identifies the manifest layout that GPG signatures of
// sealed bundles cover.
const bundleManifestSchema = "openartifact.release-bundle/v1"

// BundleKeyResolver finds the signer for a seal request's gpg_key_id (a stored
// key id, a fingerprint, or "config").
type BundleKeyResolver interface {
	SignerByRef(ctx context.Context, ref string) (gpg.Signer, *models.GPGKey, error)
}

// WithGPGKeys enables sealing with gpg_key_id: the sealed bundle's manifest is
// signed with the referenced key.
func (h *BundlesHandler) WithGPGKeys(keys BundleKeyResolver) *BundlesHandler {
	h.keys = keys
	return h
}

type bundleManifest struct {
	Schema      string               `json:"schema"`
	Name        string               `json:"name"`
	Version     string               `json:"version"`
	Description string               `json:"description"`
	Items       []bundleManifestItem `json:"items"`
}

type bundleManifestItem struct {
	SHA256       string `json:"sha256"`
	ArtifactPath string `json:"artifact_path"`
	Format       string `json:"format"`
	SizeBytes    int64  `json:"size_bytes"`
	SourceRepoID string `json:"source_repo_id"`
}

// buildBundleManifest renders the deterministic document a bundle signature
// covers: bundle identity plus its items ordered by digest. Identical items
// always render identical bytes, so the signature can be checked against a
// manifest fetched (or rebuilt) later.
func buildBundleManifest(b *models.ReleaseBundle, items []*models.ReleaseBundleItem) ([]byte, error) {
	m := bundleManifest{
		Schema: bundleManifestSchema, Name: b.Name, Version: b.Version, Description: b.Description,
		Items: make([]bundleManifestItem, 0, len(items)),
	}

	for _, i := range items {
		m.Items = append(m.Items, bundleManifestItem{
			SHA256: i.SHA256, ArtifactPath: i.ArtifactPath, Format: i.Format, SizeBytes: i.SizeBytes, SourceRepoID: i.SourceRepoID,
		})
	}

	sort.Slice(m.Items, func(a, c int) bool {
		if m.Items[a].SHA256 != m.Items[c].SHA256 {
			return m.Items[a].SHA256 < m.Items[c].SHA256
		}

		return m.Items[a].ArtifactPath < m.Items[c].ArtifactPath
	})

	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")

	if err := enc.Encode(m); err != nil {
		return nil, err //nolint:wrapcheck // plain marshalling failure
	}

	return buf.Bytes(), nil
}

// signBundle signs the manifest of the bundle's current items with the key
// referenced by ref. The returned models.BundleSignature also pins the item
// set, so the seal fails if an item changed while signing.
func (h *BundlesHandler) signBundle(ctx context.Context, ref string, b *models.ReleaseBundle, items []*models.ReleaseBundleItem) (*models.BundleSignature, *apiFailure) {
	if h.keys == nil {
		return nil, &apiFailure{http.StatusInternalServerError, "internal_error", "GPG signing is not configured on this server"}
	}

	signer, _, err := h.keys.SignerByRef(ctx, ref)
	if err != nil {
		if errors.Is(err, gpgkeys.ErrNotFound) {
			return nil, validationFailure("gpg_key_id does not reference a known GPG key")
		}

		h.log.Errorw("seal bundle: loading GPG key failed", "error", err)

		return nil, &apiFailure{http.StatusInternalServerError, "internal_error", "failed to load the GPG key"}
	}

	manifest, err := buildBundleManifest(b, items)
	if err != nil {
		h.log.Errorw("seal bundle: building manifest failed", "error", err)
		return nil, &apiFailure{http.StatusInternalServerError, "internal_error", "failed to build the bundle manifest"}
	}

	sig, err := signer.DetachedSign(bytes.NewReader(manifest))
	if err != nil {
		h.log.Errorw("seal bundle: signing failed", "error", err)
		return nil, &apiFailure{http.StatusInternalServerError, "internal_error", "failed to sign the bundle manifest"}
	}

	ids := make([]string, 0, len(items))
	for _, i := range items {
		ids = append(ids, i.ID)
	}

	sort.Strings(ids)

	return &models.BundleSignature{KeyFingerprint: signer.KeyFingerprint(), Signature: string(sig), ItemIDs: ids}, nil
}

// loadBundleForRead resolves {name}/{version} for the read-only signing routes.
func (h *BundlesHandler) loadBundleForRead(w http.ResponseWriter, r *http.Request) (*models.ReleaseBundle, bool) {
	bundle, _, ok := h.loadBundle(w, r, bundleNeedView)

	return bundle, ok
}

// handleGetManifest serves the exact bytes a bundle signature covers.
func (h *BundlesHandler) handleGetManifest(w http.ResponseWriter, r *http.Request) {
	bundle, ok := h.loadBundleForRead(w, r)
	if !ok {
		return
	}

	items, err := h.db.ListBundleItems(r.Context(), bundle.ID)
	if err != nil {
		h.log.Errorw("bundle manifest: db error listing items", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	manifest, err := buildBundleManifest(bundle, items)
	if err != nil {
		h.log.Errorw("bundle manifest: render failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to build the bundle manifest")

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeBundleFilename(bundle)+`.manifest.json"`)
	w.WriteHeader(http.StatusOK)

	//nolint:gosec // G705: a JSON document served as an attachment, not HTML
	_, _ = w.Write(manifest)
}

// handleGetManifestSignature serves the armored detached signature of the manifest.
func (h *BundlesHandler) handleGetManifestSignature(w http.ResponseWriter, r *http.Request) {
	bundle, ok := h.loadBundleForRead(w, r)
	if !ok {
		return
	}

	if bundle.GPGSignature == nil || *bundle.GPGSignature == "" {
		writeError(w, http.StatusNotFound, "not_found", "bundle was not sealed with a GPG key")
		return
	}

	w.Header().Set("Content-Type", "application/pgp-signature")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeBundleFilename(bundle)+`.manifest.json.asc"`)
	w.WriteHeader(http.StatusOK)

	_, _ = w.Write([]byte(*bundle.GPGSignature))
}

func safeBundleFilename(b *models.ReleaseBundle) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, b.Name+"-"+b.Version)
}
