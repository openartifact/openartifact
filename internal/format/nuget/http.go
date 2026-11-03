// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/httpx"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/pathseg"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
	pkgnuget "github.com/openartifact/openartifact/pkg/nuget"
)

// registrationPageSize is the number of versions per registration page.
const registrationPageSize = 64

const (
	// pushFormOverhead leaves room for form fields and multipart boundaries on
	// top of the package.
	pushFormOverhead = 1 << 20
)

// validPackageID is the id charset NuGet accepts (pushedPackage also refuses
// the dot segments it admits); anything else (notably "/")
// would be stored but never retrievable through the URL routes.
var validPackageID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// HTTPStore is the part of the database HTTPHandler needs.
type HTTPStore interface {
	quarantine.Checker
	BlobReferenceCount(ctx context.Context, sha256 string) (int, error)
	CreateNuGetPackage(ctx context.Context, pkg *models.NuGetPackage) error
	DecrementStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error
	DeleteNuGetPackage(ctx context.Context, repoID string, packageID string, version string) error
	GetNuGetPackage(ctx context.Context, repoID string, packageID string, version string) (*models.NuGetPackage, error)
	GetRawArtifact(ctx context.Context, repoID string, path string) (*models.RawArtifact, error)
	GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error)
	// CountFileDownload counts one download of a package file; it is batched and
	// written to the database with the next flush.
	CountFileDownload(fileID string)
	ListLicensePolicies(ctx context.Context, repoID string) ([]*models.LicensePolicy, error)
	ListNuGetPackageVersions(ctx context.Context, repoID string, packageID string) ([]*models.NuGetPackage, error)
	NuGetVersionsStamp(ctx context.Context, repoID string, packageID string) (string, error)
	ReserveStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) (bool, error)
	SearchNuGetPackages(ctx context.Context, repoID string, query string, skip int, take int) ([]*models.NuGetPackage, int, error)
	SetNuGetDependencyGroups(ctx context.Context, repoID string, packageID string, version string, groups []byte) error
	UpsertRawArtifact(ctx context.Context, artifact *models.RawArtifact) error
}

type HTTPHandler struct {
	*base.Handler

	// maxPush bounds one pushed .nupkg.
	maxPush int64
	// deps caches dependency groups by blob digest; nuspecGate bounds the
	// concurrent nupkg reads behind cache misses.
	deps       *depCache
	nuspecGate base.Gate
	DB         HTTPStore
}

func NewHTTPHandler(log *logger.Logger, db HTTPStore, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{
		Handler:    base.New(log, db, blobStore, baseURL),
		maxPush:    maxNuGetPackageBytes,
		deps:       newDepCache(depCacheEntries),
		nuspecGate: base.NewGate(maxConcurrentNuspecReads), DB: db,
	}
}

func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Get("/v3/index.json", base.Compress(h.handleServiceIndex))
	r.Put("/v3/package", h.handlePushPackage)
	// dotnet nuget push appends a slash to the PackagePublish URL of the service index.
	r.Put("/v3/package/", h.handlePushPackage)
	r.Delete("/v3/package/{id}/{version}", h.handleDeletePackage)
	r.Get("/v3/package/{id}/index.json", h.handleVersionIndex)
	r.Get("/v3/package/{id}/{version}/{filename}", h.handleDownloadPackage)
	r.Get("/v3/registration/{id}/index.json", h.handleRegistrationIndex)
	// {versionfile} is "<version>.json"; a chi parameter would stop at the
	// first '.' of the version.
	r.Get("/v3/registration/{id}/{versionfile}", base.Compress(h.handleRegistrationLeaf))
	r.Get("/v3/query", base.Compress(h.handleSearch))
	r.Get("/v3/autocomplete", base.Compress(h.handleAutocomplete))
}

func (h *HTTPHandler) RegisterRoutes(r chi.Router, prefix string) {
	r.Route(prefix, func(r chi.Router) {
		h.RegisterRepoRoutes(r)
	})
	h.Log.Infow("nuget handler registered", "prefix", prefix)
}

// resolveRepo returns the ID of the repository named in the URL. When there is
// none (unknown repository or a lookup failure) it writes the response and
// returns "".
func (h *HTTPHandler) resolveRepo(w http.ResponseWriter, r *http.Request) string {
	return h.ResolveRepoIDOrRespond(w, r, urlparams.Vars(r)["name"])
}

// resolveRepoInfo is resolveRepo for handlers that also need the repository's
// visibility (to set cache headers). It writes the response and returns nil
// when there is no repository.
func (h *HTTPHandler) resolveRepoInfo(w http.ResponseWriter, r *http.Request) *models.Repository {
	repoName := urlparams.Vars(r)["name"]

	repo, err := h.ResolveRepo(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return nil
	}

	if repo == nil {
		http.NotFound(w, r)
	}

	return repo
}

