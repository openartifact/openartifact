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

// SetBaseURL sets the server base URL the merged packuments' tarball URLs
// point at (the same value hosted npm repositories use).
func (h *Handler) SetBaseURL(baseURL string) { h.baseURL = strings.TrimRight(baseURL, "/") }

// serveNPM answers packument and version-manifest requests by merging the
// members' packuments; it reports false for every other path (tarballs), which
// the caller resolves by first hit. Versions are the union of the members'
// (the first member with a version wins), each dist-tag comes from the first
// member that has it, and every tarball URL points back at the group. members
// are already narrowed by applyHostedWins: for a package a hosted member holds
// (and unless the group merges proxy versions) only the hosted members are
// left, so a proxy's versions of that name never reach the merged packument.
func (h *Handler) serveNPM(w http.ResponseWriter, r *http.Request, grp *models.Repository, relPath string, members []*models.Repository, be Backend) bool {
	p, err := pkgnpm.ParsePath(relPath)
	if err != nil || p.Kind == pkgnpm.KindTarball {
		return false
	}

	// Members are asked for the packument itself, always with GET and without
	// the client's conditional headers. A version manifest is cut from the
	// full packument.
	mr := r.Clone(r.Context())
	mr.Method = http.MethodGet
	mr.Header.Del("If-None-Match")
	mr.Header.Del("If-Modified-Since")

	abbreviated := p.Kind == pkgnpm.KindPackument && pkgnpm.WantsAbbreviated(r.Header.Get("Accept"))
	if p.Kind == pkgnpm.KindVersion || !abbreviated {
		mr.Header.Set("Accept", "application/json")
	}

	memberPath := strings.Replace(p.Package, "/", "%2f", 1)

	// Every member's packument is held whole until merged: bound how many
	// merges run at once. The slot is taken only once a member starts
	// delivering (not while it waits on its upstream) and freed before the
	// merged document is written to the client.
	sess := h.newSession(r.Context())
	defer sess.release()

	var (
		docs   [][]byte
		failed bool
	)

	for _, m := range members {
		cw := sess.capture(maxPackumentBytes)
		be.ServeMember(cw, mr, m, memberPath)

		switch {
		case cw.status == http.StatusNotFound:
		case cw.status == http.StatusOK && !cw.overflow:
			docs = append(docs, cw.body.Bytes())
		default:
			failed = true
		}
	}

	if len(docs) == 0 {
		if failed {
			http.Error(w, "member repository error", http.StatusBadGateway)
		} else {
			http.NotFound(w, r)
		}

		return true
	}

	// The merge is a pure function of the members' documents, so its result is
	// cached under a digest of them and serves as the validator: a poll that
	// changes nothing neither parses nor rewrites a thousand-version packument
	// again.
	tarballBase := h.baseURL + "/repos/" + grp.Name
	key := mergeKey(grp.ID, tarballBase, p, docs)

	rendered, err := base.MergedRendered(r.Context(), key, func(context.Context) (*base.Rendered, error) {
		body, err := mergePackuments(tarballBase, p, docs)
		if err != nil {
			return nil, err
		}

		return &base.Rendered{Body: body, ContentType: "application/json"}, nil
	})

	sess.release()

	if errors.Is(err, errNoVersion) {
		http.NotFound(w, r)
		return true
	}

	if err != nil {
		h.log.Errorw("failed to merge npm packuments", "group", grp.Name, "package", p.Package, "error", err)
		http.Error(w, "member repository error", http.StatusBadGateway)

		return true
	}

	w.Header().Add("Vary", "Accept")

	if revalidateMerged(w, r, grp.Public, `"`+key[:32]+`"`, packumentMaxAge, failed) {
		return true
	}

	rendered.Serve(w, r)

	return true
}

// mergeKey is the hex SHA-256 over everything a merged document is built from:
// the group, where its tarball URLs point, what was asked for, and the bytes of
// every member document in order.
func mergeKey(groupID, tarballBase string, p pkgnpm.Path, docs [][]byte) string {
	h := sha256.New()

	var n [8]byte

	field := func(b []byte) {
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}

	field([]byte(groupID))
	field([]byte(tarballBase))
	field([]byte(strconv.Itoa(int(p.Kind))))
	field([]byte(p.Package))
	field([]byte(p.Version))

	for _, d := range docs {
		field(d)
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
