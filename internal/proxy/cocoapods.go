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
	"slices"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/presign"
	pkgcocoapods "github.com/openartifact/openartifact/pkg/cocoapods"
)

// CocoaPods proxy repositories mirror a CocoaPods CDN spec repository (default
// https://cdn.cocoapods.org); clients use it as a Podfile `source` or through
// `pod repo add-cdn`. There is no hosted or group repository of the format:
// the CDN is a static tree of files, which a proxy mirrors one request at a time.
//
//   - CocoaPods-version.yml, the all_pods_versions_X_Y_Z.txt shards and
//     deprecated_podspecs.txt are cached under their own paths with the
//     repository's cache TTL and serve-stale rules, after a check that they
//     are what they claim to be (a captive portal's page is not cached). A shard
//     is requested only under the sharding the version file announces, and
//     every pod in it must hash into it. With curation on, the shard is
//     rendered for the client: the versions the rules hide are dropped, and a
//     pod with none left loses its line. Shards are never rendered otherwise.
//   - all_pods.txt, the list of every pod name `pod` downloads from the CDN root,
//     is cached and served as it is, after a check of its shape. It names pods
//     only, so curation does not filter it: the shard and the podspec carry the
//     versions the rules hide.
//   - A podspec (Specs/X/Y/Z/NAME/VERSION/NAME.podspec.json) is cached verbatim
//     under its own path as an immutable file, once it is a JSON object of that
//     pod and version, and rendered for the client: its "source" is pointed at
//     this repository (pkgcocoapods.Podspec.Rewrite) and everything else of the
//     document stays as published. The cache is upstream-shaped and independent
//     of the server's own URL.
//   - A source archive (dl/X/Y/Z/NAME/VERSION/REF/FILE) is fetched from the
//     URL the cached podspec publishes, never from anything the client sent: REF
//     is a digest of that URL and its checksums (Archive.Ref), so a request
//     that does not name what the podspec publishes is a 404. The repository's
//     credentials never go to the host an archive lives on. The archive is
//     refused, and not cached, when it does not match the sha256 or sha1 the
//     podspec publishes. An archive that a checksum or a commit fixes is
//     cached as immutable, one that a moving tag or no checksum names for the
//     repository TTL.
//   - A pod version curation hides refuses the podspec and the archive (403).
//
// The CDN publishes no dates, so the minimum age (min_age_hours) holds nothing back.
const (
	// cocoapodsMaxShard bounds a shard or deprecated_podspecs.txt read into memory.
	cocoapodsMaxShard = 64 << 20

	// cocoapodsMaxAge is how long shared caches may reuse a rendering of a public repository.
	cocoapodsMaxAge = 60

	cocoapodsSpecsDir = "Specs/"
	cocoapodsDLDir    = "dl/"

	cocoapodsTextType = "text/plain; charset=utf-8"
	cocoapodsYAMLType = "text/yaml; charset=utf-8"
)

// cocoapodsReadSpool reads a spooled upstream document of at most limit bytes.
func cocoapodsReadSpool(spool io.ReadSeeker, limit int) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(spool, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read document: %w", err)
	}

	if len(body) > limit {
		return nil, fmt.Errorf("%w: document larger than %d bytes", pkgcocoapods.ErrDocument, limit)
	}

	return body, nil
}

// cocoapodsVerify is the fetch check of a document: it reads the spool and hands
// it to check.
func cocoapodsVerify(limit int, check func(body []byte) error) func(io.ReadSeeker) error {
	return func(spool io.ReadSeeker) error {
		body, err := cocoapodsReadSpool(spool, limit)
		if err != nil {
			return err
		}

		return check(body)
	}
}

// cocoapodsVersionDoc is CocoaPods-version.yml.
var cocoapodsVersionDoc = metaDoc{ //nolint:gochecknoglobals // constant document description
	key: pkgcocoapods.VersionFile,
	opts: staticOpts(
		fetchOpts{contentType: cocoapodsYAMLType, verify: cocoapodsVerify(pkgcocoapods.MaxVersionFile, func(body []byte) error {
			_, err := pkgcocoapods.ParseVersionDoc(body)
			return err //nolint:wrapcheck // logged by store
		})},
	),
	limit: pkgcocoapods.MaxVersionFile,
	what:  "cocoapods version file",
	bad:   badDocInternal,
}

