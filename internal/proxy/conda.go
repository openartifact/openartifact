// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"crypto/md5" //nolint:gosec // the digest repodata publishes next to the sha256, checked when it is the only one
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/pkg/conda"
)

// DefaultMaxCondaIndex caps one repodata file fetched from a conda upstream,
// both as downloaded and decompressed. The conda-forge linux-64 repodata.json
// is about 460 MB (59 MB as .zst) and keeps growing, far beyond what any
// other format's index reaches, so it has a limit of its own.
const DefaultMaxCondaIndex int64 = 2 << 30

// serveConda answers a GET/HEAD for a conda proxy repository. The upstream is
// a channel (https://conda.anaconda.org/conda-forge). Its repodata files are
// served byte for byte, so the package URLs in them (<subdir>/<file>, relative
// to the channel) resolve through the proxy and every download is checked
// against the digest the repodata lists. Everything else a channel may carry
// (repodata.jlap, sharded repodata, index pages) answers 404 and clients fall
// back to the plain files.
func (s *Service) serveConda(w http.ResponseWriter, r *http.Request, repo *models.Repository, relPath string) {
	p, ok := conda.ParsePath(relPath)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch p.Kind {
	case conda.KindRepodata, conda.KindChanneldata:
		s.serveVerbatim(w, r, repo, s.condaDoc(p))
	case conda.KindPackage:
		s.serveDownload(w, r, repo, p.Key(), func(ctx context.Context) (download, error) {
			return s.locateCondaPackage(ctx, repo, p)
		})
	case conda.KindOther:
		http.NotFound(w, r)
	}
}

// condaDoc is a repodata or channeldata file of the channel, fetched and
// checked as a whole.
func (s *Service) condaDoc(p conda.Path) metaDoc {
	d := metaDoc{key: p.Key(), what: "conda " + p.File}

	switch p.Kind {
	case conda.KindRepodata:
		limit := s.condaIndexLimit()

		d.opts = staticOpts(fetchOpts{verify: func(spool io.ReadSeeker) error {
			return conda.ValidateIndex(spool, p.Comp, limit)
		}})
	case conda.KindChanneldata:
		d.opts = staticOpts(fetchOpts{verify: func(spool io.ReadSeeker) error {
			return conda.ValidateJSONObject(spool, s.maxMeta)
		}})
	case conda.KindPackage, conda.KindOther:
	}

	return d
}

func (s *Service) condaIndexLimit() int64 { return min(s.maxBody, s.maxCondaIndex) }

// locateCondaPackage finds a package file in the upstream's repodata and the
// digest it lists: a file the repodata does not list is not served at all.
func (s *Service) locateCondaPackage(ctx context.Context, repo *models.Repository, p conda.Path) (download, error) {
	key := p.Key()

	target, err := upstreamURL(repo.UpstreamURL, key)
	if err != nil {
		s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
		return download{}, errInternal
	}

	entry, err := s.condaEntry(ctx, repo, p)
	if errors.Is(err, errNotFound) {
		s.recordNegative(ctx, repo, key)
	}

	if err != nil {
		return download{}, err
	}

	opts := fetchOpts{wantSHA256: entry.SHA256}

	if entry.SHA256 == "" {
		vouched := false
		opts.vouched = &vouched
		opts.verify = func(spool io.ReadSeeker) error { return verifyCondaFile(spool, entry, &vouched) }
	}

	return download{url: target, opts: opts}, nil
}

// verifyCondaFile checks a package that its repodata entry lists without a
// sha256: the md5 when there is one (setting vouched), and the size.
func verifyCondaFile(spool io.ReadSeeker, e conda.Entry, vouched *bool) error {
	if e.MD5 != "" {
		h := md5.New() //nolint:gosec // see the import
		if _, err := io.Copy(h, spool); err != nil {
			return fmt.Errorf("hash file: %w", err)
		}

		if got := hex.EncodeToString(h.Sum(nil)); got != e.MD5 {
			return fmt.Errorf("%w: md5 %s, repodata lists %s", errVerify, got, e.MD5)
		}

		*vouched = true
	}

	if e.Size >= 0 {
		size, err := spool.Seek(0, io.SeekEnd)
		if err != nil {
			return fmt.Errorf("size file: %w", err)
		}

		if size != e.Size {
			return fmt.Errorf("%w: %d bytes, repodata lists %d", errVerify, size, e.Size)
		}
	}

	return nil
}

// condaEntry finds the repodata entry of p's package file in the upstream's
// repodata of the subdirectory, preferring the compressed copy, which clients
// fetch first and which is a seventh of the size. errNotFound when no
// repodata lists it.
func (s *Service) condaEntry(ctx context.Context, repo *models.Repository, p conda.Path) (conda.Entry, error) {
	for _, comp := range []conda.Compression{conda.CompZstd, conda.CompNone} {
		idx := conda.Path{Kind: conda.KindRepodata, Subdir: p.Subdir, File: conda.RepodataFile(false, comp), Comp: comp}

		e, err := s.lookupCondaEntry(ctx, repo, idx, p.File)
		if errors.Is(err, errNotFound) && !errors.Is(err, errNotListed) {
			continue // the upstream has no such index file
		}

		return e, unlisted(err)
	}

	return conda.Entry{}, errNotFound
}

