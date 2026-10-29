// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/pathseg"
	pkgnuget "github.com/openartifact/openartifact/pkg/nuget"
)

// maxNuGetDocBytes bounds one member's metadata document held in memory.
const maxNuGetDocBytes = 128 << 20

// nugetMetaMaxAge is how long shared caches may reuse a public group's
// metadata before revalidating.
const nugetMetaMaxAge = 60

const nugetContentType = "application/json"

// serveNuGet answers the NuGet v3 metadata routes of a group and reports
// whether the path was one of them; package files (.nupkg, .nuspec) are left
// to the first-hit resolution of the caller.
//
//   - The service index is the group's own: registrations and flat container
//     of this group. Publishing and search are not offered; /v3/query and
//     /v3/autocomplete answer 404.
//   - Flat container version lists are united across the members.
//   - Registration indexes are merged: one item per version (the first member
//     that has it wins), ordered by version, in inline pages; members' pages
//     that are not inline are fetched from them.
//   - A registration leaf comes from the first member that has it.
//
// Every URL a member returns is rewritten to point at the group.
//
// Hosted wins: for an id a hosted member holds (and unless the group merges
// proxy versions) members is narrowed to the hosted ones by applyHostedWins
// before any of this runs, so registrations and version lists of that id carry
// no proxy versions and its package files are never fetched from a proxy.
func (h *Handler) serveNuGet(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) bool {
	segs := strings.Split(strings.Trim(relPath, "/"), "/")
	if len(segs) < 2 || segs[0] != "v3" {
		return false
	}

	// The members answer with URLs under the origin this request reached (they
	// resolve it the same way), so the rewrite below needs it per request.
	origin := h.publicBase(w, r)

	switch {
	case len(segs) == 2 && segs[1] == "index.json":
		h.serveNuGetIndex(w, r, grp, origin)
	case len(segs) == 2 && (segs[1] == "query" || segs[1] == "autocomplete"):
		http.NotFound(w, r)
	case len(segs) == 4 && segs[1] == "package" && segs[3] == "index.json" && nugetSegment(segs[2]):
		h.serveNuGetVersions(w, r, grp, members, be, origin, relPath)
	case len(segs) == 4 && segs[1] == "registration" && segs[3] == "index.json" && nugetSegment(segs[2]):
		h.serveNuGetRegistration(w, r, grp, members, be, origin, strings.ToLower(segs[2]))
	case len(segs) >= 4 && segs[1] == "registration" && nugetSegment(segs[2]):
		h.serveNuGetLeaf(w, r, grp, members, be, origin, relPath)
	default:
		return false
	}

	return true
}

// nugetSegment reports whether s is a plain id/version path segment.
func nugetSegment(s string) bool {
	return pathseg.Name(s)
}

// nugetPrepare makes the request members are asked with accept JSON.
func nugetPrepare(mr *http.Request) { mr.Header.Set("Accept", "application/json") }

// nugetAsk is what members are asked for the document at path; first stops
// asking once that many members have it (0: ask all).
func nugetAsk(path string, first int) memberAsk {
	return memberAsk{path: samePath(path), limit: maxNuGetDocBytes, prepare: nugetPrepare, first: first}
}

// nugetGroupDoc returns the document of answer a with every URL of its member
// rewritten to point at the group. ok is false when the document is unreadable.
func (h *Handler) nugetGroupDoc(grp *models.Repository, origin string, a answer, path string) ([]byte, bool) {
	body, err := pkgnuget.RewriteURLs(a.body, pkgnuget.URLPrefix{
		From: nugetV3(origin, a.member.Name),
		To:   nugetV3(origin, grp.Name),
	})
	if err != nil {
		h.log.Warnw("unreadable nuget document from group member", "group", grp.Name, "member", a.member.Name, "path", path, "error", err)
		return nil, false
	}

	return body, true
}

// nugetV3 is the root of repository name's v3 resources under origin.
func nugetV3(origin, name string) string { return origin + "/repos/" + name + "/v3/" }