// repoBaseURL is the repository's root URL as the client reached it (BASE_URL
// when set, else the request's origin). NuGet clients take the service index
// @ids as full URLs, so every URL built from it is absolute and its document's
// validator must carry it (versionsETag).
func (h *HTTPHandler) repoBaseURL(w http.ResponseWriter, r *http.Request) string {
	return h.PublicBase(w, r) + "/repos/" + urlparams.Vars(r)["name"]
}

func registrationIndexURL(baseURL, id string) string {
	return baseURL + "/v3/registration/" + url.PathEscape(id) + "/index.json"
}

func registrationLeafURL(baseURL, id, version string) string {
	return baseURL + "/v3/registration/" + url.PathEscape(id) + "/" + url.PathEscape(version) + ".json"
}

func packageContentURL(baseURL, id, version string) string {
	return fmt.Sprintf(
		"%s/v3/package/%s/%s/%s.%s.nupkg",
		baseURL,
		url.PathEscape(id),
		url.PathEscape(version),
		url.PathEscape(id),
		url.PathEscape(version),
	)
}

func nupkgName(id, version string) string { return id + "." + version + ".nupkg" }

func (h *HTTPHandler) handleServiceIndex(w http.ResponseWriter, r *http.Request) {
	if h.resolveRepo(w, r) == "" {
		return
	}

	baseURL := h.repoBaseURL(w, r)

	index := map[string]any{
		"version": "3.0.0",
		"resources": []map[string]string{
			{
				"@id":   baseURL + "/v3/registration/",
				"@type": "RegistrationsBaseUrl",
			},
			{
				"@id":   baseURL + "/v3/package/",
				"@type": "PackageBaseAddress/3.0.0",
			},
			{
				"@id":   baseURL + "/v3/query",
				"@type": "SearchQueryService",
			},
			{
				"@id":   baseURL + "/v3/autocomplete",
				"@type": "SearchAutocompleteService",
			},
			{
				"@id":   baseURL + "/v3/package",
				"@type": "PackagePublish/2.0.0",
			},
		},
	}

	httpx.WriteJSON(w, http.StatusOK, index)
}

// readPushedPackage reads the "package" part of a push request, hashed with
// SHA-512 (sum512 receives it) besides the SHA-256 of the part. On failure it
// writes the response and returns false.
func (h *HTTPHandler) readPushedPackage(w http.ResponseWriter, r *http.Request, sum512 hash.Hash) (*base.Spool, bool) {
	// Cap the whole body: the package is bounded by maxPush, the form fields by
	// the overhead.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxPush+pushFormOverhead)

	form, err := base.ReadForm(r, base.FormSpec{
		FileField: "package",
		MaxFile:   h.maxPush,
		MaxFields: pushFormOverhead,
		Hashes:    []hash.Hash{sum512},
	})

	switch {
	case err == nil:
		// The part is all a push needs; the form owns nothing else.
		return form.File, true
	case base.IsTooLarge(err):
		http.Error(w, "package too large", http.StatusRequestEntityTooLarge)
	case errors.Is(err, base.ErrNoFormFile):
		h.Log.Errorw("failed to get package file", "error", err)
		http.Error(w, "package file required", http.StatusBadRequest)
	default:
		h.Log.Errorw("failed to read push form", "error", err)
		http.Error(w, "invalid request", http.StatusBadRequest)
	}

	return nil, false
}

// pushedPackage builds the database record for a pushed .nupkg from its
// nuspec metadata. The id is lower-cased and the version normalized (see
// pkgnuget.Normalize), the form clients request. It returns an error for a
// package without a usable id or version.
func pushedPackage(repoID string, metadata map[string]any, part *base.Spool, sum512 hash.Hash) (*models.NuGetPackage, error) {
	id, _ := metadata["id"].(string)
	// "." and ".." fit the charset but are no id: as a URL segment they name the
	// directory, so no client can fetch the package.
	if !validPackageID.MatchString(id) || !pathseg.Name(id) {
		return nil, fmt.Errorf("invalid package id %q", id) //nolint:err113 // reported to the log only
	}

	rawVersion, _ := metadata["version"].(string)

	version, ok := pkgnuget.Normalize(rawVersion)
	if !ok {
		return nil, fmt.Errorf("invalid package version %q", rawVersion) //nolint:err113 // reported to the log only
	}

	str := func(key string) string {
		s, _ := metadata[key].(string)
		return s
	}
	requireLicenseAcceptance, _ := metadata["requireLicenseAcceptance"].(bool)

	// Record the dependency groups with the row so the registration index never
	// has to open the package again. A package whose nuspec cannot be read
	// again leaves the column empty; it is then derived on first use.
	var depGroups json.RawMessage
	if nuspec, err := parseNuspec(part.Reader()); err == nil {
		depGroups = marshalDependencyGroups(nuspec.Metadata.Dependencies.DependencyGroups())
	}

	return &models.NuGetPackage{
		RepositoryID:             repoID,
		DependencyGroups:         depGroups,
		PackageID:                strings.ToLower(id),
		Version:                  version,
		Title:                    str("title"),
		Description:              str("description"),
		Summary:                  str("summary"),
		Authors:                  str("authors"),
		Owners:                   str("owners"),
		Tags:                     str("tags"),
		ProjectURL:               str("projectUrl"),
		LicenseURL:               str("licenseUrl"),
		IconURL:                  str("iconUrl"),
		RequireLicenseAcceptance: requireLicenseAcceptance,
		MinClientVersion:         str("minClientVersion"),
		BlobKey:                  part.SHA256Hex(),
		PackageHash:              base64.StdEncoding.EncodeToString(sum512.Sum(nil)),
		PackageHashAlgorithm:     "SHA512",
		PackageSize:              part.Size(),
	}, nil
}

