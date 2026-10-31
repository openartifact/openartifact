// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/pkg/conda"
)

// The version gate of a conda proxy (see gate.go): minimum age and curation.
//
// Repodata dates a package with the "timestamp" of its entry (milliseconds,
// older entries seconds): when it was built, which is the nearest thing to a
// publish time a channel states. An entry without one passes. While the gate
// is on, repodata.json, current_repodata.json and their .zst copies are rebuilt
// from the cached upstream file without the entries too young: the result is
// the same document the groups build ("info" with the subdirectory, "packages"
// and "packages.conda"; "removed" and the other top-level keys are not carried
// over), spooled to disk entry by entry, and served plain or zstd-compressed.
// The .bz2 copy cannot be produced and answers 404 (clients fall back to the
// others). A package file of an entry the gate holds back is refused (403).
//
// Curation names a package by the "name" of its entry (the file name's name
// too: both are asked) and its version by the entry's "version"; neither needs
// a timestamp, so a package without one is still curated.

const (
	// condaGatedMaxAge is how long shared caches may reuse a gated repodata.
	condaGatedMaxAge = 60

	condaZstdType = "application/zstd"
)

// condaCheck is nil when the package file named file (whose repodata entry states
// the given name and version, "" for a field it lacks) is available under g, else
// the gateError of the first coordinates it holds back: the ones of the file
// name, then the entry's own, which a repodata may state differently.
func condaCheck(g versionGate, file conda.File, name, version string, published time.Time) error {
	if err := checkGate(g, file.Name, file.Version, published); err != nil {
		return err
	}

	if (name == "" || name == file.Name) && (version == "" || version == file.Version) {
		return nil
	}

	if name == "" {
		name = file.Name
	}

	if version == "" {
		version = file.Version
	}

	return checkGate(g, name, version, published)
}

// condaDrop is the Drop hook of a repodata rebuild under g.
func condaDrop(g versionGate) func(conda.File, json.RawMessage) bool {
	return func(file conda.File, entry json.RawMessage) bool {
		name, version := conda.EntryIdentity(entry)

		return condaCheck(g, file, name, version, conda.EntryTimestamp(entry)) != nil
	}
}

// serveCondaGated answers a repodata request while the gate is on.
func (s *Service) serveCondaGated(w http.ResponseWriter, r *http.Request, repo *models.Repository, p conda.Path, g versionGate) {
	if p.Comp == conda.CompBzip2 {
		http.NotFound(w, r)
		return
	}

	d := s.condaDoc(p)

	res, err := s.resolveDoc(r.Context(), repo, d)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")

	if res.stale {
		h.Set("Warning", staleWarning)
	}

	// The rendering depends on the cached file and on which entries the gate
	// holds back at this time and under these rules.
	etag := func(blobKey string) string { return `"` + blobKey + `~` + g.Stamp() + `"` }

	if base.Revalidate(w, r, repo.Public, etag(res.entry.BlobKey), condaGatedMaxAge) {
		return
	}

	contentType := jsonMediaType
	if p.Comp == conda.CompZstd {
		contentType = condaZstdType
	}

	h.Set("Content-Type", contentType)

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	doc, loaded, err := s.condaFiltered(r.Context(), repo, d, res, p, g)
	if err != nil {
		s.failWithoutValidators(w, r, err)
		return
	}
	defer doc.Close()

	if loaded.entry.BlobKey != res.entry.BlobKey {
		// The cached file was gone and fetched again: the validator must
		// describe what is sent.
		h.Set("ETag", etag(loaded.entry.BlobKey))
	}

	writeCondaDocument(w, p, doc)
}

// condaFiltered rebuilds the repodata of d from its cached file without the
// entries g holds back. A cache row whose blob is gone is dropped
// and the file fetched once more. The returned resolution is the one the
// document was built from.
func (s *Service) condaFiltered(
	ctx context.Context, repo *models.Repository, d metaDoc, res *resolution, p conda.Path, g versionGate,
) (*conda.Document, *resolution, error) {
	for attempt := 0; ; attempt++ {
		doc, err := s.buildCondaFiltered(ctx, repo, d.key, res.entry.BlobKey, p, g)
		if err == nil {
			return doc, res, nil
		}

		if !errors.Is(err, errBlobMissing) || attempt > 0 {
			return nil, nil, err
		}

		s.log.Warnw("proxy cache blob missing, refetching", "repo", repo.Name, "path", d.key)

		if derr := s.db.DeleteProxyCacheEntry(ctx, repo.ID, d.key); derr != nil {
			s.log.Errorw("failed to drop stale proxy cache entry", "repo", repo.Name, "path", d.key, "error", derr)
		}

		if res, err = s.resolveDoc(ctx, repo, d); err != nil {
			return nil, nil, err
		}
	}
}

// buildCondaFiltered streams the cached repodata blobKey through a rebuild,
// holding one of the bounded document slots for the pass.
func (s *Service) buildCondaFiltered(
	ctx context.Context, repo *models.Repository, key, blobKey string, p conda.Path, g versionGate,
) (*conda.Document, error) {
	if err := s.packuments.Acquire(ctx); err != nil {
		return nil, err //nolint:wrapcheck // caller checks for cancellation
	}
	defer s.packuments.Release()

	rc, err := s.blobStore.Get(ctx, blobKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errBlobMissing, err)
	}
	defer func() { base.CloseLog(rc, s.log) }()

	b, err := conda.NewBuilder(p.Subdir, "")
	if err != nil {
		s.log.Errorw("failed to start conda repodata rebuild", "repo", repo.Name, "error", err)
		return nil, errInternal
	}
	defer b.Close()

	_, err = b.Add(rc, conda.MemberOptions{MaxBytes: s.condaIndexLimit(), Drop: condaDrop(g)})

	switch {
	case err == nil:
	case errors.Is(err, conda.ErrMalformed), errors.Is(err, conda.ErrTooLarge):
		return nil, s.badCondaIndex(ctx, repo, key, err)
	case ctx.Err() != nil:
		return nil, ctx.Err() //nolint:wrapcheck // caller checks for cancellation
	default:
		s.log.Errorw("failed to rebuild conda repodata", "repo", repo.Name, "path", key, "error", err)
		return nil, errInternal
	}

	doc, err := b.Finish()
	if err != nil {
		s.log.Errorw("failed to finish conda repodata rebuild", "repo", repo.Name, "path", key, "error", err)
		return nil, errInternal
	}

	return doc, nil
}

// writeCondaDocument sends a rebuilt repodata plain, with its length, or
// zstd-compressed on the fly.
func writeCondaDocument(w http.ResponseWriter, p conda.Path, doc *conda.Document) {
	if p.Comp != conda.CompZstd {
		w.Header().Set("Content-Length", strconv.FormatInt(doc.Size(), 10))
		w.WriteHeader(http.StatusOK)
		_, _ = doc.WriteTo(w)

		return
	}

	w.WriteHeader(http.StatusOK)

	enc, err := zstd.NewWriter(w, zstd.WithEncoderConcurrency(2))
	if err != nil {
		return
	}

	// The status line is out: a failed copy can only abort the body.
	_, _ = doc.WriteTo(enc)
	_ = enc.Close()
}
