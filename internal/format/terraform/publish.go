// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
)

// defaultProtocols is what a provider declares when neither the upload nor its
// registry manifest says otherwise.
func defaultProtocols() []string { return []string{"5.0"} }

// checksumHeader lets a CI job have the server verify its upload.
const checksumHeader = "X-Checksum-Sha256"

// spoolBody reads the upload into a spool, answering 413 for a body over max
// and 400 for an empty or unreadable one. On success the caller owns the spool.
func (h *HTTPHandler) spoolBody(w http.ResponseWriter, r *http.Request, maxBytes int64) *base.Spool {
	defer func() { _ = r.Body.Close() }()

	if r.ContentLength > maxBytes {
		writeErrors(w, http.StatusRequestEntityTooLarge, "upload too large")
		return nil
	}

	spool, err := base.SpoolLimited(r.Body, maxBytes)
	if err != nil {
		if errors.Is(err, base.ErrContentTooLarge) {
			writeErrors(w, http.StatusRequestEntityTooLarge, "upload too large")
			return nil
		}

		h.Log.Errorw("failed to read terraform upload", "error", err)
		writeErrors(w, http.StatusBadRequest, "failed to read upload")

		return nil
	}

	if spool.Size() == 0 {
		spool.Close()
		writeErrors(w, http.StatusBadRequest, "empty upload")

		return nil
	}

	return spool
}

// writeArchiveError answers a rejected archive: 400 for a malformed or unsafe
// one, 422 for a well-formed archive that is not a module or provider.
func writeArchiveError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest

	if ae, ok := errors.AsType[*archiveError](err); ok && ae.semantic {
		status = http.StatusUnprocessableEntity
	}

	writeErrors(w, status, err.Error())
}

// commitUpload reserves quota, stores the blob and creates the row (create
// runs under the blob's lock). It writes the error response itself and
// returns false on failure; on success the reservation stays with the row.
func (h *HTTPHandler) commitUpload(
	w http.ResponseWriter, r *http.Request, repo *models.Repository, spool *base.Spool, meta map[string]string,
	create func(ctx context.Context, blobKey string) error,
) bool {
	ctx := r.Context()
	size, sha := spool.Size(), spool.SHA256Hex()

	if err := quota.Enforce(ctx, h.DB, repo.ID, size); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			quota.WriteExceeded(w)
			return false
		}

		h.Log.Errorw("failed to reserve quota", "repo", repo.Name, "error", err)
		writeErrors(w, http.StatusInternalServerError, "internal error")

		return false
	}

	err := storage.CommitBlob(ctx, h.DB, h.BlobStore, storage.SeekerBlob(sha, spool.Reader(), meta), create)
	if err == nil {
		return true
	}

	h.ReleaseQuota(ctx, repo.ID, repo.Name, size)

	// The blob is content-addressed and may be shared; this drops it unless
	// another row references it.
	if cleanErr := storage.SafeDeleteBlob(ctx, h.DB, h.BlobStore, sha); cleanErr != nil {
		h.Log.Warnw("failed to clean up terraform blob", "sha256", sha, "error", cleanErr)
	}

	if errors.Is(err, database.ErrDuplicate) || database.IsUniqueViolation(err) {
		writeErrors(w, http.StatusConflict, "this version already exists")
		return false
	}

	h.Log.Errorw("failed to store terraform upload", "repo", repo.Name, "error", err)
	writeErrors(w, http.StatusInternalServerError, "internal error")

	return false
}

// handleUploadModule answers PUT /_api/modules/{mod}/{system}/{version}: the
// body is the module archive (zip or tar.gz).
func (h *HTTPHandler) handleUploadModule(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	vars := urlparams.Vars(r)
	name, system, version := vars["mod"], vars["system"], vars["version"]

	switch {
	case !validUploadName(name):
		writeErrors(w, http.StatusBadRequest, "invalid module name")
		return
	case !validModuleSystem(system):
		writeErrors(w, http.StatusBadRequest, "invalid module system")
		return
	case !validVersion(version):
		writeErrors(w, http.StatusBadRequest, "version must be a semantic version such as 1.2.3 or 1.2.3-rc.1")
		return
	}

	spool := h.spoolBody(w, r, maxModuleBytes)
	if spool == nil {
		return
	}
	defer spool.Close()

	info, err := validateModuleArchive(spool, spool.Size())
	if err != nil {
		writeArchiveError(w, err)
		return
	}

	existing, err := h.DB.GetTerraformModule(r.Context(), repo.ID, name, system, version)
	if err != nil {
		h.Log.Errorw("failed to check existing terraform module", "repo", repo.Name, "module", name, "error", err)
		writeErrors(w, http.StatusInternalServerError, "internal error")

		return
	}

	if existing != nil {
		writeErrors(w, http.StatusConflict, "this module version already exists")
		return
	}

	mod := &models.TerraformModule{
		RepositoryID: repo.ID, Name: name, System: system, Version: version, ArchiveExt: info.ext,
		Checksum: spool.SHA256Hex(), SizeBytes: spool.Size(),
	}

	meta := map[string]string{"sha256": mod.Checksum, "filename": moduleFileName(name, system, version, info.ext)}

	if !h.commitUpload(w, r, repo, spool, meta, func(ctx context.Context, blobKey string) error {
		mod.BlobKey = blobKey
		mod.CreatedAt = importmeta.CreatedAt(ctx)

		return h.DB.CreateTerraformModule(ctx, mod)
	}) {
		return
	}

	path := "modules/" + name + "/" + system + "/" + version
	h.Emit(r.Context(), repo.ID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repo.Name, Format: formatName, Name: "modules/" + name + "/" + system, Version: version,
		Path: path, SHA256: mod.Checksum, Size: mod.SizeBytes,
	}))

	writeJSON(w, http.StatusCreated, map[string]any{
		"id": mod.ID, "name": name, "system": system, "version": version, "sha256": mod.Checksum, "size": mod.SizeBytes,
	})
}

