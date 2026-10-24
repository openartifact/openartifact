// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/ocipath"
)

// maxListBody caps a tag or referrer listing read from an upstream.
const maxListBody = 16 << 20

var (
	lastRE     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	artifactRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+/-]{0,254}$`)
	linkNextRE = regexp.MustCompile(`^<([^>]*)>\s*;\s*rel="?next"?`)

	errListTooLarge = errors.New("upstream listing too large")
	errNotObject    = errors.New("upstream tag list is not a JSON object")
)

// serveOCITags passes tags/list through to the upstream, forwarding the
// pagination parameters. Nothing is cached: a listing is live data. The
// upstream's repository name and next-page Link are rewritten to the name the
// client used.
func (s *Service) serveOCITags(w http.ResponseWriter, r *http.Request, repo *models.Repository, req ocipath.Request, up ociUpstream) {
	q := url.Values{}

	if n := r.URL.Query().Get("n"); n != "" {
		if v, err := strconv.Atoi(n); err != nil || v < 0 {
			ocipath.WriteError(w, http.StatusBadRequest, "PAGINATION_NUMBER_INVALID", "invalid n")
			return
		}

		q.Set("n", n)
	}

	if last := r.URL.Query().Get("last"); last != "" {
		if !lastRE.MatchString(last) {
			ocipath.WriteError(w, http.StatusBadRequest, "PAGINATION_NUMBER_INVALID", "invalid last")
			return
		}

		q.Set("last", last)
	}

	target := up.base + "/v2/" + up.name + "/tags/list"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}

	hdr, body, release, ok := s.fetchList(w, r, repo, up, target)
	if !ok {
		return
	}

	defer release()

	out, err := rewriteTagList(body, req.Name)
	if err != nil {
		s.log.Warnw("docker proxy upstream tag list unreadable", "repo", repo.Name, "error", err)
		ocipath.WriteError(w, http.StatusBadGateway, "UNAVAILABLE", "upstream registry error")

		return
	}

	release()

	if next := rewriteNextLink(hdr.Get("Link"), req.Name); next != "" {
		w.Header().Set("Link", next)
	}

	writeList(w, r, "application/json", out)
}

// rewriteTagList names the repository of an upstream tag listing the way the
// client addressed it, leaving the rest of the document (the tags, whatever
// else the upstream sends) as it is.
func rewriteTagList(body []byte, name string) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode tag list: %w", err)
	}

	// null decodes into a nil map without error; assigning to it would panic.
	if doc == nil {
		return nil, errNotObject
	}

	quoted, err := json.Marshal(name)
	if err != nil {
		return nil, fmt.Errorf("encode name: %w", err)
	}

	doc["name"] = quoted

	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode tag list: %w", err)
	}

	return out, nil
}

// serveOCIReferrers passes the referrers listing of a digest through.
func (s *Service) serveOCIReferrers(w http.ResponseWriter, r *http.Request, repo *models.Repository, req ocipath.Request, up ociUpstream) {
	if !digestRE.MatchString(req.Ref) {
		ocipath.WriteError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest")
		return
	}

	target := up.base + "/v2/" + up.name + "/referrers/" + req.Ref

	if at := r.URL.Query().Get("artifactType"); at != "" {
		if !artifactRE.MatchString(at) {
			ocipath.WriteError(w, http.StatusBadRequest, "UNSUPPORTED", "invalid artifactType")
			return
		}

		target += "?" + url.Values{"artifactType": {at}}.Encode()
	}

	hdr, body, release, ok := s.fetchList(w, r, repo, up, target)
	if !ok {
		return
	}

	defer release()

	if f := hdr.Get("Oci-Filters-Applied"); f != "" {
		w.Header().Set("Oci-Filters-Applied", f)
	}

	// The listing is an image index; an upstream-chosen type is never echoed.
	ct := "application/vnd.oci.image.index.v1+json"
	if mt, ok := ocipath.SafeMediaType(hdr.Get("Content-Type")); ok {
		ct = mt
	}

	// The body is not parsed: nothing but the slot-free write remains.
	out := body
	release()

	writeList(w, r, ct, out)
}

// fetchList GETs a listing from the upstream and returns its headers and whole body, holding one of
// the bounded in-memory document slots until the returned release is called (idempotent; release is
// non-nil whenever ok). It writes the error response and returns ok=false when there is no usable
// listing.
func (s *Service) fetchList(w http.ResponseWriter, r *http.Request, repo *models.Repository, up ociUpstream, target string) (http.Header, []byte, func(), bool) {
	resp, err := s.ociDo(r.Context(), repo, up, http.MethodGet, target, "application/json")
	if err != nil {
		s.writeListError(w, err)
		return nil, nil, nil, false
	}

	defer func() { base.CloseLog(resp.Body, s.log) }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden:
		ocipath.WriteError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
		return nil, nil, nil, false
	default:
		s.log.Warnw("docker proxy upstream listing failed", "repo", repo.Name, "status", resp.StatusCode)
		ocipath.WriteError(w, http.StatusBadGateway, "UNAVAILABLE", "upstream registry error")

		return nil, nil, nil, false
	}

	if err := s.packuments.Acquire(r.Context()); err != nil {
		return nil, nil, nil, false // client went away; nothing to write
	}

	release := sync.OnceFunc(s.packuments.Release)

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxListBody+1))
	if err != nil || len(body) > maxListBody {
		release()

		s.log.Warnw("docker proxy upstream listing unreadable", "repo", repo.Name, "error", errors.Join(err, errListTooLarge))
		ocipath.WriteError(w, http.StatusBadGateway, "UNAVAILABLE", "upstream registry error")

		return nil, nil, nil, false
	}

	return resp.Header, body, release, true
}

func (s *Service) writeListError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}

	if errors.Is(err, errUpstream) {
		ocipath.WriteError(w, http.StatusBadGateway, "UNAVAILABLE", "upstream registry error")
		return
	}

	ocipath.WriteError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
}

func writeList(w http.ResponseWriter, r *http.Request, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// rewriteNextLink points the upstream's rel="next" Link at the listing the
// client is paging through ("" when there is no next page).
func rewriteNextLink(link, name string) string {
	m := linkNextRE.FindStringSubmatch(strings.TrimSpace(link))
	if m == nil {
		return ""
	}

	u, err := url.Parse(m[1])
	if err != nil || u.RawQuery == "" {
		return ""
	}

	return "</v2/" + name + "/tags/list?" + u.RawQuery + `>; rel="next"`
}
