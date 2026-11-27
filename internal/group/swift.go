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
	"github.com/openartifact/openartifact/pkg/swift"
)

const (
	// swiftMetaMaxAge is how long shared caches may reuse a public group's
	// generated registry documents before revalidating.
	swiftMetaMaxAge = 60

	// swiftMaxManifest bounds a manifest read from one member.
	swiftMaxManifest = 4 << 20

	jsonContentType = "application/json"
)

// SwiftHooks is the swift group: merged release lists and identifiers, release
// documents and manifests of the first member that has them, archives first-hit.
// Every error answer is problem+json, like the swift repositories'.
var SwiftHooks = Hooks{ //nolint:gochecknoglobals // immutable format hook value
	ServeMerged:      (*Handler).serveSwift,
	PackageRef:       swiftRef,
	MethodNotAllowed: swiftReadOnly,
}

// swiftReadOnly answers a write to a group repository. Login is refused the
// same way: the protocol's 501 is not used, every write is a 405.
func swiftReadOnly(w http.ResponseWriter) {
	swift.WriteProblem(w, http.StatusMethodNotAllowed, "group repositories are read-only")
}

// swiftRef is the hosted-wins reference of a swift request: the package
// "scope/name", asked of hosted members for its release list.
func swiftRef(relPath string) (PkgRef, bool) {
	p, ok := swift.ParsePath(relPath)
	if !ok {
		return PkgRef{}, false
	}

	ref := PkgRef{probe: p.Scope + "/" + p.Name, accept: swift.MediaJSON}

	switch p.Kind {
	case swift.KindList, swift.KindRelease, swift.KindManifest:
		return ref, true
	case swift.KindArchive:
		ref.file = true
		return ref, true
	case swift.KindIdentifiers, swift.KindLogin:
	}

	return PkgRef{}, false
}

// serveSwift answers every request of a swift group. The group dispatch runs
// before the hosted handler, so the protocol negotiation is made here.
//
// The release list is the union of the members' lists by version, newest first,
// with the release URLs and the latest-version link of the group. The release
// document and the manifest come from the first member that has the release
// (hosted wins: unless the group merges proxy versions, a package a readable
// hosted member holds is served by the hosted members only, see
// applyHostedWins), with their links pointed at the group; a manifest request
// for a swift-version the first member has no manifest for answers 303 to the
// plain manifest, as that member would. The archive is the first member's, as
// it streams. Identifiers are the union of the members' answers.
func (h *Handler) serveSwift(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) bool {
	if !swift.CheckRequest(w, r) {
		return true
	}

	p, ok := swift.ParsePath(relPath)
	if !ok {
		swift.WriteProblem(w, http.StatusNotFound, "not found")
		return true
	}

	root := h.publicBase(w, r) + "/repos/swift/" + url.PathEscape(grp.Name)

	switch p.Kind {
	case swift.KindList:
		h.serveSwiftList(w, r, grp, root, p, relPath, members, be)
	case swift.KindRelease:
		h.serveSwiftRelease(w, r, grp, root, p, relPath, members, be)
	case swift.KindManifest:
		h.serveSwiftManifest(w, r, grp, root, p, relPath, members, be)
	case swift.KindArchive:
		served, failed := firstHit(w, r, members, func(*models.Repository) string { return relPath }, be)
		if !served {
			swiftNoDocument(w, failed)
		}
	case swift.KindIdentifiers:
		h.serveSwiftIdentifiers(w, r, grp, relPath, members, be)
	case swift.KindLogin:
		swiftReadOnly(w)
	}

	return true
}

// swiftNoDocument answers a request nothing could be served for: 404, or 502
// when a member failed on the way.
func swiftNoDocument(w http.ResponseWriter, partial bool) {
	if partial {
		swift.WriteProblem(w, http.StatusBadGateway, "member repository error")
		return
	}

	swift.WriteProblem(w, http.StatusNotFound, "not found")
}

// swiftAsk makes members answer the v1 JSON representation.
func swiftAsk(relPath string, limit int) memberAsk {
	return memberAsk{
		path:    samePath(relPath),
		limit:   limit,
		prepare: func(mr *http.Request) { mr.Header.Set("Accept", swift.MediaJSON) },
	}
}

func swiftReleaseURL(root string, p swift.Path, version string) string {
	return root + "/" + p.Scope + "/" + p.Name + "/" + version
}

// writeSwift answers a merged document with a content validator; partial (a
// member is missing from it) sends it uncacheable.
func writeSwift(w http.ResponseWriter, r *http.Request, grp *models.Repository, body []byte, contentType string, partial bool) {
	writeMerged(w, r, grp.Public, body, contentType, base.ContentETag(body), swiftMetaMaxAge, partial)
}

func (h *Handler) serveSwiftList(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	root string,
	p swift.Path,
	relPath string,
	members []*models.Repository,
	be Backend,
) {
	sess := h.newSession(r.Context())
	defer sess.release()

	c := h.collect(r, sess, members, swiftAsk(relPath, swift.MaxDocBytes), be)

	var versions []string

	for _, a := range c.docs {
		list, err := swift.ParseReleaseList(a.body)
		if err != nil {
			h.log.Warnw("group member served an unusable swift release list", "group_member", a.member.Name, "error", err)
			c.fail(a.member)

			continue
		}

		for v := range list.Releases {
			if !slices.Contains(versions, v) {
				versions = append(versions, v)
			}
		}
	}

	if len(versions) == 0 {
		swiftNoDocument(w, c.partial())
		return
	}

	swift.SortNewestFirst(versions)

	body := swift.ListBody(versions, func(v string) string { return swiftReleaseURL(root, p, v) })

	w.Header().Add("Link", swift.Link{URL: swiftReleaseURL(root, p, swift.Latest(versions)), Rel: swift.RelLatest}.String())
	writeSwift(w, r, grp, body, jsonContentType, c.partial())
}

