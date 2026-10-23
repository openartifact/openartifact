// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
)

// maxConanInfoBytes bounds the conaninfo.txt a package search reads; a real one
// is a few hundred bytes.
const maxConanInfoBytes = 1 << 20

// handleSearch lists the recipes whose reference matches q, a glob as conan
// takes it (`zlib/*`, `*`); ignorecase=False makes it case sensitive.
func (h *HTTPHandler) handleSearch(w http.ResponseWriter, r *http.Request) {
	s, ok := h.repoScopeOf(w, r)
	if !ok {
		return
	}

	var pattern *regexp.Regexp

	if q := r.URL.Query().Get("q"); q != "" {
		var err error

		pattern, err = globRegexp(q, !strings.EqualFold(r.URL.Query().Get("ignorecase"), "false"))
		if err != nil {
			http.Error(w, "invalid search pattern", http.StatusBadRequest)
			return
		}
	}

	recipes, err := h.DB.ListConanRecipes(r.Context(), s.repoID)
	if err != nil {
		h.fail(w, "failed to list conan recipes", err, "repo", s.repoName)
		return
	}

	results := make([]string, 0, len(recipes))

	for _, rec := range recipes {
		ref := splitRecipe(rec)
		if pattern == nil || ref.partialMatch(pattern) {
			results = append(results, ref.String())
		}
	}

	h.writeJSON(w, map[string]any{"results": results})
}

// recipeRef is a recipe as conan prints it: name/version, with @user[/channel]
// only when it has them.
type recipeRef struct {
	name, version, user, channel string
}

// splitRecipe parses the stored "user/channel", where "_" stands for none.
func splitRecipe(rec models.ConanRecipe) recipeRef {
	user, channel, _ := strings.Cut(rec.UserChannel, "/")
	if user == "_" {
		user = ""
	}

	if channel == "_" {
		channel = ""
	}

	return recipeRef{name: rec.Name, version: rec.Version, user: user, channel: channel}
}

// tokens are the pieces of the reference in order.
func (r recipeRef) tokens() []string {
	tokens := []string{r.name, "/", r.version}

	if r.user != "" {
		tokens = append(tokens, "@", r.user)
	}

	if r.channel != "" {
		tokens = append(tokens, "/", r.channel)
	}

	return tokens
}

func (r recipeRef) String() string { return strings.Join(r.tokens(), "") }

// partialMatch reports whether pattern matches any prefix of the reference cut
// at a token boundary (name, name/, name/version, name/version@, ...), which is
// how conan lets `zlib` and `zlib/1.3` find zlib/1.3@user/channel.
func (r recipeRef) partialMatch(pattern *regexp.Regexp) bool {
	var partial strings.Builder

	for _, t := range r.tokens() {
		partial.WriteString(t)

		if pattern.MatchString(partial.String()) {
			return true
		}
	}

	return false
}

// globRegexp translates a shell glob (`*`, `?`, `[seq]`, `[!seq]`) into an
// anchored regular expression, like Python's fnmatch.translate.
func globRegexp(glob string, ignoreCase bool) (*regexp.Regexp, error) {
	var b strings.Builder

	b.WriteString("(?s")

	if ignoreCase {
		b.WriteString("i")
	}

	b.WriteString(")^")

	rs := []rune(glob)

	for i := 0; i < len(rs); i++ {
		switch c := rs[i]; c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '[':
			end := classEnd(rs, i)
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}

			writeClass(&b, rs[i+1:end])

			i = end
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}

	b.WriteString(`\z`)

	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("compile search pattern: %w", err)
	}

	return re, nil
}

// classEnd returns the index of the ']' closing the character class opened at
// rs[open], or -1 when it is not closed.
func classEnd(rs []rune, open int) int {
	j := open + 1
	if j < len(rs) && rs[j] == '!' {
		j++
	}

	// A ']' right after the opening is a member of the class.
	if j < len(rs) && rs[j] == ']' {
		j++
	}

	for ; j < len(rs); j++ {
		if rs[j] == ']' {
			return j
		}
	}

	return -1
}

