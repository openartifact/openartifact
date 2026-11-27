// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/presign"
	pkgpub "github.com/openartifact/openartifact/pkg/pub"
)

// Pub proxy repositories mirror a pub server (default https://pub.dev); clients
// set PUB_HOSTED_URL (or a dependency's "hosted" URL) to HOST/repos/pub/REPO:
//
//   - The package document (api/packages/<name>) is cached as the upstream sent
//     it and rendered per request: every archive_url is replaced by this
//     repository's own archive URL, so the upstream's download host is never
//     handed to clients.
//   - Archives (api/archives/<name>-<version>.tar.gz) are cached under that key,
//     immutable. The upstream URL and the digest come from the package document
//     (archive_url, archive_sha256); bytes that do not match the digest are
//     refused and never cached.
//
// With the version gate on (see gate.go) the package document is rendered
// without the releases the gate holds back (a "published" time too young for the
// minimum age, or a version the curation rules make unavailable), "latest" is
// recomputed from what is left, and an archive of such a release is refused, also
// when it is already cached.

// pubDocMaxAge is how long shared caches may reuse the rendering of a public
// repository's package document.
const pubDocMaxAge = 60

// pubDoc is the upstream package document of name, cached as it came.
func pubDoc(name string) metaDoc {
	return metaDoc{
		key: pkgpub.PackagePath(name),
		opts: staticOpts(fetchOpts{
			accept: pkgpub.ContentType, contentType: pkgpub.ContentType, verify: pubVerifyDoc(name),
		}),
		limit: pkgpub.MaxDocBytes,
		what:  "pub package document",
		bad:   badDocUpstream,
	}
}

// pubVerifyDoc refuses an upstream answer that is not the package document of
// name (an HTML error page served with 200, a truncated document).
func pubVerifyDoc(name string) func(io.ReadSeeker) error {
	return func(spool io.ReadSeeker) error {
		if _, err := spool.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind spool: %w", err)
		}

		return pkgpub.CheckDoc(spool, name) //nolint:wrapcheck // the cause is the message
	}
}

func (s *Service) servePub(w http.ResponseWriter, r *http.Request, repo *models.Repository, relPath string) {
	p, ok := pkgpub.ParsePath(relPath)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch p.Kind {
	case pkgpub.KindPackage, pkgpub.KindVersion:
		s.servePubDoc(w, r, repo, p)
	case pkgpub.KindArchive:
		s.servePubArchive(w, r, repo, p)
	case pkgpub.KindNew, pkgpub.KindUpload, pkgpub.KindFinish:
		// Publishing: a proxy repository is read-only.
		http.NotFound(w, r)
	}
}

// pubRoot is the URL the archives of this repository are linked below, on the origin
// of the request (BASE_URL when set; see publicBase).
func (s *Service) pubRoot(w http.ResponseWriter, r *http.Request, repo *models.Repository) string {
	return s.publicBase(w, r) + "/repos/" + "pub" + "/" + url.PathEscape(repo.Name)
}

func (s *Service) servePubDoc(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgpub.Path) {
	// archive_url is absolute, so the origin is part of the validator below and
	// the rewrite happens at serve time.
	root := s.pubRoot(w, r, repo)

	g, err := s.gateFor(r.Context(), repo, time.Now())
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	// Curation needs only the names: a curated version is refused before the
	// document is read or fetched.
	if p.Kind == pkgpub.KindVersion {
		if err = checkGate(g, p.Name, p.Version, time.Time{}); err != nil {
			s.writeResolveError(w, r, err)
			return
		}
	}

	s.serveRendered(w, r, repo, renderedDoc{
		metaDoc: pubDoc(p.Name),
		maxAge:  pubDocMaxAge,
		// The rendering depends on the cached blob, the origin of the archive
		// URLs, the version asked for and the gate; a version document and a
		// package document of the same blob differ by path.
		etag: func(blobKey string) string {
			sum := sha256.Sum256([]byte(blobKey + "\x00" + root + "\x00" + p.Version + "\x00" + g.Stamp()))
			return `"` + hex.EncodeToString(sum[:]) + `"`
		},
		render: func(body []byte, _ *resolution) ([]byte, string, error) {
			out, err := renderPub(body, p, root, g)

			return out, pkgpub.ContentType, err
		},
	})
}

// renderPub points the archive URLs of the upstream package document at this
// repository's root and leaves out the releases g holds back. For a version
// path it cuts that release's entry out. A package none of whose releases is
// left is errNotFound, as a package without releases.
func renderPub(body []byte, p pkgpub.Path, root string, g versionGate) ([]byte, error) {
	doc, err := pkgpub.ParseDoc(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errUnusableDoc, err)
	}

	link := func(v *pkgpub.Version) string { return root + "/" + pkgpub.ArchivePath(p.Name, v.Version) }
	keep := func(v *pkgpub.Version) bool {
		hidden, _ := g.Hidden(p.Name, v.Version, v.Published)
		return !hidden
	}

	if p.Kind == pkgpub.KindVersion {
		v := doc.Find(p.Version)
		if v == nil || !keep(v) {
			return nil, errNotFound
		}

		return pkgpub.MarshalVersion(v, link) //nolint:wrapcheck // an encoding failure is a server error
	}

	if !slices.ContainsFunc(doc.Versions, keep) {
		return nil, errNotFound
	}

	return doc.Marshal(link, keep) //nolint:wrapcheck // an encoding failure is a server error
}

