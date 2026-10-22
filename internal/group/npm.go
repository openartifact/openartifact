// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
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
// member that has it, and every tarball URL points back at the group.
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
	// merges run at once.
	if h.packuments.Acquire(r.Context()) != nil {
		return true
	}
	defer h.packuments.Release()

	var (
		docs   [][]byte
		failed bool
	)

	for _, m := range members {
		cw := newCaptureWriter(maxPackumentBytes)
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

	body, err := mergePackuments(h.baseURL+"/repos/"+grp.Name, p, docs)
	if errors.Is(err, errNoVersion) {
		http.NotFound(w, r)
		return true
	}

	if err != nil {
		h.log.Errorw("failed to merge npm packuments", "group", grp.Name, "package", p.Package, "error", err)
		http.Error(w, "member repository error", http.StatusBadGateway)

		return true
	}

	sum := sha256.Sum256(body)

	w.Header().Add("Vary", "Accept")

	if revalidateMerged(w, r, grp.Public, `"`+hex.EncodeToString(sum[:])+`"`, packumentMaxAge, failed) {
		return true
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}

	return true
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
