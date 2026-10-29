// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/repotype"
	pkgconan "github.com/openartifact/openartifact/pkg/conan"
)

const (
	// maxMemberDocBytes bounds one member's search or revision document held in
	// memory.
	maxMemberDocBytes = 32 << 20

	// mergedDocMaxAge is how long shared caches may reuse a public group's merged
	// documents before revalidating; revision lists move whenever a member is
	// published to.
	mergedDocMaxAge = 60

	// maxConanProbeBytes bounds the search answer a hosted-wins probe reads.
	maxConanProbeBytes = 8 << 20
)

// serveConan answers the requests of a conan group that merge over its members
// and reports whether it handled the request; file lists and files fall through
// to the generic first-hit resolution.
//
// A reference is resolved from every member that has it: the revision lists
// (and "latest", which is the newest revision of them) of a recipe or package
// are the union of the members' lists, newest first, so a revision published to
// any member can be installed from the group. A revision's files come from the
// first member that has it, as its identifier names its content. Searches are
// the union of the members' results, and the package search of a recipe the
// union of the members' packages. Hosted wins: unless the group merges proxy
// versions, a package name that a readable hosted member holds is resolved from
// the hosted members only (see applyHostedWins); a search likewise drops the
// proxies' results of such names.
func (h *Handler) serveConan(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) bool {
	if p, ok := pkgconan.ParsePath(relPath); ok {
		return h.serveConanV2(w, r, grp, p, relPath, members, be)
	}

	p, ok := pkgconan.ParseV1Path(relPath)
	if !ok {
		return false
	}

	switch p.Kind {
	case pkgconan.V1Search:
		h.serveConanSearch(w, r, grp, relPath, members, be)
	case pkgconan.V1PackageSearch:
		h.serveConanPackages(w, r, grp, relPath, members, be)
	case pkgconan.V1Snapshot, pkgconan.V1PackageSnapshot:
		h.serveConanFirstMap(w, r, grp, relPath, members, be, false)
	case pkgconan.V1DownloadURLs, pkgconan.V1PackageDownloadURLs, pkgconan.V1Digest, pkgconan.V1PackageDigest:
		h.serveConanFirstMap(w, r, grp, relPath, members, be, true)
	case pkgconan.V1File, pkgconan.V1PackageFile, pkgconan.V1Other:
		return false
	}

	return true
}

func (h *Handler) serveConanV2(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	p pkgconan.Path,
	relPath string,
	members []*models.Repository,
	be Backend,
) bool {
	switch p.Kind {
	case pkgconan.KindSearch:
		h.serveConanSearch(w, r, grp, relPath, members, be)
	case pkgconan.KindRecipeLatest, pkgconan.KindPackageLatest, pkgconan.KindRecipeRevisions, pkgconan.KindPackageRevisions:
		h.serveConanRevisions(w, r, grp, p, relPath, members, be)
	case pkgconan.KindPackageSearch:
		h.serveConanPackages(w, r, grp, relPath, members, be)
	case pkgconan.KindFiles, pkgconan.KindFile, pkgconan.KindOther:
		return false
	}

	return true
}

// jsonAsk asks the members (only the first n of those that have it when first
// is n > 0) for the JSON document at path, with rawQuery as its query.
func jsonAsk(path, rawQuery string, first int) memberAsk {
	return memberAsk{
		path:  samePath(path),
		limit: maxMemberDocBytes,
		first: first,
		prepare: func(mr *http.Request) {
			mr.URL.RawQuery = rawQuery
			mr.Header.Set("Accept", "application/json")
		},
	}
}

