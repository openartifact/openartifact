// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/publicurl"
	pkgnpm "github.com/openartifact/openartifact/pkg/npm"
)

// npm proxy repositories mirror a registry (default https://registry.npmjs.org):
//
//   - Packuments (GET /<pkg>, GET /@scope%2fname) are cached verbatim under the
//     key "<pkg>" (full) or "-/install-v1/<pkg>" (abbreviated, for clients that
//     send Accept: application/vnd.npm.install-v1+json) with the repository's
//     cache TTL and serve-stale rules. Every response rewrites each
//     dist.tarball to this repository, so the cache stays upstream-shaped and
//     independent of the server's own URL.
//   - Tarballs (GET /<pkg>/-/<file>) are cached under "<pkg>/-/<file>",
//     immutable. The upstream URL and digest come from the packument's dist
//     object for that file; bytes that do not match dist.integrity (or
//     dist.shasum) are refused and never cached.
//   - GET /<pkg>/<version-or-tag> is cut from the full packument.

// maxPackumentBytes bounds a packument read into memory for rewriting; large
// packages' full packuments reach tens of megabytes.
const maxPackumentBytes = 256 << 20

// maxConcurrentPackuments bounds the packuments rewritten in memory at once.
const maxConcurrentPackuments = 8

// packumentMaxAge is how long shared caches may reuse a public repository's
// packument before revalidating (as for hosted npm repositories).
const packumentMaxAge = 60

// SetBaseURL sets BASE_URL, the explicit override of the origin rewritten
// tarball URLs point at ("" derives it from each request). Call it before
// serving requests.
func (s *Service) SetBaseURL(baseURL string) {
	s.baseURL = strings.TrimRight(baseURL, "/")
	s.refreshTerraformMinter()
}

// publicBase is the origin of the absolute URLs rewritten metadata hands to the
// client for r: BASE_URL when set, else the request's origin (see
// base.Handler.PublicBase). Call it before the response headers are written and
// fold the result into the document's validator.
func (s *Service) publicBase(w http.ResponseWriter, r *http.Request) string {
	return publicurl.Resolve(w, r, s.baseURL)
}

func (s *Service) serveNPM(w http.ResponseWriter, r *http.Request, repo *models.Repository, relPath string) {
	p, err := pkgnpm.ParsePath(relPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	switch p.Kind {
	case pkgnpm.KindTarball:
		s.serveNPMTarball(w, r, repo, p)
	case pkgnpm.KindVersion:
		s.serveNPMPackument(w, r, repo, p, false)
	default:
		s.serveNPMPackument(w, r, repo, p, pkgnpm.WantsAbbreviated(r.Header.Get("Accept")))
	}
}

// npmPackumentKey is the cache key of a package's (abbreviated) packument.
func npmPackumentKey(pkg string, abbreviated bool) string {
	if abbreviated {
		return "-/install-v1/" + pkg
	}

	return pkg
}

// npmPackumentURL appends the package to the upstream base URL with the scope
// separator escaped (@scope%2fname), as npm sends it.
func npmPackumentURL(baseURL, pkg string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse upstream url: %w", err)
	}

	u.RawPath = strings.TrimRight(u.EscapedPath(), "/") + "/" + url.PathEscape(pkg)
	u.Path = strings.TrimRight(u.Path, "/") + "/" + pkg
	u.RawQuery = ""
	u.Fragment = ""

	return u.String(), nil
}

// npmPackumentDoc is the upstream packument of pkg, full or abbreviated: refused
// unless it is a JSON object.
func npmPackumentDoc(pkg string, abbreviated bool) metaDoc {
	accept := "application/json"
	if abbreviated {
		accept = pkgnpm.UpstreamAccept
	}

	return metaDoc{
		key:   npmPackumentKey(pkg, abbreviated),
		url:   func(upstream string) (string, error) { return npmPackumentURL(upstream, pkg) },
		opts:  staticOpts(fetchOpts{accept: accept, verify: verifyJSONObject}),
		limit: maxPackumentBytes,
		what:  "packument",
		bad:   badDocUpstream,
	}
}

// verifyJSONObject refuses an upstream packument that is not a JSON object
// (an HTML error page served with 200, a truncated document).
func verifyJSONObject(spool io.ReadSeeker) error {
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind spool: %w", err)
	}

	var top map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(spool, maxPackumentBytes+1)).Decode(&top); err != nil || top == nil {
		return errNotPackument
	}

	return nil
}

var errNotPackument = errors.New("upstream answer is not a packument")

