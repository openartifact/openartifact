// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"bytes"
	"crypto/sha256"
	"net/http"
	"strconv"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/pkg/compactindex"
)

const (
	// maxRubyGemsVersionsBytes bounds one member's versions file held in
	// memory (rubygems.org's is a few tens of MiB).
	maxRubyGemsVersionsBytes = 256 << 20

	// rubygemsMaxAge is how long shared caches may reuse a public group's
	// compact index files before revalidating.
	rubygemsMaxAge = 60

	// maxSharedGems bounds how many gems of one versions merge are held by
	// several members: each needs its members' info files to compute the MD5 of
	// the merged one.
	maxSharedGems = 5000

	rubygemsContentType = "text/plain; charset=utf-8"
)

// serveRubyGems answers the compact index of a rubygems group and reports
// whether it handled the request; gem and gemspec downloads, and the paths a
// group does not generate, fall through to the generic first-hit resolution.
//
// Merge rules, the same for versions and info/<gem> so that the MD5 a versions
// line carries is the MD5 of the info file the group serves:
//   - hosted members all contribute; of the proxy members only the first one that
//     has the gem does (proxies mirror public registries, which overlap almost
//     entirely, and merging them would mean fetching the info file of every gem);
//   - a gem a hosted member holds is served by the hosted members alone unless the
//     group merges proxy versions (applyHostedWins narrows info/<gem> and the
//     downloads, this file narrows versions);
//   - a gem one member holds is served as that member serves it; one several
//     hold gets an info file that is the union of its versions by version field,
//     the earliest member winning a duplicate, and the versions line is derived from
//     that file;
//   - names is the union of the members' names.
//
// The legacy Marshal indexes (specs.4.8.gz and the like) are not merged: current
// bundler and gem read the compact index, so they are 404. A group with a single
// readable member passes every request through to it, ranges included.
func (h *Handler) serveRubyGems(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) bool {
	p, ok := compactindex.ParsePath(relPath)
	if !ok {
		return false
	}

	switch p.Kind {
	case compactindex.KindSpecs:
		http.NotFound(w, r)

		return true
	case compactindex.KindGem, compactindex.KindGemspec:
		return false
	case compactindex.KindVersions, compactindex.KindNames, compactindex.KindInfo:
	}

	if len(members) <= 1 {
		return false
	}

	switch p.Kind {
	case compactindex.KindVersions:
		h.serveRubyGemsVersions(w, r, grp, members, be)
	case compactindex.KindNames:
		h.serveRubyGemsNames(w, r, grp, members, be)
	case compactindex.KindInfo:
		h.serveRubyGemsInfo(w, r, grp, members, p.Gem, be)
	case compactindex.KindGem, compactindex.KindGemspec, compactindex.KindSpecs:
	}

	return true
}

// gemsDoc is one member's answer for a compact index document.
type gemsDoc struct {
	member *models.Repository
	body   []byte
}

// memberDocs asks the members for path and returns the bodies of those that have
// it. When proxyOnce is set only the first proxy member that has it counts: the
// members after it are not asked. failed reports that a member errored out, in
// which case the result may be incomplete.
func (h *Handler) memberDocs(
	r *http.Request,
	sess *mergeSession,
	members []*models.Repository,
	path string,
	limit int,
	proxyOnce bool,
	be Backend,
) ([]gemsDoc, bool) {
	mr := r.Clone(r.Context())
	mr.Method = http.MethodGet

	for _, hdr := range []string{"If-None-Match", "If-Modified-Since", "If-Match", "If-Unmodified-Since", "If-Range", "Range"} {
		mr.Header.Del(hdr)
	}

	var (
		docs       []gemsDoc
		failed     bool
		proxyTaken bool
	)

	for _, m := range members {
		if proxyOnce && proxyTaken && m.RepoType == repotype.Proxy {
			continue
		}

		cw := sess.capture(limit)
		be.ServeMember(cw, mr, m, path)

		switch {
		case cw.status == http.StatusNotFound:
		case cw.status == http.StatusOK && !cw.overflow:
			docs = append(docs, gemsDoc{member: m, body: cw.body.Bytes()})
			proxyTaken = proxyTaken || m.RepoType == repotype.Proxy
		default:
			h.log.Warnw("group member failed to serve a compact index file", "group_member", m.Name, "path", path, "status", cw.status)

			failed = true
		}
	}

	return docs, failed
}

