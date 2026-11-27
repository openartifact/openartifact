// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"net/http"
	"net/url"

	"github.com/openartifact/openartifact/internal/database/models"
	pkgpub "github.com/openartifact/openartifact/pkg/pub"
)

// pubMaxAge is how long shared caches may reuse a public group's merged
// package documents before revalidating.
const pubMaxAge = 60

// PubHooks is the pub group: merged package documents, archive downloads first-hit.
var PubHooks = Hooks{ //nolint:gochecknoglobals // immutable format hook value
	ServeMerged: (*Handler).servePub,
	PackageRef:  pubRef,
}

// servePub answers the package documents of a pub group and reports whether it
// handled the request; archive downloads fall through to the generic first-hit
// resolution.
//
// The package document is the union of the members' documents by version, the
// first member that lists a version providing its entry, ordered by version,
// with "latest" recomputed over the union. A version document is that entry
// alone. Every archive_url the group hands out is the group's own, and a
// download asks the members in order, so the member whose entry won the merge
// serves the archive. Paths that are not read paths of the API (publishing)
// are 404: a group is read-only.
//
// Hosted wins: unless the group merges proxy versions, a package a readable
// hosted member holds is served by the hosted members only (see
// applyHostedWins), so its document and archives never contain a proxy's.
func (h *Handler) servePub(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) bool {
	p, ok := pkgpub.ParsePath(relPath)
	if !ok {
		http.NotFound(w, r)
		return true
	}

	switch p.Kind {
	case pkgpub.KindPackage, pkgpub.KindVersion:
		h.servePubDoc(w, r, grp, p, members, be)
	case pkgpub.KindArchive:
		return false
	case pkgpub.KindNew, pkgpub.KindUpload, pkgpub.KindFinish:
		http.NotFound(w, r)
	}

	return true
}

func (h *Handler) servePubDoc(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	p pkgpub.Path,
	members []*models.Repository,
	be Backend,
) {
	// archive_url is absolute: the body, and with it its content ETag, holds the
	// origin the client reached.
	root := h.publicBase(w, r) + "/repos/pub/" + url.PathEscape(grp.Name)
	link := func(v *pkgpub.Version) string { return root + "/" + pkgpub.ArchivePath(p.Name, v.Version) }

	h.serveMergedDoc(w, r, grp, members, be, mergedDoc{
		ask: memberAsk{
			path:    samePath(pkgpub.PackagePath(p.Name)),
			limit:   pkgpub.MaxDocBytes,
			prepare: func(mr *http.Request) { mr.Header.Set("Accept", pkgpub.ContentType) },
		},
		what:        "pub package documents",
		maxAge:      pubMaxAge,
		contentType: pkgpub.ContentType,
		merge: func(c *collection) ([]byte, error) {
			docs := make([]*pkgpub.Doc, 0, len(c.docs))

			for _, d := range c.docs {
				doc, err := pkgpub.ParseDoc(d.body)
				if err != nil || doc.Name != p.Name {
					c.fail(d.member)
					continue
				}

				docs = append(docs, doc)
			}

			merged := pkgpub.Merge(docs)
			if merged == nil {
				return nil, errNoDocument
			}

			if p.Kind == pkgpub.KindVersion {
				v := merged.Find(p.Version)
				if v == nil {
					return nil, errNoDocument
				}

				return pkgpub.MarshalVersion(v, link) //nolint:wrapcheck // logged by serveMergedDoc
			}

			return merged.Marshal(link, nil) //nolint:wrapcheck // logged by serveMergedDoc
		},
	})
}

// pubRef is the package a pub request names: the document probe is the
// package document, and an archive is a file of the package its name says.
func pubRef(relPath string) (PkgRef, bool) {
	p, ok := pkgpub.ParsePath(relPath)
	if !ok {
		return PkgRef{}, false
	}

	switch p.Kind {
	case pkgpub.KindPackage, pkgpub.KindVersion:
		return PkgRef{probe: pkgpub.PackagePath(p.Name)}, true
	case pkgpub.KindArchive:
		return PkgRef{probe: pkgpub.PackagePath(p.Name), file: true}, true
	case pkgpub.KindNew, pkgpub.KindUpload, pkgpub.KindFinish:
	}

	return PkgRef{}, false
}
