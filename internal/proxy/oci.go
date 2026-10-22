// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/ocipath"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
)

const (
	// maxManifestBody caps a manifest or index fetched from an upstream; the
	// distribution spec asks registries to accept at most 4 MiB.
	maxManifestBody int64 = 8 << 20

	// ociManifestAccept asks the upstream for every manifest type it may hold:
	// the stored copy is served to every client regardless of its Accept.
	ociManifestAccept = "application/vnd.oci.image.index.v1+json, " +
		"application/vnd.oci.image.manifest.v1+json, " +
		"application/vnd.docker.distribution.manifest.list.v2+json, " +
		"application/vnd.docker.distribution.manifest.v2+json"

	defaultManifestType = "application/vnd.docker.distribution.manifest.v2+json"
	blobContentType     = "application/octet-stream"
)

var (
	tagRE    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
	digestRE = regexp.MustCompile(`^(?:sha256:[a-f0-9]{64}|sha512:[a-f0-9]{128})$`)

	// errUpstreamDenied is the upstream refusing (401/403) a request that
	// carried the credentials it asked for: for a pull-through cache that is
	// "the image does not exist for us", not a server fault.
	errUpstreamDenied = errors.New("upstream denied access")

	errDigestMismatch = errors.New("content digest mismatch")
	errBadManifest    = errors.New("upstream sent an invalid manifest")
)

// ociSpec describes what a fetched body must satisfy before it is cached.
type ociSpec struct {
	manifest bool
	// digest is the content digest the body must have ("" for a tag).
	digest string
	limit  int64
}

