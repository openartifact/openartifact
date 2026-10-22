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
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	pkgnuget "github.com/openartifact/openartifact/pkg/nuget"
)

// maxNuGetDocBytes bounds one member's metadata document held in memory.
const maxNuGetDocBytes = 128 << 20

// nugetMetaMaxAge is how long shared caches may reuse a public group's
// metadata before revalidating.
const nugetMetaMaxAge = 60

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
func (h *Handler) serveNuGet(w http.ResponseWriter, r *http.Request, grp *models.Repository, relPath string, members []*models.Repository, be Backend) bool {
	segs := strings.Split(strings.Trim(relPath, "/"), "/")
	if len(segs) < 2 || segs[0] != "v3" {
		return false
	}

	groupV3 := h.baseURL + "/repos/" + grp.Name + "/v3/"

	// Members are asked with GET and without the client's conditional headers.
	mr := r.Clone(r.Context())
	mr.Method = http.MethodGet
	mr.Header.Del("If-None-Match")
	mr.Header.Del("If-Modified-Since")
	mr.Header.Set("Accept", "application/json")

	// Member documents are held whole until merged: one gate slot per request,
	// taken when a member starts delivering and freed before the answer is
	// written.
	sess := h.newSession(r.Context())
	defer sess.release()

	nr := &nugetRequest{h: h, grp: grp, members: members, be: be, mr: mr, groupV3: groupV3, sess: sess}

	switch {
	case len(segs) == 2 && segs[1] == "index.json":
		nr.serveIndex(w, r)
	case len(segs) == 2 && (segs[1] == "query" || segs[1] == "autocomplete"):
		http.NotFound(w, r)
	case len(segs) == 4 && segs[1] == "package" && segs[3] == "index.json" && nugetSegment(segs[2]):
		nr.serveVersions(w, r, relPath)
	case len(segs) == 4 && segs[1] == "registration" && segs[3] == "index.json" && nugetSegment(segs[2]):
		nr.serveRegistration(w, r, strings.ToLower(segs[2]))
	case len(segs) >= 4 && segs[1] == "registration" && nugetSegment(segs[2]):
		nr.serveLeaf(w, r, relPath)
	default:
		return false
	}

	return true
}

// nugetSegment reports whether s is a plain id/version path segment.
func nugetSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00")
}

// nugetRequest is one NuGet metadata request of a group.
type nugetRequest struct {
	h       *Handler
	grp     *models.Repository
	members []*models.Repository
	be      Backend
	mr      *http.Request
	groupV3 string // ".../repos/<group>/v3/"
	sess    *mergeSession
}

// memberDoc is what asking one member for a path yielded.
type memberDoc struct {
	body   []byte
	found  bool // 200 with a complete body
	failed bool // anything but 200 and 404
}

// ask requests relPath from member m and returns its document with every
// URL of the member rewritten to point at the group.
func (n *nugetRequest) ask(m *models.Repository, relPath string) memberDoc {
	cw := n.sess.capture(maxNuGetDocBytes)
	n.be.ServeMember(cw, n.mr, m, relPath)

	switch {
	case cw.status == http.StatusNotFound:
		return memberDoc{}
	case cw.status != http.StatusOK || cw.overflow:
		return memberDoc{failed: true}
	}

	body, err := pkgnuget.RewriteURLs(cw.body.Bytes(), pkgnuget.URLPrefix{
		From: n.h.baseURL + "/repos/" + m.Name + "/v3/",
		To:   n.groupV3,
	})
	if err != nil {
		n.h.log.Warnw("unreadable nuget document from group member", "group", n.grp.Name, "member", m.Name, "path", relPath, "error", err)
		return memberDoc{failed: true}
	}

	return memberDoc{body: body, found: true}
}