// pushedLicenses returns the licenses a policy is enforced on: those of the
// package's own .nuspec, <license type="expression"> or, for packages that only
// have the deprecated <licenseUrl>, the license that URL names. A package whose
// archive cannot be read safely (two .nuspec files, an oversized one) is
// refused, and so is one whose .nuspec cannot be read while the repository has
// a blocking policy (false, response written).
func (h *HTTPHandler) pushedLicenses(
	w http.ResponseWriter,
	r *http.Request,
	repoID string,
	pkg *models.NuGetPackage,
	part *base.Spool,
	metadata map[string]any,
) ([]string, bool) {
	res, err := licensemeta.Read(r.Context(), "nuget", pkg.PackageID+"."+pkg.Version+".nupkg", func() (io.ReadCloser, error) {
		return part.Reader(), nil
	})

	switch {
	case licensemeta.Unsafe(err):
		h.Log.Warnw(
			"nuget push rejected: package metadata cannot be read safely",
			"id",
			pkg.PackageID,
			"version",
			pkg.Version,
			"error",
			err,
		)
		licensepolicy.RejectUnsafe(w, h.Log)

		return nil, false
	case err == nil:
		return res.Licenses, true
	}

	h.Log.Debugw("nuget push: nuspec unreadable for licenses", "id", pkg.PackageID, "error", err)

	if licensepolicy.RefuseUnderivable(w, r, h.DB, h.Log, repoID) {
		return nil, false
	}

	if expr, _ := metadata["licenseExpression"].(string); expr != "" {
		return []string{expr}, true
	}

	return nil, true
}

