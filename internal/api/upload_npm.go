// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	formatnpm "github.com/openartifact/openartifact/internal/format/npm"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
	pkgnpm "github.com/openartifact/openartifact/pkg/npm"
)

const MaxNPMUploadBytes int64 = 200 << 20

// maxConcurrentNPMPublishes bounds the publishes buffering a body at once: a
// publish holds roughly three copies of its body (raw bytes, the decoded
// base64 string and the tarball), up to ~600 MiB for a 200 MiB upload.
const maxConcurrentNPMPublishes = 4

// maxNPMDistTagBody caps the dist-tag PUT body, a JSON string holding a version.
const maxNPMDistTagBody int64 = 1 << 10

type NPMUploadHandler struct {
	log       *logger.Logger
	db        database.Database
	blobStore storage.BlobStore
	baseURL   string
	reg       *metrics.Registry
	npmWorker *worker.NPMMetadataWorker
	publisher webhook.Publisher

	publishSlots chan struct{} // bounds concurrent publishes
}

func NewNPMUploadHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string, reg *metrics.Registry) *NPMUploadHandler {
	return &NPMUploadHandler{log: log, db: db, blobStore: blobStore, baseURL: baseURL, reg: reg,
		publishSlots: make(chan struct{}, maxConcurrentNPMPublishes)}
}

// SetNPMWorker wires the debounced npm packument regeneration worker.
// Call NotifyStale(packageID) after each successful publish.
func (h *NPMUploadHandler) SetNPMWorker(w *worker.NPMMetadataWorker) {
	h.npmWorker = w
}

// SetPublisher wires the webhook publisher used for artifact.uploaded and
// artifact.deleted events. It must be called before the handler serves requests.
func (h *NPMUploadHandler) SetPublisher(p webhook.Publisher) {
	h.publisher = p
}

// Register mounts the publish/unpublish routes under /repos/{name} on a router
// that has no other /repos/{name} subrouter (tests). Production code shares the
// /repos/{name} subrouter with the other formats and calls RegisterRepoRoutes.
func (h *NPMUploadHandler) Register(router chi.Router) {
	router.Route("/repos/{name}", h.RegisterRepoRoutes)
}

// RegisterRepoRoutes registers the routes relative to a /repos/{name}
// subrouter. They must live inside that subrouter: a sibling /repos/{repo}/*
// route is shadowed by the mount and every PUT/DELETE 404s.
func (h *NPMUploadHandler) RegisterRepoRoutes(r chi.Router) {
	r.Put("/-/package/{pkg}/dist-tags/{tag}", h.handlePutDistTag)
	r.Delete("/-/package/{pkg}/dist-tags/{tag}", h.handleDeleteDistTag)
	r.Put("/*", h.handlePublish)
	r.Delete("/*", h.handleUnpublish)
}

// HandlePublish serves an npm publish (or `-rev` unpublish) PUT. It is
// exported for routers that share PUT "/*" with another format.
func (h *NPMUploadHandler) HandlePublish(w http.ResponseWriter, r *http.Request) {
	h.handlePublish(w, r)
}

func (h *NPMUploadHandler) resolveRepo(ctx context.Context, name string) (*models.Repository, error) {
	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		return nil, err
	}

	return repo, nil
}

// requireAuth requires a principal that the RBAC middleware actually
// authenticated (valid session, token or legacy key). Merely carrying an
// "Authorization: Bearer ..." header proves nothing: any string passes that
// check, so it must never be the authentication decision.
func requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if p := currentPrincipal(r); p != nil && !p.IsAnonymous {
		return true
	}

	w.Header().Set("WWW-Authenticate", `Bearer realm="openartifact"`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)

	if encErr := json.NewEncoder(w).Encode(map[string]string{"error": "unauthenticated"}); encErr != nil {
		_ = encErr // response header already written
	}

	return false
}

type npmPublishBody struct {
	Name        string                     `json:"name"`
	Versions    map[string]json.RawMessage `json:"versions"`
	Attachments map[string]*npmAttachment  `json:"-"`
	DistTags    map[string]string          `json:"dist-tags"`
}

// attachment returns the tarball attachment of a version. The npm CLI keys
// attachments by the full (scoped) name, "@scope/pkg-1.0.0.tgz"; the bare
// filename is accepted too.
func (b *npmPublishBody) attachment(version string) (*npmAttachment, bool) {
	att, ok := b.Attachments[b.Name+"-"+version+".tgz"]
	if !ok {
		att, ok = b.Attachments[formatnpm.TarballFilename(b.Name, version)]
	}

	return att, ok
}

