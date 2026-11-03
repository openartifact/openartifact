// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/publicurl"
	pkgnpm "github.com/openartifact/openartifact/pkg/npm"
)

// maxPackumentBytes bounds one member's packument held in memory. Full
// packuments of very large packages reach tens of megabytes.
const maxPackumentBytes = 256 << 20

// packumentMaxAge is how long shared caches may reuse a public group's
// packument before revalidating (as for hosted npm repositories).
const packumentMaxAge = 60

// maxConcurrentMerges bounds the packument merges held in memory at once.
const maxConcurrentMerges = 8

// SetBaseURL sets BASE_URL, the explicit override of the origin the merged
// packuments' tarball URLs point at ("" derives it from each request).
func (h *Handler) SetBaseURL(baseURL string) { h.baseURL = strings.TrimRight(baseURL, "/") }

// publicBase is the origin of the absolute URLs a merged document hands to the
// client for r: BASE_URL when set, else the request's origin (see
// base.Handler.PublicBase). Call it before the response headers are written and
// fold the result into the document's validator.
func (h *Handler) publicBase(w http.ResponseWriter, r *http.Request) string {
	return publicurl.Resolve(w, r, h.baseURL)
}

// NPMHooks is the npm group: merged packuments, tarballs first-hit.
var NPMHooks = Hooks{ //nolint:gochecknoglobals // immutable format hook value
	ServeMerged: (*Handler).serveNPM,
	PackageRef:  npmRef,
}

// serveNPM answers packument and version-manifest requests by merging the
// members' packuments; it reports false for every other path (tarballs), which
// the caller resolves by first hit. Versions are the union of the members'
// (the first member with a version wins), each dist-tag comes from the first
// member that has it, and every tarball URL points back at the group. members
// are already narrowed by applyHostedWins: for a package a hosted member holds
// (and unless the group merges proxy versions) only the hosted members are
// left, so a proxy's versions of that name never reach the merged packument.
func (h *Handler) serveNPM(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) bool {
	p, err := pkgnpm.ParsePath(relPath)
	if err != nil || p.Kind == pkgnpm.KindTarball {
		return false
	}

	// Members are asked for the packument itself; a version manifest is cut
	// from the full packument.
	abbreviated := p.Kind == pkgnpm.KindPackument && pkgnpm.WantsAbbreviated(r.Header.Get("Accept"))
	ask := memberAsk{
		path:  samePath(strings.Replace(p.Package, "/", "%2f", 1)),
		limit: maxPackumentBytes,
		prepare: func(mr *http.Request) {
			if !abbreviated {
				mr.Header.Set("Accept", "application/json")
			}
		},
	}

	tarballBase := h.publicBase(w, r) + "/repos/" + grp.Name

	w.Header().Add("Vary", "Accept")

	// The merge is a pure function of the members' documents, which each
	// member names by a validator: the group's validator is derived from those
	// without reading or merging anything, so a poll that changes nothing is a
	// 304 (and a repeat of a merge seen before a cache hit) for the price of a
	// validator probe per member.
	stamp, validated := memberStamp(r, members, ask, be)
	if !validated {
		return h.mergeNPM(w, r, grp, p, members, ask, be, tarballBase, "")
	}

	key := stampKey(grp.ID, tarballBase, p, abbreviated, stamp)

	if base.Revalidate(w, r, grp.Public, keyETag(key), packumentMaxAge) {
		return true
	}

	if rendered, hit := base.CachedMerged(key); hit {
		rendered.Serve(w, r)
		return true
	}

	return h.mergeNPM(w, r, grp, p, members, ask, be, tarballBase, key)
}

