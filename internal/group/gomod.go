// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/openartifact/openartifact/internal/database/models"
)

// maxGoListBytes bounds one member's version list held in memory.
const maxGoListBytes = 16 << 20

// goListMaxAge is how long shared caches may reuse a public group's merged
// list before revalidating.
const goListMaxAge = 60

// serveGo answers "<module>/@v/list" with the union of the members' lists and
// reports false for every other GOPROXY path (.info, .mod, .zip, @latest),
// which the caller resolves by first hit: the first member that has the
// module version serves it, so a version published to an earlier (hosted)
// member shadows the same version of a later (proxy) member. Hosted wins goes
// further (see applyHostedWins): for a module a hosted member has versions of
// (and unless the group merges proxy versions) the list is the hosted
// members' only, and .info/.mod/.zip/@latest requests are never answered by a
// proxy.
func (h *Handler) serveGo(w http.ResponseWriter, r *http.Request, grp *models.Repository, relPath string, members []*models.Repository, be Backend) bool {
	if mod, ok := strings.CutSuffix(relPath, "/@v/list"); !ok || mod == "" {
		return false
	}

	// Members are asked with a plain GET, never with the client's conditional
	// headers: the merged body is what gets revalidated.
	mr := r.Clone(r.Context())
	mr.Method = http.MethodGet
	mr.Header.Del("If-None-Match")
	mr.Header.Del("If-Modified-Since")

	var (
		versions [][]byte
		answered bool
		failed   bool
	)

	sess := h.newSession(r.Context())
	defer sess.release()

	for _, m := range members {
		cw := sess.capture(maxGoListBytes)
		be.ServeMember(cw, mr, m, relPath)

		switch {
		case cw.status == http.StatusNotFound:
		case cw.status == http.StatusOK && !cw.overflow:
			answered = true
			versions = append(versions, bytes.Fields(cw.body.Bytes())...)
		default:
			failed = true
		}
	}

	// Members that failed are skipped as long as another one answered (as for
	// npm packuments); with no answer at all a failure is 502, not 404, so a
	// client does not conclude the module does not exist.
	if !answered {
		if failed {
			http.Error(w, "member repository error", http.StatusBadGateway)
		} else {
			http.NotFound(w, r)
		}

		return true
	}

	body := mergeGoVersions(versions)

	sess.release()

	sum := sha256.Sum256(body)

	if revalidateMerged(w, r, grp.Public, `"`+hex.EncodeToString(sum[:])+`"`, goListMaxAge, failed) {
		return true
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}

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