func (s *Service) servePubArchive(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgpub.Path) {
	g, err := s.gateFor(r.Context(), repo, time.Now())
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	// The curation rules need no publish time, so they are asked before the
	// cache is read: an archive cached before the rules were set is refused too.
	// The minimum age is checked in locatePubArchive, when the file is fetched.
	if err = checkGate(g, p.Name, p.Version, time.Time{}); err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	s.serveDownload(w, r, repo, pkgpub.ArchivePath(p.Name, p.Version), func(ctx context.Context) (download, error) {
		return s.locatePubArchive(ctx, repo, p, g)
	})
}

// pubArchiveTarget resolves the archive_url of a release against the URL its
// package document was fetched from; it must be an absolute http(s) URL.
func pubArchiveTarget(docURL, archiveURL string) (string, error) {
	docRef, err := url.Parse(docURL)
	if err != nil {
		return "", fmt.Errorf("parse document url: %w", err)
	}

	ref, err := url.Parse(archiveURL)
	if err != nil {
		return "", fmt.Errorf("parse archive url: %w", err)
	}

	target := docRef.ResolveReference(ref)
	if (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return "", fmt.Errorf("%w: unusable archive url %q", pkgpub.ErrInvalidDoc, archiveURL)
	}

	target.Fragment = ""

	return target.String(), nil
}

// locatePubArchive finds an archive at the URL its release names in the package
// document, cached only when it matches the document's digest. A release the
// document does not list is errNotFound, one g holds back is its gateError.
func (s *Service) locatePubArchive(ctx context.Context, repo *models.Repository, p pkgpub.Path, g versionGate) (download, error) {
	doc, _, err := parseDoc(ctx, s, repo, pubDoc(p.Name), func(body []byte, _ *resolution) (*pkgpub.Doc, error) {
		return pkgpub.ParseDoc(body) //nolint:wrapcheck // logged by parseDoc
	})
	if err != nil {
		return download{}, err
	}

	v := doc.Find(p.Version)
	if v == nil {
		return download{}, errNotFound
	}

	if err = checkGate(g, p.Name, p.Version, v.Published); err != nil {
		return download{}, err
	}

	docURL, err := upstreamURL(repo.UpstreamURL, pkgpub.PackagePath(p.Name))
	if err != nil {
		s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
		return download{}, errInternal
	}

	target, err := pubArchiveTarget(docURL, v.ArchiveURL)
	if err != nil {
		s.log.Warnw("proxy upstream archive url refused", "repo", repo.Name, "package", p.Name, "version", p.Version, "error", err)
		return download{}, errUpstream
	}

	// A release the upstream lists without a digest is accepted unverified.
	return download{url: target, opts: fetchOpts{wantSHA256: v.SHA256, unverified: v.SHA256 == ""}}, nil
}

// cachedPubDoc is the parsed cached package document of name, if there is a
// usable one.
func (s *Service) cachedPubDoc(ctx context.Context, repo *models.Repository, name string) (*pkgpub.Doc, bool) {
	body, ok := s.cachedDocBody(ctx, repo, pubDoc(name))
	if !ok {
		return nil, false
	}

	doc, err := pkgpub.ParseDoc(body)
	if err != nil {
		return nil, false
	}

	return doc, true
}

// pubPublished is Hooks.Published: the "published" time of version in the
// cached package document of name.
func (s *Service) pubPublished(ctx context.Context, repo *models.Repository, name, version string) (time.Time, bool) {
	doc, ok := s.cachedPubDoc(ctx, repo, name)
	if !ok {
		return time.Time{}, false
	}

	v := doc.Find(version)
	if v == nil {
		return time.Time{}, false
	}

	return v.Published, !v.Published.IsZero()
}

// pubVersions is Hooks.Versions: the releases of the cached package document of name.
func (s *Service) pubVersions(ctx context.Context, repo *models.Repository, name string) ([]DatedVersion, bool) {
	doc, ok := s.cachedPubDoc(ctx, repo, name)
	if !ok {
		return nil, false
	}

	out := make([]DatedVersion, len(doc.Versions))
	for i, v := range doc.Versions {
		out[i] = DatedVersion{Version: v.Version, Published: v.Published}
	}

	return out, true
}

// PubHooks is the proxy hooks of the pub format.
var PubHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	Serve:     (*Service).servePub,
	Published: (*Service).pubPublished,
	Versions:  (*Service).pubVersions,
	// Archives, checked against the archive_sha256 of their release.
	Immutable:  func(key string) bool { return strings.HasPrefix(key, pkgpub.ArchivesDir) },
	IsMetadata: func(key string) bool { return !strings.HasPrefix(key, pkgpub.ArchivesDir) },
	BlobPath:   presign.PubBlobPath,
}
