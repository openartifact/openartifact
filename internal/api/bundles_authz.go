// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
)

// Release bundles are authorized per bundle, not by the global `manage`
// permission (the RBAC middleware only requires a real principal on these
// routes, see middleware.isBundleRoute). Rules, applied here:
//
//   - an administrator (global `manage`) may do everything;
//   - the creator owns the bundle: only the owner may add or remove items,
//     seal, archive or delete it;
//   - a bundle is visible (get, items, promotions, manifest) to its owner and
//     to anyone with `read` on every source repository of its items; anyone
//     else gets 404, as if it did not exist;
//   - adding items needs `read` on each item's source repository, sealing
//     needs `read` on every source repository of the bundle;
//   - promoting needs `read` on every source repository and `write` on the
//     target repository; it does not need ownership;
//   - listing bundles and managing pipelines stay administrative.
//
// Repository checks are repository-level (empty artifact path), so a
// permission target limited to a path pattern does not qualify: the server
// does not verify that an item's digest is stored at its declared path.

// bundleNeed is the minimum standing a handler requires on an existing bundle.
type bundleNeed int

const (
	// bundleNeedView: see the bundle (owner or read on all its sources).
	bundleNeedView bundleNeed = iota
	// bundleNeedOwn: change the bundle (owner).
	bundleNeedOwn
)

// bundleAccess is what the caller may do with one bundle.
type bundleAccess struct {
	admin bool
	owner bool
	// reads reports `read` on every source repository of the bundle's items
	// (true for a bundle without items).
	reads bool
	items []*models.ReleaseBundleItem
}

func (a bundleAccess) visible() bool {
	return a.admin || a.owner || (a.reads && len(a.items) > 0)
}

// isBundleOwner reports whether p created the bundle.
func isBundleOwner(p *auth.Principal, b *models.ReleaseBundle) bool {
	return p != nil && !p.IsAnonymous && p.Username != "" && b.CreatedBy == p.Username
}

// bundleAccessFor resolves the caller's standing on a bundle.
func (h *BundlesHandler) bundleAccessFor(ctx context.Context, p *auth.Principal, b *models.ReleaseBundle) (bundleAccess, error) {
	admin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil {
		return bundleAccess{}, err
	}

	if admin {
		return bundleAccess{admin: true, owner: true, reads: true}, nil
	}

	items, err := h.db.ListBundleItems(ctx, b.ID)
	if err != nil {
		return bundleAccess{}, err //nolint:wrapcheck // caller logs and maps to 500
	}

	reads, err := h.readsAllSources(ctx, p, items)
	if err != nil {
		return bundleAccess{}, err
	}

	return bundleAccess{owner: isBundleOwner(p, b), reads: reads, items: items}, nil
}

// readsAllSources reports whether p holds `read` on the source repository of
// every item.
func (h *BundlesHandler) readsAllSources(ctx context.Context, p *auth.Principal, items []*models.ReleaseBundleItem) (bool, error) {
	if len(items) == 0 {
		return true, nil
	}

	canRead, err := h.evaluator.RepoReadFilter(ctx, *p)
	if err != nil {
		return false, err //nolint:wrapcheck // caller logs and maps to 500
	}

	checked := make(map[string]struct{}, len(items))

	for _, item := range items {
		if _, done := checked[item.SourceRepoID]; done {
			continue
		}

		checked[item.SourceRepoID] = struct{}{}

		repo, err := h.db.GetRepositoryByID(ctx, item.SourceRepoID)
		if err != nil {
			return false, err //nolint:wrapcheck // caller logs and maps to 500
		}

		if repo == nil {
			return false, nil
		}

		ok, err := canRead(repo)
		if err != nil || !ok {
			return false, err
		}
	}

	return true, nil
}

