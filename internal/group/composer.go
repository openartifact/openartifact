// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/repotype"
	pkgcomposer "github.com/openartifact/openartifact/pkg/composer"
)

const (
	// composerMetaMaxAge is how long shared caches may reuse a public group's
	// generated metadata before revalidating.
	composerMetaMaxAge = 60

	// composerMaxDoc bounds a member's document held in memory.
	composerMaxDoc = 64 << 20
)

// serveComposer answers the Composer metadata requests of a composer group and
// reports whether it handled the request; dist downloads fall through to the
// generic first-hit resolution.
//
// packages.json is the group's own: a metadata-url pointing at its p2 documents
// and, when every member is hosted, the union of the members' "available-packages"
// (a proxy cannot enumerate its upstream, and Composer trusts the list to be
// complete). A p2 document is the union of the members' documents by normalized
// version, the first member in order that has a version describing it, in the
// members' order. Every dist is relinked to the group under the file name the
// member serves it with (its content reference), so the download is asked of the
// members in order and only the member that published that version has a dist
// of that reference. list.json and search.json are not merged: they answer 404.
//
// Hosted wins: unless the group merges proxy versions, a package a readable
// hosted member holds is served by the hosted members only (see applyHostedWins).
func (h *Handler) serveComposer(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) bool {
	p, ok := pkgcomposer.ParsePath(relPath)
	if !ok {
		http.NotFound(w, r)
		return true
	}

	root := h.baseURL + "/repos/" + url.PathEscape(grp.Name)

	switch p.Kind {
	case pkgcomposer.KindRoot:
		h.serveComposerRoot(w, r, grp, root, members, be)
	case pkgcomposer.KindMetadata:
		h.serveComposerMetadata(w, r, grp, root, p, relPath, members, be)
	case pkgcomposer.KindDist:
		return false
	case pkgcomposer.KindList, pkgcomposer.KindSearch, pkgcomposer.KindUpload:
		http.NotFound(w, r)
	}

	return true
}

func (h *Handler) serveComposerRoot(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	root string,
	members []*models.Repository,
	be Backend,
) {
	doc := map[string]any{"packages": []any{}, "metadata-url": root + "/p2/%package%.json"}

	if names, ok := h.composerAvailable(r, members, be); ok {
		doc["available-packages"] = names
	}

	body, err := json.Marshal(doc)
	if err != nil {
		h.log.Errorw("failed to encode composer document", "group", grp.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	writeMerged(w, r, grp.Public, body, "application/json", base.ContentETag(body), composerMetaMaxAge, false)
}

// composerAvailable is the union of the "available-packages" of the members'
// packages.json, sorted. It is only known when there are members, every one of
// them is hosted and each answered with such a list.
func (h *Handler) composerAvailable(r *http.Request, members []*models.Repository, be Backend) ([]string, bool) {
	if len(members) == 0 {
		return nil, false
	}

	mr := memberGetRequest(r)

	sess := h.newSession(r.Context())
	defer sess.release()

	seen := map[string]bool{}
	names := []string{}

	for _, m := range members {
		if m.RepoType == repotype.Proxy {
			return nil, false
		}

		cw := sess.capture(composerMaxDoc)
		be.ServeMember(cw, mr, m, "packages.json")

		if cw.status == 0 {
			cw.status = http.StatusOK
		}

		var doc struct {
			Available *[]string `json:"available-packages"` //nolint:tagliatelle // Composer wire format
		}

		if cw.status != http.StatusOK || cw.overflow || json.Unmarshal(cw.body.Bytes(), &doc) != nil || doc.Available == nil {
			return nil, false
		}

		for _, n := range *doc.Available {
			if !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}

	slices.Sort(names)

	return names, true
}

func (h *Handler) serveComposerMetadata(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	root string,
	p pkgcomposer.Path,
	relPath string,
	members []*models.Repository,
	be Backend,
) {
	mr := memberGetRequest(r)

	sess := h.newSession(r.Context())
	defer sess.release()

	var (
		merged []pkgcomposer.Version
		seen   = map[string]bool{}
		failed bool
	)

	for _, m := range members {
		cw := sess.capture(composerMaxDoc)
		be.ServeMember(cw, mr, m, relPath)

		if cw.status == 0 {
			cw.status = http.StatusOK
		}

		if cw.status == http.StatusNotFound {
			continue
		}

		versions, err := pkgcomposer.ParseDocument(cw.body.Bytes(), p.Name)
		if cw.status != http.StatusOK || cw.overflow || err != nil {
			failed = true
			continue
		}

		pkgcomposer.RelinkMember(versions, p.Name, root)

		for _, v := range versions {
			if key := v.MergeKey(); !seen[key] {
				seen[key] = true

				merged = append(merged, v)
			}
		}
	}

	sess.release()

	if len(merged) == 0 {
		if failed {
			http.Error(w, "member repository error", http.StatusBadGateway)
		} else {
			http.NotFound(w, r)
		}

		return
	}

	body, err := pkgcomposer.EncodeDocument(map[string][]pkgcomposer.Version{p.Name: merged}, true)
	if err != nil {
		h.log.Errorw("failed to encode composer metadata", "group", grp.Name, "package", p.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	writeMerged(w, r, grp.Public, body, "application/json", base.ContentETag(body), composerMetaMaxAge, failed)
}
