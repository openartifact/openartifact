// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"net/http"
	"slices"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/pypisimple"
	"github.com/openartifact/openartifact/internal/repotype"
)

const (
	// maxPyPIPageBytes bounds one member's simple page held in memory (the
	// full pypi.org index is a few tens of MiB).
	maxPyPIPageBytes = 256 << 20

	// pypiPageMaxAge is how long shared caches may reuse a public group's
	// simple pages before revalidating.
	pypiPageMaxAge = 60

	// pypiMemberAccept asks members for PEP 691 JSON (proxy members honor
	// it; hosted members answer HTML).
	pypiMemberAccept = pypisimple.ContentTypeJSON + ", text/html;q=0.1"
)

// servePyPI answers the simple API and file downloads of a pypi group and
// reports whether it handled the request (other paths fall through to the
// generic first-hit resolution).
//
// Project pages are merged across all members: the union of their files by
// filename, the earliest member winning a duplicate, every link rewritten to
// the group. Files are resolved first-hit in member order, which picks the
// member whose page entry won the merge.
//
// Hosted wins: for a project a hosted member holds (and unless the group
// merges proxy versions) members is already narrowed to the hosted ones by
// applyHostedWins, so the page lists only hosted files and a proxy-only file of
// that project is 404. The index lists names only and is merged as is.
func (h *Handler) servePyPI(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	members []*models.Repository,
	relPath string,
	be Backend,
) bool {
	kind, project, filename := pypisimple.ParsePath(relPath)

	switch kind {
	case pypisimple.PathIndex:
		h.servePyPIIndex(w, r, grp, members, be)
	case pypisimple.PathProject:
		h.servePyPIProject(w, r, grp, members, project, be)
	case pypisimple.PathFile:
		h.serveFirstHitAs(w, r, members, func(m *models.Repository) string {
			return pypiFilePath(m, project, filename)
		}, be)
	case pypisimple.PathOther:
		return false
	}

	return true
}

// pypiFilePath is the path of a distribution file inside member m: proxy
// members key files by project, hosted members by filename.
func pypiFilePath(m *models.Repository, project, filename string) string {
	if m.RepoType == repotype.Proxy {
		return "files/" + project + "/" + filename
	}

	return "files/" + filename
}

// pypiAsk is what every member is asked for: the page itself, as PEP 691 JSON
// where it can.
func pypiAsk(path string) memberAsk {
	return memberAsk{
		path:  samePath(path),
		limit: maxPyPIPageBytes,
		prepare: func(mr *http.Request) {
			mr.Header.Set("Accept", pypiMemberAccept)
		},
	}
}

func (h *Handler) servePyPIIndex(w http.ResponseWriter, r *http.Request, grp *models.Repository, members []*models.Repository, be Backend) {
	offer, ok := pypisimple.Negotiate(r.Header.Get("Accept"))
	if !ok {
		http.Error(w, "no acceptable simple API serialization", http.StatusNotAcceptable)
		return
	}

	h.serveMergedDoc(w, r, grp, members, be, mergedDoc{
		ask:         pypiAsk("simple/"),
		what:        "simple index",
		maxAge:      pypiPageMaxAge,
		vary:        "Accept",
		contentType: offer.ContentType,
		// A group none of whose members has an index lists nothing.
		allowEmpty: true,
		merge: func(c *collection) ([]byte, error) {
			lists := make([][]string, 0, len(c.docs))

			for _, d := range c.docs {
				names, err := pypisimple.ParseIndex(d.body, d.contentType)
				if err != nil {
					h.log.Warnw("group member served an unparsable simple index", "group_member", d.member.Name, "error", err)
					c.fail(d.member)

					continue
				}

				lists = append(lists, names)
			}

			if len(lists) == 0 && c.partial() {
				return nil, errNoDocument
			}

			names := pypisimple.MergeNames(lists...)
			slices.Sort(names)

			return pypisimple.RenderIndex(offer.Format, names, func(name string) string { //nolint:wrapcheck // logged by serveMergedDoc
				return pypisimple.ProjectURL(grp.Name, name)
			})
		},
	})
}

func (h *Handler) servePyPIProject(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	members []*models.Repository,
	project string,
	be Backend,
) {
	offer, ok := pypisimple.Negotiate(r.Header.Get("Accept"))
	if !ok {
		http.Error(w, "no acceptable simple API serialization", http.StatusNotAcceptable)
		return
	}

	h.serveMergedDoc(w, r, grp, members, be, mergedDoc{
		ask:         pypiAsk("simple/" + project + "/"),
		what:        "simple page",
		maxAge:      pypiPageMaxAge,
		vary:        "Accept",
		contentType: offer.ContentType,
		merge: func(c *collection) ([]byte, error) {
			lists := make([][]pypisimple.File, 0, len(c.docs))

			for _, d := range c.docs {
				files, err := pypisimple.ParseProject(d.body, d.contentType)
				if err != nil {
					h.log.Warnw(
						"group member served an unparsable simple page",
						"group_member",
						d.member.Name,
						"project",
						project,
						"error",
						err,
					)
					c.fail(d.member)

					continue
				}

				lists = append(lists, files)
			}

			if len(lists) == 0 {
				return nil, errNoDocument
			}

			merged := pypisimple.ServableFiles(pypisimple.Merge(lists...))

			return pypisimple.RenderProject(
				offer.Format,
				project,
				merged,
				func(f *pypisimple.File) string { //nolint:wrapcheck // logged by serveMergedDoc
					return pypisimple.FileURL(grp.Name, project, f.Filename)
				},
			)
		},
	})
}