// errNotListed is errNotFound for an index that exists and does not list the
// file, as opposed to an index that does not exist.
var errNotListed = fmt.Errorf("%w: not listed", errNotFound)

func unlisted(err error) error {
	if errors.Is(err, errNotListed) {
		return errNotFound
	}

	return err
}

// lookupCondaEntry reads file's entry from the cached (or freshly fetched)
// repodata idx. A cache row whose blob is gone is dropped and fetched once
// more; an index that no longer parses is dropped and refused.
func (s *Service) lookupCondaEntry(ctx context.Context, repo *models.Repository, idx conda.Path, file string) (conda.Entry, error) {
	d := s.condaDoc(idx)
	key := d.key

	for attempt := 0; ; attempt++ {
		res, err := s.resolveDoc(ctx, repo, d)
		if err != nil {
			return conda.Entry{}, err
		}

		e, err := s.readCondaEntry(ctx, repo, key, res.entry.BlobKey, file)
		if err == nil || !errors.Is(err, errBlobMissing) || attempt > 0 {
			return e, err
		}

		s.log.Warnw("proxy cache blob missing, refetching", "repo", repo.Name, "path", key)

		if derr := s.db.DeleteProxyCacheEntry(ctx, repo.ID, key); derr != nil {
			s.log.Errorw("failed to drop stale proxy cache entry", "repo", repo.Name, "path", key, "error", derr)
		}
	}
}

var errBlobMissing = errors.New("cached blob missing")

// readCondaEntry searches the cached repodata blob for file, holding one of
// the bounded document slots for the scan.
func (s *Service) readCondaEntry(ctx context.Context, repo *models.Repository, key, blobKey, file string) (conda.Entry, error) {
	if err := s.packuments.Acquire(ctx); err != nil {
		return conda.Entry{}, err //nolint:wrapcheck // caller checks for cancellation
	}
	defer s.packuments.Release()

	rc, err := s.blobStore.Get(ctx, blobKey)
	if err != nil {
		return conda.Entry{}, fmt.Errorf("%w: %w", errBlobMissing, err)
	}
	defer func() { base.CloseLog(rc, s.log) }()

	plain, _, err := conda.Open(rc, s.condaIndexLimit())
	if err != nil {
		return conda.Entry{}, s.badCondaIndex(ctx, repo, key, err)
	}
	defer func() { _ = plain.Close() }()

	e, err := conda.Lookup(plain, file, s.condaIndexLimit())

	switch {
	case err == nil:
		return e, nil
	case errors.Is(err, conda.ErrNotListed):
		return conda.Entry{}, errNotListed
	case errors.Is(err, conda.ErrMalformed), errors.Is(err, conda.ErrTooLarge):
		return conda.Entry{}, s.badCondaIndex(ctx, repo, key, err)
	}

	s.log.Errorw("failed to read cached conda repodata", "repo", repo.Name, "path", key, "error", err)

	return conda.Entry{}, errInternal
}

// badCondaIndex drops a cached repodata that does not parse (it was verified
// when stored, so the blob was damaged) instead of serving it until its TTL
// ends.
func (s *Service) badCondaIndex(ctx context.Context, repo *models.Repository, key string, err error) error {
	s.log.Warnw("cached conda repodata unreadable", "repo", repo.Name, "path", key, "error", err)

	if derr := s.db.DeleteProxyCacheEntry(ctx, repo.ID, key); derr != nil {
		s.log.Errorw("failed to drop unreadable proxy cache entry", "repo", repo.Name, "path", key, "error", derr)
	}

	return errUpstream
}

// isCondaIndexKey reports whether key is a repodata file, the only conda
// files that outgrow the common metadata limit.
func isCondaIndexKey(key string) bool {
	return strings.Contains(key[strings.LastIndexByte(key, '/')+1:], "repodata")
}

// condaImmutable reports whether name is a conda package file.
func condaImmutable(name string) bool {
	return strings.HasSuffix(name, conda.ExtConda) || strings.HasSuffix(name, conda.ExtTarBz2)
}

// CondaHooks is the proxy hooks of the conda format.
var CondaHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	Serve: (*Service).serveConda,
	// Package files, verified against the repodata digest; repodata moves.
	Immutable: func(key string) bool { return condaImmutable(keyName(key)) },
	// channeldata.json; repodata is capped by IsLargeMetadata instead.
	IsMetadata:      func(key string) bool { return !condaImmutable(keyName(key)) },
	IsLargeMetadata: isCondaIndexKey,
	PackageExts:     []string{conda.ExtConda, conda.ExtTarBz2},
	BlobPath:        presign.CondaBlobPath,
}
