// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
)

// The group pipeline.
//
// A group answers a request in one of two ways, and a format only says which
// paths are which and supplies the merge:
//
//   - First hit (Handler.serveFirstHit): files, and every path a format does
//     not merge, come from the first member that has them, in member order.
//   - Merged documents (Handler.serveMergedDoc): a metadata document is the
//     union of the members' documents. The pipeline asks every readable member
//     for it (a plain GET behind a capture, under one bounded gate slot per
//     request), skips members that answer 404, counts a member that fails (5xx,
//     oversized, unreadable) as failed, hands the answers to the format's
//     merge hook (parse, first-member-wins by the format's merge key, rewrite
//     URLs back to the group), and answers with a content validator. A merge
//     that had to skip a failed member is partial: it goes out no-store and
//     without validator, so neither a client nor a shared cache keeps it once
//     the member recovers. With no document at all the group answers 404, or
//     502 when a member failed on the way.
//
// Hosted wins (dependency confusion) is applied before either: applyHostedWins
// narrows the members to the hosted ones for a package a hosted member holds,
// so no merge hook ever sees a proxy's entries of that name. Formats whose
// merge needs to look at the hosted and proxy documents separately (helm
// filters the proxy index by the hosted names) tell them apart by
// repotype.Proxy on answer.member.

// errMemberFailed is returned by a merge hook that cannot produce a complete
// answer because a member's document is missing or unreadable (fail closed):
// the group answers 502.
var errMemberFailed = errors.New("member repository error")

// errNoDocument is returned by a merge hook when none of the answers was
// usable (every document failed to parse): the group answers 404, or 502 when
// a member failed.
var errNoDocument = errors.New("no usable member document")

// answer is what asking one member for a document yielded.
type answer struct {
	member *models.Repository
	// body is the member's complete 200 answer; empty unless found.
	body        []byte
	contentType string
	found       bool // 200 with a complete body
	failed      bool // anything but 200 and 404
}

// memberAsk says what every member is asked for.
type memberAsk struct {
	// path is the path asked of member m.
	path func(m *models.Repository) string
	// limit bounds one member's answer held in memory; a bigger one counts as a
	// failed member.
	limit int
	// prepare adjusts the request members are asked with (memberGetRequest:
	// a plain GET without conditional or range headers), for example its
	// Accept header or query.
	prepare func(mr *http.Request)
	// first stops asking once that many members have answered (0: ask all).
	first int
}

// samePath is memberAsk.path for formats that ask every member for the same path.
func samePath(p string) func(*models.Repository) string {
	return func(*models.Repository) string { return p }
}

// memberGetRequest is the request members are asked with: a plain GET without
// the client's conditional or range headers.
func memberGetRequest(r *http.Request) *http.Request {
	mr := r.Clone(r.Context())
	mr.Method = http.MethodGet

	for _, hdr := range []string{"If-None-Match", "If-Modified-Since", "If-Match", "If-Unmodified-Since", "If-Range", "Range"} {
		mr.Header.Del(hdr)
	}

	return mr
}

// askMember requests path of member m behind a capture of the session and
// classifies the answer: 404 is "has no such document", a complete 200 is the
// document, everything else is a failed member.
func (h *Handler) askMember(sess *mergeSession, mr *http.Request, m *models.Repository, path string, limit int, be Backend) answer {
	cw := sess.capture(limit)
	be.ServeMember(cw, mr, m, path)

	switch {
	case cw.status == http.StatusNotFound:
		return answer{member: m}
	case cw.status == http.StatusOK && !cw.overflow:
		return answer{member: m, body: cw.body.Bytes(), contentType: cw.header.Get("Content-Type"), found: true}
	}

	h.log.Warnw("group member failed to serve a document", "group_member", m.Name, "path", path, "status", cw.status)

	return answer{member: m, failed: true}
}

// collection is what asking the members of a group for one document yielded.
type collection struct {
	// docs are the members' documents in member order.
	docs []answer
	// failed are the members that failed, plus those whose document the merge
	// found unusable (see fail).
	failed []*models.Repository
}