type npmPreparedVersion struct {
	version  string
	manifest json.RawMessage
	filename string
	att      *npmAttachment
	integrity,
	sha1Hex,
	sha256Hex string
}

// npmPublishError carries an HTTP status for failures during publish.
type npmPublishError struct {
	status int
	msg    string
}

var (
	errEmptyDistTag     = errors.New("empty dist-tag")
	errDistTagIsVersion = errors.New("dist-tag must not be a valid semver version")
)

func validateNPMTagName(tag string) error {
	if tag == "" {
		return errEmptyDistTag
	}

	if pkgnpm.ValidateSemver(tag) == nil {
		return fmt.Errorf("%w: %s", errDistTagIsVersion, tag)
	}

	return nil
}

// prepareNPMVersions validates and decodes every version in a publish body
// before anything is written, so validation failures leave no partial state.
func prepareNPMVersions(pub *npmPublishBody) ([]*npmPreparedVersion, *npmPublishError) {
	prepared := make([]*npmPreparedVersion, 0, len(pub.Versions))

	for version, manifestRaw := range pub.Versions {
		if err := pkgnpm.ValidateSemver(version); err != nil {
			return nil, &npmPublishError{http.StatusBadRequest, "invalid semver: " + version}
		}

		var manifestMap map[string]any

		dec := json.NewDecoder(bytes.NewReader(manifestRaw))
		dec.UseNumber()

		if err := dec.Decode(&manifestMap); err != nil {
			return nil, &npmPublishError{http.StatusBadRequest, "invalid manifest JSON"}
		}

		if name, _ := manifestMap["name"].(string); name != pub.Name {
			return nil, &npmPublishError{http.StatusBadRequest, "manifest name does not match package name"}
		}

		if mv, _ := manifestMap["version"].(string); mv != version {
			return nil, &npmPublishError{http.StatusBadRequest, "manifest version does not match version key: " + version}
		}

		filename := formatnpm.TarballFilename(pub.Name, version)

		att, ok := pub.attachment(version)
		if !ok {
			return nil, &npmPublishError{http.StatusBadRequest, "missing attachment: " + filename}
		}

		if err := att.validate(); err != nil {
			return nil, &npmPublishError{http.StatusBadRequest, "invalid attachment: " + err.Error()}
		}

		integrity, sha1Hex, sha256Hex, err := att.integrity()
		if err != nil {
			return nil, &npmPublishError{http.StatusInternalServerError, "internal error"}
		}

		if dist, ok := manifestMap["dist"].(map[string]any); ok {
			if declared, ok := dist["integrity"].(string); ok && declared != "" && declared != integrity {
				return nil, &npmPublishError{http.StatusBadRequest, "integrity mismatch"}
			}
		}

		prepared = append(prepared, &npmPreparedVersion{
			version: version, manifest: manifestRaw, filename: filename, att: att,
			integrity: integrity, sha1Hex: sha1Hex, sha256Hex: sha256Hex,
		})
	}

	return prepared, nil
}

// npmLicenses returns the licenses a policy is enforced on, over every
// prepared version: those of the package.json inside each tarball. The
// manifest in the publish JSON is only the fallback for a version whose tarball
// declares none, so a client cannot publish a tarball under licenses other than
// the ones it ships. A tarball that cannot be read safely is refused, and so is
// one that cannot be read at all while the repository has a blocking policy
// (false, response written).
func (h *NPMUploadHandler) npmLicenses(w http.ResponseWriter, r *http.Request, repoID string, prepared []*npmPreparedVersion) ([]string, bool) {
	var declared []string

	for _, pv := range prepared {
		res, err := licensemeta.Read(r.Context(), "npm", pv.filename, func() (io.ReadCloser, error) {
			return os.Open(pv.att.path)
		})

		switch {
		case licensemeta.Unsafe(err):
			h.log.Warnw("npm publish rejected: tarball metadata cannot be read safely", "version", pv.version, "error", err)
			licensepolicy.RejectUnsafe(w, h.log)

			return nil, false
		case err != nil:
			h.log.Debugw("npm publish: tarball metadata unreadable", "version", pv.version, "error", err)

			if licensepolicy.RefuseUnderivable(w, r, h.db, h.log, repoID) {
				return nil, false
			}
		case !res.Empty():
			declared = append(declared, res.Licenses...)
			continue
		}

		declared = append(declared, licensemeta.ParsePackageJSON(pv.manifest)...)
	}

	return declared, true
}

