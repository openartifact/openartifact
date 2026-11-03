// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	htmlesc "html"
	"net/http"
	"net/url"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/pypisimple"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/pyversion"
)

// simpleIndexMaxAge is how long shared caches may reuse a public repository's
// simple index pages and legacy JSON documents before revalidating.
const simpleIndexMaxAge = 60

const (
	htmlContentType = "text/html; charset=utf-8"
	jsonContentType = "application/json"
)

// Document kinds, mixed into every validator so the documents of one project
// (page and JSON) never share an ETag or a render-cache key.
const (
	kindSimpleIndex   = "simple-index"
	kindSimpleProject = "simple-project"
	kindJSON          = "json"
)

// documentETag is the validator of a generated document: a digest of the
// document kind and every input of its bytes other than the stamped rows (the
// repository, the project, and for a document with absolute URLs the repository
// name and origin). The stamp moves with the rows, so the ETag and the render
// cache key built from it change whenever the document would.
func documentETag(parts ...string) string {
	sum := sha256.New()

	for _, p := range parts {
		_, _ = sum.Write([]byte(p))
		_, _ = sum.Write([]byte{0})
	}

	return `"` + hex.EncodeToString(sum.Sum(nil)[:16]) + `"`
}

// resolveRepoOrRespond loads the repository of the request; on an unknown
// repository or a lookup failure it writes the response and returns nil.
func (h *HTTPHandler) resolveRepoOrRespond(w http.ResponseWriter, r *http.Request, name string) *models.Repository {
	repo, err := h.ResolveRepo(r.Context(), name)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return nil
	}

	if repo == nil {
		http.NotFound(w, r)
		return nil
	}

	return repo
}