// cocoapodsDeprecatedDoc is deprecated_podspecs.txt.
var cocoapodsDeprecatedDoc = metaDoc{ //nolint:gochecknoglobals // constant document description
	key:   pkgcocoapods.DeprecatedFile,
	opts:  staticOpts(fetchOpts{contentType: cocoapodsTextType, verify: cocoapodsVerify(cocoapodsMaxShard, pkgcocoapods.VerifyDeprecated)}),
	limit: cocoapodsMaxShard,
	what:  "cocoapods deprecated podspecs",
	bad:   badDocInternal,
}

// cocoapodsAllPodsDoc is all_pods.txt.
var cocoapodsAllPodsDoc = metaDoc{ //nolint:gochecknoglobals // constant document description
	key:   pkgcocoapods.AllPodsFile,
	opts:  staticOpts(fetchOpts{contentType: cocoapodsTextType, verify: cocoapodsVerify(cocoapodsMaxShard, pkgcocoapods.VerifyAllPods)}),
	limit: cocoapodsMaxShard,
	what:  "cocoapods pod list",
	bad:   badDocInternal,
}

// cocoapodsShardDoc is the shard file.
func cocoapodsShardDoc(file string) metaDoc {
	return metaDoc{
		key: file,
		opts: staticOpts(fetchOpts{contentType: cocoapodsTextType, verify: cocoapodsVerify(cocoapodsMaxShard, func(body []byte) error {
			return pkgcocoapods.VerifyShardFile(body, file) //nolint:wrapcheck // logged by store
		})}),
		limit: cocoapodsMaxShard,
		what:  "cocoapods shard",
		bad:   badDocInternal,
	}
}

// cocoapodsSpecDoc is the podspec of name version in the shard directory dir; its
// cache key is the path the CDN serves it under.
func cocoapodsSpecDoc(dir, name, version string) metaDoc {
	return metaDoc{
		key: pkgcocoapods.SpecPath(dir, name, version),
		opts: staticOpts(fetchOpts{contentType: jsonMediaType, accept: jsonMediaType,
			verify: cocoapodsVerify(pkgcocoapods.MaxPodspec, func(body []byte) error {
				_, err := pkgcocoapods.ParsePodspec(body, name, version)
				return err //nolint:wrapcheck // logged by store
			})}),
		limit: pkgcocoapods.MaxPodspec,
		what:  "cocoapods podspec",
		bad:   badDocInternal,
	}
}

// cocoapodsPrefixLengths is the sharding of the repository, from its version
// file (fetched when it is not cached).
func (s *Service) cocoapodsPrefixLengths(ctx context.Context, repo *models.Repository) ([]int, error) {
	d, _, err := parseDoc(ctx, s, repo, cocoapodsVersionDoc, func(body []byte, _ *resolution) (pkgcocoapods.VersionDoc, error) {
		return pkgcocoapods.ParseVersionDoc(body) //nolint:wrapcheck // logged by parseDoc
	})
	if err != nil {
		return nil, err
	}

	return d.PrefixLengths, nil
}

// cachedPrefixLengths is cocoapodsPrefixLengths from the cache alone; the
// default sharding when the version file is not cached.
func (s *Service) cachedPrefixLengths(ctx context.Context, repo *models.Repository) []int {
	if body, ok := s.cachedDocBody(ctx, repo, cocoapodsVersionDoc); ok {
		if d, err := pkgcocoapods.ParseVersionDoc(body); err == nil {
			return d.PrefixLengths
		}
	}

	return pkgcocoapods.DefaultPrefixLengths
}

