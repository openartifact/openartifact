// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/ocipath"
)

// maxMemberTags caps the tag listing read from one member.
const maxMemberTags = 16 << 20

// maxMemberTagPages bounds the pages followed for one member's tag listing.
const maxMemberTagPages = 1000

// ServeOCI answers a /v2/ request addressed to the docker group grp. req is the
// parsed path and relPath the part below the group's name ("<image>/manifests/
// <ref>"): the group is a read-only view, so only pulls are served. A manifest
// or blob comes from the first member that has it (members the caller cannot
// read behave as if they did not); a tag listing is the union of every readable
// member's tags. A blocked group answers 403 DENIED.
//
// Hosted wins (unless grp.MergeProxyVersions): an image that a readable hosted
// member holds (it answers <image>/tags/list with 200) is served from the
// hosted members only — its tag list is theirs alone and a manifest or blob
// they lack is unknown, never a proxy's. Images no hosted member holds are
// served by the proxies; if a hosted member could not say, the request is
// refused with UNAVAILABLE rather than risk serving the proxy's image.
func (h *Handler) ServeOCI(w http.ResponseWriter, r *http.Request, grp *models.Repository, req ocipath.Request, relPath string, be Backend) {
	if ocipath.RejectNonPull(w, r, req.Kind) {
		return
	}

	if grp.Blocked {
		ocipath.WriteError(w, http.StatusForbidden, "DENIED", "repository is blocked")
		return
	}

	members, err := h.members(r, grp)
	if err != nil {
		h.log.Errorw("failed to load group members", "group", grp.Name, "error", err)
		ocipath.WriteError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	readable := make([]*models.Repository, 0, len(members))

	for _, m := range members {
		if be.Authorize(r, m, relPath) {
			readable = append(readable, m)
		}
	}

	readable, done := h.applyHostedWinsOCI(w, r, grp, readable, req, relPath, be)
	if done {
		return
	}

	if req.Kind == ocipath.KindTags {
		h.serveTags(w, r, readable, req, relPath, be)
		return
	}

	served, memberFailed := firstHit(w, r, readable, func(*models.Repository) string { return relPath }, be)
	if served {
		return
	}

	if memberFailed {
		ocipath.WriteError(w, http.StatusBadGateway, "UNAVAILABLE", "member repository error")
		return
	}

	code, msg := ocipath.NotFoundCode(req.Kind)
	ocipath.WriteError(w, http.StatusNotFound, code, msg)
}

// serveTags merges the tags of every member that knows the image, paginating
// the union (n, last) the way a single registry would.
func (h *Handler) serveTags(w http.ResponseWriter, r *http.Request, members []*models.Repository, req ocipath.Request, relPath string, be Backend) {
	// Members are read in full; pagination applies to the merged list.
	// Always with GET: a HEAD to a member carries no body to merge, and the
	// group drops the body itself when answering HEAD.
	mr := r.Clone(r.Context())
	mr.Method = http.MethodGet
	mr.URL.RawQuery = ""

	var (
		tags   []string
		found  bool
		failed bool
	)

	for _, m := range members {
		mt, mfound, mfailed := h.memberTags(mr, m, relPath, be)
		failed = failed || mfailed

		if mfound {
			found = true
			tags = append(tags, mt...)
		}
	}

	if !found {
		if failed {
			ocipath.WriteError(w, http.StatusBadGateway, "UNAVAILABLE", "member repository error")
			return
		}

		ocipath.WriteError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")

		return
	}

	if failed {
		// A member erred or its listing could not be read to the end: this
		// union may be missing tags, so no cache may keep it.
		w.Header().Set("Cache-Control", "no-store")
	}

	slices.Sort(tags)
	tags = slices.Compact(tags)

	page, next, ok := paginateTags(tags, r.URL.Query().Get("last"), r.URL.Query().Get("n"))
	if !ok {
		ocipath.WriteError(w, http.StatusBadRequest, "PAGINATION_NUMBER_INVALID", "invalid n")
		return
	}

	if next != "" {
		q := url.Values{"last": {next}, "n": {strconv.Itoa(len(page))}}
		w.Header().Set("Link", "</v2/"+req.Name+"/tags/list?"+q.Encode()+`>; rel="next"`)
	}

	body, err := json.Marshal(map[string]any{"name": req.Name, "tags": page})
	if err != nil {
		ocipath.WriteError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// memberTags reads every tag of one member. A member that paginates by default
// (a proxy forwarding a registry's first page) answers with a rel="next" Link;
// it is followed until exhausted so the merged list is never silently cut. found
// reports that the member knows the image; failed that the list may be
// incomplete or the member erred.
func (h *Handler) memberTags(mr *http.Request, m *models.Repository, relPath string, be Backend) ([]string, bool, bool) {
	var (
		tags  []string
		found bool
	)

	pageReq := mr.Clone(mr.Context())
	budget := maxMemberTags
	last := ""

	for range maxMemberTagPages {
		rec := &recorder{header: http.Header{}, limit: budget}
		be.ServeMember(rec, pageReq, m, relPath)

		switch {
		case rec.status == http.StatusOK && !rec.overflow:
		case rec.status == http.StatusOK || rec.status >= http.StatusInternalServerError:
			return tags, found, true
		case found:
			return tags, found, true // a later page vanished
		default:
			return nil, false, false // unknown to this member or not readable
		}

		var doc struct {
			Tags []string `json:"tags"`
		}

		if json.Unmarshal(rec.body.Bytes(), &doc) != nil {
			return tags, found, true
		}

		found = true
		tags = append(tags, doc.Tags...)
		budget -= rec.body.Len()

		query, more := nextPageQuery(rec.header.Get("Link"))
		if !more {
			return tags, found, false
		}

		// A next page that does not move forward would loop forever.
		next := query.Get("last")
		if next == "" || next == last {
			return tags, found, true
		}

		last = next
		pageReq.URL.RawQuery = query.Encode()
	}

	h.log.Warnw("group member tag list has too many pages", "group_member", m.Name)

	return tags, found, true
}

var linkNextRE = regexp.MustCompile(`^<([^>]*)>\s*;\s*rel="?next"?`)

// nextPageQuery returns the n/last query of a rel="next" Link; more is false
// when the listing has no further page. A Link that cannot be followed comes
// back with more set and no "last", which the caller treats as a failure.
func nextPageQuery(link string) (url.Values, bool) {
	m := linkNextRE.FindStringSubmatch(strings.TrimSpace(link))
	if m == nil {
		return nil, false
	}

	u, err := url.Parse(m[1])
	if err != nil {
		return nil, true
	}

	q := u.Query()
	out := url.Values{}

	for _, k := range []string{"n", "last"} {
		if v := q.Get(k); v != "" {
			out.Set(k, v)
		}
	}

	return out, true
}

// paginateTags applies the registry pagination parameters to the sorted tags:
// only tags after last, at most n of them. next is the last tag of a page that
// has more after it.
func paginateTags(tags []string, last, nStr string) ([]string, string, bool) {
	if last != "" {
		i, _ := slices.BinarySearch(tags, last)
		if i < len(tags) && tags[i] == last {
			i++
		}

		tags = tags[i:]
	}

	if nStr == "" {
		return nonNil(tags), "", true
	}

	n, err := strconv.Atoi(nStr)
	if err != nil || n < 0 {
		return nil, "", false
	}

	switch {
	case n == 0:
		return []string{}, "", true
	case n >= len(tags):
		return nonNil(tags), "", true
	}

	return tags[:n], tags[n-1], true
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}

// recorder captures one member's response for merging.
type recorder struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	limit    int
	overflow bool
}

func (c *recorder) Header() http.Header { return c.header }

func (c *recorder) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
}

func (c *recorder) Write(b []byte) (int, error) {
	c.WriteHeader(http.StatusOK)

	if c.body.Len()+len(b) > c.limit {
		c.overflow = true
		return len(b), nil
	}

	return c.body.Write(b)
}
