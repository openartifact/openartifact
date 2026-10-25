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
	"github.com/openartifact/openartifact/internal/pathseg"
	"github.com/openartifact/openartifact/internal/repotype"
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

// helmIndexes is what the members' index.yaml documents yielded.
type helmIndexes struct {
	indexes []*pkghelm.Index
	hosted  []*pkghelm.Index // the hosted members' share of indexes
	proxies []*pkghelm.Index // and the proxies'
	failed  bool

	hostedFailed bool // a hosted member's index is missing from the merge
}

// collectHelmIndexes asks every member for its index.yaml.
func (h *Handler) collectHelmIndexes(grp *models.Repository, members []*models.Repository, mr *http.Request, sess *mergeSession, be Backend) helmIndexes {
	var got helmIndexes

	for _, m := range members {
		cw := sess.capture(maxHelmIndexBytes)
		be.ServeMember(cw, mr, m, "index.yaml")

		switch {
		case cw.status == http.StatusNotFound:
		case cw.status == http.StatusOK && !cw.overflow:
			ix, err := pkghelm.ParseIndex(cw.body.Bytes())
			if err != nil {
				h.log.Warnw("member helm index unreadable", "group", grp.Name, "member", m.Name, "error", err)

				got.failed = true
				got.hostedFailed = got.hostedFailed || m.RepoType != repotype.Proxy

				continue
			}

			got.indexes = append(got.indexes, ix)

			if m.RepoType == repotype.Proxy {
				got.proxies = append(got.proxies, ix)
			} else {
				got.hosted = append(got.hosted, ix)
			}
		default:
			got.failed = true
			got.hostedFailed = got.hostedFailed || m.RepoType != repotype.Proxy
		}
	}

	return got
}

// serveHelm answers index.yaml by merging the members' indexes: a chart
// version is listed once, from the first member that has it (name and version
// decide), and every chart URL points back at the group. It reports false for
// chart downloads (charts/<file>, and their .prov files), which the caller
// resolves by first hit, and answers 404 for everything else, so that a hosted
// member's chart listing (GET /charts) is not exposed through the group.
//
// Hosted wins (unless the group merges proxy versions): a chart name listed by
// any hosted member is removed from every proxy member's index before the
// merge, so only the hosted versions of it are published. A hosted index that
// cannot be read fails the request (502) while a proxy index is in play,
// since it may hold names that must hide the proxy's. Chart downloads are
// decided by applyHostedWins.
func (h *Handler) serveHelm(w http.ResponseWriter, r *http.Request, grp *models.Repository, relPath string, members []*models.Repository, be Backend) bool {
	rel := strings.TrimPrefix(relPath, "/")

	if file, ok := strings.CutPrefix(rel, helmChartsDir); ok && pathseg.Name(file) {
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

	// The indexes are held whole until merged: one gate slot per request,
	// taken when a member starts delivering and freed before the answer is
	// written.
	sess := h.newSession(r.Context())
	defer sess.release()

	got := h.collectHelmIndexes(grp, members, mr, sess, be)
	indexes, failed := got.indexes, got.failed

	if !grp.MergeProxyVersions {
		// A hosted index that could not be read may hold names the proxies
		// also list: serving them unfiltered would let a proxy's version win.
		if got.hostedFailed && len(got.proxies) > 0 {
			http.Error(w, "member repository error", http.StatusBadGateway)
			return true
		}

		dropHostedCharts(got.hosted, got.proxies)
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

	sess.release()

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

// dropHostedCharts removes from the proxy indexes every chart name the hosted
// indexes list. RewriteURLs drops a version for which no URLs are returned,
// and a chart left without versions; the surviving entries keep their URLs.
func dropHostedCharts(hosted, proxies []*pkghelm.Index) {
	if len(hosted) == 0 || len(proxies) == 0 {
		return
	}

	names := make(map[string]struct{})

	for _, ix := range hosted {
		for cv := range ix.Versions() {
			names[cv.Name] = struct{}{}
		}
	}

	if len(names) == 0 {
		return
	}

	for _, ix := range proxies {
		ix.RewriteURLs(func(cv pkghelm.ChartVersion) []string {
			if _, held := names[cv.Name]; held {
				return nil
			}

			return cv.URLs
		})
	}
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
