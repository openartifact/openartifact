// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/ocipath"
)

// maxMemberTags caps the tag listing read from one member.
const maxMemberTags = 16 << 20

// ServeOCI answers a /v2/ request addressed to the docker group grp. req is the
// parsed path and relPath the part below the group's name ("<image>/manifests/
// <ref>"): the group is a read-only view, so only pulls are served. A manifest
// or blob comes from the first member that has it (members the caller cannot
// read behave as if they did not); a tag listing is the union of every readable
// member's tags.
func (h *Handler) ServeOCI(w http.ResponseWriter, r *http.Request, grp *models.Repository, req ocipath.Request, relPath string, be Backend) {
	if ocipath.RejectNonPull(w, r, req.Kind) {
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

	if req.Kind == ocipath.KindTags {
		h.serveTags(w, r, readable, req, relPath, be)
		return
	}

	memberFailed := false

	for _, m := range readable {
		pw := newProbeWriter(w)
		be.ServeMember(pw, r, m, relPath)

		switch {
		case pw.committed:
			return
		case pw.failed:
			memberFailed = true
		}
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
	mr := r.Clone(r.Context())
	mr.URL.RawQuery = ""

	var (
		tags   []string
		found  bool
		failed bool
	)

	for _, m := range members {
		rec := &recorder{header: http.Header{}, limit: maxMemberTags}
		be.ServeMember(rec, mr, m, relPath)

		switch {
		case rec.status == http.StatusOK && !rec.overflow:
			var doc struct {
				Tags []string `json:"tags"`
			}

			if json.Unmarshal(rec.body.Bytes(), &doc) != nil {
				failed = true
				continue
			}

			found = true
			tags = append(tags, doc.Tags...)
		case rec.status == http.StatusOK || rec.status >= http.StatusInternalServerError:
			failed = true
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
