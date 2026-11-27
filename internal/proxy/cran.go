// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // the MD5sum PACKAGES field is the only digest CRAN publishes for a package
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/presign"
	pkgcran "github.com/openartifact/openartifact/pkg/cran"
)

// CRAN proxy repositories mirror a CRAN-style repository (upstream_url is its
// root, for example https://cloud.r-project.org):
//
//   - The index of a tree (src/contrib, bin/windows/contrib/<R>,
//     bin/macosx/.../contrib/<R>) is cached once, as the upstream's
//     <tree>/PACKAGES.gz under that key, with the repository's cache TTL and
//     serve-stale rules, and PACKAGES, PACKAGES.gz and PACKAGES.rds are all
//     rendered from it, whichever the client asks for. A tree the upstream does
//     not have is 404. Versions the repository's curation rules make
//     unavailable are left out of the index.
//   - A package file is cached under its canonical key <tree>/<pkg>_<ver>.<ext>,
//     wherever it was found upstream: the current source version is in
//     src/contrib, an older one in src/contrib/Archive/<pkg>, and a client may
//     ask either path. A file the index lists is checked against the MD5sum it
//     lists (and cached as immutable when it is a source package); a file it
//     does not list (an archived version) is fetched without a digest to check
//     and cached for the repository TTL only.
//
// CRAN publishes no time per version, so the minimum age (min_age_hours) does
// not apply; curation does, by package name and version.

const (
	cranIndexKeyName = "PACKAGES.gz"

	// cranIndexMaxAge is how long shared caches may reuse the rendering of a
	// public repository's index before revalidating.
	cranIndexMaxAge = 60

	cranSourcePrefix = pkgcran.SourceTree + "/"
)

var errNotCRANIndex = fmt.Errorf("%w: upstream answer is not a PACKAGES index", errUnusableDoc)

func (s *Service) serveCRAN(w http.ResponseWriter, r *http.Request, repo *models.Repository, rel string) {
	p, ok := pkgcran.ParsePath(rel)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch p.Kind {
	case pkgcran.KindIndex:
		s.serveCRANIndex(w, r, repo, p)
	case pkgcran.KindPackage, pkgcran.KindArchivePackage:
		s.serveCRANPackage(w, r, repo, p)
	case pkgcran.KindArchiveRoot, pkgcran.KindArchiveDir, pkgcran.KindOther:
		// The upstream's directory listings are not mirrored.
		http.NotFound(w, r)
	}
}

// cranIndexDoc is the upstream PACKAGES.gz of a tree, refused unless it parses
// as an index (an HTML error page served with 200, a truncated download).
func cranIndexDoc(tree pkgcran.Tree) metaDoc {
	return metaDoc{
		key:   tree.Dir + "/" + cranIndexKeyName,
		opts:  staticOpts(fetchOpts{verify: verifyCRANIndex}),
		limit: pkgcran.MaxIndexBytes,
		what:  "cran index",
		bad:   badDocDrop,
	}
}

func verifyCRANIndex(spool io.ReadSeeker) error {
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind spool: %w", err)
	}

	if _, err := readCRANIndex(spool); err != nil {
		return errNotCRANIndex
	}

	return nil
}

// readCRANIndex decompresses and parses a PACKAGES.gz.
func readCRANIndex(r io.Reader) ([]pkgcran.Record, error) {
	text, err := pkgcran.Gunzip(r, pkgcran.MaxIndexBytes)
	if err != nil {
		return nil, fmt.Errorf("read index: %w", err)
	}

	recs, err := pkgcran.ParsePackages(bytes.NewReader(text), pkgcran.MaxIndexBytes)
	if err != nil {
		return nil, fmt.Errorf("parse index: %w", err)
	}

	return recs, nil
}

// cranIndexETag identifies one rendering of a cached index: the upstream body
// it derives from, the variant, and the version gate (stamp: which versions it
// lets through).
func cranIndexETag(blobKey string, variant pkgcran.Variant, stamp string) string {
	sum := sha256.Sum256([]byte(blobKey + "\x00" + variant.File() + "\x00" + stamp))
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func (s *Service) serveCRANIndex(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgcran.Path) {
	g, err := s.gateFor(r.Context(), repo, time.Now())
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	s.serveRendered(w, r, repo, renderedDoc{
		metaDoc: cranIndexDoc(p.Tree),
		maxAge:  cranIndexMaxAge,
		etag:    func(blobKey string) string { return cranIndexETag(blobKey, p.Variant, g.Stamp()) },
		render: func(body []byte, _ *resolution) ([]byte, string, error) {
			recs, err := readCRANIndex(bytes.NewReader(body))
			if err != nil {
				return nil, "", fmt.Errorf("%w: %w", errNotCRANIndex, err)
			}

			if g.Active() {
				recs = dropHeldCRAN(recs, g)
			}

			out, err := renderCRANVariant(recs, p)

			return out, p.Variant.ContentType(), err
		},
	})
}

// dropHeldCRAN removes the records of the versions the gate holds back.
func dropHeldCRAN(recs []pkgcran.Record, g versionGate) []pkgcran.Record {
	out := recs[:0:0]

	for _, rec := range recs {
		if hidden, _ := g.Hidden(rec.Get("Package"), rec.Get("Version"), time.Time{}); !hidden {
			out = append(out, rec)
		}
	}

	return out
}

func renderCRANVariant(recs []pkgcran.Record, p pkgcran.Path) ([]byte, error) {
	switch p.Variant {
	case pkgcran.VariantGzip:
		return pkgcran.Gzip(pkgcran.RenderText(recs)) //nolint:wrapcheck // caller logs
	case pkgcran.VariantRDS:
		return pkgcran.RenderRDS(recs, p.Tree.Binary) //nolint:wrapcheck // caller logs
	case pkgcran.VariantText:
	}

	return pkgcran.RenderText(recs), nil
}

func (s *Service) serveCRANPackage(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgcran.Path) {
	s.serveCachedFetch(w, r, repo, p.Canonical(), func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		return s.fetchCRANPackage(fctx, repo, p)
	})
}