// writeClass writes the regexp character class for the glob class body.
func writeClass(b *strings.Builder, body []rune) {
	b.WriteString("[")

	if len(body) > 0 && body[0] == '!' {
		b.WriteString("^")

		body = body[1:]
	}

	for _, c := range body {
		if c == '\\' || c == '[' || c == ']' || c == '^' {
			b.WriteString(`\`)
		}

		b.WriteRune(c)
	}

	b.WriteString("]")
}

// handleSearchPackages lists the packages of a recipe revision (the latest one
// without {rrev}) as {package_id: {"content": conaninfo.txt}}, or {package_id: {}}
// with list_only=True.
func (h *HTTPHandler) handleSearchPackages(w http.ResponseWriter, r *http.Request) {
	h.searchPackages(w, r, false)
}

// handleV1SearchPackages is the Conan 1 package search, which looks in every
// recipe revision: a package id found in a newer revision wins. The response
// carries the raw conaninfo.txt under "content", which Conan 1.48 and later read.
func (h *HTTPHandler) handleV1SearchPackages(w http.ResponseWriter, r *http.Request) {
	h.searchPackages(w, r, true)
}

// searchPackages answers a package search of one recipe revision, or of all of
// them (newest first) with allRevisions.
func (h *HTTPHandler) searchPackages(w http.ResponseWriter, r *http.Request, allRevisions bool) {
	s, ok := h.scopeOf(w, r)
	if !ok {
		return
	}

	files, ok := h.recipeFiles(w, r, s)
	if !ok {
		return
	}

	rrevs := []string{s.rrev}

	switch {
	case allRevisions || s.rrev == "":
		revs := recipeRevisions(files)
		if len(revs) == 0 {
			http.NotFound(w, r)
			return
		}

		rrevs = rrevs[:0]
		for _, rev := range revs {
			rrevs = append(rrevs, rev.ID)
		}

		if !allRevisions {
			rrevs = rrevs[:1]
		}
	case !hasFile(revisionFiles(files, s.rrev, "", ""), manifestFile):
		http.NotFound(w, r)
		return
	}

	listOnly := strings.EqualFold(r.URL.Query().Get("list_only"), "true")
	out := map[string]map[string]string{}

	for _, rrev := range rrevs {
		for _, pid := range packageIDs(files, rrev) {
			if _, seen := out[pid]; seen {
				continue
			}

			if listOnly {
				out[pid] = map[string]string{}
				continue
			}

			info, found, err := h.latestConanInfo(r, files, rrev, pid)
			if err != nil {
				h.fail(w, "failed to read conaninfo", err, "package", pid)
				return
			}

			if found {
				out[pid] = map[string]string{"content": info}
			}
		}
	}

	h.writeJSON(w, out)
}

// latestConanInfo reads conaninfo.txt of the newest published revision of a
// package; found is false when that revision has none.
func (h *HTTPHandler) latestConanInfo(r *http.Request, files []*models.ConanPackage, rrev, pid string) (string, bool, error) {
	revs := packageRevisions(files, rrev, pid)
	if len(revs) == 0 {
		return "", false, nil
	}

	for _, f := range revisionFiles(files, rrev, pid, revs[0].ID) {
		if f.Filename != conanInfoFile {
			continue
		}

		rc, err := h.BlobStore.Get(r.Context(), f.BlobKey)
		if err != nil {
			return "", false, fmt.Errorf("open conaninfo.txt: %w", err)
		}
		defer base.CloseLog(rc, h.Log)

		data, err := io.ReadAll(io.LimitReader(rc, maxConanInfoBytes))
		if err != nil {
			return "", false, fmt.Errorf("read conaninfo.txt: %w", err)
		}

		return string(data), true, nil
	}

	return "", false, nil
}
