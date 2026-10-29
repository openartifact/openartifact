// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"bytes"
	"crypto/sha256"
	"net/http"

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

// gemsETag is the validator of a compact index file: the MD5 of its body, as
// rubygems.org sends.
func gemsETag(body []byte) string {
	return `"` + compactindex.InfoMD5(body) + `"`
}

// setGemsDigest adds the SHA-256 digest headers rubygems.org sends to a
// complete compact index file.
func setGemsDigest(w http.ResponseWriter, body []byte) {
	sum := sha256.Sum256(body)
	w.Header().Set("Digest", compactindex.DigestValue(sum[:]))
	w.Header().Set("Repr-Digest", compactindex.DigestValue(sum[:]))
}

// writeGemsDoc writes a generated compact index file. A partial document (a
// member failed while it was assembled) is sent uncacheable, without validator
// and without digest.
func writeGemsDoc(w http.ResponseWriter, r *http.Request, grp *models.Repository, body []byte, partial bool) {
	if !partial {
		setGemsDigest(w, body)
	}

	writeMerged(w, r, grp.Public, body, rubygemsContentType, gemsETag(body), rubygemsMaxAge, partial)
}

// collectGems asks the members for the compact index file path. With proxyOnce
// only the first proxy member that has it counts: the proxy members after it
// are not asked.
func (h *Handler) collectGems(
	r *http.Request,
	sess *mergeSession,
	members []*models.Repository,
	path string,
	limit int,
	proxyOnce bool,
	be Backend,
) collection {
	ask := memberAsk{path: samePath(path), limit: limit}
	if !proxyOnce {
		return h.collect(r, sess, members, ask, be)
	}

	var (
		c          collection
		proxyTaken bool
	)

	for _, m := range members {
		isProxy := m.RepoType == repotype.Proxy
		if proxyTaken && isProxy {
			continue
		}

		one := h.collect(r, sess, []*models.Repository{m}, ask, be)
		c.docs = append(c.docs, one.docs...)
		c.failed = append(c.failed, one.failed...)
		proxyTaken = proxyTaken || (isProxy && len(one.docs) > 0)
	}

	return c
}

// mergedInfo returns the info file the group serves for a gem its members'
// docs describe: the single document as is, several merged by version. A
// document that cannot be read is recorded as failed.
func (h *Handler) mergedInfo(c *collection, gem string) []byte {
	if len(c.docs) == 1 {
		return c.docs[0].body
	}

	sources := make([][]compactindex.InfoLine, 0, len(c.docs))

	for _, d := range c.docs {
		lines, err := compactindex.ParseInfoBytes(d.body)
		if err != nil {
			h.log.Warnw("group member served an unparsable gem info file", "group_member", d.member.Name, "gem", gem, "error", err)
			c.fail(d.member)

			continue
		}

		sources = append(sources, lines)
	}

	return compactindex.RenderInfo(compactindex.MergeInfo(sources...))
}

// serveRubyGemsInfo answers info/<gem>. It does not use serveMergedDoc: only
// the first proxy that has the gem is asked (collectGems).
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

	c := h.collectGems(r, sess, members, "info/"+gem, compactindex.MaxInfoBytes, true, be)
	if len(c.docs) == 0 {
		writeNoDocument(w, r, c.partial())
		return
	}

	body := h.mergedInfo(&c, gem)

	sess.release()

	writeGemsDoc(w, r, grp, body, c.partial())
}

func (h *Handler) serveRubyGemsNames(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	members []*models.Repository,
	be Backend,
) {
	h.serveMergedDoc(w, r, grp, members, be, mergedDoc{
		ask:         memberAsk{path: samePath("names"), limit: maxRubyGemsVersionsBytes},
		what:        "rubygems names",
		maxAge:      rubygemsMaxAge,
		contentType: rubygemsContentType,
		allowEmpty:  true,
		etag:        gemsETag,
		merge: func(c *collection) ([]byte, error) {
			var names []string

			for _, d := range c.docs {
				list, err := compactindex.ParseNames(bytes.NewReader(d.body))
				if err != nil {
					h.log.Warnw("group member served an unparsable names file", "group_member", d.member.Name, "error", err)
					c.fail(d.member)

					continue
				}

				names = append(names, list...)
			}

			if len(names) == 0 && c.partial() {
				return nil, errMemberFailed
			}

			body := compactindex.RenderNames(names)
			if !c.partial() {
				setGemsDigest(w, body)
			}

			return body, nil
		},
	})
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

// serveRubyGemsVersions answers versions. It does not use serveMergedDoc: the
// merge fetches the info files of the gems several members hold, within the
// same session.
func (h *Handler) serveRubyGemsVersions(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	members []*models.Repository,
	be Backend,
) {
	sess := h.newSession(r.Context())
	defer sess.release()

	c := h.collectGems(r, sess, members, "versions", maxRubyGemsVersionsBytes, false, be)

	var sources []versionsSource

	for _, d := range c.docs {
		v, err := compactindex.ParseVersions(bytes.NewReader(d.body))
		if err != nil {
			h.log.Warnw("group member served an unparsable versions file", "group_member", d.member.Name, "error", err)
			c.fail(d.member)

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
		writeNoDocument(w, r, c.partial())
		return
	}

	out, mergeFailed, ok := h.mergeVersions(r, sess, grp, sources, be)
	if !ok {
		writeNoDocument(w, r, true)
		return
	}

	sess.release()

	writeGemsDoc(w, r, grp, out, c.partial() || mergeFailed)
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

	c := h.collectGems(r, sess, members, "info/"+name, compactindex.MaxInfoBytes, true, be)
	if len(c.docs) == 0 {
		return holders[0].entry, false
	}

	body := h.mergedInfo(&c, name)

	lines, err := compactindex.ParseInfoBytes(body)
	if err != nil || len(lines) == 0 {
		return holders[0].entry, false
	}

	versions := make([]string, len(lines))
	for i, l := range lines {
		versions[i] = l.Version
	}

	return compactindex.Entry{Name: name, Versions: versions, InfoMD5: compactindex.InfoMD5(body)}, !c.partial()
}