func (h *HTTPHandler) handlePushPackage(w http.ResponseWriter, r *http.Request) {
	repoName := urlparams.Vars(r)["name"]

	repoID := h.resolveRepo(w, r)
	if repoID == "" {
		return
	}

	sum512 := sha512.New()

	part, ok := h.readPushedPackage(w, r, sum512)
	if !ok {
		return
	}
	defer part.Close()

	metadata, err := NewNuGetHandler().Parse(r.Context(), part.Reader())
	if err != nil {
		h.Log.Errorw("failed to parse package metadata", "error", err)
		http.Error(w, "invalid package", http.StatusBadRequest)

		return
	}

	pkg, err := pushedPackage(repoID, metadata, part, sum512)
	if err != nil {
		h.Log.Errorw("rejected nuget package", "error", err)
		http.Error(w, "invalid package", http.StatusBadRequest)

		return
	}

	licenses, ok := h.pushedLicenses(w, r, repoID, pkg, part, metadata)
	if !ok || licensepolicy.Enforce(w, r, h.DB, h.Log, repoID, licenses...) {
		return
	}

	existing, err := h.DB.GetNuGetPackage(r.Context(), repoID, pkg.PackageID, pkg.Version)
	if err != nil {
		h.Log.Errorw("failed to check existing nuget package", "id", pkg.PackageID, "version", pkg.Version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if existing != nil {
		http.Error(w, "package version already exists", http.StatusConflict)
		return
	}

	// Reserve quota before anything is persisted; every failure path below
	// returns the reservation.
	if !h.ReserveQuotaOrRespond(r.Context(), w, repoID, repoName, pkg.PackageSize) {
		return
	}

	if err := h.storePackage(r.Context(), pkg, part); err != nil {
		h.ReleaseQuota(r.Context(), repoID, repoName, pkg.PackageSize)

		if errors.Is(err, database.ErrDuplicate) || database.IsUniqueViolation(err) {
			http.Error(w, "package version already exists", http.StatusConflict)
			return
		}

		h.Log.Errorw("failed to store nuget package", "id", pkg.PackageID, "version", pkg.Version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repoName,
		Format:     "nuget",
		Name:       pkg.PackageID,
		Version:    pkg.Version,
		Path:       nupkgName(pkg.PackageID, pkg.Version),
		SHA256:     pkg.BlobKey,
		Size:       pkg.PackageSize,
	}))

	w.WriteHeader(http.StatusCreated)
}

// storePackage stores the blob, then inserts the referencing row under the
// per-blob lock (the blob's existence is re-checked under it, so a concurrent
// unpublish cannot delete the blob in between).
// When the row is not created (a concurrent push of the same version won) the
// blob is dropped again unless something else references it.
func (h *HTTPHandler) storePackage(ctx context.Context, pkg *models.NuGetPackage, part *base.Spool) error {
	blob := storage.ReaderBlob(pkg.BlobKey, map[string]string{
		"sha256":   pkg.BlobKey,
		"filename": nupkgName(pkg.PackageID, pkg.Version),
		"package":  pkg.PackageID,
		"version":  pkg.Version,
	}, func() io.Reader { return part.Reader() })

	err := storage.CommitBlob(ctx, h.DB, h.BlobStore, blob, func(ctx context.Context, _ string) error {
		pkg.CreatedAt = importmeta.CreatedAt(ctx)

		if err := h.DB.CreateNuGetPackage(ctx, pkg); err != nil {
			return fmt.Errorf("create nuget package: %w", err)
		}

		return nil
	})
	if err == nil {
		return nil
	}

	if cleanErr := storage.SafeDeleteBlob(ctx, h.DB, h.BlobStore, pkg.BlobKey); cleanErr != nil {
		h.Log.Warnw("failed to clean up nuget blob", "sha256", pkg.BlobKey, "error", cleanErr)
	}

	return err
}

func (h *HTTPHandler) handleDeletePackage(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	packageID := strings.ToLower(vars["id"])
	version, _ := pkgnuget.Normalize(vars["version"])

	repoID := h.resolveRepo(w, r)
	if repoID == "" {
		return
	}

	// Get package to find blob key
	pkg, err := h.DB.GetNuGetPackage(r.Context(), repoID, packageID, version)
	if err != nil {
		h.Log.Errorw("failed to get nuget package", "id", packageID, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	if err := trash.GuardReleaseByName(r, h.DB, repoName, "nuget", version); err != nil {
		if errors.Is(err, trash.ErrImmutableRelease) {
			http.Error(w, "release versions of this repository are immutable", http.StatusConflict)
			return
		}

		h.Log.Errorw("failed to check release immutability", "repo", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	err = trash.Delete(
		r.Context(),
		h.DB,
		trash.Request{
			RepoID:   repoID,
			Kind:     models.TrashKindNuGet,
			IDs:      []string{pkg.ID},
			Resource: nupkgName(packageID, version),
			Package:  packageID,
			Version:  version,
		},
		func() error {
			if err := h.DB.DeleteNuGetPackage(r.Context(), repoID, packageID, version); err != nil {
				return fmt.Errorf("delete nuget package: %w", err)
			}

			h.ReleaseQuota(r.Context(), repoID, repoName, pkg.PackageSize)

			// The blob is content-addressed and may be shared; remove it only once no
			// row references it any more.
			if err := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, pkg.BlobKey); err != nil {
				h.Log.Warnw("failed to clean up blob after nuget delete", "key", pkg.BlobKey, "error", err)
			}

			return nil
		},
	)
	if err != nil {
		if errors.Is(err, database.ErrTrashNotFound) {
			http.NotFound(w, r)
			return
		}

		h.Log.Errorw("failed to delete nuget package", "id", packageID, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repoName,
		Format:     "nuget",
		Name:       packageID,
		Version:    version,
		Path:       nupkgName(packageID, version),
		SHA256:     pkg.BlobKey,
		Size:       pkg.PackageSize,
	}))

	w.WriteHeader(http.StatusNoContent)
}

// sortedVersions returns the packages ordered by version, oldest first.
// Each version is parsed once, not on every comparison; unparsable ones sort
// first, in string order (pkgnuget.CompareStrings).
func sortedVersions(pkgs []*models.NuGetPackage) []*models.NuGetPackage {
	type keyed struct {
		pkg *models.NuGetPackage
		ver pkgnuget.Version
		ok  bool
	}

	keys := make([]keyed, len(pkgs))
	for i, p := range pkgs {
		v, err := pkgnuget.ParseVersion(p.Version)
		keys[i] = keyed{pkg: p, ver: v, ok: err == nil}
	}

	slices.SortStableFunc(keys, func(a, b keyed) int {
		switch {
		case !a.ok && !b.ok:
			return strings.Compare(a.pkg.Version, b.pkg.Version)
		case !a.ok:
			return -1
		case !b.ok:
			return 1
		}

		return a.ver.Compare(b.ver)
	})

	out := make([]*models.NuGetPackage, len(keys))
	for i, k := range keys {
		out[i] = k.pkg
	}

	return out
}

// latestVersion returns the highest stable version, or the highest prerelease
// when there is no stable one.
func latestVersion(pkgs []*models.NuGetPackage) *models.NuGetPackage {
	sorted := sortedVersions(pkgs)

	for _, p := range slices.Backward(sorted) {
		if v, err := pkgnuget.ParseVersion(p.Version); err == nil && !v.IsPrerelease() {
			return p
		}
	}

	return sorted[len(sorted)-1]
}

// handleVersionIndex serves the PackageBaseAddress version list
// (GET {base}/{id}/index.json), the first request `dotnet restore` makes for a
// package.
func (h *HTTPHandler) handleVersionIndex(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepoInfo(w, r)
	if repo == nil {
		return
	}

	packageID := strings.ToLower(urlparams.Vars(r)["id"])

	stamp, ok := h.versionsStamp(w, r, repo.ID, packageID)
	if !ok {
		return
	}

	// The list holds version strings only, so no origin takes part.
	etag := versionsETag("flat", repo.ID, "", packageID, stamp)

	h.ServeRendered(w, r, repo.Public, etag, etag, registrationMaxAge, func(ctx context.Context) (*base.Rendered, error) {
		pkgs, err := h.DB.ListNuGetPackageVersions(ctx, repo.ID, packageID)
		if err != nil {
			return nil, fmt.Errorf("list nuget versions: %w", err)
		}

		versions := make([]string, 0, len(pkgs))
		for _, p := range sortedVersions(pkgs) {
			versions = append(versions, p.Version)
		}

		body, err := json.Marshal(map[string]any{"versions": versions})
		if err != nil {
			return nil, fmt.Errorf("encode version list: %w", err)
		}

		return &base.Rendered{Body: append(body, '\n'), ContentType: "application/json"}, nil
	}, "failed to render nuget version list", "id", packageID)
}

// versionsStamp fingerprints the live versions of one package. It writes the
// response and returns false when the stamp fails or the package has no
// version.
func (h *HTTPHandler) versionsStamp(w http.ResponseWriter, r *http.Request, repoID, packageID string) (string, bool) {
	stamp, err := h.DB.NuGetVersionsStamp(r.Context(), repoID, packageID)
	if err != nil {
		h.Log.Errorw("failed to stamp nuget versions", "id", packageID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return "", false
	}

	// The stamp is "<count>:<hash>".
	if strings.HasPrefix(stamp, "0:") {
		http.NotFound(w, r)
		return "", false
	}

	return stamp, true
}

type packageFile int

const (
	fileUnknown packageFile = iota
	fileNupkg
	fileNuspec
)

// classifyPackageFile maps the {filename} of a PackageBaseAddress request to
// the file it names: <id>.<version>.nupkg (version as requested or
// normalized) or <id>.nuspec. Names are matched case-insensitively.
func classifyPackageFile(filename, packageID, rawVersion, version string) packageFile {
	name := strings.ToLower(filename)

	switch name {
	case nupkgName(packageID, version), nupkgName(packageID, strings.ToLower(rawVersion)):
		return fileNupkg
	case packageID + ".nuspec":
		return fileNuspec
	}

	return fileUnknown
}

func (h *HTTPHandler) handleDownloadPackage(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	packageID := strings.ToLower(vars["id"])
	version, _ := pkgnuget.Normalize(vars["version"])

	kind := classifyPackageFile(vars["filename"], packageID, vars["version"], version)
	if kind == fileUnknown {
		http.NotFound(w, r)
		return
	}

	repoID := h.resolveRepo(w, r)
	if repoID == "" {
		return
	}

	pkg, err := h.DB.GetNuGetPackage(r.Context(), repoID, packageID, version)
	if err != nil {
		h.Log.Errorw("failed to get nuget package", "id", packageID, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(
		pkg.ID, nupkgName(packageID, version), vars["filename"], r.URL.Path)...) {
		return
	}

	if kind == fileNuspec {
		h.serveNuspec(w, r, packageID, pkg)
		return
	}

	// Count the download before the blob is opened or redirected to: a
	// presigned response never reaches the streaming code below.
	h.DB.CountFileDownload(pkg.ID)

	if h.TryPresign(
		w,
		r,
		presign.Blob{SHA256: pkg.BlobKey, Size: pkg.PackageSize, Filename: nupkgName(packageID, version), ContentType: "application/zip"},
	) {
		return
	}

	// Get blob
	rc, err := h.BlobStore.Get(r.Context(), pkg.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get blob", "key", pkg.BlobKey, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = rc.Close() }()

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Length", strconv.FormatInt(pkg.PackageSize, 10))
	w.WriteHeader(http.StatusOK)

	if _, err := iocopy.Copy(w, rc); err != nil {
		h.Log.Errorw("failed to write package response", "id", packageID, "version", pkg.Version, "error", err)
	}
}

// serveNuspec answers GET {base}/{id}/{version}/{id}.nuspec with the nuspec
// extracted from the stored package.
func (h *HTTPHandler) serveNuspec(w http.ResponseWriter, r *http.Request, packageID string, pkg *models.NuGetPackage) {
	rc, err := h.BlobStore.Get(r.Context(), pkg.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get blob", "key", pkg.BlobKey, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = rc.Close() }()

	nuspec, err := readNuspec(rc)
	if err != nil {
		h.Log.Errorw("failed to read nuspec", "id", packageID, "version", pkg.Version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Length", strconv.Itoa(len(nuspec)))
	w.WriteHeader(http.StatusOK)

	_, _ = w.Write(nuspec)
}

// catalogEntry is the registration "catalogEntry" object of one version. The
// server keeps no catalog, so its @id is the version's registration leaf.
// complete is false when the dependency groups could not be determined.
func (h *HTTPHandler) catalogEntry(ctx context.Context, baseURL, packageID string, v *models.NuGetPackage) (map[string]any, bool) {
	leaf := registrationLeafURL(baseURL, packageID, v.Version)
	groups, complete := h.dependencyGroups(ctx, packageID, v)

	return map[string]any{
		"@id":                      leaf,
		"@type":                    "PackageDetails",
		"id":                       v.PackageID,
		"version":                  v.Version,
		"description":              v.Description,
		"authors":                  v.Authors,
		"title":                    v.Title,
		"summary":                  v.Summary,
		"tags":                     v.Tags,
		"projectUrl":               v.ProjectURL,
		"licenseUrl":               v.LicenseURL,
		"iconUrl":                  v.IconURL,
		"requireLicenseAcceptance": v.RequireLicenseAcceptance,
		"listed":                   true,
		"published":                v.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		"packageContent":           packageContentURL(baseURL, packageID, v.Version),
		"dependencyGroups":         groups,
	}, complete
}

// registrationMaxAge is how long shared caches may reuse a public repository's
// registration index before revalidating.
const registrationMaxAge = 60

// versionsETag identifies a document built from the live versions of one
// package (kind names the document): it changes when a version is added,
// removed or republished and never because of download counters. Rows are
// immutable and a republish is a new row with a new id, so the stamp (count and
// hash of the ids) identifies the content. baseURL is the repository root the
// URLs of the document are built on, origin included, or "" for a document
// without URLs: a document for one origin must never revalidate (or be served
// from the render cache) for another.
func versionsETag(kind, repoID, baseURL, packageID, stamp string) string {
	buf := make([]byte, 0, 64+len(kind)+len(repoID)+len(baseURL)+len(packageID)+len(stamp))

	for _, s := range []string{kind, repoID, baseURL, packageID, stamp} {
		buf = append(append(buf, s...), 0)
	}

	sum := sha256.Sum256(buf)

	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

func (h *HTTPHandler) handleRegistrationIndex(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepoInfo(w, r)
	if repo == nil {
		return
	}

	packageID := strings.ToLower(urlparams.Vars(r)["id"])

	stamp, ok := h.versionsStamp(w, r, repo.ID, packageID)
	if !ok {
		return
	}

	baseURL := h.repoBaseURL(w, r)

	// Revalidation is answered from the stamp alone: no row is loaded, no
	// dependency group is resolved and no JSON is built.
	etag := versionsETag("registration", repo.ID, baseURL, packageID, stamp)

	h.ServeRendered(w, r, repo.Public, etag, etag, registrationMaxAge, func(ctx context.Context) (*base.Rendered, error) {
		pkgs, err := h.DB.ListNuGetPackageVersions(ctx, repo.ID, packageID)
		if err != nil {
			return nil, fmt.Errorf("list nuget versions: %w", err)
		}

		return h.renderRegistrationIndex(ctx, baseURL, packageID, pkgs)
	}, "failed to render nuget registration", "id", packageID)
}

func (h *HTTPHandler) renderRegistrationIndex(
	ctx context.Context,
	baseURL, packageID string,
	pkgs []*models.NuGetPackage,
) (*base.Rendered, error) {
	indexURL := registrationIndexURL(baseURL, packageID)
	versions := sortedVersions(pkgs)

	var pages []map[string]any

	complete := true

	for chunk := range slices.Chunk(versions, registrationPageSize) {
		items := make([]map[string]any, len(chunk))
		for i, v := range chunk {
			entry, ok := h.catalogEntry(ctx, baseURL, packageID, v)
			complete = complete && ok

			items[i] = map[string]any{
				"@id":            registrationLeafURL(baseURL, packageID, v.Version),
				"@type":          "Package",
				"catalogEntry":   entry,
				"packageContent": packageContentURL(baseURL, packageID, v.Version),
				"registration":   indexURL,
			}
		}

		lower, upper := chunk[0].Version, chunk[len(chunk)-1].Version
		pages = append(pages, map[string]any{
			"@id":    indexURL + "#page/" + lower + "/" + upper,
			"@type":  "catalog:CatalogPage",
			"count":  len(items),
			"items":  items,
			"lower":  lower,
			"parent": indexURL,
			"upper":  upper,
		})
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(map[string]any{
		"@id":   indexURL,
		"count": len(pages),
		"items": pages,
	}); err != nil {
		return nil, fmt.Errorf("encode registration index: %w", err)
	}

	// A rendering missing dependency groups is served once, never remembered.
	return &base.Rendered{Body: buf.Bytes(), ContentType: "application/json", Transient: !complete}, nil
}

// handleRegistrationLeaf serves GET {registration}/{id}/{version}.json.
func (h *HTTPHandler) handleRegistrationLeaf(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	packageID := strings.ToLower(vars["id"])

	rawVersion, isJSON := strings.CutSuffix(vars["versionfile"], ".json")
	if !isJSON {
		http.NotFound(w, r)
		return
	}

	version, _ := pkgnuget.Normalize(rawVersion)

	repoID := h.resolveRepo(w, r)
	if repoID == "" {
		return
	}

	pkg, err := h.DB.GetNuGetPackage(r.Context(), repoID, packageID, version)
	if err != nil {
		h.Log.Errorw("failed to get nuget package", "id", packageID, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	baseURL := h.repoBaseURL(w, r)
	leaf := registrationLeafURL(baseURL, packageID, pkg.Version)

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"@id":            leaf,
		"@type":          "Package",
		"catalogEntry":   leaf,
		"listed":         true,
		"packageContent": packageContentURL(baseURL, packageID, pkg.Version),
		"published":      pkg.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		"registration":   registrationIndexURL(baseURL, packageID),
	})
}

// groupByPackage splits rows ordered by package id into one slice per package.
func groupByPackage(pkgs []*models.NuGetPackage) [][]*models.NuGetPackage {
	var groups [][]*models.NuGetPackage

	for _, p := range pkgs {
		if n := len(groups); n > 0 && groups[n-1][0].PackageID == p.PackageID {
			groups[n-1] = append(groups[n-1], p)
			continue
		}

		groups = append(groups, []*models.NuGetPackage{p})
	}

	return groups
}

// searchEntry is the search result of one package: the latest version's
// metadata plus every version.
func searchEntry(baseURL string, versions []*models.NuGetPackage) map[string]any {
	latest := latestVersion(versions)
	indexURL := registrationIndexURL(baseURL, latest.PackageID)

	var downloads int

	entries := make([]map[string]any, 0, len(versions))

	for _, v := range sortedVersions(versions) {
		downloads += v.DownloadCount
		entries = append(entries, map[string]any{
			"@id":       registrationLeafURL(baseURL, v.PackageID, v.Version),
			"version":   v.Version,
			"downloads": v.DownloadCount,
		})
	}

	return map[string]any{
		"@id":            indexURL,
		"@type":          "Package",
		"registration":   indexURL,
		"id":             latest.PackageID,
		"version":        latest.Version,
		"description":    latest.Description,
		"summary":        latest.Summary,
		"title":          latest.Title,
		"iconUrl":        latest.IconURL,
		"licenseUrl":     latest.LicenseURL,
		"projectUrl":     latest.ProjectURL,
		"tags":           latest.Tags,
		"authors":        latest.Authors,
		"totalDownloads": downloads,
		"versions":       entries,
	}
}

func (h *HTTPHandler) handleSearch(w http.ResponseWriter, r *http.Request) {
	repoID := h.resolveRepo(w, r)
	if repoID == "" {
		return
	}

	q := r.URL.Query()
	query := q.Get("q")
	skip := parseIntMin(q.Get("skip"), 0, 0)
	take := parseIntClamp(q.Get("take"), 20, 1, 100)

	// One row per version of every package on the page; totalHits counts packages.
	packages, total, err := h.DB.SearchNuGetPackages(r.Context(), repoID, query, skip, take)
	if err != nil {
		h.Log.Errorw("failed to search nuget packages", "query", query, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	baseURL := h.repoBaseURL(w, r)
	groups := groupByPackage(packages)
	data := make([]map[string]any, len(groups))

	for i, versions := range groups {
		data[i] = searchEntry(baseURL, versions)
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"totalHits": total,
		"data":      data,
	})
}

func (h *HTTPHandler) handleAutocomplete(w http.ResponseWriter, r *http.Request) {
	repoID := h.resolveRepo(w, r)
	if repoID == "" {
		return
	}

	query := r.URL.Query().Get("q")

	packages, total, err := h.DB.SearchNuGetPackages(r.Context(), repoID, query, 0, 100)
	if err != nil {
		h.Log.Errorw("failed to search nuget packages", "query", query, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Extract unique package IDs
	groups := groupByPackage(packages)
	data := make([]string, len(groups))

	for i, versions := range groups {
		data[i] = versions[0].PackageID
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"totalHits": total,
		"data":      data,
	})
}

func parseIntMin(s string, defaultVal, minVal int) int {
	if s == "" {
		return defaultVal
	}

	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}

	if v < minVal {
		return minVal
	}

	return v
}

func parseIntClamp(s string, defaultVal, minVal, maxVal int) int {
	if s == "" {
		return defaultVal
	}

	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}

	if v < minVal {
		return minVal
	}

	if v > maxVal {
		return maxVal
	}

	return v
}

// marshalDependencyGroups encodes dependency groups for storage. It returns nil
// (not recorded) if encoding fails, which cannot happen for these plain values.
func marshalDependencyGroups(groups []map[string]any) json.RawMessage {
	data, err := json.Marshal(groups)
	if err != nil {
		return nil
	}

	return data
}

// dependencyGroups returns the registration "dependencyGroups" JSON of one
// version. It is stored with the row (written at publish time), so the common
// case reads nothing but the row already in hand. A package without a stored
// value (published before the column existed, or restored from a backup) is
// derived from its nupkg once and the result is stored for next time. complete
// is false when that derivation failed: the empty list returned then is a
// degraded answer for this response only.
func (h *HTTPHandler) dependencyGroups(ctx context.Context, packageID string, v *models.NuGetPackage) (json.RawMessage, bool) {
	if v.DependencyGroups != nil {
		return v.DependencyGroups, true
	}

	groups, ok := h.nuspecDependencyGroups(ctx, packageID, v)
	if !ok {
		return json.RawMessage("[]"), false
	}

	// Best effort and never overwriting: the next request reads the column,
	// and a failure only means this package is derived again.
	if err := h.DB.SetNuGetDependencyGroups(context.WithoutCancel(ctx), v.RepositoryID, v.PackageID, v.Version, groups); err != nil {
		h.Log.Warnw("failed to store dependency groups", "id", packageID, "version", v.Version, "error", err)
	}

	return groups, true
}

// nuspecDependencyGroups derives the dependency groups of one package version
// from its .nupkg blob, cached by blob digest (content-addressed, never stale).
// A miss reads the package behind nuspecGate, which bounds how many packages
// are held in memory at once. ok is false when the package could not be read
// or parsed; the failure is logged and not cached, and the caller degrades to
// no dependency groups rather than failing the whole response.
func (h *HTTPHandler) nuspecDependencyGroups(ctx context.Context, packageID string, v *models.NuGetPackage) (json.RawMessage, bool) {
	if groups, ok := h.deps.get(v.BlobKey); ok {
		return groups, true
	}

	if err := h.nuspecGate.Acquire(ctx); err != nil {
		h.Log.Warnw("dependency groups skipped", "id", packageID, "version", v.Version, "error", err)

		return nil, false
	}
	defer h.nuspecGate.Release()

	// Another request may have filled the entry while this one waited.
	if groups, ok := h.deps.get(v.BlobKey); ok {
		return groups, true
	}

	groups, ok := h.readDependencyGroups(ctx, packageID, v)
	if ok {
		h.deps.put(v.BlobKey, groups)
	}

	return groups, ok
}

// readDependencyGroups parses the stored package of one version. ok is false
// when the package could not be read, so the caller does not cache the result.
func (h *HTTPHandler) readDependencyGroups(ctx context.Context, packageID string, v *models.NuGetPackage) (json.RawMessage, bool) {
	rc, err := h.BlobStore.Get(ctx, v.BlobKey)
	if err != nil {
		h.Log.Warnw("failed to fetch package blob for dependency groups", "id", packageID, "version", v.Version, "error", err)

		return nil, false
	}
	defer func() { _ = rc.Close() }()

	nuspec, err := parseNuspec(rc)
	if err != nil {
		h.Log.Warnw("failed to parse nuspec for dependency groups", "id", packageID, "version", v.Version, "error", err)

		return nil, false
	}

	groups := marshalDependencyGroups(nuspec.Metadata.Dependencies.DependencyGroups())

	return groups, groups != nil
}