func (s *Service) serveNPMPackument(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgnpm.Path, abbreviated bool) {
	// dist.tarball is absolute (pacote keeps it verbatim), so the origin is
	// part of the validator below and the rewrite happens at serve time.
	tarballBase := s.publicBase(w, r) + "/repos/" + repo.Name
	now := time.Now()

	// The abbreviated document carries no publish times, so a gated repository
	// answers an abbreviated request from the full packument (a registry may
	// answer with the full document; clients read it the same way) rather than
	// let it list versions the gate holds back.
	abbreviated = abbreviated && minAge(repo) == 0

	s.serveRendered(w, r, repo, renderedDoc{
		metaDoc: npmPackumentDoc(p.Package, abbreviated),
		maxAge:  packumentMaxAge,
		vary:    "Accept",
		// The rendered document depends on the cached blob, the origin of the tarball URLs and the gate;
		// a version manifest and a packument of the same blob differ by path.
		etag: func(blobKey string) string {
			sum := sha256.Sum256([]byte(blobKey + "\x00" + tarballBase + "\x00" + p.Version + "\x00" + gateStamp(repo, now)))
			return `"` + hex.EncodeToString(sum[:]) + `"`
		},
		render: func(body []byte, res *resolution) ([]byte, string, error) {
			out, err := renderNPM(body, p, tarballBase, repo, now)

			ct := "application/json"
			if abbreviated && strings.HasPrefix(res.entry.ContentType, pkgnpm.AbbreviatedContentType) {
				ct = pkgnpm.AbbreviatedContentType
			}

			return out, ct, err
		},
	})
}

// renderNPM hides the versions repo's minimum age holds back at now, points
// the packument's tarball URLs at this repository and, for a version path, cuts
// that version's manifest out. A packument whose every version is held back is
// errNotFound, as a package without versions.
func renderNPM(doc []byte, p pkgnpm.Path, tarballBase string, repo *models.Repository, now time.Time) ([]byte, error) {
	if minAge(repo) > 0 {
		var err error

		doc, err = pkgnpm.HideVersions(doc, func(published time.Time) bool { return tooNew(repo, published, now) })
		if errors.Is(err, pkgnpm.ErrAllVersionsHidden) {
			return nil, errNotFound
		}

		if err != nil {
			return nil, fmt.Errorf("apply minimum age: %w", err)
		}
	}

	out, err := pkgnpm.RewriteTarballs(doc, p.Package, tarballBase)
	if err != nil {
		return nil, fmt.Errorf("rewrite packument: %w", err)
	}

	if p.Kind != pkgnpm.KindVersion {
		return out, nil
	}

	manifest, found, err := pkgnpm.VersionManifest(out, p.Version)
	if err != nil {
		return nil, fmt.Errorf("read version: %w", err)
	}

	if !found {
		return nil, errNotFound
	}

	return manifest, nil
}

func (s *Service) serveNPMTarball(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgnpm.Path) {
	s.serveDownload(w, r, repo, p.Package+"/-/"+p.File, func(ctx context.Context) (download, error) {
		return s.locateNPMTarball(ctx, repo, p)
	})
}

// locateNPMTarball finds p.File in the cached packument: the URL its version's
// dist object names, cached only when the bytes match dist.integrity /
// dist.shasum. A file no version of the packument lists is errNotFound.
func (s *Service) locateNPMTarball(ctx context.Context, repo *models.Repository, p pkgnpm.Path) (download, error) {
	rel, _, err := parseDoc(ctx, s, repo, npmPackumentDoc(p.Package, false),
		func(doc []byte, _ *resolution) (*pkgnpm.Release, error) {
			rel, found, ferr := pkgnpm.FindRelease(doc, p.Package, p.File)
			if ferr != nil || !found {
				return nil, ferr //nolint:wrapcheck // logged by parseDoc
			}

			return &rel, nil
		})
	if err != nil {
		return download{}, err
	}

	if rel == nil {
		return download{}, errNotFound
	}

	if tooNew(repo, rel.Published, time.Now()) {
		return download{}, fmt.Errorf("%w: %s@%s", errTooNew, p.Package, rel.Version)
	}

	dist := &rel.Dist

	var vouched bool

	return download{url: dist.Tarball, opts: fetchOpts{
		vouched: &vouched,
		verify: func(spool io.ReadSeeker) error {
			if _, serr := spool.Seek(0, io.SeekStart); serr != nil {
				return fmt.Errorf("rewind spool: %w", serr)
			}

			ok, verr := pkgnpm.VerifyDist(spool, *dist)
			vouched = ok

			return verr //nolint:wrapcheck // logged by store
		},
	}}, nil
}

// NPMHooks is the proxy hooks of the npm format.
var NPMHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	Serve: (*Service).serveNPM,
	// Tarballs (<pkg>/-/<file>), not packuments.
	Immutable:  func(key string) bool { return strings.HasSuffix(keyName(key), ".tgz") && strings.Contains(key, "/-/") },
	IsMetadata: func(key string) bool { return !strings.Contains(key, "/-/") },
	BlobPath:   presign.NPMBlobPath,
}
