// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/openartifact/openartifact/internal/database/models"
)

// ErrNotFound is returned by Open for a file the upstream does not have.
var ErrNotFound = errors.New("proxy: not found upstream")

// ErrBlocked is returned by Open for a blocked proxy repository.
var ErrBlocked = errors.New("proxy: repository is blocked")

// Open returns the body of relPath of a path-mirrored proxy repository (deb,
// rpm, maven, raw), fetching and caching it exactly as Serve would. It is for
// server-side consumers of upstream metadata (deb/rpm groups merging a proxy
// member's indexes), which read it once and must see what a client of the
// proxy would. An expired entry is revalidated, and served stale when the
// upstream is down within the repository's max-stale window. ErrNotFound is
// returned for an upstream 404; any other error means the file could not be
// obtained. The caller closes the reader.
func (s *Service) Open(ctx context.Context, repo *models.Repository, relPath string) (io.ReadCloser, error) {
	if repo.Blocked {
		return nil, ErrBlocked
	}

	key, ok := CleanPath(relPath)
	if !ok {
		return nil, ErrNotFound
	}

	res, err := s.resolve(ctx, repo, key)
	if err != nil {
		return nil, openError(repo, key, err)
	}

	rc, err := s.blobStore.Get(ctx, res.entry.BlobKey)
	if err != nil && res.fromCache {
		// The cache row outlived its blob: drop it and fetch once more.
		if derr := s.db.DeleteProxyCacheEntry(ctx, repo.ID, key); derr != nil {
			s.log.Errorw("failed to drop stale proxy cache entry", "repo", repo.Name, "path", key, "error", derr)
		}

		if res, err = s.resolve(ctx, repo, key); err != nil {
			return nil, openError(repo, key, err)
		}

		rc, err = s.blobStore.Get(ctx, res.entry.BlobKey)
	}

	if err != nil {
		return nil, fmt.Errorf("proxy %s: open cached %s: %w", repo.Name, key, err)
	}

	return rc, nil
}

func openError(repo *models.Repository, key string, err error) error {
	if errors.Is(err, errNotFound) {
		return ErrNotFound
	}

	return fmt.Errorf("proxy %s: fetch %s: %w", repo.Name, key, err)
}
