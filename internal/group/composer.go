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
	"github.com/openartifact/openartifact/internal/publicurl"
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

// ComposerHooks is the composer group: merged p2 documents, dists first-hit.
var ComposerHooks = Hooks{ //nolint:gochecknoglobals // immutable format hook value
	ServeMerged: (*Handler).serveComposer,
	PackageRef:  composerRef,
}

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

	switch p.Kind {
	case pkgcomposer.KindRoot:
		h.serveComposerRoot(w, r, grp, members, be)
	case pkgcomposer.KindMetadata:
		// dist.url is downloaded by Composer as-is, so it is absolute; the
		// merged document carries the origin and its content digest is the
		// validator, so no origin ever revalidates against another's.
		root := h.publicBase(w, r) + "/repos/" + url.PathEscape(grp.Name)

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
	members []*models.Repository,
	be Backend,
) {
	// metadata-url is path-absolute: Composer resolves a value starting with "/"
	// against the scheme and host of the repository URL
	// (ComposerRepository::canonicalizeUrl), so the document names no origin.
	prefix := publicurl.PathPrefix(h.baseURL)
	doc := map[string]any{"packages": []any{}, "metadata-url": prefix + "/repos/" + url.PathEscape(grp.Name) + "/p2/%package%.json"}

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
	if len(members) == 0 || slices.ContainsFunc(members, func(m *models.Repository) bool { return m.RepoType == repotype.Proxy }) {
		return nil, false
	}

	sess := h.newSession(r.Context())
	defer sess.release()

	c := h.collect(r, sess, members, memberAsk{path: samePath("packages.json"), limit: composerMaxDoc}, be)
	if c.partial() || len(c.docs) != len(members) {
		return nil, false
	}

	seen := map[string]bool{}
	names := []string{}

	for _, d := range c.docs {
		var doc struct {
			Available *[]string `json:"available-packages"` //nolint:tagliatelle // Composer wire format
		}

		if json.Unmarshal(d.body, &doc) != nil || doc.Available == nil {
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
	h.serveMergedDoc(w, r, grp, members, be, mergedDoc{
		ask:         memberAsk{path: samePath(relPath), limit: composerMaxDoc},
		what:        "composer metadata",
		maxAge:      composerMetaMaxAge,
		contentType: "application/json",
		merge: func(c *collection) ([]byte, error) {
			var (
				merged []pkgcomposer.Version
				seen   = map[string]bool{}
			)

			for _, d := range c.docs {
				versions, err := pkgcomposer.ParseDocument(d.body, p.Name)
				if err != nil {
					c.fail(d.member)
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

			if len(merged) == 0 {
				return nil, errNoDocument
			}

			return pkgcomposer.EncodeDocument(
				map[string][]pkgcomposer.Version{p.Name: merged},
				true,
			) //nolint:wrapcheck // logged by serveMergedDoc
		},
	})
}

// composerRef: a hosted member holds a package when its stable or its "~dev"
// p2 document exists; its dists are files of the package.
func composerRef(relPath string) (PkgRef, bool) {
	p, ok := pkgcomposer.ParsePath(relPath)
	if !ok || (p.Kind != pkgcomposer.KindMetadata && p.Kind != pkgcomposer.KindDist) {
		return PkgRef{}, false
	}

	return PkgRef{
		probe:     "p2/" + p.Name + ".json",
		altProbes: []string{"p2/" + p.Name + "~dev.json"},
		accept:    "application/json",
		file:      p.Kind == pkgcomposer.KindDist,
	}, true
}
