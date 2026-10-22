// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	pkghelm "github.com/openartifact/openartifact/pkg/helm"
)

const (
	// maxHelmIndexBytes bounds one member's index.yaml held in memory; large
	// public chart repositories publish indexes of tens of megabytes.
	maxHelmIndexBytes = 256 << 20

	// helmIndexMaxAge is how long shared caches may reuse a public group's
	// index.yaml before revalidating (as for hosted helm repositories).
	helmIndexMaxAge = 60

	helmChartsDir = "charts/"
)

// serveHelm answers index.yaml by merging the members' indexes: a chart
// version is listed once, from the first member that has it (name and version
// decide), and every chart URL points back at the group. It reports false for
// chart downloads (charts/<file>, and their .prov files), which the caller
// resolves by first hit, and answers 404 for everything else, so that a hosted
// member's chart listing (GET /charts) is not exposed through the group.
func (h *Handler) serveHelm(w http.ResponseWriter, r *http.Request, grp *models.Repository, relPath string, members []*models.Repository, be Backend) bool {
	rel := strings.TrimPrefix(relPath, "/")

	if file, ok := strings.CutPrefix(rel, helmChartsDir); ok && file != "" && !strings.Contains(file, "/") {
		return false
	}

	if rel != "index.yaml" {
		http.NotFound(w, r)
		return true
	}

	// Members are asked for the index itself, always with GET and without the
	// client's conditional headers.
	mr := r.Clone(r.Context())
	mr.Method = http.MethodGet
	mr.Header.Del("If-None-Match")
	mr.Header.Del("If-Modified-Since")

	var (
		indexes []*pkghelm.Index
		failed  bool
	)

	for _, m := range members {
		cw := newCaptureWriter(maxHelmIndexBytes)
		be.ServeMember(cw, mr, m, "index.yaml")

		switch {
		case cw.status == http.StatusNotFound:
		case cw.status == http.StatusOK && !cw.overflow:
			ix, err := pkghelm.ParseIndex(cw.body.Bytes())
			if err != nil {
				h.log.Warnw("member helm index unreadable", "group", grp.Name, "member", m.Name, "error", err)

				failed = true

				continue
			}

			indexes = append(indexes, ix)
		default:
			failed = true
		}
	}

	if len(indexes) == 0 {
		if failed {
			http.Error(w, "member repository error", http.StatusBadGateway)
		} else {
			http.NotFound(w, r)
		}

		return true
	}

	body, err := mergeHelmIndexes(h.baseURL+"/repos/"+grp.Name+"/"+helmChartsDir, indexes)
	if err != nil {
		h.log.Errorw("failed to merge helm indexes", "group", grp.Name, "error", err)
		http.Error(w, "member repository error", http.StatusBadGateway)

		return true
	}

	sum := sha256.Sum256(body)

	if revalidateMerged(w, r, grp.Public, `"`+hex.EncodeToString(sum[:])+`"`, helmIndexMaxAge, failed) {
		return true
	}

	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}

	return true
}

// mergeHelmIndexes merges indexes and points every chart URL at chartBase
// (BASE_URL/repos/<group>/charts/) plus the chart's file name. Member indexes
// are what this server rendered: their URLs end in the file a charts/<file>
// request resolves through the member.
func mergeHelmIndexes(chartBase string, indexes []*pkghelm.Index) ([]byte, error) {
	merged := pkghelm.MergeIndexes(indexes)

	merged.RewriteURLs(func(cv pkghelm.ChartVersion) []string {
		var out []string

		for _, raw := range cv.URLs {
			file, ok := pkghelm.RelativeChartFile(raw)
			if !ok {
				continue
			}

			if u := chartBase + url.PathEscape(file); !slices.Contains(out, u) {
				out = append(out, u)
			}
		}

		return out
	})

	return merged.Marshal() //nolint:wrapcheck // caller logs
}