// requestedProtocols reads ?protocols=5.0,6.0; nil when absent.
func requestedProtocols(r *http.Request) ([]string, error) {
	raw := r.URL.Query().Get("protocols")
	if raw == "" {
		return nil, nil
	}

	return normalizeProtocols(strings.Split(raw, ","))
}

// checksumMismatch verifies the optional X-Checksum-Sha256 header against the
// received body. It returns a message for the client when the header is
// malformed or does not match, "" otherwise.
func checksumMismatch(r *http.Request, sha string) string {
	want := r.Header.Get(checksumHeader)
	if want == "" {
		return ""
	}

	if len(want) != len(sha) || !strings.EqualFold(want, sha) {
		return checksumHeader + " does not match the uploaded bytes"
	}

	return ""
}

// handleUploadProvider answers PUT /_api/providers/{type}/{version}/{os}/{arch}:
// the body is the platform zip as the provider's release build produces it.
func (h *HTTPHandler) handleUploadProvider(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	vars := urlparams.Vars(r)
	typ, version, osName, arch := vars["type"], vars["version"], vars["os"], vars["arch"]

	switch {
	case !validUploadName(typ):
		writeErrors(w, http.StatusBadRequest, "invalid provider type")
		return
	case !validVersion(version):
		writeErrors(w, http.StatusBadRequest, "version must be a semantic version such as 1.2.3 or 1.2.3-rc.1")
		return
	case !validPlatformPart(osName) || !validPlatformPart(arch):
		writeErrors(w, http.StatusBadRequest, "invalid os or arch")
		return
	}

	protocols, err := requestedProtocols(r)
	if err != nil {
		writeErrors(w, http.StatusBadRequest, err.Error())
		return
	}

	spool := h.spoolBody(w, r, maxProviderBytes)
	if spool == nil {
		return
	}
	defer spool.Close()

	if msg := checksumMismatch(r, spool.SHA256Hex()); msg != "" {
		writeErrors(w, http.StatusBadRequest, msg)
		return
	}

	info, err := validateProviderArchive(spool, spool.Size(), typ, version)
	if err != nil {
		writeArchiveError(w, err)
		return
	}

	prov, status, msg := h.newProvider(r, repo.ID, typ, version, osName, arch, protocols, info)
	if status != 0 {
		writeErrors(w, status, msg)
		return
	}

	prov.Checksum, prov.SizeBytes = spool.SHA256Hex(), spool.Size()
	meta := map[string]string{"sha256": prov.Checksum, "filename": providerZipName(typ, version, osName, arch)}

	if !h.commitUpload(w, r, repo, spool, meta, func(ctx context.Context, blobKey string) error {
		prov.BlobKey = blobKey
		prov.CreatedAt = importmeta.CreatedAt(ctx)

		return h.DB.CreateTerraformProvider(ctx, prov)
	}) {
		return
	}

	path := "providers/" + typ + "/" + version + "/" + osName + "/" + arch
	h.Emit(r.Context(), repo.ID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repo.Name, Format: formatName, Name: "providers/" + typ, Version: version,
		Path: path, SHA256: prov.Checksum, Size: prov.SizeBytes,
	}))

	writeJSON(w, http.StatusCreated, map[string]any{
		"id": prov.ID, "type": typ, "version": version, "os": osName, "arch": arch, "protocols": prov.Protocols,
		"sha256": prov.Checksum, "size": prov.SizeBytes,
	})
}

// newProvider builds the row of a provider upload after checking it against
// the platforms of the version that exist: the same platform twice is a
// conflict, and every platform of a version has to declare the same plugin
// protocols. protocols is what the request asked for. A non-zero status
// carries the error response.
func (h *HTTPHandler) newProvider(r *http.Request, repoID, typ, version, osName, arch string, protocols []string, info archiveInfo,
) (*models.TerraformProvider, int, string) {
	siblings, err := h.DB.ListTerraformProviderVersionPlatforms(r.Context(), repoID, typ, version)
	if err != nil {
		h.Log.Errorw("failed to list terraform provider platforms", "repo", repoID, "type", typ, "error", err)
		return nil, http.StatusInternalServerError, "internal error"
	}

	if slices.ContainsFunc(siblings, func(p *models.TerraformProvider) bool { return p.OS == osName && p.Arch == arch }) {
		return nil, http.StatusConflict, "this provider platform already exists"
	}

	// The upload's own word wins, then the manifest inside the zip; only
	// when neither says anything does a platform inherit what the version
	// already declares, and failing that the default.
	explicit := protocols
	if explicit == nil {
		explicit = info.protocols
	}

	resolved := explicit

	switch {
	case explicit != nil && len(siblings) > 0 && !slices.Equal(siblings[0].Protocols, explicit):
		return nil, http.StatusConflict, "the platforms of a provider version must declare the same protocols"
	case explicit == nil && len(siblings) > 0:
		resolved = siblings[0].Protocols
	case explicit == nil:
		resolved = defaultProtocols()
	}

	return &models.TerraformProvider{
		RepositoryID: repoID, Type: typ, Version: version, OS: osName, Arch: arch, Protocols: slices.Clone(resolved),
	}, 0, ""
}