// write answers with body (a validator on its digest; none, and no-store,
// when a member failed meanwhile) or, when there is none, with 404 or, if a
// member failed, 502.
func (n *nugetRequest) write(w http.ResponseWriter, r *http.Request, body []byte, failed bool) {
	// Everything held for the merge is in body by now.
	n.sess.release()

	if body == nil {
		if failed {
			http.Error(w, "member repository error", http.StatusBadGateway)
		} else {
			http.NotFound(w, r)
		}

		return
	}

	sum := sha256.Sum256(body)

	if revalidateMerged(w, r, n.grp.Public, `"`+hex.EncodeToString(sum[:])+`"`, nugetMetaMaxAge, failed) {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func (n *nugetRequest) serveIndex(w http.ResponseWriter, r *http.Request) {
	idx := pkgnuget.ServiceIndex{
		PackageBase:   "x/",
		Registrations: "x/",
		// The merged registrations carry whatever the members have, semver 2.0
		// versions included.
		RegistrationTypes: []string{"RegistrationsBaseUrl", "RegistrationsBaseUrl/3.6.0", "RegistrationsBaseUrl/Versioned"},
	}

	body, err := idx.Render(strings.TrimSuffix(n.groupV3, "/v3/"))
	if err != nil {
		n.h.log.Errorw("failed to render nuget group service index", "group", n.grp.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	n.write(w, r, body, false)
}

// serveLeaf answers a registration leaf (or page) from the first member that
// has it.
func (n *nugetRequest) serveLeaf(w http.ResponseWriter, r *http.Request, relPath string) {
	failed := false

	for _, m := range n.members {
		d := n.ask(m, relPath)
		if d.found {
			n.write(w, r, d.body, failed)
			return
		}

		failed = failed || d.failed
	}

	n.write(w, r, nil, failed)
}

func (n *nugetRequest) serveVersions(w http.ResponseWriter, r *http.Request, relPath string) {
	var (
		docs   [][]byte
		failed bool
	)

	for _, m := range n.members {
		d := n.ask(m, relPath)
		if d.found {
			docs = append(docs, d.body)
		}

		failed = failed || d.failed
	}

	if len(docs) == 0 {
		n.write(w, r, nil, failed)
		return
	}

	body, err := pkgnuget.MergeVersionLists(docs...)
	if err != nil {
		n.h.log.Errorw("failed to merge nuget version lists", "group", n.grp.Name, "error", err)
		http.Error(w, "member repository error", http.StatusBadGateway)

		return
	}

	n.write(w, r, body, failed)
}

func (n *nugetRequest) serveRegistration(w http.ResponseWriter, r *http.Request, id string) {
	var (
		sets   [][]json.RawMessage
		failed bool
	)

	for _, m := range n.members {
		items, ok, memberFailed := n.memberItems(m, id)
		if ok {
			sets = append(sets, items)
		}

		failed = failed || memberFailed
	}

	if len(sets) == 0 {
		n.write(w, r, nil, failed)
		return
	}

	indexURL := n.groupV3 + "registration/" + url.PathEscape(id) + "/index.json"

	body, err := pkgnuget.MergeRegistrationItems(indexURL, sets...)
	if err != nil {
		n.h.log.Errorw("failed to merge nuget registrations", "group", n.grp.Name, "package", id, "error", err)
		http.Error(w, "member repository error", http.StatusBadGateway)

		return
	}

	n.write(w, r, body, failed)
}

// memberItems returns every package item of member m's registration of id,
// fetching the pages its index does not inline. ok is false when m has no
// registration or could not deliver it whole; failed when that is an error.
func (n *nugetRequest) memberItems(m *models.Repository, id string) ([]json.RawMessage, bool, bool) {
	d := n.ask(m, "v3/registration/"+id+"/index.json")
	if !d.found {
		return nil, false, d.failed
	}

	pages, err := pkgnuget.ParsePages(d.body)
	if err != nil {
		n.h.log.Warnw("unreadable nuget registration from group member", "group", n.grp.Name, "member", m.Name, "package", id, "error", err)
		return nil, false, true
	}

	var items []json.RawMessage

	for _, p := range pages {
		if p.Inline {
			items = append(items, p.Items...)
			continue
		}

		if !nugetSegment(p.Lower) || !nugetSegment(p.Upper) {
			return nil, false, true
		}

		pd := n.ask(m, "v3/registration/"+id+"/page/"+p.Lower+"/"+p.Upper+".json")
		if !pd.found {
			return nil, false, true
		}

		pageItems, perr := pkgnuget.ParsePageItems(pd.body)
		if perr != nil {
			n.h.log.Warnw("unreadable nuget registration page from group member", "group", n.grp.Name, "member", m.Name, "package", id, "error", perr)
			return nil, false, true
		}

		items = append(items, pageItems...)
	}

	return items, true, false
}
