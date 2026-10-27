// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/repotype"
	pkgconan "github.com/openartifact/openartifact/pkg/conan"
)

const (
	// maxConanDocBytes bounds one member's search or revision document held in
	// memory.
	maxConanDocBytes = 32 << 20

	// conanDocMaxAge is how long shared caches may reuse a public group's merged
	// documents before revalidating; revision lists move whenever a member is
	// published to.
	conanDocMaxAge = 60

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

// conanDoc is the answer of one member.
type conanDoc struct {
	member *models.Repository
	body   []byte
}

// collectConan asks every member for relPath (a GET without the client's
// conditional headers) and returns the documents of those that answered 200.
// failed reports that a member failed (a 5xx, an oversized answer), so the
// merged answer may lack what that member holds.
func (h *Handler) collectConan(
	r *http.Request,
	members []*models.Repository,
	relPath, rawQuery string,
	be Backend,
) ([]conanDoc, bool) {
	return h.collectConanN(r, members, relPath, rawQuery, 0, be)
}

// collectConanN is collectConan stopping at the first limit documents (all when
// limit is 0).
func (h *Handler) collectConanN(
	r *http.Request,
	members []*models.Repository,
	relPath, rawQuery string,
	limit int,
	be Backend,
) ([]conanDoc, bool) {
	var docs []conanDoc

	failed := false

	mr := r.Clone(r.Context())
	mr.Method = http.MethodGet
	mr.URL.RawQuery = rawQuery
	mr.Header.Set("Accept", "application/json")

	for _, hdr := range []string{"If-None-Match", "If-Modified-Since", "Range"} {
		mr.Header.Del(hdr)
	}

	sess := h.newSession(r.Context())
	defer sess.release()

	for _, m := range members {
		if limit > 0 && len(docs) >= limit {
			break
		}

		cw := sess.capture(maxConanDocBytes)
		be.ServeMember(cw, mr, m, relPath)

		switch {
		case cw.status == http.StatusNotFound:
		case cw.status == http.StatusOK && !cw.overflow:
			docs = append(docs, conanDoc{member: m, body: slices.Clone(cw.body.Bytes())})
		default:
			failed = true
		}
	}

	return docs, failed
}

// writeConanMerged answers a merged document, or the status of an empty merge:
// 404 when no member has it, 502 when one failed on the way.
func (h *Handler) writeConanMerged(w http.ResponseWriter, r *http.Request, grp *models.Repository, body []byte, failed bool) {
	if body == nil {
		if failed {
			http.Error(w, "member repository error", http.StatusBadGateway)
		} else {
			http.NotFound(w, r)
		}

		return
	}

	sum := sha256.Sum256(body)
	if revalidateMerged(w, r, grp.Public, `"`+hex.EncodeToString(sum[:])+`"`, conanDocMaxAge, failed) {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
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
	docs, failed := h.collectConan(r, members, relPath, "", be)

	var lists [][]pkgconan.Revision

	for _, d := range docs {
		var (
			revs []pkgconan.Revision
			err  error
		)

		if p.Kind == pkgconan.KindRecipeLatest || p.Kind == pkgconan.KindPackageLatest {
			var rev pkgconan.Revision

			rev, err = pkgconan.ParseRevision(d.body)
			revs = []pkgconan.Revision{rev}
		} else {
			revs, err = pkgconan.ParseRevisions(d.body)
		}

		if err != nil {
			h.log.Warnw("member conan revision document unreadable", "group", grp.Name, "member", d.member.Name, "error", err)

			failed = true

			continue
		}

		lists = append(lists, revs)
	}

	merged := pkgconan.MergeRevisions(lists...)
	if len(merged) == 0 {
		h.writeConanMerged(w, r, grp, nil, failed)
		return
	}

	if p.Kind == pkgconan.KindRecipeLatest || p.Kind == pkgconan.KindPackageLatest {
		h.writeConanMerged(w, r, grp, pkgconan.RenderRevision(merged[0]), failed)
		return
	}

	h.writeConanMerged(w, r, grp, pkgconan.RenderRevisions(merged), failed)
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
	docs, failed := h.collectConan(r, members, relPath, r.URL.RawQuery, be)

	parsed := make([]map[string]json.RawMessage, 0, len(docs))

	for _, d := range docs {
		pk, err := pkgconan.ParsePackageSearch(d.body)
		if err != nil {
			h.log.Warnw("member conan package search unreadable", "group", grp.Name, "member", d.member.Name, "error", err)

			failed = true

			continue
		}

		parsed = append(parsed, pk)
	}

	if len(parsed) == 0 {
		h.writeConanMerged(w, r, grp, nil, failed)
		return
	}

	h.writeConanMerged(w, r, grp, pkgconan.MergePackageSearch(parsed...), failed)
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
	docs, failed := h.collectConanN(r, members, relPath, "", 1, be)
	if len(docs) == 0 {
		h.writeConanMerged(w, r, grp, nil, failed)
		return
	}

	m, err := pkgconan.ParseFileMap(docs[0].body)
	if err != nil {
		h.log.Warnw("member conan document unreadable", "group", grp.Name, "member", docs[0].member.Name, "error", err)
		h.writeConanMerged(w, r, grp, nil, true)

		return
	}

	if rewriteURLs {
		from := pkgconan.RepoURL(h.baseURL, docs[0].member.Name) + "/v1/files/"
		to := pkgconan.RepoURL(h.baseURL, grp.Name) + "/v1/files/"

		for name, u := range m {
			if rest, ok := strings.CutPrefix(u, from); ok {
				m[name] = to + rest
			}
		}
	}

	h.writeConanMerged(w, r, grp, pkgconan.RenderFileMap(m), false)
}

// serveConanSearch merges the recipe searches of the members.
func (h *Handler) serveConanSearch(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) {
	docs, failed := h.collectConan(r, members, relPath, r.URL.RawQuery, be)

	var results []string

	byMember := make([][]string, 0, len(docs))

	for _, d := range docs {
		found, err := pkgconan.ParseSearch(d.body)
		if err != nil {
			h.log.Warnw("member conan search unreadable", "group", grp.Name, "member", d.member.Name, "error", err)

			failed = true

			continue
		}

		byMember = append(byMember, found)
	}

	hidden, ok := h.conanHiddenNames(r, grp, docs, members, relPath, be)
	if !ok {
		http.Error(w, "member repository error", http.StatusBadGateway)
		return
	}

	seen := make(map[string]struct{})

	for i, found := range byMember {
		for _, ref := range found {
			if _, dup := seen[ref]; dup {
				continue
			}

			if _, hide := hidden[docs[i].member.ID][strings.ToLower(pkgconan.RefName(ref))]; hide {
				continue
			}

			seen[ref] = struct{}{}

			results = append(results, ref)
		}
	}

	h.writeConanMerged(w, r, grp, pkgconan.RenderSearch(results), failed)
}

// conanHiddenNames applies hosted wins to a search: for every proxy member that
// answered, the lowercased names (of its results) a readable hosted member
// holds, whatever the query matched there. ok is false when a hosted member
// could not say, which fails the search closed. Nothing is hidden for a group
// that merges proxy versions or has no hosted or no proxy member.
func (h *Handler) conanHiddenNames(
	r *http.Request,
	grp *models.Repository,
	docs []conanDoc,
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
	all, failed := h.collectConan(r, hosted, relPath, "", be)
	if failed {
		return nil, false
	}

	for _, d := range all {
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
