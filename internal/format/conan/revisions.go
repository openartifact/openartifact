// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// revision is a published recipe or package revision and when it was
// published (the upload time of its manifest).
type revision struct {
	ID   string
	Time time.Time
}

// revisionJSON is the wire form of a revision; time is ISO 8601 with an offset,
// which the conan client parses with datetime.fromisoformat.
type revisionJSON struct {
	Revision string `json:"revision"`
	Time     string `json:"time"`
}

func (r revision) wire() revisionJSON {
	return revisionJSON{Revision: r.ID, Time: r.Time.UTC().Format("2006-01-02T15:04:05.000000+00:00")}
}

// newestFirst orders revisions with the latest publication first.
func newestFirst(revs []revision) {
	slices.SortStableFunc(revs, func(a, b revision) int {
		return cmp.Or(b.Time.Compare(a.Time), strings.Compare(b.ID, a.ID))
	})
}

// isLive reports whether f is a file conan clients can see; rows stored before
// files were tracked have no name.
func isLive(f *models.ConanPackage) bool { return f.Filename != "" }

// recipeRevisions returns the published revisions of a recipe, newest first.
func recipeRevisions(files []*models.ConanPackage) []revision {
	var revs []revision

	for _, f := range files {
		if isLive(f) && f.PackageID == "" && f.Filename == manifestFile {
			revs = append(revs, revision{ID: f.RecipeRevision, Time: f.UpdatedAt})
		}
	}

	newestFirst(revs)

	return revs
}

// packageRevisions returns the published revisions of one package of a recipe
// revision, newest first.
func packageRevisions(files []*models.ConanPackage, rrev, pid string) []revision {
	var revs []revision

	for _, f := range files {
		if isLive(f) && f.RecipeRevision == rrev && f.PackageID == pid && f.Filename == manifestFile {
			revs = append(revs, revision{ID: f.PackageRevision, Time: f.UpdatedAt})
		}
	}

	newestFirst(revs)

	return revs
}

// packageIDs returns the ids of the packages of a recipe revision that have a
// published revision, sorted.
func packageIDs(files []*models.ConanPackage, rrev string) []string {
	var ids []string

	for _, f := range files {
		if isLive(f) && f.RecipeRevision == rrev && f.PackageID != "" && f.Filename == manifestFile && !slices.Contains(ids, f.PackageID) {
			ids = append(ids, f.PackageID)
		}
	}

	slices.Sort(ids)

	return ids
}

// revisionFiles returns the files of one recipe revision (pid empty) or one
// package revision.
func revisionFiles(files []*models.ConanPackage, rrev, pid, prev string) []*models.ConanPackage {
	var out []*models.ConanPackage

	for _, f := range files {
		if isLive(f) && f.RecipeRevision == rrev && f.PackageID == pid && f.PackageRevision == prev {
			out = append(out, f)
		}
	}

	return out
}

// recipeFiles loads every live file of the addressed recipe; it writes the
// response and returns false on error.
func (h *HTTPHandler) recipeFiles(w http.ResponseWriter, r *http.Request, s *scope) ([]*models.ConanPackage, bool) {
	files, err := h.DB.ListConanRecipeFiles(r.Context(), s.repoID, s.recipe.Name, s.recipe.Version, s.recipe.UserChannel)
	if err != nil {
		h.fail(w, "failed to list conan files", err, "recipe", conanPath(s.recipe.Name, s.recipe.Version, s.recipe.UserChannel, ""))
		return nil, false
	}

	return files, true
}

// handleRecipeRevisions lists the published revisions of a recipe.
func (h *HTTPHandler) handleRecipeRevisions(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scopeOf(w, r)
	if !ok {
		return
	}

	files, ok := h.recipeFiles(w, r, s)
	if !ok {
		return
	}

	h.writeRevisions(w, r, recipeRevisions(files))
}

// handleRecipeLatest returns the newest published revision of a recipe.
func (h *HTTPHandler) handleRecipeLatest(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scopeOf(w, r)
	if !ok {
		return
	}

	files, ok := h.recipeFiles(w, r, s)
	if !ok {
		return
	}

	h.writeLatest(w, r, recipeRevisions(files))
}

// handlePackageRevisions lists the published revisions of one package.
func (h *HTTPHandler) handlePackageRevisions(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scopeOf(w, r)
	if !ok {
		return
	}

	files, ok := h.recipeFiles(w, r, s)
	if !ok {
		return
	}

	h.writeRevisions(w, r, packageRevisions(files, s.rrev, s.pid))
}

// handlePackageLatest returns the newest published revision of one package.
func (h *HTTPHandler) handlePackageLatest(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scopeOf(w, r)
	if !ok {
		return
	}

	files, ok := h.recipeFiles(w, r, s)
	if !ok {
		return
	}

	h.writeLatest(w, r, packageRevisions(files, s.rrev, s.pid))
}

// writeRevisions answers {"revisions": [...]}, 404 when there is none.
func (h *HTTPHandler) writeRevisions(w http.ResponseWriter, r *http.Request, revs []revision) {
	if len(revs) == 0 {
		http.NotFound(w, r)
		return
	}

	out := make([]revisionJSON, 0, len(revs))
	for _, rev := range revs {
		out = append(out, rev.wire())
	}

	h.writeJSON(w, map[string]any{"revisions": out})
}

// writeLatest answers the newest revision, 404 when there is none.
func (h *HTTPHandler) writeLatest(w http.ResponseWriter, r *http.Request, revs []revision) {
	if len(revs) == 0 {
		http.NotFound(w, r)
		return
	}

	h.writeJSON(w, revs[0].wire())
}