// mergeNPM collects the members' packuments and merges them. vkey is the
// cache key and validator derived from the members' validators, already sent
// as the response's (see serveNPM); "" when the members gave none, and the
// merge is then identified by the documents themselves.
func (h *Handler) mergeNPM(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	p pkgnpm.Path,
	members []*models.Repository,
	ask memberAsk,
	be Backend,
	tarballBase, vkey string,
) bool {
	// Every member's packument is held whole until merged: bound how many
	// merges run at once. The slot is taken only once a member starts
	// delivering (not while it waits on its upstream) and freed before the
	// merged document is written to the client.
	sess := h.newSession(r.Context())
	defer sess.release()

	c := h.collect(r, sess, members, ask, be)
	if len(c.docs) == 0 {
		dropValidators(w)
		writeNoDocument(w, r, c.partial())

		return true
	}

	docs := make([][]byte, len(c.docs))
	for i, d := range c.docs {
		docs[i] = d.body
	}

	// Without the members' validators the merge, a pure function of their
	// documents, is cached under a digest of them, which then serves as the
	// validator: a poll that changes nothing neither parses nor rewrites a
	// thousand-version packument again, though every member has to deliver it.
	validated := vkey != ""

	key := vkey
	if !validated {
		key = mergeKey(grp.ID, tarballBase, p, docs)
	}

	rendered, err := base.MergedRendered(r.Context(), key, func(context.Context) (*base.Rendered, error) {
		body, err := mergePackuments(tarballBase, p, docs)
		if err != nil {
			return nil, err
		}

		// A merge that lacks a failed member must not stand in for the complete
		// one under the members' validators.
		return &base.Rendered{Body: body, ContentType: "application/json", Compact: true, Transient: validated && c.partial()}, nil
	})

	sess.release()

	if errors.Is(err, errNoVersion) {
		dropValidators(w)
		http.NotFound(w, r)

		return true
	}

	if err != nil {
		dropValidators(w)
		h.log.Errorw("failed to merge npm packuments", "group", grp.Name, "package", p.Package, "error", err)
		http.Error(w, "member repository error", http.StatusBadGateway)

		return true
	}

	if !validated && revalidateMerged(w, r, grp.Public, keyETag(key), packumentMaxAge, c.partial()) {
		return true
	}

	rendered.Serve(w, r)

	return true
}

// dropValidators takes the validators off a response that turned out to be an
// error, not the document they were set for.
func dropValidators(w http.ResponseWriter) {
	w.Header().Del("ETag")
	w.Header().Del("Cache-Control")
}

// keyETag is the entity-tag of the merge cached under key.
func keyETag(key string) string { return `"` + key[:32] + `"` }

// stampKey is the hex SHA-256 over what a merge is identified by when the
// members' validators name their documents: the group, the origin and
// repository its tarball URLs point at, what was asked for and the digest of the
// members' validators (memberStamp).
func stampKey(groupID, tarballBase string, p pkgnpm.Path, abbreviated bool, members string) string {
	return hashFields(
		[]byte("stamp"), []byte(groupID), []byte(tarballBase), []byte(strconv.Itoa(int(p.Kind))),
		[]byte(p.Package), []byte(p.Version), []byte(strconv.FormatBool(abbreviated)), []byte(members),
	)
}

// mergeKey is the hex SHA-256 over everything a merged document is built from:
// the group, the origin and repository its tarball URLs point at, what was asked for, and the bytes of
// every member document in order.
func mergeKey(groupID, tarballBase string, p pkgnpm.Path, docs [][]byte) string {
	fields := make([][]byte, 0, 6+len(docs))
	fields = append(fields, []byte("docs"), []byte(groupID), []byte(tarballBase), []byte(strconv.Itoa(int(p.Kind))))
	fields = append(fields, []byte(p.Package), []byte(p.Version))

	return hashFields(append(fields, docs...)...)
}

// hashFields is the hex SHA-256 of fields, each prefixed by its length so no two
// field lists hash alike.
func hashFields(fields ...[]byte) string {
	h := sha256.New()

	var n [8]byte

	for _, b := range fields {
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}

	return hex.EncodeToString(h.Sum(nil))
}

var errNoVersion = errors.New("version not found in merged packument")

// mergePackuments merges docs, points every tarball URL below tarballBase and,
// for a version path, cuts the version manifest out (errNoVersion when absent).
func mergePackuments(tarballBase string, p pkgnpm.Path, docs [][]byte) ([]byte, error) {
	merged, err := pkgnpm.MergePackuments(docs)
	if err != nil {
		return nil, err //nolint:wrapcheck // caller logs
	}

	merged, err = pkgnpm.RewriteTarballs(merged, p.Package, tarballBase)
	if err != nil {
		return nil, err //nolint:wrapcheck // caller logs
	}

	if p.Kind == pkgnpm.KindPackument {
		return merged, nil
	}

	manifest, found, err := pkgnpm.VersionManifest(merged, p.Version)
	if err != nil {
		return nil, err //nolint:wrapcheck // caller logs
	}

	if !found {
		return nil, errNoVersion
	}

	return manifest, nil
}

func npmRef(relPath string) (PkgRef, bool) {
	p, err := pkgnpm.ParsePath(relPath)
	if err != nil {
		return PkgRef{}, false
	}

	return PkgRef{
		probe:  strings.Replace(p.Package, "/", "%2f", 1),
		accept: "application/json",
		file:   p.Kind == pkgnpm.KindTarball,
	}, true
}