//nolint:gocyclo,cyclop // protocol handler: branching mirrors spec state machine
func (h *NPMUploadHandler) handlePublish(w http.ResponseWriter, r *http.Request) {
	if !requireAuth(w, r) {
		return
	}

	uploadResult := "success"

	if h.reg != nil {
		defer func() { h.reg.CountUpload(metrics.FormatNPM, uploadResult) }()
	}

	if h.publishSlots != nil {
		select {
		case h.publishSlots <- struct{}{}:
			defer func() { <-h.publishSlots }()
		case <-r.Context().Done():
			uploadResult = "error"

			http.Error(w, "request cancelled", http.StatusServiceUnavailable)

			return
		}
	}

	vars := urlparams.Vars(r)
	repoName := vars["name"]
	rawPkg := vars["*"]

	// `npm unpublish pkg@ver` PUTs the reduced packument to /pkg/-rev/<rev>.
	revPut := false

	if base, _, ok := strings.Cut(rawPkg, "/-rev/"); ok {
		rawPkg = base
		revPut = true
	}

	pkgName := npmDecodePackageName(rawPkg)

	body := http.MaxBytesReader(w, r.Body, MaxNPMUploadBytes)

	parsed, cleanup, err := parseNPMPublishBody(body)
	if err != nil {
		uploadResult = "error"

		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}

		if errors.Is(err, errNPMBodySyntax) || errors.Is(err, io.ErrUnexpectedEOF) {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		h.log.Errorw("failed to read npm publish body", "error", err)
		http.Error(w, "failed to read body", http.StatusBadRequest)

		return
	}

	defer cleanup()

	pub := *parsed

	if pub.Name != pkgName {
		uploadResult = "error"

		http.Error(w, "name mismatch", http.StatusBadRequest)

		return
	}

	if err := pkgnpm.ValidatePackageName(pub.Name); err != nil {
		uploadResult = "error"

		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	repo, err := h.resolveRepo(r.Context(), repoName)
	if err != nil {
		uploadResult = "error"

		h.log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repo == nil {
		uploadResult = "error"

		http.NotFound(w, r)

		return
	}

	pkg, err := h.db.GetNPMPackageByName(r.Context(), repo.ID, pub.Name)
	if err != nil {
		uploadResult = "error"

		h.log.Errorw("failed to get npm package", "name", pub.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if revPut {
		h.handleRevPut(w, r, repo, pkg, &pub)
		return
	}

	for tag := range pub.DistTags {
		if err := validateNPMTagName(tag); err != nil {
			uploadResult = "error"

			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}
	}

	deprecations, derr := h.extractNPMDeprecations(r.Context(), pkg, &pub)
	if derr != nil {
		uploadResult = "error"

		h.log.Errorw("failed to read npm versions for deprecation", "pkg", pub.Name, "error", derr)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	prepared, perr := prepareNPMVersions(&pub)
	if perr != nil {
		uploadResult = "error"

		http.Error(w, perr.msg, perr.status)

		return
	}

	// Every dist-tag must target a version in this publish or an existing one.
	for tag, ver := range pub.DistTags {
		if _, ok := pub.Versions[ver]; ok {
			continue
		}

		var existing *models.NPMVersion

		if pkg != nil {
			existing, err = h.db.GetNPMVersion(r.Context(), pkg.ID, ver)
			if err != nil {
				uploadResult = "error"

				h.log.Errorw("failed to check dist-tag version", "pkg", pub.Name, "tag", tag, "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)

				return
			}
		}

		if existing == nil {
			uploadResult = "error"

			http.Error(w, "dist-tag "+tag+" points to unknown version "+ver, http.StatusBadRequest)

			return
		}
	}

	// Reject duplicate versions before storing anything.
	if pkg != nil {
		for _, pv := range prepared {
			existing, err := h.db.GetNPMVersion(r.Context(), pkg.ID, pv.version)
			if err != nil {
				uploadResult = "error"

				h.log.Errorw("failed to check existing version", "pkg", pub.Name, "version", pv.version, "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)

				return
			}

			if existing != nil {
				uploadResult = "error"

				writeJSON(w, http.StatusConflict, map[string]string{"error": "version already exists"})

				return
			}
		}
	}

	declared, ok := h.npmLicenses(w, r, repo.ID, prepared)
	if !ok || licensepolicy.Enforce(w, r, h.db, h.log, repo.ID, declared...) {
		uploadResult = "error"
		return
	}

	createdPkg := false
	createdVersions := make([]*npmPreparedVersion, 0, len(prepared))

	// rollback undoes everything this request created so a failed publish
	// leaves no empty package or partial version set.
	rollback := func() {
		ctx := context.WithoutCancel(r.Context())

		if pkg != nil {
			for _, pv := range createdVersions {
				if delErr := h.db.DeleteNPMVersion(ctx, pkg.ID, pv.version); delErr != nil {
					// The row is still there, so it keeps its blob and its quota.
					h.log.Errorw("npm publish rollback: delete version", "pkg", pub.Name, "version", pv.version, "error", delErr)
					continue
				}

				h.releaseQuota(ctx, repo.ID, pv.att.size)

				if delErr := storage.SafeDeleteBlob(ctx, h.db, h.blobStore, pv.sha256Hex); delErr != nil {
					h.log.Warnw("npm publish rollback: delete blob", "sha256", pv.sha256Hex, "error", delErr)
				}
			}

			if createdPkg {
				if delErr := h.db.DeleteNPMPackage(ctx, pkg.ID); delErr != nil {
					h.log.Errorw("npm publish rollback: delete package", "pkg", pub.Name, "error", delErr)
				}
			}
		}
	}

	fail := func(logMsg string, err error) {
		uploadResult = "error"

		h.log.Errorw(logMsg, "pkg", pub.Name, "error", err)
		rollback()
		http.Error(w, "internal error", http.StatusInternalServerError)
	}

	if pkg == nil && len(prepared) > 0 {
		scope := ""

		name := pub.Name
		if strings.HasPrefix(pub.Name, "@") {
			if idx := strings.Index(pub.Name, "/"); idx > 0 {
				scope = pub.Name[:idx]
				name = pub.Name[idx+1:]
			}
		}

		pkg, err = h.db.CreateNPMPackage(r.Context(), &models.NPMPackage{
			RepositoryID:   repo.ID,
			Scope:          scope,
			Name:           name,
			FullName:       pub.Name,
			LatestVersion:  prepared[0].version,
			PackumentStale: true,
		})
		if err != nil {
			uploadResult = "error"

			if isUniqueViolation(err) {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "package already exists"})
				return
			}

			h.log.Errorw("failed to create npm package", "name", pub.Name, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)

			return
		}

		createdPkg = true
	}

	if pkg == nil || (len(prepared) == 0 && len(deprecations) == 0) {
		uploadResult = "error"

		http.Error(w, "no versions to publish", http.StatusBadRequest)

		return
	}

	for _, pv := range prepared {
		h.log.Debugw("npm publish", "repo", repoName, "pkg", pub.Name, "version", pv.version)

		if err := h.storeNPMVersion(r.Context(), repo.ID, pkg.ID, pv); err != nil {
			switch {
			case errors.Is(err, quota.ErrExceeded):
				uploadResult = "error"

				rollback()
				quota.WriteExceeded(w)
			case errors.Is(err, errNPMVersionExists):
				uploadResult = "error"

				rollback()
				writeJSON(w, http.StatusConflict, map[string]string{"error": "version already exists"})
			default:
				fail("failed to store npm version", err)
			}

			return
		}

		createdVersions = append(createdVersions, pv)
	}

	for tag, ver := range pub.DistTags {
		if _, err := h.db.UpsertNPMDistTag(r.Context(), &models.NPMDistTag{
			PackageID: pkg.ID,
			Tag:       tag,
			Version:   ver,
		}); err != nil {
			fail("failed to upsert dist-tag", err)
			return
		}
	}

	if err := h.applyNPMDeprecations(r.Context(), pkg, deprecations); err != nil {
		fail("failed to apply npm deprecation", err)
		return
	}

	if err := h.db.MarkPackumentStale(r.Context(), pkg.ID); err != nil {
		h.log.Warnw("failed to mark packument stale", "pkg", pub.Name, "error", err)
	}

	if h.npmWorker != nil {
		h.npmWorker.NotifyStale(pkg.ID)
	}

	for _, pv := range createdVersions {
		webhook.Emit(r.Context(), h.publisher, repo.ID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
			Repository: repo.Name,
			Format:     "npm",
			Name:       pub.Name,
			Version:    pv.version,
			Path:       pv.filename,
			SHA256:     pv.sha256Hex,
			Size:       pv.att.size,
		}))
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"ok":  true,
		"id":  pub.Name,
		"rev": "1-" + time.Now().UTC().Format("20060102150405"),
	})
}

var errNPMVersionExists = errors.New("npm version already exists")

func (h *NPMUploadHandler) releaseQuota(ctx context.Context, repoID string, size int64) {
	if err := quota.Release(ctx, h.db, repoID, size); err != nil {
		h.log.Warnw("failed to release npm quota", "repo_id", repoID, "error", err)
	}
}

// storeNPMVersion reserves quota for one tarball and persists its blob and
// version row. The tarball bytes are stored first; existence is re-checked
// under the blob lock, which covers only the blob record and the referencing
// insert, so a concurrent unpublish cannot delete the blob in between. The
// reservation is released on every failure path.
func (h *NPMUploadHandler) storeNPMVersion(ctx context.Context, repoID, pkgID string, pv *npmPreparedVersion) error {
	size := pv.att.size

	if err := quota.Enforce(ctx, h.db, repoID, size); err != nil {
		return err
	}

	f, err := os.Open(pv.att.path)
	if err != nil {
		h.releaseQuota(ctx, repoID, size)

		return fmt.Errorf("failed to open spooled tarball: %w", err)
	}

	defer func() { _ = f.Close() }()

	blob := storage.SeekerBlob(pv.sha256Hex, f, map[string]string{
		"sha256": pv.sha256Hex,
		"format": "npm",
	})

	err = storage.CommitBlob(ctx, h.db, h.blobStore, blob, func(ctx context.Context, _ string) error {
		// Upsert into blobs table to get UUID for npm_versions.blob_id FK.
		blobUUID, err := h.db.UpsertBlob(ctx, pv.sha256Hex, pv.sha256Hex, size)
		if err != nil {
			return fmt.Errorf("failed to upsert npm blob: %w", err)
		}

		if _, err := h.db.CreateNPMVersion(ctx, &models.NPMVersion{
			PackageID:       pkgID,
			Version:         pv.version,
			ManifestJSON:    pv.manifest,
			Integrity:       pv.integrity,
			Shasum:          pv.sha1Hex,
			BlobID:          blobUUID,
			TarballFilename: pv.filename,
			CreatedAt:       importmeta.CreatedAt(ctx),
		}); err != nil {
			if isUniqueViolation(err) {
				return errNPMVersionExists
			}

			return fmt.Errorf("failed to create npm version: %w", err)
		}

		return nil
	})
	if err != nil {
		h.releaseQuota(ctx, repoID, size)

		// Drop the blob we may have written unless a row references it.
		if delErr := storage.SafeDeleteBlob(context.WithoutCancel(ctx), h.db, h.blobStore, pv.sha256Hex); delErr != nil {
			h.log.Warnw("failed to clean up npm blob after failed publish", "sha256", pv.sha256Hex, "error", delErr)
		}

		return err
	}

	return nil
}

const npmUnpublishWindow = 72 * time.Hour

// canUnpublish applies npm's policy: versions may be removed freely within
// 72 hours of publication; afterwards only repos allowing full unpublish may.
func canUnpublish(repo *models.Repository, v *models.NPMVersion) bool {
	return repo.AllowFullUnpublish || time.Since(v.CreatedAt) <= npmUnpublishWindow
}

// npmVersionSize resolves the stored size of a version's tarball so its quota
// reservation can be released. Unresolvable blobs (missing sha or object)
// report 0: the artifact slot is still returned.
func (h *NPMUploadHandler) npmVersionSize(ctx context.Context, v *models.NPMVersion) int64 {
	if v.BlobSizeBytes > 0 {
		return v.BlobSizeBytes
	}

	if v.BlobSHA256 == "" {
		return 0
	}

	id, exists, err := h.blobStore.ExistsBySHA256(ctx, v.BlobSHA256)
	if err != nil || !exists {
		return 0
	}

	size, err := h.blobStore.GetSize(ctx, id)
	if err != nil {
		return 0
	}

	return size
}

// errNPMImmutableRelease is the client-facing 409 text for a release version of
// an immutable repository (trash.ErrImmutableRelease).
const errNPMImmutableRelease = "released versions are immutable in this repository; an admin may override with force"

// guardNPMReleases rejects the unpublish of any release version of an
// immutable repository (trash.ErrImmutableRelease) unless an admin forced it.
func guardNPMReleases(r *http.Request, repo *models.Repository, versions []*models.NPMVersion) error {
	for _, v := range versions {
		if err := trash.GuardRelease(r, repo, "npm", v.Version); err != nil {
			return err
		}
	}

	return nil
}

// writeNPMRemoveError maps a removeNPMVersions / guardNPMReleases failure to
// npm's JSON error style: 409 for an immutable release, 404 for versions that
// are no longer live, 500 otherwise.
func (h *NPMUploadHandler) writeNPMRemoveError(w http.ResponseWriter, pkgName string, err error) {
	switch {
	case errors.Is(err, trash.ErrImmutableRelease):
		writeJSON(w, http.StatusConflict, map[string]string{"error": errNPMImmutableRelease})
	case errors.Is(err, database.ErrTrashNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "version not found"})
	default:
		h.log.Errorw("failed to delete npm versions", "pkg", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (h *NPMUploadHandler) emitNPMDeleted(ctx context.Context, repo *models.Repository, pkg *models.NPMPackage, v *models.NPMVersion, size int64) {
	webhook.Emit(ctx, h.publisher, repo.ID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repo.Name,
		Format:     "npm",
		Name:       pkg.FullName,
		Version:    v.Version,
		Path:       v.TarballFilename,
		SHA256:     v.BlobSHA256,
		Size:       size,
	}))
}

// removeNPMVersions unpublishes the given live versions through trash.Delete
// and emits artifact.deleted for each. When the repository keeps a trash the
// version rows are only stamped deleted: tarballs, quota and dist-tags stay
// untouched (dist-tags are merely hidden and reappear on restore) and the
// packument is marked stale so its ETag changes. Otherwise the rows, their
// orphaned dist-tags and unreferenced blobs are removed and each version's
// quota released. Callers must have checked canUnpublish and guardNPMReleases.
func (h *NPMUploadHandler) removeNPMVersions(ctx context.Context, repo *models.Repository, pkg *models.NPMPackage, versions []*models.NPMVersion) error {
	if len(versions) == 0 {
		return nil
	}

	ids := make([]string, len(versions))
	sizes := make([]int64, len(versions))
	labels := make([]string, len(versions))

	for i, v := range versions {
		ids[i] = v.ID
		// Resolve the size while the blob is still referenced by this row.
		sizes[i] = h.npmVersionSize(ctx, v)
		labels[i] = pkg.FullName + "@" + v.Version
	}

	hardRan := false

	err := trash.Delete(ctx, h.db, trash.Request{
		RepoID:   repo.ID,
		Kind:     models.TrashKindNPM,
		IDs:      ids,
		Resource: strings.Join(labels, ", "),
	}, func() error {
		hardRan = true
		return h.hardDeleteNPMVersions(ctx, repo, pkg, versions, sizes)
	})
	if err != nil {
		return err
	}

	if !hardRan {
		for i, v := range versions {
			h.emitNPMDeleted(ctx, repo, pkg, v, sizes[i])
		}
	}

	// Bumps the packument ETag now; the regeneration the callers schedule
	// follows after its debounce.
	if err := h.db.MarkPackumentStale(ctx, pkg.ID); err != nil {
		h.log.Warnw("failed to mark packument stale after unpublish", "pkg", pkg.FullName, "error", err)
	}

	return nil
}

// hardDeleteNPMVersions is the permanent unpublish: version rows, quota, the
// artifact.deleted event, unreferenced blobs and the dist-tags of the removed
// versions.
func (h *NPMUploadHandler) hardDeleteNPMVersions(ctx context.Context, repo *models.Repository, pkg *models.NPMPackage, versions []*models.NPMVersion, sizes []int64) error {
	removed := make(map[string]bool, len(versions))

	// Read the tags while the versions are still live: GetNPMDistTags only
	// returns tags of live versions, so after the delete the tags of the
	// removed ones would be invisible and stay behind.
	tags, err := h.db.GetNPMDistTags(ctx, pkg.ID)
	if err != nil {
		return err
	}

	for i, v := range versions {
		size := sizes[i]

		if err := h.db.DeleteNPMVersion(ctx, pkg.ID, v.Version); err != nil {
			return err
		}

		removed[v.Version] = true

		h.releaseQuota(ctx, repo.ID, size)

		h.emitNPMDeleted(ctx, repo, pkg, v, size)

		if v.BlobSHA256 == "" {
			continue
		}

		if delErr := storage.SafeDeleteBlob(ctx, h.db, h.blobStore, v.BlobSHA256); delErr != nil {
			h.log.Warnw("failed to clean up blob after unpublish", "sha256", v.BlobSHA256, "error", delErr)
		}
	}

	for tag, ver := range tags {
		if removed[ver] {
			if err := h.db.DeleteNPMDistTag(ctx, pkg.ID, tag); err != nil {
				return err
			}
		}
	}

	return nil
}

// handleRevPut handles the PUT /pkg/-rev/<rev> step of `npm unpublish pkg@ver`:
// versions missing from the submitted packument are removed.
func (h *NPMUploadHandler) handleRevPut(w http.ResponseWriter, r *http.Request, repo *models.Repository, pkg *models.NPMPackage, pub *npmPublishBody) {
	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	existing, err := h.db.GetNPMVersionsByPackage(r.Context(), pkg.ID)
	if err != nil {
		h.log.Errorw("failed to get npm versions", "pkg", pub.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	var toRemove []*models.NPMVersion

	for _, v := range existing {
		if _, keep := pub.Versions[v.Version]; keep {
			continue
		}

		if !canUnpublish(repo, v) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cannot unpublish after 72h, use npm deprecate instead"})
			return
		}

		toRemove = append(toRemove, v)
	}

	for tag, ver := range pub.DistTags {
		if err := validateNPMTagName(tag); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if _, ok := pub.Versions[ver]; !ok {
			http.Error(w, "dist-tag "+tag+" points to unknown version "+ver, http.StatusBadRequest)
			return
		}
	}

	if err := guardNPMReleases(r, repo, toRemove); err != nil {
		h.writeNPMRemoveError(w, pub.Name, err)
		return
	}

	if len(toRemove) == len(existing) && len(existing) > 0 {
		if err := h.removeNPMVersions(r.Context(), repo, pkg, toRemove); err != nil {
			h.writeNPMRemoveError(w, pub.Name, err)
			return
		}

		// A no-op while trashed versions of the package remain.
		if err := h.db.DeleteNPMPackage(r.Context(), pkg.ID); err != nil {
			h.log.Errorw("failed to delete npm package", "pkg", pub.Name, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)

			return
		}
	} else {
		if err := h.removeNPMVersions(r.Context(), repo, pkg, toRemove); err != nil {
			h.writeNPMRemoveError(w, pub.Name, err)
			return
		}

		for tag, ver := range pub.DistTags {
			if _, err := h.db.UpsertNPMDistTag(r.Context(), &models.NPMDistTag{PackageID: pkg.ID, Tag: tag, Version: ver}); err != nil {
				h.log.Errorw("failed to upsert dist-tag", "pkg", pub.Name, "tag", tag, "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)

				return
			}
		}

		if len(pub.DistTags) > 0 {
			h.markStaleAfterTagChange(r.Context(), pkg)
		}
	}

	if h.npmWorker != nil {
		h.npmWorker.NotifyStale(pkg.ID)
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": pub.Name, "rev": "2-" + time.Now().UTC().Format("20060102150405")})
}

// handleUnpublish serves two DELETE forms:
//
//	DELETE /pkg/-rev/<rev>               remove the whole package
//	DELETE /pkg/-/<file>.tgz/-rev/<rev>  final step of `npm unpublish pkg@ver`
func (h *NPMUploadHandler) handleUnpublish(w http.ResponseWriter, r *http.Request) {
	if !requireAuth(w, r) {
		return
	}

	vars := urlparams.Vars(r)
	repoName := vars["name"]
	rawPkg := vars["*"]

	repo, err := h.resolveRepo(r.Context(), repoName)
	if err != nil {
		h.log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repo == nil {
		http.NotFound(w, r)
		return
	}

	base, _, _ := strings.Cut(rawPkg, "/-rev/")

	var tarball string

	if before, after, ok := strings.Cut(base, "/-/"); ok {
		base = before
		tarball = after
	}

	pkgName := npmDecodePackageName(base)

	pkg, err := h.db.GetNPMPackageByName(r.Context(), repo.ID, pkgName)
	if err != nil {
		h.log.Errorw("failed to get npm package", "name", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	versions, err := h.db.GetNPMVersionsByPackage(r.Context(), pkg.ID)
	if err != nil {
		h.log.Errorw("failed to get npm versions", "pkg", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if tarball != "" {
		// The preceding PUT normally removed the version already; deleting
		// the tarball of a version that is gone is a successful no-op.
		for _, v := range versions {
			if v.TarballFilename != tarball {
				continue
			}

			if !canUnpublish(repo, v) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "cannot unpublish after 72h, use npm deprecate instead"})
				return
			}

			single := []*models.NPMVersion{v}

			if err := guardNPMReleases(r, repo, single); err != nil {
				h.writeNPMRemoveError(w, pkgName, err)
				return
			}

			if err := h.removeNPMVersions(r.Context(), repo, pkg, single); err != nil {
				h.writeNPMRemoveError(w, pkgName, err)
				return
			}

			break
		}
	} else {
		for _, v := range versions {
			if !canUnpublish(repo, v) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "cannot unpublish after 72h, use npm deprecate instead"})
				return
			}
		}

		if err := guardNPMReleases(r, repo, versions); err != nil {
			h.writeNPMRemoveError(w, pkgName, err)
			return
		}

		if err := h.removeNPMVersions(r.Context(), repo, pkg, versions); err != nil {
			h.writeNPMRemoveError(w, pkgName, err)
			return
		}

		// A no-op while trashed versions of the package remain.
		if err := h.db.DeleteNPMPackage(r.Context(), pkg.ID); err != nil {
			h.log.Errorw("failed to delete npm package", "pkg", pkgName, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)

			return
		}
	}

	if h.npmWorker != nil {
		h.npmWorker.NotifyStale(pkg.ID)
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *NPMUploadHandler) handlePutDistTag(w http.ResponseWriter, r *http.Request) {
	if !requireAuth(w, r) {
		return
	}

	vars := urlparams.Vars(r)
	repoName := vars["name"]
	pkgName := npmDecodePackageName(vars["pkg"])
	tag := vars["tag"]

	if err := validateNPMTagName(tag); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	repo, err := h.resolveRepo(r.Context(), repoName)
	if err != nil {
		h.log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repo == nil {
		http.NotFound(w, r)
		return
	}

	pkg, err := h.db.GetNPMPackageByName(r.Context(), repo.ID, pkgName)
	if err != nil {
		h.log.Errorw("failed to get npm package", "name", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	var version string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxNPMDistTagBody)).Decode(&version); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	v, err := h.db.GetNPMVersion(r.Context(), pkg.ID, version)
	if err != nil {
		h.log.Errorw("failed to get npm version", "pkg", pkgName, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if v == nil {
		http.Error(w, "version not found", http.StatusNotFound)
		return
	}

	if _, err := h.db.UpsertNPMDistTag(r.Context(), &models.NPMDistTag{
		PackageID: pkg.ID,
		Tag:       tag,
		Version:   version,
	}); err != nil {
		h.log.Errorw("failed to upsert dist-tag", "pkg", pkgName, "tag", tag, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.markStaleAfterTagChange(r.Context(), pkg)

	distTags, err := h.db.GetNPMDistTags(r.Context(), pkg.ID)
	if err != nil {
		h.log.Errorw("failed to get npm dist-tags", "pkg", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(distTags); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *NPMUploadHandler) handleDeleteDistTag(w http.ResponseWriter, r *http.Request) {
	if !requireAuth(w, r) {
		return
	}

	vars := urlparams.Vars(r)
	repoName := vars["name"]
	pkgName := npmDecodePackageName(vars["pkg"])
	tag := vars["tag"]

	if tag == "latest" {
		http.Error(w, "cannot delete 'latest' dist-tag", http.StatusBadRequest)
		return
	}

	repo, err := h.resolveRepo(r.Context(), repoName)
	if err != nil {
		h.log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repo == nil {
		http.NotFound(w, r)
		return
	}

	pkg, err := h.db.GetNPMPackageByName(r.Context(), repo.ID, pkgName)
	if err != nil {
		h.log.Errorw("failed to get npm package", "name", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	if err := h.db.DeleteNPMDistTag(r.Context(), pkg.ID, tag); err != nil {
		h.log.Errorw("failed to delete dist-tag", "pkg", pkgName, "tag", tag, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.markStaleAfterTagChange(r.Context(), pkg)

	distTags, err := h.db.GetNPMDistTags(r.Context(), pkg.ID)
	if err != nil {
		h.log.Errorw("failed to get npm dist-tags", "pkg", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")

	if encErr := json.NewEncoder(w).Encode(distTags); encErr != nil {
		_ = encErr // response header already written
	}
}

// markStaleAfterTagChange bumps the packument ETag after dist-tags changed, so
// a client holding the cached packument revalidates instead of getting a 304
// with the old tags, and schedules the regeneration.
func (h *NPMUploadHandler) markStaleAfterTagChange(ctx context.Context, pkg *models.NPMPackage) {
	if err := h.db.MarkPackumentStale(ctx, pkg.ID); err != nil {
		h.log.Warnw("failed to mark packument stale after dist-tag change", "pkg", pkg.FullName, "error", err)
	}

	if h.npmWorker != nil {
		h.npmWorker.NotifyStale(pkg.ID)
	}
}

func npmDecodePackageName(raw string) string {
	s := strings.ReplaceAll(raw, "%2F", "/")
	s = strings.ReplaceAll(s, "%2f", "/")

	return s
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()

	return strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "unique") ||
		strings.Contains(msg, "23505")
}