func (s *Service) serveCocoaPods(w http.ResponseWriter, r *http.Request, repo *models.Repository, relPath string) {
	p, ok := pkgcocoapods.ParsePath(relPath)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch p.Kind {
	case pkgcocoapods.KindVersionFile:
		s.serveVerbatim(w, r, repo, cocoapodsVersionDoc)
		return
	case pkgcocoapods.KindAllPods:
		// Names only: a curated pod still shows up here, its shard and podspec are what the rules gate.
		s.serveVerbatim(w, r, repo, cocoapodsAllPodsDoc)
		return
	}

	// Everything else is placed by the repository's sharding: a path that
	// names another is a 404 without a request to the upstream.
	lengths, err := s.cocoapodsPrefixLengths(r.Context(), repo)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	switch p.Kind {
	case pkgcocoapods.KindDeprecated:
		s.serveVerbatim(w, r, repo, cocoapodsDeprecatedDoc)
	case pkgcocoapods.KindShard:
		s.serveCocoaPodsShard(w, r, repo, p, lengths)
	case pkgcocoapods.KindPodspec:
		if p.Dir != pkgcocoapods.ShardDir(p.Name, lengths) {
			http.NotFound(w, r)
			return
		}

		s.serveCocoaPodsSpec(w, r, repo, p)
	case pkgcocoapods.KindDownload:
		if p.Dir != pkgcocoapods.ShardDir(p.Name, lengths) {
			http.NotFound(w, r)
			return
		}

		s.serveCocoaPodsArchive(w, r, repo, p)
	case pkgcocoapods.KindVersionFile, pkgcocoapods.KindAllPods:
		// served above
	}
}

func (s *Service) serveCocoaPodsShard(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgcocoapods.Path, lengths []int) {
	parts, _ := pkgcocoapods.ShardFileParts(p.Shard)

	if !slices.EqualFunc(parts, lengths, func(part string, n int) bool { return len(part) == n }) {
		http.NotFound(w, r)
		return
	}

	g, err := s.gateFor(r.Context(), repo, time.Now())
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	d := cocoapodsShardDoc(p.Shard)

	if !g.Set.Active() {
		s.serveVerbatim(w, r, repo, d)
		return
	}

	s.serveRendered(w, r, repo, renderedDoc{
		metaDoc: d,
		maxAge:  cocoapodsMaxAge,
		etag: func(blobKey string) string {
			sum := sha256.Sum256([]byte(blobKey + "\x00" + g.Stamp()))
			return `"` + hex.EncodeToString(sum[:16]) + `"`
		},
		render: func(body []byte, _ *resolution) ([]byte, string, error) {
			out, err := pkgcocoapods.FilterShard(body, func(name, version string) bool {
				hidden, _ := g.Hidden(name, version, time.Time{})
				return !hidden
			})
			if err != nil {
				return nil, "", fmt.Errorf("%w: %w", errUnusableDoc, err)
			}

			return out, cocoapodsTextType, nil
		},
	})
}

func (s *Service) serveCocoaPodsSpec(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgcocoapods.Path) {
	// The archive URL is downloaded by the client as it is, so it is absolute and
	// its origin is part of the validator; the rewrite happens at serve time.
	root := s.publicBase(w, r) + composerRepoPath(repo)

	s.serveRendered(w, r, repo, renderedDoc{
		metaDoc: cocoapodsSpecDoc(p.Dir, p.Name, p.Version),
		maxAge:  cocoapodsMaxAge,
		etag: func(blobKey string) string {
			sum := sha256.Sum256([]byte(blobKey + "\x00" + root))
			return `"` + hex.EncodeToString(sum[:16]) + `"`
		},
		render: func(body []byte, _ *resolution) ([]byte, string, error) {
			spec, err := pkgcocoapods.ParsePodspec(body, p.Name, p.Version)
			if err != nil {
				return nil, "", fmt.Errorf("%w: %w", errUnusableDoc, err)
			}

			a, ok := spec.Archive()
			if !ok {
				return body, jsonMediaType, nil
			}

			out, _ := spec.Rewrite(root + "/" + pkgcocoapods.DownloadPath(p.Dir, p.Name, p.Version, a.Ref(), a.File))

			return out, jsonMediaType, nil
		},
	})
}

func (s *Service) serveCocoaPodsArchive(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgcocoapods.Path) {
	key := pkgcocoapods.ArchiveKey(p.Name, p.Version, p.Ref, p.File)
	if len(key) > maxPathLen {
		http.NotFound(w, r)
		return
	}

	s.serveDownload(w, r, repo, key, func(ctx context.Context) (download, error) {
		return s.locateCocoaPodsArchive(ctx, repo, p)
	})
}