// serveConanRevisions merges the revision lists, or the latest revision, of a
// recipe or package.
func (h *Handler) serveConanRevisions(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	p pkgconan.Path,
	relPath string,
	members []*models.Repository,
	be Backend,
) {
	latest := p.Kind == pkgconan.KindRecipeLatest || p.Kind == pkgconan.KindPackageLatest

	h.serveMergedDoc(w, r, grp, members, be, mergedDoc{
		ask:         jsonAsk(relPath, "", 0),
		what:        "conan revisions",
		maxAge:      mergedDocMaxAge,
		contentType: "application/json",
		merge: func(c *collection) ([]byte, error) {
			var lists [][]pkgconan.Revision

			for _, d := range c.docs {
				var (
					revs []pkgconan.Revision
					err  error
				)

				if latest {
					var rev pkgconan.Revision

					rev, err = pkgconan.ParseRevision(d.body)
					revs = []pkgconan.Revision{rev}
				} else {
					revs, err = pkgconan.ParseRevisions(d.body)
				}

				if err != nil {
					h.log.Warnw("member conan revision document unreadable", "group", grp.Name, "member", d.member.Name, "error", err)
					c.fail(d.member)

					continue
				}

				lists = append(lists, revs)
			}

			merged := pkgconan.MergeRevisions(lists...)
			switch {
			case len(merged) == 0:
				return nil, errNoDocument
			case latest:
				return pkgconan.RenderRevision(merged[0]), nil
			}

			return pkgconan.RenderRevisions(merged), nil
		},
	})
}

// serveConanPackages merges the package searches of a recipe (revision): the
// packages of every member, a package found by several taken from the first.
func (h *Handler) serveConanPackages(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) {
	h.serveMergedDoc(w, r, grp, members, be, mergedDoc{
		ask:         jsonAsk(relPath, r.URL.RawQuery, 0),
		what:        "conan package search",
		maxAge:      mergedDocMaxAge,
		contentType: "application/json",
		merge: func(c *collection) ([]byte, error) {
			parsed := make([]map[string]json.RawMessage, 0, len(c.docs))

			for _, d := range c.docs {
				pk, err := pkgconan.ParsePackageSearch(d.body)
				if err != nil {
					h.log.Warnw("member conan package search unreadable", "group", grp.Name, "member", d.member.Name, "error", err)
					c.fail(d.member)

					continue
				}

				parsed = append(parsed, pk)
			}

			if len(parsed) == 0 {
				return nil, errNoDocument
			}

			return pkgconan.MergePackageSearch(parsed...), nil
		},
	})
}

// serveConanFirstMap answers a Conan 1 map document (snapshot, download URLs,
// digest) from the first member that has it. The URLs a member names point at
// itself; rewriteURLs makes them point at the group, so the files are requested
// through it.
func (h *Handler) serveConanFirstMap(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
	rewriteURLs bool,
) {
	// The members answer with URLs under the origin this request reached (they
	// resolve it from the same request), which the rewrite replaces per request.
	var origin string
	if rewriteURLs {
		origin = h.publicBase(w, r)
	}

	h.serveMergedDoc(w, r, grp, members, be, mergedDoc{
		ask:         jsonAsk(relPath, "", 1),
		what:        "conan file map",
		maxAge:      mergedDocMaxAge,
		contentType: "application/json",
		merge: func(c *collection) ([]byte, error) {
			d := c.docs[0]

			m, err := pkgconan.ParseFileMap(d.body)
			if err != nil {
				h.log.Warnw("member conan document unreadable", "group", grp.Name, "member", d.member.Name, "error", err)
				c.fail(d.member)

				return nil, errNoDocument
			}

			if rewriteURLs {
				from := pkgconan.RepoURL(origin, d.member.Name) + "/v1/files/"
				to := pkgconan.RepoURL(origin, grp.Name) + "/v1/files/"

				for name, u := range m {
					if rest, ok := strings.CutPrefix(u, from); ok {
						m[name] = to + rest
					}
				}
			}

			return pkgconan.RenderFileMap(m), nil
		},
	})
}