// partial reports that a member is missing from the merge.
func (c *collection) partial() bool { return len(c.failed) > 0 }

// fail records that m's document could not be used.
func (c *collection) fail(m *models.Repository) { c.failed = append(c.failed, m) }

// collect asks members in order for ask's document.
func (h *Handler) collect(r *http.Request, sess *mergeSession, members []*models.Repository, ask memberAsk, be Backend) collection {
	mr := memberGetRequest(r)
	if ask.prepare != nil {
		ask.prepare(mr)
	}

	var c collection

	for _, m := range members {
		if ask.first > 0 && len(c.docs) >= ask.first {
			break
		}

		a := h.askMember(sess, mr, m, ask.path(m), ask.limit, be)

		switch {
		case a.found:
			c.docs = append(c.docs, a)
		case a.failed:
			c.fail(m)
		}
	}

	return c
}

// writeNoDocument answers a merge that has nothing to serve: 404, or 502 when
// a member failed on the way (so a client does not cache "does not exist" for a
// document a broken member might have).
func writeNoDocument(w http.ResponseWriter, r *http.Request, partial bool) {
	if partial {
		http.Error(w, "member repository error", http.StatusBadGateway)
		return
	}

	http.NotFound(w, r)
}

// mergedDoc describes one merged metadata document of a group.
type mergedDoc struct {
	ask memberAsk
	// what names the document in log messages ("helm index").
	what string
	// maxAge is how long shared caches may reuse the merge of a public group.
	maxAge int
	// vary names the request header the rendering depends on (Accept), if any.
	vary string
	// contentType of the merged document.
	contentType string
	// allowEmpty merges even when no member has the document, as long as none
	// failed (a group with nothing to list still answers an empty index).
	allowEmpty bool
	// etag is the validator of the merged body; nil means base.ContentETag.
	etag func(body []byte) string
	// merge builds the merged document from the members' answers: parse, apply
	// the format's merge key (the first member that has an entry wins), and
	// rewrite URLs back to the group. It records unusable documents with
	// collection.fail, and may return errMemberFailed or errNoDocument.
	merge func(c *collection) ([]byte, error)
}

// serveMergedDoc answers a request for md with the union of the members'
// documents (see the package pipeline comment).
func (h *Handler) serveMergedDoc(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	members []*models.Repository,
	be Backend,
	md mergedDoc,
) {
	// The documents are held whole until merged: one gate slot per request,
	// taken when a member starts delivering and freed before the answer is
	// written.
	sess := h.newSession(r.Context())
	defer sess.release()

	c := h.collect(r, sess, members, md.ask, be)

	if len(c.docs) == 0 && (!md.allowEmpty || c.partial()) {
		writeNoDocument(w, r, c.partial())
		return
	}

	body, err := md.merge(&c)

	sess.release()

	switch {
	case errors.Is(err, errNoDocument):
		writeNoDocument(w, r, c.partial())
		return
	case errors.Is(err, errMemberFailed):
		http.Error(w, "member repository error", http.StatusBadGateway)
		return
	case err != nil:
		h.log.Errorw("failed to merge "+md.what, "group", grp.Name, "error", err)
		http.Error(w, "member repository error", http.StatusBadGateway)

		return
	}

	etag := base.ContentETag
	if md.etag != nil {
		etag = md.etag
	}

	if md.vary != "" {
		w.Header().Add("Vary", md.vary)
	}

	writeMerged(w, r, grp.Public, body, md.contentType, etag(body), md.maxAge, c.partial())
}

// writeMerged answers with a merged document and its validator; partial (a
// member is missing from it) sends it uncacheable and without validator.
func writeMerged(w http.ResponseWriter, r *http.Request, public bool, body []byte, contentType, etag string, maxAge int, partial bool) {
	if revalidateMerged(w, r, public, etag, maxAge, partial) {
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