// canReadAnyRepository reports whether p may read at least one repository: the
// bar for creating a draft bundle (which names no repository yet).
func (h *BundlesHandler) canReadAnyRepository(ctx context.Context, p *auth.Principal) (bool, error) {
	canRead, err := h.evaluator.RepoReadFilter(ctx, *p)
	if err != nil {
		return false, err //nolint:wrapcheck // caller logs and maps to 500
	}

	const pageSize = 200

	for page := 1; ; page++ {
		repos, err := h.db.ListRepositories(ctx, page, pageSize, "")
		if err != nil {
			return false, err //nolint:wrapcheck // caller logs and maps to 500
		}

		for _, repo := range repos {
			ok, err := canRead(repo)
			if err != nil || ok {
				return ok, err
			}
		}

		if len(repos) < pageSize {
			return false, nil
		}
	}
}

// requireBundleCreator writes the error response and returns false unless p may
// create a bundle: an administrator, or a principal that can read some repository.
func (h *BundlesHandler) requireBundleCreator(w http.ResponseWriter, r *http.Request) bool {
	ctx := r.Context()
	p := currentPrincipal(r)

	admin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err == nil && !admin && p != nil && !p.IsAnonymous {
		admin, err = h.canReadAnyRepository(ctx, p)
	}

	if err != nil {
		h.log.Errorw("bundle authorization: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")

		return false
	}

	if !admin {
		writeError(w, http.StatusForbidden, "forbidden", "read access to at least one repository is required")
		return false
	}

	return true
}

// loadBundle resolves {name}/{version} of the request and authorizes the
// caller for need. It writes the error response (404 for an unknown bundle or
// one the caller may not see, 403 for a visible bundle the caller may not
// change) and returns ok=false on failure.
func (h *BundlesHandler) loadBundle(w http.ResponseWriter, r *http.Request, need bundleNeed) (*models.ReleaseBundle, bundleAccess, bool) {
	ctx := r.Context()
	p := currentPrincipal(r)

	bundle, err := h.db.GetReleaseBundle(ctx, urlparams.Vars(r)["name"], urlparams.Vars(r)["version"])
	if err != nil {
		h.log.Errorw("bundle: db error getting bundle", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return nil, bundleAccess{}, false
	}

	if bundle == nil {
		writeError(w, http.StatusNotFound, "not_found", "bundle not found")
		return nil, bundleAccess{}, false
	}

	access, err := h.bundleAccessFor(ctx, p, bundle)
	if err != nil {
		h.log.Errorw("bundle authorization: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")

		return nil, bundleAccess{}, false
	}

	switch {
	case !access.visible():
		writeError(w, http.StatusNotFound, "not_found", "bundle not found")
		return nil, bundleAccess{}, false
	case need == bundleNeedOwn && !access.owner:
		writeError(w, http.StatusForbidden, "forbidden", "only the bundle's creator or an administrator can change it")
		return nil, bundleAccess{}, false
	}

	return bundle, access, true
}

// requireSourceReads writes a 403 and returns false when the caller lacks
// `read` on a source repository of the bundle.
func requireSourceReads(w http.ResponseWriter, access bundleAccess) bool {
	if !access.reads {
		writeError(w, http.StatusForbidden, "forbidden", "read access to every source repository of the bundle is required")
		return false
	}

	return true
}

// requireTargetWrite checks `write` on the promotion target. A caller who cannot
// even read the target gets 404, like an unknown repository.
func (h *BundlesHandler) requireTargetWrite(w http.ResponseWriter, r *http.Request, access bundleAccess, target *models.Repository) bool {
	if access.admin {
		return true
	}

	ctx := r.Context()
	p := currentPrincipal(r)

	canWrite, err := h.evaluator.Evaluate(ctx, *p, target.Name, "", "write")
	if err != nil {
		h.log.Errorw("promote bundle: auth evaluation error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")

		return false
	}

	if canWrite {
		return true
	}

	canRead, err := h.evaluator.Evaluate(ctx, *p, target.Name, "", "read")
	if err == nil && !canRead {
		writeError(w, http.StatusNotFound, "not_found", "target repository not found")
		return false
	}

	writeError(w, http.StatusForbidden, "forbidden", "write access to the target repository is required")

	return false
}
