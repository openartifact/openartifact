// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"net/http"
	"net/url"
	"slices"
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

// HelmHooks is the helm group: merged index.yaml, charts first-hit.
var HelmHooks = Hooks{ //nolint:gochecknoglobals // immutable format hook value
	ServeMerged: (*Handler).serveHelm,
	PackageRef:  helmRef,
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
func (h *Handler) serveHelm(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) bool {
	rel := strings.TrimPrefix(relPath, "/")

	if file, ok := strings.CutPrefix(rel, helmChartsDir); ok && pathseg.Name(file) {
		return false
	}

	if rel != "index.yaml" {
		http.NotFound(w, r)
		return true
	}

	h.serveMergedDoc(w, r, grp, members, be, mergedDoc{
		ask:         memberAsk{path: samePath("index.yaml"), limit: maxHelmIndexBytes},
		what:        "helm indexes",
		maxAge:      helmIndexMaxAge,
		contentType: "application/yaml",
		merge: func(c *collection) ([]byte, error) {
			var indexes, hosted, proxies []*pkghelm.Index

			for _, d := range c.docs {
				ix, err := pkghelm.ParseIndex(d.body)
				if err != nil {
					h.log.Warnw("member helm index unreadable", "group", grp.Name, "member", d.member.Name, "error", err)
					c.fail(d.member)

					continue
				}

				indexes = append(indexes, ix)

				if d.member.RepoType == repotype.Proxy {
					proxies = append(proxies, ix)
				} else {
					hosted = append(hosted, ix)
				}
			}

			if !grp.MergeProxyVersions {
				// A hosted index that could not be read may hold names the
				// proxies also list: serving them unfiltered would let a
				// proxy's version win.
				if c.hostedFailed() && len(proxies) > 0 {
					return nil, errMemberFailed
				}

				dropHostedCharts(hosted, proxies)
			}

			if len(indexes) == 0 {
				return nil, errNoDocument
			}

			return mergeHelmIndexes(indexes)
		},
	})

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

// mergeHelmIndexes merges indexes and points every chart URL at the relative
// reference charts/<file>, which helm resolves against the group's URL. Member indexes
// are what this server rendered: their URLs end in the file a charts/<file>
// request resolves through the member.
func mergeHelmIndexes(indexes []*pkghelm.Index) ([]byte, error) {
	merged := pkghelm.MergeIndexes(indexes)

	merged.RewriteURLs(func(cv pkghelm.ChartVersion) []string {
		var out []string

		for _, raw := range cv.URLs {
			file, ok := pkghelm.RelativeChartFile(raw)
			if !ok {
				continue
			}

			if u := helmChartsDir + url.PathEscape(file); !slices.Contains(out, u) {
				out = append(out, u)
			}
		}

		return out
	})

	return merged.Marshal() //nolint:wrapcheck // caller logs
}

func helmRef(relPath string) (PkgRef, bool) {
	file, ok := strings.CutPrefix(strings.TrimPrefix(relPath, "/"), helmChartsDir)
	if !ok || !pathseg.Name(file) {
		return PkgRef{}, false
	}

	return PkgRef{chartFile: file, file: true, owner: (*Handler).ownsChart}, true
}