// mergedInfo returns the info file the group serves for a gem its members'
// docs describe: the single document as is, several merged by version. failed
// reports a document that could not be read.
func (h *Handler) mergedInfo(docs []gemsDoc, gem string) ([]byte, bool) {
	if len(docs) == 1 {
		return docs[0].body, false
	}

	failed := false
	sources := make([][]compactindex.InfoLine, 0, len(docs))

	for _, d := range docs {
		lines, err := compactindex.ParseInfoBytes(d.body)
		if err != nil {
			h.log.Warnw("group member served an unparsable gem info file", "group_member", d.member.Name, "gem", gem, "error", err)

			failed = true

			continue
		}

		sources = append(sources, lines)
	}

	return compactindex.RenderInfo(compactindex.MergeInfo(sources...)), failed
}

func (h *Handler) serveRubyGemsInfo(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	members []*models.Repository,
	gem string,
	be Backend,
) {
	sess := h.newSession(r.Context())
	defer sess.release()

	docs, failed := h.memberDocs(r, sess, members, "info/"+gem, compactindex.MaxInfoBytes, true, be)
	if len(docs) == 0 {
		if failed {
			http.Error(w, "member repository error", http.StatusBadGateway)
			return
		}

		http.NotFound(w, r)

		return
	}

	body, unreadable := h.mergedInfo(docs, gem)

	sess.release()

	writeRubyGemsDoc(w, r, grp, body, failed || unreadable)
}

func (h *Handler) serveRubyGemsNames(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	members []*models.Repository,
	be Backend,
) {
	sess := h.newSession(r.Context())
	defer sess.release()

	docs, failed := h.memberDocs(r, sess, members, "names", maxRubyGemsVersionsBytes, false, be)
	if len(docs) == 0 && failed {
		http.Error(w, "member repository error", http.StatusBadGateway)
		return
	}

	var names []string

	for _, d := range docs {
		list, err := compactindex.ParseNames(bytes.NewReader(d.body))
		if err != nil {
			h.log.Warnw("group member served an unparsable names file", "group_member", d.member.Name, "error", err)

			failed = true

			continue
		}

		names = append(names, list...)
	}

	if len(names) == 0 && failed {
		http.Error(w, "member repository error", http.StatusBadGateway)
		return
	}

	sess.release()

	writeRubyGemsDoc(w, r, grp, compactindex.RenderNames(names), failed)
}

// versionsHolder is a member's versions entry for one gem.
type versionsHolder struct {
	member *models.Repository
	entry  compactindex.Entry
}

// versionsSource is a member's parsed versions file.
type versionsSource struct {
	member  *models.Repository
	entries map[string]compactindex.Entry
	created string
}

func (h *Handler) serveRubyGemsVersions(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	members []*models.Repository,
	be Backend,
) {
	sess := h.newSession(r.Context())
	defer sess.release()

	docs, failed := h.memberDocs(r, sess, members, "versions", maxRubyGemsVersionsBytes, false, be)

	var sources []versionsSource

	for _, d := range docs {
		v, err := compactindex.ParseVersions(bytes.NewReader(d.body))
		if err != nil {
			h.log.Warnw("group member served an unparsable versions file", "group_member", d.member.Name, "error", err)

			failed = true

			continue
		}

		src := versionsSource{member: d.member, entries: make(map[string]compactindex.Entry, len(v.Entries)), created: v.CreatedAt}
		for _, e := range v.Entries {
			if len(e.Versions) > 0 {
				src.entries[e.Name] = e
			}
		}

		sources = append(sources, src)
	}

	if len(sources) == 0 {
		if failed {
			http.Error(w, "member repository error", http.StatusBadGateway)
			return
		}

		http.NotFound(w, r)

		return
	}

	out, mergeFailed, ok := h.mergeVersions(r, sess, grp, sources, be)
	if !ok {
		http.Error(w, "member repository error", http.StatusBadGateway)
		return
	}

	sess.release()

	writeRubyGemsDoc(w, r, grp, out, failed || mergeFailed)
}