// serveConanSearch merges the recipe searches of the members. An unreadable
// result or a failed member leaves the merge partial, and a search with no
// result at all is an empty list, not a 404. The merge does not fit
// serveMergedDoc because the hosted-wins probe of the hosted members reuses the
// request's one gate slot.
func (h *Handler) serveConanSearch(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) {
	sess := h.newSession(r.Context())
	defer sess.release()

	c := h.collect(r, sess, members, jsonAsk(relPath, r.URL.RawQuery, 0), be)

	type memberResults struct {
		member *models.Repository
		refs   []string
	}

	byMember := make([]memberResults, 0, len(c.docs))

	for _, d := range c.docs {
		found, err := pkgconan.ParseSearch(d.body)
		if err != nil {
			h.log.Warnw("member conan search unreadable", "group", grp.Name, "member", d.member.Name, "error", err)
			c.fail(d.member)

			continue
		}

		byMember = append(byMember, memberResults{member: d.member, refs: found})
	}

	hidden, ok := h.conanHiddenNames(r, sess, grp, c.docs, members, relPath, be)

	sess.release()

	if !ok {
		http.Error(w, "member repository error", http.StatusBadGateway)
		return
	}

	var results []string

	seen := make(map[string]struct{})

	for _, mr := range byMember {
		for _, ref := range mr.refs {
			if _, dup := seen[ref]; dup {
				continue
			}

			if _, hide := hidden[mr.member.ID][strings.ToLower(pkgconan.RefName(ref))]; hide {
				continue
			}

			seen[ref] = struct{}{}

			results = append(results, ref)
		}
	}

	body := pkgconan.RenderSearch(results)
	writeMerged(w, r, grp.Public, body, "application/json", base.ContentETag(body), mergedDocMaxAge, c.partial())
}

// conanHiddenNames applies hosted wins to a search: for every proxy member that
// answered, the lowercased names (of its results) a readable hosted member
// holds, whatever the query matched there. ok is false when a hosted member
// could not say, which fails the search closed. Nothing is hidden for a group
// that merges proxy versions or has no hosted or no proxy member.
func (h *Handler) conanHiddenNames(
	r *http.Request,
	sess *mergeSession,
	grp *models.Repository,
	docs []answer,
	members []*models.Repository,
	relPath string,
	be Backend,
) (map[string]map[string]struct{}, bool) {
	hosted, hasProxy := splitHosted(members)
	if grp.MergeProxyVersions || len(hosted) == 0 || !hasProxy {
		return nil, true
	}

	owned := make(map[string]struct{})

	// Every recipe of the hosted members, not just those the query matched: a
	// proxy's result of a name they hold under another version is hidden too.
	all := h.collect(r, sess, hosted, jsonAsk(relPath, "", 0), be)
	if all.partial() {
		return nil, false
	}

	for _, d := range all.docs {
		found, err := pkgconan.ParseSearch(d.body)
		if err != nil {
			return nil, false
		}

		for _, ref := range found {
			owned[strings.ToLower(pkgconan.RefName(ref))] = struct{}{}
		}
	}

	hidden := make(map[string]map[string]struct{}, len(docs))

	for _, d := range docs {
		if d.member.RepoType == repotype.Proxy {
			hidden[d.member.ID] = owned
		}
	}

	return hidden, true
}

// conanRef is the package a conan request names, for hosted wins: a hosted
// member holds it when its recipe search for the name (any version, user and
// channel) finds a recipe.
func conanRef(relPath string) (pkgRef, bool) {
	name := pkgconan.NameOf(relPath)
	if name == "" {
		return pkgRef{}, false
	}

	return pkgRef{
		probe:  pkgconan.SearchPath,
		query:  url.Values{"q": {name + "/*"}}.Encode(),
		accept: "application/json",
		holds: func(doc []byte) (bool, error) {
			found, err := pkgconan.ParseSearch(doc)
			if err != nil {
				return false, err //nolint:wrapcheck // logged by the caller
			}

			return slices.ContainsFunc(found, func(ref string) bool { return strings.EqualFold(pkgconan.RefName(ref), name) }), nil
		},
		file: conanFileRequest(relPath),
	}, true
}

// conanFileRequest reports whether relPath is a request for a revision's file
// list or one of its files, as opposed to package metadata.
func conanFileRequest(relPath string) bool {
	if p, ok := pkgconan.ParsePath(relPath); ok {
		return p.Kind == pkgconan.KindFiles || p.Kind == pkgconan.KindFile
	}

	p, ok := pkgconan.ParseV1Path(relPath)

	return ok && (p.Kind == pkgconan.V1File || p.Kind == pkgconan.V1PackageFile)
}