// cocoapodsArchive is the source archive the cached podspec of p publishes, nil
// when the podspec's source is not one the proxy serves.
func (s *Service) cocoapodsArchive(ctx context.Context, repo *models.Repository, p pkgcocoapods.Path) (*pkgcocoapods.Archive, error) {
	a, _, err := parseDoc(ctx, s, repo, cocoapodsSpecDoc(p.Dir, p.Name, p.Version),
		func(body []byte, _ *resolution) (*pkgcocoapods.Archive, error) {
			spec, err := pkgcocoapods.ParsePodspec(body, p.Name, p.Version)
			if err != nil {
				return nil, err //nolint:wrapcheck // logged by parseDoc
			}

			if a, ok := spec.Archive(); ok {
				return &a, nil
			}

			return nil, nil //nolint:nilnil // a source the client downloads itself
		})

	return a, err
}

// locateCocoaPodsArchive finds the archive the upstream's podspec publishes for
// the version of p. The reference and the file name in the request must be the
// ones the podspec gives (a stale or forged URL is errNotFound, without a negative
// cache entry: the podspec may simply have changed).
func (s *Service) locateCocoaPodsArchive(ctx context.Context, repo *models.Repository, p pkgcocoapods.Path) (download, error) {
	a, err := s.cocoapodsArchive(ctx, repo, p)
	if err != nil {
		return download{}, err
	}

	if a == nil || a.Ref() != p.Ref || a.File != p.File {
		return download{}, errNotFound
	}

	return download{url: a.URL, opts: fetchOpts{
		// Only a checksum or a commit fixes what the archive is: nothing else vouches
		// for it, so the repository TTL applies.
		unverified: !a.Pinned,
		verify:     func(spool io.ReadSeeker) error { return a.Verify(spool) }, //nolint:wrapcheck // logged by store
	}}, nil
}

// cocoapodsCoordinates is the pod version a podspec or archive request is for.
func cocoapodsCoordinates(rel string) (string, string, bool) {
	p, ok := pkgcocoapods.ParsePath(rel)
	if !ok || (p.Kind != pkgcocoapods.KindPodspec && p.Kind != pkgcocoapods.KindDownload) {
		return "", "", false
	}

	return p.Name, p.Version, true
}

// cocoapodsKeyCoordinates is the pod version a cached archive belongs to; the
// podspecs are metadata, not packages.
func cocoapodsKeyCoordinates(key string) (string, string, bool) {
	return pkgcocoapods.ParseArchiveKey(key)
}

// CocoaPodsHooks is the proxy hooks of the cocoapods format.
var CocoaPodsHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	Serve: (*Service).serveCocoaPods,
	// Podspecs and the archives their checksum or commit fixes never change once
	// published; an archive without that is cached for the TTL (see locate).
	Immutable: func(key string) bool {
		return strings.HasPrefix(key, cocoapodsSpecsDir) || strings.HasPrefix(key, cocoapodsDLDir)
	},
	IsMetadata:  func(key string) bool { return !strings.HasPrefix(key, cocoapodsDLDir) },
	BlobPath:    presign.CocoaPodsBlobPath,
	Coordinates: cocoapodsCoordinates,
	Versions:    (*Service).cocoapodsVersions,
}

// cocoapodsVersions lists the versions of the pod name its cached shard names,
// without dates (the CDN states none).
func (s *Service) cocoapodsVersions(ctx context.Context, repo *models.Repository, name string) ([]DatedVersion, bool) {
	if !pkgcocoapods.ValidName(name) {
		return nil, false
	}

	body, ok := s.cachedDocBody(ctx, repo, cocoapodsShardDoc(pkgcocoapods.ShardFile(name, s.cachedPrefixLengths(ctx, repo))))
	if !ok {
		return nil, false
	}

	versions, found, err := pkgcocoapods.VersionsOf(body, name)
	if err != nil || !found {
		return nil, false
	}

	out := make([]DatedVersion, 0, len(versions))
	for _, v := range versions {
		out = append(out, DatedVersion{Version: v})
	}

	return out, true
}
