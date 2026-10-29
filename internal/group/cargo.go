// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"net/http"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	pkgcargo "github.com/openartifact/openartifact/pkg/cargo"
)

const (
	// cargoIndexMaxAge is how long shared caches may reuse a public group's
	// index files before revalidating.
	cargoIndexMaxAge = 60

	// cargoConfigMaxAge is the same for the generated config.json.
	cargoConfigMaxAge = 300
)

// CargoHooks is the cargo group: merged sparse index entries, crate downloads first-hit.
var CargoHooks = Hooks{ //nolint:gochecknoglobals // immutable format hook value
	ServeMerged: (*Handler).serveCargo,
	PackageRef:  cargoRef,
}

// serveCargo answers config.json and index files of a cargo group and
// reports whether it handled the request; crate downloads fall through to the
// generic first-hit resolution, which picks the member whose index entry won
// the merge.
//
// config.json is generated (dl and api point at the group). An index file is
// the union of the members' files by version, the first member that lists a
// version providing its entry. Paths that are not registry paths are 404.
// Hosted wins: when a hosted member has the crate's index file (and the group
// does not merge proxy versions) only the hosted members are asked (see
// applyHostedWins), so the index lists only hosted versions and a download of
// any other version is 404.
func (h *Handler) serveCargo(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) bool {
	p, err := pkgcargo.ParsePath(relPath)
	if err != nil {
		http.NotFound(w, r)
		return true
	}

	switch p.Kind {
	case pkgcargo.KindConfig:
		h.serveCargoConfig(w, r, grp)
	case pkgcargo.KindIndex:
		h.serveCargoIndex(w, r, grp, p, members, be)
	case pkgcargo.KindDownload:
		return false
	}

	return true
}

func (h *Handler) serveCargoConfig(w http.ResponseWriter, r *http.Request, grp *models.Repository) {
	// The body carries the origin the client reached, so its content ETag differs per origin.
	body := pkgcargo.RenderConfig(h.publicBase(w, r), grp.Name, grp.Public)
	writeMerged(w, r, grp.Public, body, "application/json", base.ContentETag(body), cargoConfigMaxAge, false)
}

func (h *Handler) serveCargoIndex(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	p pkgcargo.Path,
	members []*models.Repository,
	be Backend,
) {
	h.serveMergedDoc(w, r, grp, members, be, mergedDoc{
		ask:         memberAsk{path: samePath("index/" + p.IndexPath), limit: pkgcargo.MaxIndexBytes},
		what:        "cargo index files",
		maxAge:      cargoIndexMaxAge,
		contentType: "text/plain; charset=utf-8",
		merge: func(c *collection) ([]byte, error) {
			docs := make([][]byte, len(c.docs))
			for i, d := range c.docs {
				docs[i] = d.body
			}

			return pkgcargo.MergeIndex(docs) //nolint:wrapcheck // logged by serveMergedDoc
		},
	})
}

func cargoRef(relPath string) (PkgRef, bool) {
	p, err := pkgcargo.ParsePath(relPath)
	if err != nil {
		return PkgRef{}, false
	}

	switch p.Kind {
	case pkgcargo.KindIndex:
		return PkgRef{probe: "index/" + p.IndexPath}, true
	case pkgcargo.KindDownload:
		return PkgRef{probe: "index/" + pkgcargo.IndexPathFor(strings.ToLower(p.Crate)), file: true}, true
	case pkgcargo.KindConfig:
	}

	return PkgRef{}, false
}