// ServeOCI answers a request under /v2/ for the docker proxy repository repo:
// manifests and blobs are pulled through (and cached), tag and referrer
// listings are passed through, anything that would write is refused. req is
// the parsed /v2/<repo>/<image>/... path.
func (s *Service) ServeOCI(w http.ResponseWriter, r *http.Request, repo *models.Repository, req ocipath.Request) {
	if repo.Blocked {
		ocipath.WriteError(w, http.StatusForbidden, "DENIED", "repository is blocked")
		return
	}

	if ocipath.RejectNonPull(w, r, req.Kind) {
		return
	}

	_, image := ocipath.SplitName(req.Name)

	switch {
	case image == "":
		ocipath.WriteError(w, http.StatusNotFound, "NAME_UNKNOWN", "a proxy repository serves images below it: <repository>/<image>")
		return
	case !ocipath.ValidImage(image):
		ocipath.WriteError(w, http.StatusBadRequest, "NAME_INVALID", "invalid repository name")
		return
	}

	up, err := ociTarget(repo, image)
	if err != nil {
		s.log.Errorw("invalid docker proxy upstream url", "repo", repo.Name, "error", err)
		ocipath.WriteError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	switch req.Kind {
	case ocipath.KindManifest:
		s.serveOCIManifest(w, r, repo, req, image, up)
	case ocipath.KindBlob:
		s.serveOCIBlob(w, r, repo, req, image, up)
	case ocipath.KindTags:
		s.serveOCITags(w, r, repo, req, up)
	case ocipath.KindReferrers:
		s.serveOCIReferrers(w, r, repo, req, up)
	case ocipath.KindUnknown, ocipath.KindUploadStart, ocipath.KindUpload:
		ocipath.WriteError(w, http.StatusNotFound, "NAME_UNKNOWN", "unknown registry endpoint")
	}
}

func (s *Service) serveOCIManifest(w http.ResponseWriter, r *http.Request, repo *models.Repository, req ocipath.Request, image string, up ociUpstream) {
	ref := req.Ref
	if !digestRE.MatchString(ref) && !tagRE.MatchString(ref) {
		ocipath.WriteError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest not found")
		return
	}

	key := image + "/manifests/" + ref
	fetch := func(ctx context.Context) (*models.ProxyCacheEntry, error) {
		return s.fetchOCIManifest(ctx, repo, up, ref, key)
	}

	s.serveOCIEntry(w, r, repo, req, key, fetch)
}

func (s *Service) serveOCIBlob(w http.ResponseWriter, r *http.Request, repo *models.Repository, req ocipath.Request, image string, up ociUpstream) {
	digest := req.Ref
	if !digestRE.MatchString(digest) {
		ocipath.WriteError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest")
		return
	}

	key := image + "/blobs/" + digest
	fetch := func(ctx context.Context) (*models.ProxyCacheEntry, error) {
		return s.fetchOCIBlob(ctx, repo, up, digest, key)
	}

	s.serveOCIEntry(w, r, repo, req, key, fetch)
}

// serveOCIEntry resolves key (fetching on a miss or an expired tag) and writes
// the manifest or blob. A cache row whose blob disappeared from storage is
// dropped and fetched once more.
func (s *Service) serveOCIEntry(w http.ResponseWriter, r *http.Request, repo *models.Repository, req ocipath.Request, key string, fetch fetchFunc) {
	var (
		res *resolution
		rc  io.ReadCloser
		err error
	)

	head := r.Method == http.MethodHead

	for attempt := 0; ; attempt++ {
		res, err = s.resolveWith(r.Context(), repo, key, fetch)
		if err != nil {
			s.writeOCIResolveError(w, req.Kind, err)
			return
		}

		entry := res.entry

		if quarantine.Enforce(w, r, s.db, repo.ID, quarantine.PathKeys(key, entry.ID, req.Name+"/"+kindSegment(req.Kind)+"/"+req.Ref, req.Ref, r.URL.Path)...) {
			return
		}

		if head {
			break
		}

		// Layers are the bulk of a pull: a registry may answer a blob GET with
		// a redirect, which every client follows. Manifests always stream.
		if req.Kind == ocipath.KindBlob && !res.stale &&
			presign.Try(s.blobStore, w, r, presign.Blob{
				SHA256:      entry.BlobKey,
				Size:        entry.SizeBytes,
				ContentType: blobContentType,
				Repository:  repo,
				CheckExists: true,
			}) {
			return
		}

		rc, err = s.blobStore.Get(r.Context(), entry.BlobKey)
		if err == nil {
			break
		}

		if !res.fromCache || attempt > 0 {
			s.log.Errorw("failed to open cached blob", "repo", repo.Name, "path", key, "blob_key", entry.BlobKey, "error", err)
			ocipath.WriteError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

			return
		}

		s.log.Warnw("proxy cache blob missing, refetching", "repo", repo.Name, "path", key, "error", err)

		if derr := s.db.DeleteProxyCacheEntry(r.Context(), repo.ID, key); derr != nil {
			s.log.Errorw("failed to drop stale proxy cache entry", "repo", repo.Name, "path", key, "error", derr)
		}
	}

	if rc != nil {
		defer func() { base.CloseLog(rc, s.log) }()
	}

	writeOCIEntry(w, req, res, rc)
}

func kindSegment(kind ocipath.Kind) string {
	if kind == ocipath.KindBlob {
		return "blobs"
	}

	return "manifests"
}

// writeOCIEntry writes the cached manifest or blob with the registry headers a
// client verifies: the media type and Docker-Content-Digest.
func writeOCIEntry(w http.ResponseWriter, req ocipath.Request, res *resolution, body io.Reader) {
	entry := res.entry
	h := w.Header()

	digest := req.Ref
	if !digestRE.MatchString(digest) {
		digest = "sha256:" + entry.BlobKey
	}

	// The cached type is re-checked on every read: rows written before the
	// media-type allowlist may hold an upstream-chosen type.
	ct := blobContentType
	if req.Kind != ocipath.KindBlob {
		ct = ocipath.ServedMediaType(entry.ContentType)
	}

	h.Set("Content-Type", ct)
	h.Set("Docker-Content-Digest", digest)
	h.Set("Content-Length", strconv.FormatInt(entry.SizeBytes, 10))

	if res.stale {
		h.Set("Warning", staleWarning)
	}

	w.WriteHeader(http.StatusOK)

	if body != nil {
		// The status line is already out: a failed copy can only abort the body.
		_, _ = io.Copy(w, body)
	}
}

func (s *Service) writeOCIResolveError(w http.ResponseWriter, kind ocipath.Kind, err error) {
	switch {
	case errors.Is(err, errNotFound), errors.Is(err, errUpstreamDenied):
		code, msg := ocipath.NotFoundCode(kind)
		ocipath.WriteError(w, http.StatusNotFound, code, msg)
	case errors.Is(err, errUpstream):
		ocipath.WriteError(w, http.StatusBadGateway, "UNAVAILABLE", "upstream registry error")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		s.log.Debugw("proxy request abandoned", "error", err)
	default:
		ocipath.WriteError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
	}
}

// fetchOCIManifest pulls manifests/<ref>. A tag that is already cached (and
// merely expired) is revalidated with a HEAD first: when the upstream still
// reports the cached digest only the expiry moves and no body is transferred.
func (s *Service) fetchOCIManifest(ctx context.Context, repo *models.Repository, up ociUpstream, ref, key string) (*models.ProxyCacheEntry, error) {
	target := up.base + "/v2/" + up.name + "/manifests/" + ref

	if !digestRE.MatchString(ref) {
		prior, err := s.db.GetProxyCacheEntry(ctx, repo.ID, key)
		if err != nil {
			s.log.Errorw("failed to read proxy cache entry", "repo", repo.Name, "path", key, "error", err)
			return nil, errInternal
		}

		if prior != nil && prior.StatusCode == http.StatusOK && prior.BlobKey != "" {
			entry, rerr := s.revalidateOCITag(ctx, repo, up, target, key, prior)
			if rerr != nil || entry != nil {
				return entry, rerr
			}
		}
	}

	spec := ociSpec{manifest: true, limit: min(maxManifestBody, s.maxBody)}
	if digestRE.MatchString(ref) {
		spec.digest = ref
	}

	return s.getOCI(ctx, repo, up, target, key, ociManifestAccept, spec)
}

// revalidateOCITag HEADs the tag. It returns the refreshed entry when the
// upstream digest still matches the cached copy, (nil, nil) when a full fetch
// is needed, and an error when the upstream says the tag is gone or is down.
func (s *Service) revalidateOCITag(ctx context.Context, repo *models.Repository, up ociUpstream, target, key string, prior *models.ProxyCacheEntry) (*models.ProxyCacheEntry, error) {
	resp, err := s.ociDo(ctx, repo, up, http.MethodHead, target, ociManifestAccept)
	if err != nil {
		return nil, err
	}

	defer func() { base.CloseLog(resp.Body, s.log) }()

	switch resp.StatusCode {
	case http.StatusOK:
		if resp.Header.Get("Docker-Content-Digest") != "sha256:"+prior.BlobKey {
			return nil, nil //nolint:nilnil // changed (or unreported) digest: fetch the manifest
		}

		now := time.Now()
		entry := *prior
		entry.ExpiresAt = now.Add(time.Duration(repo.CacheTTLSeconds) * time.Second)
		entry.UpdatedAt = now

		if uerr := s.db.UpsertProxyCacheEntry(ctx, &entry); uerr != nil {
			s.log.Errorw("failed to refresh proxy cache entry", "repo", repo.Name, "path", key, "error", uerr)
			return nil, errInternal
		}

		return &entry, nil
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusMethodNotAllowed:
		return nil, nil //nolint:nilnil // HEAD not usable here: fall back to GET
	default:
		return nil, s.ociStatusError(ctx, repo, key, resp.StatusCode)
	}
}

// fetchOCIBlob pulls blobs/<digest>.
func (s *Service) fetchOCIBlob(ctx context.Context, repo *models.Repository, up ociUpstream, digest, key string) (*models.ProxyCacheEntry, error) {
	target := up.base + "/v2/" + up.name + "/blobs/" + digest

	return s.getOCI(ctx, repo, up, target, key, "", ociSpec{digest: digest, limit: s.maxBody})
}

// getOCI GETs target and caches the verified body under key.
func (s *Service) getOCI(ctx context.Context, repo *models.Repository, up ociUpstream, target, key, accept string, spec ociSpec) (*models.ProxyCacheEntry, error) {
	resp, err := s.ociDo(ctx, repo, up, http.MethodGet, target, accept)
	if err != nil {
		return nil, err
	}

	defer func() { base.CloseLog(resp.Body, s.log) }()

	if resp.StatusCode != http.StatusOK {
		return nil, s.ociStatusError(ctx, repo, key, resp.StatusCode)
	}

	return s.storeOCI(ctx, repo, key, resp, spec)
}

// ociStatusError classifies a non-200 upstream answer for key.
func (s *Service) ociStatusError(ctx context.Context, repo *models.Repository, key string, status int) error {
	switch {
	case status == http.StatusNotFound || status == http.StatusGone:
		s.recordNegative(ctx, repo, key)
		return errNotFound
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		s.log.Warnw("docker proxy upstream denied access", "repo", repo.Name, "path", key, "status", status)
		return errUpstreamDenied
	case status >= http.StatusInternalServerError || status == http.StatusTooManyRequests:
		s.log.Warnw("docker proxy upstream returned an error", "repo", repo.Name, "path", key, "status", status)
		return errUpstreamDown
	default:
		s.log.Warnw("docker proxy upstream returned an unexpected status", "repo", repo.Name, "path", key, "status", status)
		return errUpstream
	}
}

// storeOCI spools the response, verifies its digest, and stores the blob and
// cache entry under the blob lock. Nothing is cached when verification fails.
func (s *Service) storeOCI(ctx context.Context, repo *models.Repository, key string, resp *http.Response, spec ociSpec) (*models.ProxyCacheEntry, error) {
	if resp.ContentLength > spec.limit {
		s.log.Warnw("docker proxy upstream file exceeds size limit", "repo", repo.Name, "path", key, "size", resp.ContentLength)
		return nil, errUpstream
	}

	spool, err := os.CreateTemp("", "openartifact-proxy-*")
	if err != nil {
		s.log.Errorw("failed to create proxy spool file", "error", err)
		return nil, errInternal
	}

	defer func() {
		base.CloseLog(spool, s.log)

		if rerr := os.Remove(spool.Name()); rerr != nil {
			s.log.Warnw("failed to remove proxy spool file", "error", rerr)
		}
	}()

	headerDigest := resp.Header.Get("Docker-Content-Digest")
	sums := newDigester(spec.digest, headerDigest)

	size, err := io.Copy(io.MultiWriter(spool, sums), io.LimitReader(resp.Body, spec.limit+1))
	if err != nil {
		s.log.Warnw("docker proxy upstream body read failed", "repo", repo.Name, "path", key, "error", err)
		return nil, errUpstreamDown
	}

	if size > spec.limit {
		s.log.Warnw("docker proxy upstream body oversized", "repo", repo.Name, "path", key, "size", size)
		return nil, errUpstream
	}

	if resp.ContentLength >= 0 && size != resp.ContentLength {
		s.log.Warnw("docker proxy upstream body truncated", "repo", repo.Name, "path", key, "size", size)
		return nil, errUpstreamDown
	}

	for _, want := range []string{spec.digest, headerDigest} {
		if !sums.matches(want) {
			s.log.Warnw("docker proxy upstream content does not match its digest",
				"repo", repo.Name, "path", key, "want", want, "error", errDigestMismatch)

			return nil, errUpstream
		}
	}

	if _, serr := spool.Seek(0, io.SeekStart); serr != nil {
		s.log.Errorw("failed to rewind proxy spool file", "error", serr)
		return nil, errInternal
	}

	contentType := blobContentType

	if spec.manifest {
		contentType, err = manifestType(resp.Header.Get("Content-Type"), spool)
		if err != nil {
			s.log.Warnw("docker proxy upstream manifest rejected", "repo", repo.Name, "path", key, "error", err)
			return nil, errUpstream
		}

		if _, serr := spool.Seek(0, io.SeekStart); serr != nil {
			s.log.Errorw("failed to rewind proxy spool file", "error", serr)
			return nil, errInternal
		}
	}

	sum := sums.sha256Hex()
	now := time.Now()

	entry := &models.ProxyCacheEntry{
		RepositoryID: repo.ID,
		CacheKey:     key,
		BlobKey:      sum,
		ContentType:  contentType,
		StatusCode:   http.StatusOK,
		SizeBytes:    size,
		ExpiresAt:    now.Add(ttlFor(repo, key, completeBody(resp))),
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	err = storage.WithBlobLock(ctx, s.db, sum, func(ctx context.Context) error {
		blobID, perr := s.blobStore.Put(ctx, spool, map[string]string{"sha256": sum, "content-type": contentType})
		if perr != nil {
			return fmt.Errorf("store blob: %w", perr)
		}

		if blobID != sum {
			return fmt.Errorf("%w: blob store returned id %q for sha256 %s", errInternal, blobID, sum)
		}

		if _, eerr := s.db.EnsureBlob(ctx, sum, contentType, size); eerr != nil {
			return fmt.Errorf("record blob: %w", eerr)
		}

		if uerr := s.db.UpsertProxyCacheEntry(ctx, entry); uerr != nil {
			return fmt.Errorf("record cache entry: %w", uerr)
		}

		return nil
	})
	if err != nil {
		s.log.Errorw("failed to cache proxied file", "repo", repo.Name, "path", key, "sha256", sum, "error", err)
		return nil, errInternal
	}

	return entry, nil
}

// manifestType returns the media type to serve a manifest under and checks the
// body is a JSON object: the upstream's Content-Type when it names a manifest
// type, else the document's own mediaType.
func manifestType(header string, body io.Reader) (string, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return "", fmt.Errorf("read manifest: %w", err)
	}

	var doc struct {
		MediaType string `json:"mediaType"` //nolint:tagliatelle // OCI manifest field name
	}

	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&doc); err != nil {
		return "", fmt.Errorf("%w: %w", errBadManifest, err)
	}

	// Both candidates are upstream controlled: only registry JSON types may be
	// served (and cached) under the application origin.
	if mt, ok := ocipath.SafeMediaType(header); ok && mt != "application/json" {
		return mt, nil
	}

	if mt, ok := ocipath.SafeMediaType(doc.MediaType); ok {
		return mt, nil
	}

	return defaultManifestType, nil
}

// digester hashes a body once for every algorithm a check needs.
type digester struct {
	sha256 hash.Hash
	sha512 hash.Hash
}

// newDigester always hashes sha256 (the blob store address) and adds sha512
// when one of the digests to verify uses it.
func newDigester(digests ...string) *digester {
	d := &digester{sha256: sha256.New()}

	for _, dg := range digests {
		if strings.HasPrefix(dg, "sha512:") {
			d.sha512 = sha512.New()
		}
	}

	return d
}

func (d *digester) Write(p []byte) (int, error) {
	_, _ = d.sha256.Write(p)

	if d.sha512 != nil {
		_, _ = d.sha512.Write(p)
	}

	return len(p), nil
}

func (d *digester) sha256Hex() string { return hex.EncodeToString(d.sha256.Sum(nil)) }

// matches reports whether the hashed body has the given "algo:hex" digest.
// An empty or unsupported digest imposes no constraint.
func (d *digester) matches(digest string) bool {
	algo, sum, ok := strings.Cut(digest, ":")
	if !ok {
		return true
	}

	switch algo {
	case "sha256":
		return sum == d.sha256Hex()
	case "sha512":
		return d.sha512 != nil && sum == hex.EncodeToString(d.sha512.Sum(nil))
	}

	return true
}