// fetchCRANPackage fetches the package file p names from the upstream, where
// the upstream keeps it: the tree's own directory, and for a source package the
// Archive of the package too (the current version is in one, older ones in the
// other, and which is which changes as the package is updated).
func (s *Service) fetchCRANPackage(ctx context.Context, repo *models.Repository, p pkgcran.Path) (*models.ProxyCacheEntry, error) {
	key := p.Canonical()

	listed, err := s.cranListed(ctx, repo, p)
	if err != nil {
		return nil, err
	}

	paths := []string{key}
	if !p.Tree.Binary {
		paths = []string{key, pkgcran.SourceTree + "/" + pkgcran.ArchiveDir + "/" + p.Package + "/" + p.File}
		if p.Kind == pkgcran.KindArchivePackage && listed == "" {
			paths[0], paths[1] = paths[1], paths[0]
		}
	}

	opts := cranFileOpts(listed)

	for _, upstreamPath := range paths {
		target, uerr := upstreamURL(repo.UpstreamURL, upstreamPath)
		if uerr != nil {
			s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", uerr)
			return nil, errInternal
		}

		entry, ferr := s.fetchDownload(ctx, repo, key, download{url: target, opts: opts()})
		if !errors.Is(ferr, errNotFound) {
			return entry, ferr
		}
	}

	return nil, errNotFound
}

// cranFileOpts returns the fetch options of a package file whose index record
// lists md5 ("" when it is not listed). A fresh value per fetch: vouched is
// written by verify.
func cranFileOpts(md5sum string) func() fetchOpts {
	return func() fetchOpts {
		vouched := false

		return fetchOpts{
			vouched: &vouched,
			verify:  func(spool io.ReadSeeker) error { return verifyCRANFile(spool, md5sum, &vouched) },
		}
	}
}

// verifyCRANFile checks a package against the MD5sum its index record lists
// (setting vouched). A file with no listed digest passes unchecked and is
// cached for the repository TTL only.
func verifyCRANFile(spool io.ReadSeeker, want string, vouched *bool) error {
	if want == "" {
		return nil
	}

	h := md5.New() //nolint:gosec // see the import
	if _, err := io.Copy(h, spool); err != nil {
		return fmt.Errorf("hash file: %w", err)
	}

	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("%w: md5 %s, the index lists %s", errVerify, got, want)
	}

	*vouched = true

	return nil
}

// cranListed returns the MD5sum the cached index of p's tree lists for exactly
// p's package and version, "" when it lists another version of the package, or
// none (an archived version), or no digest. An index the upstream does not have
// lists nothing. Only a failure to read an index that exists is an error.
func (s *Service) cranListed(ctx context.Context, repo *models.Repository, p pkgcran.Path) (string, error) {
	want, _, err := parseDoc(ctx, s, repo, cranIndexDoc(p.Tree), func(body []byte, _ *resolution) (string, error) {
		recs, err := readCRANIndex(bytes.NewReader(body))
		if err != nil {
			return "", err
		}

		for _, rec := range recs {
			if rec.Get("Package") == p.Package && rec.Get("Version") == p.Version {
				return rec.Get("MD5sum"), nil
			}
		}

		return "", nil
	})

	if errors.Is(err, errNotFound) {
		return "", nil
	}

	return want, err
}

// cranCoordinates parses a cache key or request path into the package and
// version of the package file it names.
func cranCoordinates(key string) (string, string, bool) {
	p, ok := pkgcran.ParsePath(key)
	if !ok || (p.Kind != pkgcran.KindPackage && p.Kind != pkgcran.KindArchivePackage) {
		return "", "", false
	}

	return p.Package, p.Version, true
}

// cranImmutable reports whether the cached file at key never changes: a source
// package whose index digest was checked. Binary packages are rebuilt in place
// by CRAN.
func cranImmutable(key string) bool {
	return strings.HasPrefix(key, cranSourcePrefix) && strings.HasSuffix(key, pkgcran.Source.Ext)
}

// CRANHooks is the proxy hooks of the cran format.
var CRANHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	Serve:       (*Service).serveCRAN,
	Immutable:   cranImmutable,
	IsMetadata:  func(key string) bool { return keyName(key) == cranIndexKeyName },
	PackageExts: []string{".tar.gz", ".tgz", ".zip"},
	Coordinates: cranCoordinates,
	BlobPath:    presign.CRANBlobPath,
}