func (h *Handler) serveNuGetIndex(w http.ResponseWriter, r *http.Request, grp *models.Repository, origin string) {
	idx := pkgnuget.ServiceIndex{
		PackageBase:   "x/",
		Registrations: "x/",
		// The merged registrations carry whatever the members have, semver 2.0
		// versions included.
		RegistrationTypes: []string{"RegistrationsBaseUrl", "RegistrationsBaseUrl/3.6.0", "RegistrationsBaseUrl/Versioned"},
	}

	body, err := idx.Render(origin + "/repos/" + grp.Name)
	if err != nil {
		h.log.Errorw("failed to render nuget group service index", "group", grp.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	writeMerged(w, r, grp.Public, body, nugetContentType, base.ContentETag(body), nugetMetaMaxAge, false)
}

// serveNuGetLeaf answers a registration leaf (or page) from the first member
// that has a readable one. It does not use serveMergedDoc: a member whose
// document is unreadable is skipped for the next one, which collect cannot do.
func (h *Handler) serveNuGetLeaf(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	members []*models.Repository,
	be Backend,
	origin, relPath string,
) {
	sess := h.newSession(r.Context())
	defer sess.release()

	mr := memberGetRequest(r)
	nugetPrepare(mr)

	var c collection

	for _, m := range members {
		a := h.askMember(sess, mr, m, relPath, maxNuGetDocBytes, be)
		if !a.found {
			if a.failed {
				c.fail(m)
			}

			continue
		}

		body, ok := h.nugetGroupDoc(grp, origin, a, relPath)
		if !ok {
			c.fail(m)
			continue
		}

		sess.release()
		writeMerged(w, r, grp.Public, body, nugetContentType, base.ContentETag(body), nugetMetaMaxAge, c.partial())

		return
	}

	writeNoDocument(w, r, c.partial())
}

func (h *Handler) serveNuGetVersions(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	members []*models.Repository,
	be Backend,
	origin, relPath string,
) {
	h.serveMergedDoc(w, r, grp, members, be, mergedDoc{
		ask:         nugetAsk(relPath, 0),
		what:        "nuget version lists",
		maxAge:      nugetMetaMaxAge,
		contentType: nugetContentType,
		merge: func(c *collection) ([]byte, error) {
			var lists [][]string

			for _, a := range c.docs {
				doc, ok := h.nugetGroupDoc(grp, origin, a, relPath)
				if !ok {
					c.fail(a.member)
					continue
				}

				// A member whose list cannot be read is left out, as one whose
				// registration cannot be, instead of failing the others' versions.
				list, err := pkgnuget.ParseVersionList(doc)
				if err != nil {
					h.log.Warnw(
						"unreadable nuget version list from group member",
						"group",
						grp.Name,
						"member",
						a.member.Name,
						"path",
						relPath,
						"error",
						err,
					)
					c.fail(a.member)

					continue
				}

				lists = append(lists, list)
			}

			if len(lists) == 0 {
				return nil, errNoDocument
			}

			return pkgnuget.MergeVersions(lists...) //nolint:wrapcheck // logged by serveMergedDoc
		},
	})
}

// serveNuGetRegistration answers the registration index of id: the union of
// the members' items. It does not use serveMergedDoc: the pages a member's
// index does not inline are fetched from it within the same session.
func (h *Handler) serveNuGetRegistration(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	members []*models.Repository,
	be Backend,
	origin, id string,
) {
	sess := h.newSession(r.Context())
	defer sess.release()

	c := h.collect(r, sess, members, nugetAsk("v3/registration/"+id+"/index.json", 0), be)

	mr := memberGetRequest(r)
	nugetPrepare(mr)

	var sets [][]json.RawMessage

	for _, a := range c.docs {
		items, ok := h.nugetMemberItems(sess, mr, grp, origin, a, id, be)
		if !ok {
			c.fail(a.member)
			continue
		}

		sets = append(sets, items)
	}

	if len(sets) == 0 {
		writeNoDocument(w, r, c.partial())
		return
	}

	indexURL := nugetV3(origin, grp.Name) + "registration/" + url.PathEscape(id) + "/index.json"

	body, err := pkgnuget.MergeRegistrationItems(indexURL, sets...)
	if err != nil {
		h.log.Errorw("failed to merge nuget registrations", "group", grp.Name, "package", id, "error", err)
		http.Error(w, "member repository error", http.StatusBadGateway)

		return
	}

	sess.release()

	writeMerged(w, r, grp.Public, body, nugetContentType, base.ContentETag(body), nugetMetaMaxAge, c.partial())
}

// nugetMemberItems returns every package item of the registration index a of
// id, fetching the pages it does not inline from a's member. ok is false when
// the registration could not be read whole.
func (h *Handler) nugetMemberItems(
	sess *mergeSession,
	mr *http.Request,
	grp *models.Repository,
	origin string,
	a answer,
	id string,
	be Backend,
) ([]json.RawMessage, bool) {
	m := a.member

	doc, ok := h.nugetGroupDoc(grp, origin, a, "v3/registration/"+id+"/index.json")
	if !ok {
		return nil, false
	}

	pages, err := pkgnuget.ParsePages(doc)
	if err != nil {
		h.log.Warnw("unreadable nuget registration from group member", "group", grp.Name, "member", m.Name, "package", id, "error", err)
		return nil, false
	}

	var items []json.RawMessage

	for _, p := range pages {
		if p.Inline {
			items = append(items, p.Items...)
			continue
		}

		if !nugetSegment(p.Lower) || !nugetSegment(p.Upper) {
			return nil, false
		}

		pagePath := "v3/registration/" + id + "/page/" + p.Lower + "/" + p.Upper + ".json"

		pa := h.askMember(sess, mr, m, pagePath, maxNuGetDocBytes, be)
		if !pa.found {
			return nil, false
		}

		pd, ok := h.nugetGroupDoc(grp, origin, pa, pagePath)
		if !ok {
			return nil, false
		}

		pageItems, perr := pkgnuget.ParsePageItems(pd)
		if perr != nil {
			h.log.Warnw(
				"unreadable nuget registration page from group member",
				"group",
				grp.Name,
				"member",
				m.Name,
				"package",
				id,
				"error",
				perr,
			)
			return nil, false
		}

		items = append(items, pageItems...)
	}

	return items, true
}
