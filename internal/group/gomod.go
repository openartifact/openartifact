// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"bytes"
	"net/http"
	"slices"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/openartifact/openartifact/internal/database/models"
)

// maxGoListBytes bounds one member's version list held in memory.
const maxGoListBytes = 16 << 20

// goListMaxAge is how long shared caches may reuse a public group's merged
// list before revalidating.
const goListMaxAge = 60

// GoHooks is the go group: merged version lists, everything else first-hit.
var GoHooks = Hooks{ //nolint:gochecknoglobals // immutable format hook value
	ServeMerged: (*Handler).serveGo,
	PackageRef:  goRef,
}

// serveGo answers "<module>/@v/list" with the union of the members' lists and
// reports false for every other GOPROXY path (.info, .mod, .zip, @latest),
// which the caller resolves by first hit: the first member that has the
// module version serves it, so a version published to an earlier (hosted)
// member shadows the same version of a later (proxy) member. Hosted wins goes
// further (see applyHostedWins): for a module a hosted member has versions of
// (and unless the group merges proxy versions) the list is the hosted
// members' only, and .info/.mod/.zip/@latest requests are never answered by a
// proxy.
func (h *Handler) serveGo(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) bool {
	if mod, ok := strings.CutSuffix(relPath, "/@v/list"); !ok || mod == "" {
		return false
	}

	// Members that failed are skipped as long as another one answered (as for
	// npm packuments); with no answer at all a failure is 502, not 404, so a
	// client does not conclude the module does not exist.
	h.serveMergedDoc(w, r, grp, members, be, mergedDoc{
		ask:         memberAsk{path: samePath(relPath), limit: maxGoListBytes},
		what:        "go version lists",
		maxAge:      goListMaxAge,
		contentType: "text/plain; charset=utf-8",
		merge: func(c *collection) ([]byte, error) {
			var versions [][]byte

			for _, d := range c.docs {
				versions = append(versions, bytes.Fields(d.body)...)
			}

			return mergeGoVersions(versions), nil
		},
	})

	return true
}

// mergeGoVersions returns the distinct valid versions among vs, highest
// first, one per line. Entries that are not semantic versions are dropped, as
// the go command ignores them.
func mergeGoVersions(vs [][]byte) []byte {
	seen := make(map[string]struct{}, len(vs))
	list := make([]string, 0, len(vs))

	for _, b := range vs {
		v := string(b)
		if _, dup := seen[v]; dup || !semver.IsValid(v) {
			continue
		}

		seen[v] = struct{}{}

		list = append(list, v)
	}

	slices.SortFunc(list, func(a, b string) int {
		if c := semver.Compare(b, a); c != 0 {
			return c
		}

		return strings.Compare(a, b)
	})

	return []byte(strings.Join(list, "\n"))
}

func goRef(relPath string) (PkgRef, bool) {
	rel := strings.TrimPrefix(relPath, "/")

	mod, _, ok := strings.Cut(rel, "/@v/")
	if !ok {
		mod, ok = strings.CutSuffix(rel, "/@latest")
	}

	if !ok || mod == "" {
		return PkgRef{}, false
	}

	return PkgRef{probe: mod + "/@v/list", nonEmpty: true, file: rel != mod+"/@v/list"}, true
}
