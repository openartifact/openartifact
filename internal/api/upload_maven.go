// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"crypto/md5"  //nolint:gosec // required by Maven protocol
	"crypto/sha1" //nolint:gosec // required by Maven protocol
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
	pkgmaven "github.com/openartifact/openartifact/pkg/maven"
)

const MaxMavenUploadBytes int64 = 1 << 30 // 1 GiB

type MavenUploadHandler struct {
	log         *logger.Logger
	db          database.Database
	blobStore   storage.BlobStore
	reg         *metrics.Registry
	mavenWorker *worker.MavenMetadataWorker
	publisher   webhook.Publisher
}

// SetPublisher wires the webhook publisher for artifact.* events.
func (h *MavenUploadHandler) SetPublisher(p webhook.Publisher) {
	h.publisher = p
}

func NewMavenUploadHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, reg *metrics.Registry) *MavenUploadHandler {
	return &MavenUploadHandler{log: log, db: db, blobStore: blobStore, reg: reg}
}

// SetMavenWorker wires the debounced maven-metadata.xml regeneration worker.
// Call NotifyStale(repoID, groupID, artifactID) after each successful upload.
func (h *MavenUploadHandler) SetMavenWorker(w *worker.MavenMetadataWorker) {
	h.mavenWorker = w
}

// Register mounts PUT/DELETE using a true chi wildcard ("/*"), not a
// regexp param: chi's {name:regexp} params never span "/" (the tail
// delimiter defaults to '/', so the regexp is only ever tested against a
// single path segment), so {path:.*} can never match a multi-segment GAV
// path and silently 404s every real upload/delete.
func (h *MavenUploadHandler) Register(router chi.Router) {
	router.Put("/repos/maven/{name}/*", h.handlePut)
	router.Delete("/repos/maven/{name}/*", h.handleDelete)
}

// errMavenNotHosted is returned for writes to proxy and group repositories,
// which are read-only views.
var errMavenNotHosted = errors.New("maven repository is not hosted")

func (h *MavenUploadHandler) resolveRepoID(ctx context.Context, name string) (string, error) {
	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		return "", err
	}

	if repo == nil {
		return "", nil
	}

	if repo.RepoType != "" && repo.RepoType != repotype.Hosted {
		return "", errMavenNotHosted
	}

	return repo.ID, nil
}