// handleSimpleIndex serves the project list. Revalidation is answered from
// PyPIIndexStamp alone (no name is loaded and nothing is rendered), and the
// page is rendered once per change of the project set.
func (h *HTTPHandler) handleSimpleIndex(w http.ResponseWriter, r *http.Request) {
	repoName := urlparams.Vars(r)["name"]

	repo := h.resolveRepoOrRespond(w, r, repoName)
	if repo == nil {
		return
	}

	stamp, err := h.DB.PyPIIndexStamp(r.Context(), repo.ID)
	if err != nil {
		h.Log.Errorw("failed to stamp pypi simple index", "repo", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// The links are relative to the page URL, so no origin takes part.
	etag := documentETag(kindSimpleIndex, repo.ID, stamp)

	h.ServeRendered(w, r, repo.Public, etag, etag, simpleIndexMaxAge, func(ctx context.Context) (*base.Rendered, error) {
		return h.renderSimpleIndex(ctx, repo.ID)
	}, "failed to render pypi simple index", "repo", repoName)
}

func (h *HTTPHandler) renderSimpleIndex(ctx context.Context, repoID string) (*base.Rendered, error) {
	storedNames, err := h.DB.ListPyPIPackageNames(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("list pypi packages: %w", err)
	}

	// Stored names are distinct but not necessarily normalized (legacy rows),
	// so two of them can collapse to one project.
	seen := make(map[string]struct{}, len(storedNames))

	var html strings.Builder

	html.Grow(64 + len(storedNames)*64)
	html.WriteString("<!DOCTYPE html>\n<html><body>\n")

	for _, stored := range storedNames {
		name := pypisimple.NormalizeName(stored)
		if _, dup := seen[name]; dup {
			continue
		}

		seen[name] = struct{}{}

		html.WriteString(`<a href="`)
		html.WriteString(htmlesc.EscapeString(url.PathEscape(name)))
		html.WriteString(`/">`)
		html.WriteString(htmlesc.EscapeString(name))
		html.WriteString("</a>\n")
	}

	html.WriteString("</body></html>\n")

	return &base.Rendered{Body: []byte(html.String()), ContentType: htmlContentType}, nil
}

// handlePackageIndex serves one project's file list as a simple page. See
// handleSimpleIndex for the caching; the stamp also answers an unknown project
// with 404 before anything is loaded.
func (h *HTTPHandler) handlePackageIndex(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	pkgName := vars["package"]

	repo := h.resolveRepoOrRespond(w, r, repoName)
	if repo == nil {
		return
	}

	normalizedName := pypisimple.NormalizeName(pkgName)

	stamp, ok := h.projectStamp(w, r, repo.ID, normalizedName)
	if !ok {
		return
	}

	// The file links are relative to the page URL ".../simple/<pkg>/" (PEP 503):
	// no origin and no repository name take part.
	etag := documentETag(kindSimpleProject, repo.ID, normalizedName, stamp)

	h.ServeRendered(w, r, repo.Public, etag, etag, simpleIndexMaxAge, func(ctx context.Context) (*base.Rendered, error) {
		versions, err := h.listProjectFiles(ctx, repo.ID, normalizedName)
		if err != nil {
			return nil, err
		}

		return renderProjectPage(versions), nil
	}, "failed to render pypi project page", "repo", repoName, "pkg", pkgName)
}

// projectStamp reads the stamp of a project's pages. It writes the response and
// returns false for a failed lookup and for a project with no live file (404).
func (h *HTTPHandler) projectStamp(w http.ResponseWriter, r *http.Request, repoID, name string) (string, bool) {
	stamp, files, err := h.DB.PyPIProjectStamp(r.Context(), repoID, name)
	if err != nil {
		h.Log.Errorw("failed to stamp pypi project", "pkg", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return "", false
	}

	if files == 0 {
		http.NotFound(w, r)
		return "", false
	}

	return stamp, true
}

func (h *HTTPHandler) listProjectFiles(ctx context.Context, repoID, name string) ([]*models.PyPIPackage, error) {
	versions, err := h.DB.ListPyPIPackageVersions(ctx, repoID, name)
	if err != nil {
		return nil, fmt.Errorf("list pypi package versions: %w", err)
	}

	return versions, nil
}

// renderProjectPage renders the files of a project. A project whose files were
// all removed between the stamp and the load renders empty and transient, so
// the empty page answers this request only and is never cached.
func renderProjectPage(versions []*models.PyPIPackage) *base.Rendered {
	var html strings.Builder

	html.Grow(len(versions) * 256)
	html.WriteString("<!DOCTYPE html>\n<html><body>\n")

	for _, pkg := range versions {
		fileURL := htmlesc.EscapeString("../../files/" + url.PathEscape(pkg.Filename))

		html.WriteString(`<a href="`)
		html.WriteString(fileURL)
		html.WriteString("#sha256=")
		html.WriteString(htmlesc.EscapeString(pkg.SHA256Digest))
		html.WriteString(`"`)

		if pkg.RequiresPython != "" {
			html.WriteString(` data-requires-python="`)
			html.WriteString(htmlesc.EscapeString(pkg.RequiresPython))
			html.WriteString(`"`)
		}

		html.WriteString(">")
		html.WriteString(htmlesc.EscapeString(pkg.Filename))
		html.WriteString("</a>\n")
	}

	html.WriteString("</body></html>\n")

	return &base.Rendered{Body: []byte(html.String()), ContentType: htmlContentType, Transient: len(versions) == 0}
}

// handleJSON serves the legacy JSON document of one project. Its file URLs are
// absolute (the tools that read it do not resolve references), so the origin and
// the repository name are part of the validator and the render-cache key.
func (h *HTTPHandler) handleJSON(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	pkgName := vars["package"]

	repo := h.resolveRepoOrRespond(w, r, repoName)
	if repo == nil {
		return
	}

	normalizedName := pypisimple.NormalizeName(pkgName)

	stamp, ok := h.projectStamp(w, r, repo.ID, normalizedName)
	if !ok {
		return
	}

	// PublicBase adds the Vary header, so it runs before the response headers
	// are written.
	publicBase := h.PublicBase(w, r)
	etag := documentETag(kindJSON, repo.ID, repoName, normalizedName, stamp, base.OriginTag(publicBase))

	h.ServeRendered(w, r, repo.Public, etag, etag, simpleIndexMaxAge, func(ctx context.Context) (*base.Rendered, error) {
		versions, err := h.listProjectFiles(ctx, repo.ID, normalizedName)
		if err != nil {
			return nil, err
		}

		return renderProjectJSON(versions, publicBase, repoName)
	}, "failed to render pypi project JSON", "repo", repoName, "pkg", pkgName)
}

// renderProjectJSON builds the legacy JSON document of a project. As with
// renderProjectPage, no files renders transient.
func renderProjectJSON(versions []*models.PyPIPackage, publicBase, repoName string) (*base.Rendered, error) {
	if len(versions) == 0 {
		return &base.Rendered{Body: []byte("{}\n"), ContentType: jsonContentType, Transient: true}, nil
	}

	releases := make(map[string][]map[string]any)

	for _, pkg := range versions {
		fileURL := fmt.Sprintf("%s/repos/%s/files/%s", publicBase, url.PathEscape(repoName), url.PathEscape(pkg.Filename))

		digests := map[string]string{"sha256": pkg.SHA256Digest}
		if pkg.MD5Digest != "" {
			digests["md5"] = pkg.MD5Digest
		}

		releases[pkg.Version] = append(releases[pkg.Version], map[string]any{
			"filename": pkg.Filename,
			"url":      fileURL,
			"digests":  digests,
		})
	}

	// info describes the highest version by PEP 440 order; the query orders
	// versions as strings, where 9.0.0 sorts above 10.0.0.
	latest := versions[0]

	for _, pkg := range versions[1:] {
		if pyversion.Compare(pkg.Version, latest.Version) > 0 {
			latest = pkg
		}
	}

	info := map[string]any{"name": latest.Name, "version": latest.Version}

	for key, value := range map[string]string{
		"summary":      latest.Summary,
		"author":       latest.Author,
		"author_email": latest.AuthorEmail,
		"license":      latest.License,
	} {
		if value != "" {
			info[key] = value
		}
	}

	var body bytes.Buffer

	if err := json.NewEncoder(&body).Encode(map[string]any{"info": info, "releases": releases}); err != nil {
		return nil, fmt.Errorf("encode pypi project JSON: %w", err)
	}

	return &base.Rendered{Body: body.Bytes(), ContentType: jsonContentType}, nil
}