// swiftFirst asks members in order for relPath and returns the capture of the
// first one whose answer decides the request: a 200 that usable accepts, or a
// 303 when redirects is set. A 404 passes to the next member; a failure, or a
// 200 that is not usable, passes too but is reported as failed.
func (h *Handler) swiftFirst(
	r *http.Request,
	sess *mergeSession,
	members []*models.Repository,
	relPath string,
	limit int,
	redirects bool,
	usable func(*captureWriter) bool,
	be Backend,
) (*captureWriter, bool) {
	mr := memberGetRequest(r)
	failed := false

	for _, m := range members {
		cw := sess.capture(limit)
		be.ServeMember(cw, mr, m, relPath)

		switch code := cw.code(); {
		case code == http.StatusNotFound:
		case code == http.StatusOK && !cw.overflow && usable(cw):
			return cw, failed
		case code == http.StatusSeeOther && redirects:
			return cw, failed
		default:
			h.log.Warnw("group member failed to serve a swift document", "group_member", m.Name, "path", relPath, "status", code)

			failed = true
		}
	}

	return nil, failed
}

func (h *Handler) serveSwiftRelease(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	root string,
	p swift.Path,
	relPath string,
	members []*models.Repository,
	be Backend,
) {
	sess := h.newSession(r.Context())
	defer sess.release()

	cw, failed := h.swiftFirst(r, sess, members, relPath, swift.MaxDocBytes, false, func(cw *captureWriter) bool {
		_, err := swift.ParseRelease(cw.body.Bytes(), swift.ID(p.Scope, p.Name), p.Version)

		return err == nil
	}, be)
	if cw == nil {
		swiftNoDocument(w, failed)
		return
	}

	for _, l := range swift.ParseLinks(cw.header.Values("Link")) {
		if l.Rel != swift.RelLatest && l.Rel != swift.RelSuccessor && l.Rel != swift.RelPredecessor {
			continue
		}

		if t, ok := swift.ParseTarget(l.URL, p.Scope, p.Name); ok && !t.Manifest {
			w.Header().Add("Link", swift.Link{URL: swiftReleaseURL(root, p, t.Version), Rel: l.Rel}.String())
		}
	}

	body := cw.body.Bytes()
	sess.release()
	writeSwift(w, r, grp, body, jsonContentType, failed)
}

func (h *Handler) serveSwiftManifest(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	root string,
	p swift.Path,
	relPath string,
	members []*models.Repository,
	be Backend,
) {
	sess := h.newSession(r.Context())
	defer sess.release()

	cw, failed := h.swiftFirst(r, sess, members, relPath, swiftMaxManifest, true, func(*captureWriter) bool { return true }, be)
	if cw == nil {
		swiftNoDocument(w, failed)
		return
	}

	manifestURL := swiftReleaseURL(root, p, p.Version) + "/" + swift.ManifestFile

	if cw.code() == http.StatusSeeOther {
		w.Header().Set("Location", manifestURL)
		w.WriteHeader(http.StatusSeeOther)

		return
	}

	hdr := w.Header()

	for _, l := range swift.ParseLinks(cw.header.Values("Link")) {
		if l.Rel != swift.RelAlternate {
			continue
		}

		t, ok := swift.ParseTarget(l.URL, p.Scope, p.Name)
		if !ok || !t.Manifest || t.SwiftVersion == "" {
			continue
		}

		l.URL = manifestURL + "?swift-version=" + url.QueryEscape(t.SwiftVersion)
		hdr.Add("Link", l.String())
	}

	if cd := cw.header.Get("Content-Disposition"); cd != "" {
		hdr.Set("Content-Disposition", cd)
	}

	body := cw.body.Bytes()
	sess.release()
	writeSwift(w, r, grp, body, swift.ContentTypeManifest, failed)
}

func (h *Handler) serveSwiftIdentifiers(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) {
	if raw := r.URL.Query().Get("url"); raw == "" || len(raw) > swift.MaxURLLen {
		swift.WriteProblem(w, http.StatusBadRequest, "url query parameter is missing or too long")
		return
	}

	sess := h.newSession(r.Context())
	defer sess.release()

	c := h.collect(r, sess, members, swiftAsk(relPath, swift.MaxDocBytes), be)

	var ids []string

	for _, a := range c.docs {
		var doc struct {
			Identifiers []string `json:"identifiers"`
		}

		if err := json.Unmarshal(a.body, &doc); err != nil {
			h.log.Warnw("group member served an unusable swift identifiers answer", "group_member", a.member.Name, "error", err)
			c.fail(a.member)

			continue
		}

		for _, id := range doc.Identifiers {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}

	if len(ids) == 0 {
		swiftNoDocument(w, c.partial())
		return
	}

	slices.Sort(ids)

	body, err := json.Marshal(map[string][]string{"identifiers": ids})
	if err != nil {
		h.log.Errorw("failed to encode swift identifiers", "group", grp.Name, "error", err)
		swift.WriteProblem(w, http.StatusInternalServerError, "internal error")

		return
	}

	writeSwift(w, r, grp, body, jsonContentType, c.partial())
}
