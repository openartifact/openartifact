// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strconv"

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
func (h *Handler) servePyPI(w http.ResponseWriter, r *http.Request, grp *models.Repository, members []*models.Repository, relPath string, be Backend) bool {
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

// memberPages asks every member for the simple page at path and returns the
// bodies (with their content types) of those that have one. failed reports
// that a member errored out, in which case the result may be incomplete.
func (h *Handler) memberPages(r *http.Request, sess *mergeSession, members []*models.Repository, path string, be Backend) ([]memberPage, bool) {
	// Members are asked for the page itself, always with GET and without the
	// client's conditional headers.
	mr := r.Clone(r.Context())
	mr.Method = http.MethodGet
	mr.Header.Del("If-None-Match")
	mr.Header.Del("If-Modified-Since")
	mr.Header.Set("Accept", pypiMemberAccept)

	var (
		pages  []memberPage
		failed bool
	)

	for _, m := range members {
		cw := sess.capture(maxPyPIPageBytes)
		be.ServeMember(cw, mr, m, path)

		switch {
		case cw.status == http.StatusNotFound:
		case cw.status == http.StatusOK && !cw.overflow:
			pages = append(pages, memberPage{member: m, body: cw.body.Bytes(), contentType: cw.header.Get("Content-Type")})
		default:
			h.log.Warnw("group member failed to serve a simple page", "group_member", m.Name, "path", path, "status", cw.status)

			failed = true
		}
	}

	return pages, failed
}

type memberPage struct {
	member      *models.Repository
	body        []byte
	contentType string
}

func (h *Handler) servePyPIIndex(w http.ResponseWriter, r *http.Request, grp *models.Repository, members []*models.Repository, be Backend) {
	offer, ok := pypisimple.Negotiate(r.Header.Get("Accept"))
	if !ok {
		http.Error(w, "no acceptable simple API serialization", http.StatusNotAcceptable)
		return
	}

	// The pages are held whole until merged: one gate slot per request, taken
	// when a member starts delivering and freed before the answer is written.
	sess := h.newSession(r.Context())
	defer sess.release()

	pages, failed := h.memberPages(r, sess, members, "simple/", be)
	if len(pages) == 0 && failed {
		http.Error(w, "member repository error", http.StatusBadGateway)
		return
	}

	lists := make([][]string, 0, len(pages))

	for _, p := range pages {
		names, err := pypisimple.ParseIndex(p.body, p.contentType)
		if err != nil {
			h.log.Warnw("group member served an unparsable simple index", "group_member", p.member.Name, "error", err)

			failed = true

			continue
		}

		lists = append(lists, names)
	}

	if len(lists) == 0 && failed {
		http.Error(w, "member repository error", http.StatusBadGateway)
		return
	}

	names := pypisimple.MergeNames(lists...)
	slices.Sort(names)

	out, err := pypisimple.RenderIndex(offer.Format, names, func(name string) string {
		return pypisimple.ProjectURL(grp.Name, name)
	})

	sess.release()

	if err != nil {
		h.log.Errorw("failed to render group simple index", "group", grp.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	writePyPIPage(w, r, grp, offer, out, failed)
}

func (h *Handler) servePyPIProject(w http.ResponseWriter, r *http.Request, grp *models.Repository, members []*models.Repository, project string, be Backend) {
	offer, ok := pypisimple.Negotiate(r.Header.Get("Accept"))
	if !ok {
		http.Error(w, "no acceptable simple API serialization", http.StatusNotAcceptable)
		return
	}

	sess := h.newSession(r.Context())
	defer sess.release()

	pages, failed := h.memberPages(r, sess, members, "simple/"+project+"/", be)

	lists := make([][]pypisimple.File, 0, len(pages))

	for _, p := range pages {
		files, err := pypisimple.ParseProject(p.body, p.contentType)
		if err != nil {
			h.log.Warnw("group member served an unparsable simple page", "group_member", p.member.Name, "project", project, "error", err)

			failed = true

			continue
		}

		lists = append(lists, files)
	}

	if len(lists) == 0 {
		if failed {
			http.Error(w, "member repository error", http.StatusBadGateway)
			return
		}

		http.NotFound(w, r)

		return
	}

	merged := pypisimple.ServableFiles(pypisimple.Merge(lists...))

	out, err := pypisimple.RenderProject(offer.Format, project, merged, func(f *pypisimple.File) string {
		return pypisimple.FileURL(grp.Name, project, f.Filename)
	})

	sess.release()

	if err != nil {
		h.log.Errorw("failed to render group simple page", "group", grp.Name, "project", project, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	writePyPIPage(w, r, grp, offer, out, failed)
}

// writePyPIPage writes a rendered simple page with a content-derived ETag. A
// page assembled while a member was failing is incomplete: it must not be
// cached or revalidated into place.
func writePyPIPage(w http.ResponseWriter, r *http.Request, grp *models.Repository, offer pypisimple.Offer, body []byte, partial bool) {
	h := w.Header()
	h.Add("Vary", "Accept")

	sum := sha256.Sum256(append([]byte(offer.ContentType+"\x00"), body...))
	if revalidateMerged(w, r, grp.Public, `"`+hex.EncodeToString(sum[:16])+`"`, pypiPageMaxAge, partial) {
		return
	}

	h.Set("Content-Type", offer.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
