// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"

	"github.com/openartifact/openartifact/internal/database/models"
	pkgcargo "github.com/openartifact/openartifact/pkg/cargo"
)

const (
	// cargoIndexMaxAge is how long shared caches may reuse a public group's
	// index files before revalidating.
	cargoIndexMaxAge = 60

	// cargoConfigMaxAge is the same for the generated config.json.
	cargoConfigMaxAge = 300
)

// serveCargo answers config.json and index files of a cargo group and
// reports whether it handled the request; crate downloads fall through to the
// generic first-hit resolution, which picks the member whose index entry won
// the merge.
//
// config.json is generated (dl and api point at the group). An index file is
// the union of the members' files by version, the first member that lists a
// version providing its entry. Paths that are not registry paths are 404.
// Hosted wins: when a hosted member has the crate's index file (and the group
// does not merge proxy versions) only the hosted members are asked (see
// applyHostedWins), so the index lists only hosted versions and a download of
// any other version is 404.
func (h *Handler) serveCargo(w http.ResponseWriter, r *http.Request, grp *models.Repository, relPath string, members []*models.Repository, be Backend) bool {
	p, err := pkgcargo.ParsePath(relPath)
	if err != nil {
		http.NotFound(w, r)
		return true
	}

	switch p.Kind {
	case pkgcargo.KindConfig:
		h.serveCargoConfig(w, r, grp)
	case pkgcargo.KindIndex:
		h.serveCargoIndex(w, r, grp, p, members, be)
	case pkgcargo.KindDownload:
		return false
	}

	return true
}

func (h *Handler) serveCargoConfig(w http.ResponseWriter, r *http.Request, grp *models.Repository) {
	h.writeCargo(w, r, grp, pkgcargo.RenderConfig(h.baseURL, grp.Name, grp.Public), "application/json", cargoConfigMaxAge, false)
}

func (h *Handler) serveCargoIndex(w http.ResponseWriter, r *http.Request, grp *models.Repository, p pkgcargo.Path, members []*models.Repository, be Backend) {
	// Members are asked for the file itself, always with GET and without the
	// client's conditional headers.
	mr := r.Clone(r.Context())
	mr.Method = http.MethodGet
	mr.Header.Del("If-None-Match")
	mr.Header.Del("If-Modified-Since")

	var (
		docs   [][]byte
		failed bool
	)

	sess := h.newSession(r.Context())
	defer sess.release()

	for _, m := range members {
		cw := sess.capture(pkgcargo.MaxIndexBytes)
		be.ServeMember(cw, mr, m, "index/"+p.IndexPath)

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

		return
	}

	body, err := pkgcargo.MergeIndex(docs)

	sess.release()

	if err != nil {
		h.log.Errorw("failed to merge cargo index files", "group", grp.Name, "crate", p.Crate, "error", err)
		http.Error(w, "member repository error", http.StatusBadGateway)

		return
	}

	h.writeCargo(w, r, grp, body, "text/plain; charset=utf-8", cargoIndexMaxAge, failed)
}

// writeCargo answers a generated document with a content ETag, honoring
// If-None-Match; partial (a member failed while it was assembled) sends it
// uncacheable and without validator.
func (h *Handler) writeCargo(w http.ResponseWriter, r *http.Request, grp *models.Repository, body []byte, contentType string, maxAge int, partial bool) {
	sum := sha256.Sum256(body)

	if revalidateMerged(w, r, grp.Public, `"`+hex.EncodeToString(sum[:])+`"`, maxAge, partial) {
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