// mergeVersions combines the members' versions files under the rules of
// serveRubyGems. ok is false when too many gems are held by several members.
func (h *Handler) mergeVersions(
	r *http.Request,
	sess *mergeSession,
	grp *models.Repository,
	sources []versionsSource,
	be Backend,
) ([]byte, bool, bool) {
	names := make(map[string]struct{})
	hostedHas := make(map[string]struct{})

	var created string

	for _, s := range sources {
		created = max(created, s.created)

		for name := range s.entries {
			names[name] = struct{}{}

			if s.member.RepoType != repotype.Proxy {
				hostedHas[name] = struct{}{}
			}
		}
	}

	merged := &compactindex.Versions{CreatedAt: created}
	failed := false
	sharedGems := 0

	for name := range names {
		_, hosted := hostedHas[name]
		holders := holdersOf(sources, name, hosted && !grp.MergeProxyVersions)

		if len(holders) == 1 {
			merged.Entries = append(merged.Entries, holders[0].entry)
			continue
		}

		sharedGems++
		if sharedGems > maxSharedGems {
			h.log.Warnw("group versions merge has too many gems held by several members", "group", grp.Name, "limit", maxSharedGems)
			return nil, false, false
		}

		entry, ok := h.mergedEntry(r, sess, holders, name, be)
		if !ok {
			failed = true
		}

		merged.Entries = append(merged.Entries, entry)
	}

	return merged.Render(), failed, true
}

// holdersOf returns the members that contribute to the merge of gem name, in
// member order: every hosted member, and the first proxy unless hostedOnly.
func holdersOf(sources []versionsSource, name string, hostedOnly bool) []versionsHolder {
	var out []versionsHolder

	proxyTaken := false

	for _, s := range sources {
		e, ok := s.entries[name]
		if !ok {
			continue
		}

		if s.member.RepoType == repotype.Proxy {
			if hostedOnly || proxyTaken {
				continue
			}

			proxyTaken = true
		}

		out = append(out, versionsHolder{member: s.member, entry: e})
	}

	return out
}

// mergedEntry is the versions entry of a gem several members hold: its versions
// and MD5 are those of the info file the group serves for it (what
// serveRubyGemsInfo builds from the same members). ok is false when that file
// could not be built, in which case the first member's entry stands.
func (h *Handler) mergedEntry(
	r *http.Request,
	sess *mergeSession,
	holders []versionsHolder,
	name string,
	be Backend,
) (compactindex.Entry, bool) {
	members := make([]*models.Repository, len(holders))
	for i, hd := range holders {
		members[i] = hd.member
	}

	docs, failed := h.memberDocs(r, sess, members, "info/"+name, compactindex.MaxInfoBytes, true, be)
	if len(docs) == 0 {
		return holders[0].entry, false
	}

	body, unreadable := h.mergedInfo(docs, name)

	lines, err := compactindex.ParseInfoBytes(body)
	if err != nil || len(lines) == 0 {
		return holders[0].entry, false
	}

	versions := make([]string, len(lines))
	for i, l := range lines {
		versions[i] = l.Version
	}

	return compactindex.Entry{Name: name, Versions: versions, InfoMD5: compactindex.InfoMD5(body)}, !failed && !unreadable
}

// writeRubyGemsDoc writes a generated compact index file with the MD5 ETag and
// the SHA-256 digest headers rubygems.org sends. A partial document (a member
// failed while it was assembled) is sent uncacheable and without validator.
func writeRubyGemsDoc(w http.ResponseWriter, r *http.Request, grp *models.Repository, body []byte, partial bool) {
	if revalidateMerged(w, r, grp.Public, `"`+compactindex.InfoMD5(body)+`"`, rubygemsMaxAge, partial) {
		return
	}

	h := w.Header()

	if !partial {
		sum := sha256.Sum256(body)
		h.Set("Digest", compactindex.DigestValue(sum[:]))
		h.Set("Repr-Digest", compactindex.DigestValue(sum[:]))
	}

	h.Set("Content-Type", rubygemsContentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