//nolint:gocyclo,cyclop // protocol handler: branching mirrors spec state machine
func (h *MavenUploadHandler) handlePut(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]

	uploadResult := "success"

	if h.reg != nil {
		defer func() { h.reg.UploadsTotal.WithLabelValues("maven", uploadResult).Inc() }()
	}

	repoID, err := h.resolveRepoID(r.Context(), repoName)
	if err != nil {
		if errors.Is(err, errMavenNotHosted) {
			uploadResult = "error"

			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "proxy and group repositories are read-only", http.StatusMethodNotAllowed)

			return
		}

		uploadResult = "error"

		h.log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repoID == "" {
		uploadResult = "error"

		http.NotFound(w, r)

		return
	}

	artPath := chi.URLParam(r, "*")
	filename := filepath.Base(artPath)

	groupPath, artifactIDCoord, version, _, parseErr := pkgmaven.ParseRepositoryPath(artPath)
	if parseErr != nil || groupPath == "" || version == "" || !pkgmaven.CleanSegments(artPath) {
		uploadResult = "error"

		http.Error(w, "invalid maven repository path", http.StatusBadRequest)

		return
	}

	checksumSuffixes := []string{".md5", ".sha1", ".sha256", ".sha512"}
	for _, suffix := range checksumSuffixes {
		if strings.HasSuffix(filename, suffix) {
			_, _ = io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, MaxMavenUploadBytes))
			w.WriteHeader(http.StatusOK)

			return
		}
	}

	// maven-metadata.xml (and its checksums) is generated server-side from
	// the stored artifacts; a client-supplied copy is acknowledged and dropped.
	if filename == "maven-metadata.xml" {
		_, _ = io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, MaxMavenUploadBytes))
		w.WriteHeader(http.StatusOK)

		return
	}

	groupID := strings.ReplaceAll(groupPath, "/", ".")
	isSnapshot := pkgmaven.IsSnapshot(version)

	parts, ok := parseMavenFilename(filename, artifactIDCoord, version, isSnapshot)
	if !ok {
		uploadResult = "error"

		http.Error(w, "filename does not match artifactId-version in path", http.StatusBadRequest)

		return
	}

	if err := quota.Precheck(r.Context(), h.db, repoID, r.ContentLength); err != nil {
		uploadResult = "error"

		if errors.Is(err, quota.ErrExceeded) {
			quota.WriteExceeded(w)

			return
		}

		h.log.Errorw("failed to precheck quota", "repository", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	tmp, err := os.CreateTemp("", "maven-upload-*")
	if err != nil {
		uploadResult = "error"

		h.log.Errorw("failed to create temp file", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	hMD5 := md5.New()   //nolint:gosec // required by Maven protocol
	hSHA1 := sha1.New() //nolint:gosec // required by Maven protocol
	hSHA256 := sha256.New()
	hSHA512 := sha512.New()

	limit := MaxMavenUploadBytes
	if parts.ext == "pom" {
		limit = pkgmaven.MaxPOMBytes
	}

	size, err := io.Copy(io.MultiWriter(tmp, hMD5, hSHA1, hSHA256, hSHA512), http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		uploadResult = "error"

		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}

		h.log.Errorw("failed to read request body", "path", artPath, "error", err)
		http.Error(w, "failed to read body", http.StatusBadRequest)

		return
	}

	md5Hex := hex.EncodeToString(hMD5.Sum(nil))
	sha1Hex := hex.EncodeToString(hSHA1.Sum(nil))
	sha256Hex := hex.EncodeToString(hSHA256.Sum(nil))
	sha512Hex := hex.EncodeToString(hSHA512.Sum(nil))

	existing, err := h.db.GetMavenArtifactByPath(r.Context(), repoID, artPath)
	if err != nil {
		uploadResult = "error"

		h.log.Errorw("failed to check existing artifact", "path", artPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if existing != nil && existing.SHA256 == sha256Hex {
		// Identical content already stored (idempotent re-publish).
		w.WriteHeader(http.StatusOK)

		return
	}

	if existing != nil && !isSnapshot {
		uploadResult = "error"

		writeMavenConflict(w, "release artifact already exists with different content")

		return
	}

	var pomInfo pomLicenseInfo

	if parts.ext == "pom" && parts.classifier == "" {
		var rejected bool

		if rejected, pomInfo = h.enforcePomLicenses(w, r, tmp, repoID, groupID, artifactIDCoord, version, artPath); rejected {
			uploadResult = "error"
			return
		}
	}

	// Reserve quota before persisting; released on every failure path below.
	if err := quota.Enforce(r.Context(), h.db, repoID, size); err != nil {
		uploadResult = "error"

		if errors.Is(err, quota.ErrExceeded) {
			quota.WriteExceeded(w)

			return
		}

		h.log.Errorw("failed to reserve quota", "repository", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	committed := false

	defer func() {
		if committed {
			return
		}

		if relErr := quota.Release(context.WithoutCancel(r.Context()), h.db, repoID, size); relErr != nil {
			h.log.Warnw("failed to release quota", "repository", repoName, "error", relErr)
		}
	}()

	// An administrator importing from another manager may preserve the origin
	// timestamp (see importmeta); everyone else is stamped "now".
	createdAt := importmeta.CreatedAt(r.Context())
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	art := models.MavenArtifact{
		RepositoryID:        repoID,
		GroupID:             groupID,
		ArtifactCoord:       artifactIDCoord,
		Version:             version,
		Classifier:          parts.classifier,
		Extension:           parts.ext,
		SnapshotTimestamp:   parts.timestamp,
		SnapshotBuildNumber: parts.build,
		IsSnapshot:          isSnapshot,
		RepoPath:            artPath,
		SHA256:              sha256Hex,
		MD5:                 md5Hex,
		SHA1:                sha1Hex,
		SHA512:              sha512Hex,
		CreatedAt:           createdAt,
		UpdatedAt:           time.Now(),
	}

	// Snapshot overwrite size must be resolved before the row is replaced.
	var oldSize int64

	if existing != nil {
		oldSize = h.blobSize(r.Context(), existing.BlobID, existing.SHA256)
	}

	var (
		stage     string
		insertErr error
	)

	lockErr := storage.WithBlobLock(r.Context(), h.db, sha256Hex, func(ctx context.Context) error {
		blobKey, exists, existsErr := h.blobStore.ExistsBySHA256(ctx, sha256Hex)
		if existsErr != nil {
			stage = "check blob"

			return existsErr
		}

		if !exists {
			if _, seekErr := tmp.Seek(0, io.SeekStart); seekErr != nil {
				stage = "rewind"

				return seekErr
			}

			blobKey, existsErr = h.blobStore.Put(ctx, tmp, map[string]string{
				"sha256":    sha256Hex,
				"format":    "maven",
				"repo_name": repoName,
			})
			if existsErr != nil {
				stage = "store blob"

				return existsErr
			}
		}

		// maven_artifacts.blob_id references blobs(id); register the blob row.
		blobID, upsertErr := h.db.UpsertBlob(ctx, sha256Hex, blobKey, size)
		if upsertErr != nil {
			stage = "upsert blob"

			return upsertErr
		}

		art.BlobID = blobID

		insertErr = h.db.CreateMavenArtifact(ctx, &art)
		if insertErr != nil {
			stage = "create artifact"

			return insertErr
		}

		return nil
	})
	if lockErr != nil {
		uploadResult = "error"

		// The blob is content-addressed and may be referenced by other
		// artifacts; SafeDeleteBlob only removes it once nothing references it.
		if delErr := storage.SafeDeleteBlob(context.WithoutCancel(r.Context()), h.db, h.blobStore, sha256Hex); delErr != nil {
			h.log.Warnw("failed to clean up blob after error", "sha256", sha256Hex, "error", delErr)
		}

		if insertErr != nil && isUniqueViolation(insertErr) {
			writeMavenConflict(w, "artifact already exists")

			return
		}

		h.log.Errorw("maven upload failed", "stage", stage, "path", artPath, "error", lockErr)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	committed = true

	if existing != nil {
		// Snapshot overwrite: the old content no longer counts, and its blob
		// may now be unreferenced.
		if relErr := quota.Release(r.Context(), h.db, repoID, oldSize); relErr != nil {
			h.log.Warnw("failed to release quota for replaced artifact", "repository", repoName, "error", relErr)
		}

		if delErr := storage.SafeDeleteBlob(r.Context(), h.db, h.blobStore, existing.SHA256); delErr != nil {
			h.log.Warnw("failed to clean up replaced blob", "sha256", existing.SHA256, "error", delErr)
		}
	}

	if h.mavenWorker != nil {
		h.mavenWorker.NotifyStale(repoID, groupID, artifactIDCoord)
	}

	h.afterPOMStored(r.Context(), repoID, art.ID, pomInfo)

	webhook.Emit(r.Context(), h.publisher, repoID, webhook.EventArtifactUploaded,
		webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
			Repository: repoName,
			Format:     "maven",
			Name:       groupID + ":" + artifactIDCoord,
			Version:    version,
			Path:       artPath,
			SHA256:     sha256Hex,
			Size:       size,
		}))

	statusCode := http.StatusCreated
	if existing != nil {
		statusCode = http.StatusOK
	}

	// The package id is what the properties API addresses artifacts by.
	w.Header().Set("X-Oa-Artifact-Id", art.ID)
	w.WriteHeader(statusCode)
}

// parseUploadedPOM parses the spooled pom from its start; nil when it cannot be
// read or parsed.
func parseUploadedPOM(pomFile *os.File) *pkgmaven.POM {
	if _, err := pomFile.Seek(0, io.SeekStart); err != nil {
		return nil
	}

	pom, err := pkgmaven.ParsePOM(pomFile)
	if err != nil {
		return nil
	}

	return pom
}

// pomLicenseInfo is what the license check learned about an uploaded pom, for
// the work that follows the store.
type pomLicenseInfo struct {
	// inherits is set when the pom declares no license and has a <parent>: its
	// licenses (inherited, possibly none yet) are recorded as its scan result.
	inherits  bool
	inherited []string
	// mayBeParent is set for a pom-packaged pom, which other poms may inherit
	// their licenses from.
	mayBeParent bool
}

// enforcePomLicenses applies the license policy to the licenses declared in
// the uploaded .pom, or inherited from its <parent> chain when it declares none
// (parents are looked up in this repository only; an absent parent leaves the
// licenses unresolved, see afterPOMStored). The jar is stored before the pom,
// so its siblings are already in the repository. It returns true when it wrote
// the rejection. On a rejection the sibling files already stored for the same
// group, artifact and version are quarantined best-effort, so the blocked
// version cannot be downloaded through them. A pom that cannot be read declares
// nothing the policy can check: without a blocking policy it is admitted, with
// one it is refused with 422 LICENSE_METADATA_UNREADABLE.
func (h *MavenUploadHandler) enforcePomLicenses(w http.ResponseWriter, r *http.Request, pomFile *os.File, repoID, groupID, artifactID, version, artPath string) (bool, pomLicenseInfo) {
	pom := parseUploadedPOM(pomFile)
	if pom == nil {
		return licensepolicy.RefuseUnderivable(w, r, h.db, h.log, repoID), pomLicenseInfo{}
	}

	info := pomLicenseInfo{mayBeParent: strings.EqualFold(strings.TrimSpace(pom.Packaging), "pom")}
	licenses := licensemeta.ParsePOM(pom)

	if parent := licensemeta.POMParent(pom); parent != nil {
		inherited, _, err := inheritedPOMLicenses(r.Context(), h.db, h.blobStore, repoID, parent)
		if err != nil {
			h.log.Warnw("maven upload: parent pom licenses could not be resolved", "path", artPath, "error", err)

			if licensepolicy.RefuseUnderivable(w, r, h.db, h.log, repoID) {
				return true, info
			}
		}

		info.inherits, info.inherited, licenses = true, inherited, inherited
	}

	if !licensepolicy.Enforce(w, r, h.db, h.log, repoID, licenses...) {
		return false, info
	}

	quarantineSiblings(context.WithoutCancel(r.Context()), h.db, h.log, repoID, groupID, artifactID, version, artPath, licenses)

	return true, info
}

// afterPOMStored does what follows a pom being stored: a pom that inherits its
// licenses gets its scan result recorded (empty while the parent is not
// stored), and a pom that may be a parent re-evaluates the poms still waiting
// for one. Failures are logged; the upload has succeeded.
func (h *MavenUploadHandler) afterPOMStored(ctx context.Context, repoID, artifactID string, info pomLicenseInfo) {
	ctx = context.WithoutCancel(ctx)

	if info.inherits {
		if err := recordPOMLicenses(ctx, h.db, h.log, repoID, artifactID, info.inherited); err != nil {
			h.log.Warnw("maven upload: recording inherited licenses failed", "artifact_id", artifactID, "error", err)
		}
	}

	if !info.mayBeParent {
		return
	}

	if n, err := reevaluateWaitingPOMs(ctx, h.db, h.blobStore, h.log, repoID); err != nil {
		h.log.Warnw("maven upload: re-evaluating poms waiting for a parent failed", "error", err)
	} else if n > 0 {
		h.log.Infow("maven upload: poms re-evaluated after their parent was stored", "repository_id", repoID, "count", n)
	}
}

// errMavenImmutableRelease is the client-facing 409 text for a delete of a
// release artifact of an immutable repository (trash.ErrImmutableRelease).
const errMavenImmutableRelease = "released versions are immutable in this repository; an admin may override with force"

func writeMavenConflict(w http.ResponseWriter, msg string) {
	body, err := json.Marshal(map[string]string{"error": msg})
	if err != nil {
		http.Error(w, msg, http.StatusConflict)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_, _ = w.Write(body)
}

// blobSize returns the stored size of the blob (row id + content digest of a
// stored artifact), or 0 when it cannot be determined.
func (h *MavenUploadHandler) blobSize(ctx context.Context, blobID, sha256Hex string) int64 {
	if blobID != "" {
		if _, n, err := h.db.GetDebBlobInfo(ctx, blobID); err == nil && n > 0 {
			return n
		}
	}

	key, ok, err := h.blobStore.ExistsBySHA256(ctx, sha256Hex)
	if err == nil && ok {
		if n, sizeErr := h.blobStore.GetSize(ctx, key); sizeErr == nil {
			return n
		}
	}

	h.log.Warnw("failed to determine blob size", "sha256", sha256Hex)

	return 0
}

func (h *MavenUploadHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]

	repoID, err := h.resolveRepoID(r.Context(), repoName)
	if err != nil {
		if errors.Is(err, errMavenNotHosted) {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "proxy and group repositories are read-only", http.StatusMethodNotAllowed)

			return
		}

		h.log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repoID == "" {
		http.NotFound(w, r)
		return
	}

	artPath := chi.URLParam(r, "*")

	if strings.HasSuffix(artPath, "/") {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	version, parseErr := parseRepositoryPathVersion(artPath)
	if parseErr != nil || version == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	art, err := h.db.GetMavenArtifactByPath(r.Context(), repoID, artPath)
	if err != nil {
		h.log.Errorw("failed to look up artifact", "path", artPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if art == nil {
		http.NotFound(w, r)
		return
	}

	if err := trash.GuardReleaseByName(r, h.db, repoName, "maven", art.Version); err != nil {
		if errors.Is(err, trash.ErrImmutableRelease) {
			writeMavenConflict(w, errMavenImmutableRelease)
			return
		}

		h.log.Errorw("failed to check release immutability", "repo", repoName, "path", artPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	size := h.blobSize(r.Context(), art.BlobID, art.SHA256)

	err = trash.Delete(r.Context(), h.db, trash.Request{
		RepoID: repoID, Kind: models.TrashKindMaven, IDs: []string{art.ID}, Resource: artPath,
	}, func() error {
		if delErr := h.db.DeleteMavenArtifact(r.Context(), art.ID); delErr != nil {
			return delErr
		}

		if relErr := quota.Release(r.Context(), h.db, repoID, size); relErr != nil {
			h.log.Warnw("failed to release quota", "repository", repoName, "error", relErr)
		}

		if blobErr := storage.SafeDeleteBlob(r.Context(), h.db, h.blobStore, art.SHA256); blobErr != nil {
			h.log.Warnw("failed to clean up blob after delete", "sha256", art.SHA256, "error", blobErr)
		}

		return nil
	})
	if err != nil {
		if errors.Is(err, database.ErrTrashNotFound) {
			http.NotFound(w, r)
			return
		}

		h.log.Errorw("failed to delete artifact", "id", art.ID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if h.mavenWorker != nil {
		h.mavenWorker.NotifyStale(repoID, art.GroupID, art.ArtifactCoord)
	}

	webhook.Emit(r.Context(), h.publisher, repoID, webhook.EventArtifactDeleted,
		webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
			Repository: repoName,
			Format:     "maven",
			Name:       art.GroupID + ":" + art.ArtifactCoord,
			Version:    art.Version,
			Path:       artPath,
			SHA256:     art.SHA256,
			Size:       size,
		}))

	w.WriteHeader(http.StatusNoContent)
}

// parseRepositoryPathVersion extracts only the version from a Maven repository path.
func parseRepositoryPathVersion(path string) (string, error) {
	groupPath, artifactID, version, filename, err := pkgmaven.ParseRepositoryPath(path)
	_ = groupPath
	_ = artifactID
	_ = filename

	return version, err
}

// mavenFileParts is the result of splitting a Maven filename.
type mavenFileParts struct {
	classifier string
	ext        string
	timestamp  string // timestamped snapshots only
	build      int    // timestamped snapshots only
}

// parseMavenFilename splits filename into classifier, extension and (for
// timestamped snapshots) timestamp/build number, given the path's artifactId
// and version. Extensions may contain dots (tar.gz, jar.asc).
func parseMavenFilename(filename, artifactID, version string, isSnapshot bool) (mavenFileParts, bool) {
	var parts mavenFileParts

	prefix := artifactID + "-" + version

	if isSnapshot {
		if t, bn, _, err := pkgmaven.ParseSnapshotTimestamp(filename); err == nil {
			base := strings.TrimSuffix(version, "-SNAPSHOT")
			prefix = artifactID + "-" + base + "-" + t + "-" + strconv.Itoa(bn)
			parts.timestamp, parts.build = t, bn
		}
	}

	rest, found := strings.CutPrefix(filename, prefix)
	if !found {
		return mavenFileParts{}, false
	}

	if after, ok := strings.CutPrefix(rest, "-"); ok {
		cls, e, hasExt := strings.Cut(after, ".")
		if !hasExt || e == "" || cls == "" {
			return mavenFileParts{}, false
		}

		parts.classifier, parts.ext = cls, e

		return parts, true
	}

	if after, ok := strings.CutPrefix(rest, "."); ok && after != "" {
		parts.ext = after

		return parts, true
	}

	return mavenFileParts{}, false
}
