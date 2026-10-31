// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"net/http"

	"github.com/openartifact/openartifact/internal/database/models"
)

// cachedDocBody is the body of the metadata document d as it sits in the cache,
// read without ever fetching: the publish-time lookups of Hooks.Published use
// it, so asking for an explanation never reaches the upstream. A copy past its
// TTL still dates a version (publish times do not change). ok is false when the
// document is not cached, is a cached 404, or cannot be read.
func (s *Service) cachedDocBody(ctx context.Context, repo *models.Repository, d metaDoc) ([]byte, bool) {
	entry, err := s.db.GetProxyCacheEntry(ctx, repo.ID, d.key)
	if err != nil || entry == nil || entry.StatusCode == http.StatusNotFound || entry.BlobKey == "" {
		return nil, false
	}

	if s.packuments.Acquire(ctx) != nil {
		return nil, false
	}
	defer s.packuments.Release()

	body, err := s.readBlobCapped(ctx, entry.BlobKey, d.what, d.limit)
	if err != nil {
		return nil, false
	}

	return body, true
}
