// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/httpcache"
	"github.com/openartifact/openartifact/internal/middleware"
)

// ResolveRepo loads a repository by name. It returns nil without an error when
// the repository does not exist.
func (h *Handler) ResolveRepo(ctx context.Context, name string) (*models.Repository, error) {
	return h.DB.GetRepositoryByName(ctx, name)
}

// SharedCacheable reports whether the response to r may be stored by shared
// caches: only an anonymous caller reading a public repository qualifies. The
// decision uses the principal the RBAC middleware resolved, never the mere
// presence of an Authorization header; a request without a resolved principal
// fails closed.
func SharedCacheable(r *http.Request, repoPublic bool) bool {
	if !repoPublic {
		return false
	}

	p := middleware.PrincipalFromContext(r.Context())

	return p != nil && p.IsAnonymous
}

// ContentETag is the quoted strong entity-tag of a document that is its own
// version: the SHA-256 of its bytes. Proxy and group repositories use it for
// documents they generate or merge, so an unchanged document revalidates to a
// 304 whatever produced it.
func ContentETag(body []byte) string {
	sum := sha256.Sum256(body)

	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// OriginTag is a short, entity-tag-safe digest of the origin a document's
// absolute URLs were built from. Fold it into the validator and any in-process
// cache key of such a document, so a client holding an ETag for one origin never
// gets a 304 (and no cache serves a rendering) for another.
func OriginTag(origin string) string {
	sum := sha256.Sum256([]byte(origin))

	return hex.EncodeToString(sum[:8])
}

// Revalidate sets the validator and cache headers of a metadata response
// (ETag, Cache-Control, Vary: Authorization) and answers 304 Not Modified when
// the request's If-None-Match matches etag. It returns true when the 304 was
// written and the caller must return. Call it only after authorisation has
// passed (the RBAC middleware runs before every format handler) and as soon as
// the ETag is known, before any expensive rendering.
//
// etag must be a quoted entity-tag. maxAgeSeconds applies to shared-cacheable
// responses only; every other response is private and must be revalidated.
func Revalidate(w http.ResponseWriter, r *http.Request, repoPublic bool, etag string, maxAgeSeconds int) bool {
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", httpcache.CacheControl(SharedCacheable(r, repoPublic), maxAgeSeconds))
	h.Add("Vary", "Authorization")

	if !httpcache.ETagMatches(r.Header.Get("If-None-Match"), etag) {
		return false
	}

	w.WriteHeader(http.StatusNotModified)

	return true
}
