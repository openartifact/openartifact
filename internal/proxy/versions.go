// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"context"
	"io"
	"strings"
	"time"

	"golang.org/x/mod/module"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/pypisimple"
	pkgansible "github.com/openartifact/openartifact/pkg/ansible"
	"github.com/openartifact/openartifact/pkg/composer"
	"github.com/openartifact/openartifact/pkg/conda"
	pkghelm "github.com/openartifact/openartifact/pkg/helm"
	pkgnuget "github.com/openartifact/openartifact/pkg/nuget"
)

// The Hooks.Versions of the formats whose cached metadata lists versions. Each
// reads only what is in the cache (it never fetches) and mirrors the Published
// hook of the format, so a version is dated the way the gate dates it.

// pypiVersions lists the releases of the cached project page, each dated by the
// first upload of its files (JSON pages only: HTML states no times).
func (s *Service) pypiVersions(ctx context.Context, repo *models.Repository, project string) ([]DatedVersion, bool) {
	project = pypisimple.NormalizeName(project)

	entry, err := s.db.GetProxyCacheEntry(ctx, repo.ID, pypiProjectKey(project))
	if err != nil || entry == nil || !pypisimple.IsJSON(entry.ContentType) {
		return nil, false
	}

	body, ok := s.cachedDocBody(ctx, repo, pypiPageDoc(pypiProjectKey(project), project+"/"))
	if !ok {
		return nil, false
	}

	files, err := pypisimple.ParseProject(body, entry.ContentType)
	if err != nil {
		return nil, false
	}

	releases := pypisimple.NewReleases(project, files)
	seen := make(map[string]struct{})

	var out []DatedVersion

	for i := range files {
		v := pypisimple.SpelledVersion(project, files[i].Filename)
		if v == "" {
			continue
		}

		if _, dup := seen[v]; dup {
			continue
		}

		seen[v] = struct{}{}

		out = append(out, DatedVersion{Version: v, Published: releases.Published(project, files[i].Filename)})
	}

	return out, true
}

// composerVersions lists the versions of the cached p2 documents (release and
// dev) of the package name.
func (s *Service) composerVersions(ctx context.Context, repo *models.Repository, name string) ([]DatedVersion, bool) {
	var (
		out   []DatedVersion
		seen  = make(map[string]struct{})
		found bool
	)

	for _, dev := range []bool{false, true} {
		body, ok := s.cachedDocBody(ctx, repo, composerDoc(name, dev))
		if !ok {
			continue
		}

		versions, err := composer.ParseDocument(body, name)
		if err != nil {
			continue
		}

		found = true

		for _, v := range versions {
			ver := v.VersionString()
			if _, dup := seen[ver]; dup || ver == "" {
				continue
			}

			seen[ver] = struct{}{}

			out = append(out, DatedVersion{Version: ver, Published: v.Published()})
		}
	}

	return out, found
}

// helmVersions lists the versions of chart name in the cached index.
func (s *Service) helmVersions(ctx context.Context, repo *models.Repository, name string) ([]DatedVersion, bool) {
	body, ok := s.cachedDocBody(ctx, repo, helmIndexDoc)
	if !ok {
		return nil, false
	}

	ix, err := pkghelm.ParseIndex(body)
	if err != nil {
		return nil, false
	}

	var out []DatedVersion

	for cv := range ix.Versions() {
		if cv.Name == name {
			out = append(out, DatedVersion{Version: cv.Version, Published: cv.Published()})
		}
	}

	return out, true
}

// nugetVersionList is Hooks.Versions: the versions of the cached registration
// index of id whose pages are inline (a page the feed links instead is not in the
// index document).
func (s *Service) nugetVersionList(ctx context.Context, repo *models.Repository, id string) ([]DatedVersion, bool) {
	p := nugetPath{kind: nugetRegIndex, id: strings.ToLower(id)}

	body, ok := s.cachedDocBody(ctx, repo, metaDoc{key: p.key(), limit: maxNuGetDocBytes, what: "nuget document"})
	if !ok {
		return nil, false
	}

	doc, err := gunzipDoc(body)
	if err != nil {
		return nil, false
	}

	pages, err := pkgnuget.ParsePages(doc)
	if err != nil {
		return nil, false
	}

	var out []DatedVersion

	for _, page := range pages {
		for _, it := range page.Items {
			if v, published := nugetItemMeta(it); v != "" {
				out = append(out, DatedVersion{Version: v, Published: published})
			}
		}
	}

	return out, len(out) > 0
}

// rubygemsVersions lists the versions of the cached versions API document of
// gem, each with the newest time of its platforms.
func (s *Service) rubygemsVersions(ctx context.Context, repo *models.Repository, gem string) ([]DatedVersion, bool) {
	body, ok := s.cachedDocBody(ctx, repo, rubygemsAPIDoc(gem))
	if !ok {
		return nil, false
	}

	dates, err := parseGemDates(body)
	if err != nil {
		return nil, false
	}

	newest := make(map[string]time.Time, len(dates))

	for field, created := range dates {
		n := gemNumber(field)
		if prev, seen := newest[n]; !seen || created.After(prev) {
			newest[n] = created
		}
	}

	out := make([]DatedVersion, 0, len(newest))
	for n, t := range newest {
		out = append(out, DatedVersion{Version: n, Published: t})
	}

	return out, true
}

// goVersions lists the versions of the cached "@v/list" of mod. A version is
// dated by its cached ".info"; one whose ".info" was never fetched has no time
// (the minimum age cannot hold it back, curation still can).
func (s *Service) goVersions(ctx context.Context, repo *models.Repository, mod string) ([]DatedVersion, bool) {
	escMod, err := module.EscapePath(mod)
	if err != nil {
		return nil, false
	}

	req := goListRequest(mod, escMod)

	body, ok := s.cachedDocBody(ctx, repo, s.goDoc(repo, req, versionGate{}))
	if !ok {
		return nil, false
	}

	lines := strings.Split(string(body), "\n")
	list := goListVersions(lines)
	out := make([]DatedVersion, len(list))

	for i, v := range list {
		published, _ := s.goInfoPublished(ctx, repo, mod, v)
		out[i] = DatedVersion{Version: v, Published: published}
	}

	return out, true
}

// condaVersions lists the versions of package pkg in the cached repodata of the
// platform subdirectories, each with its earliest build time.
func (s *Service) condaVersions(ctx context.Context, repo *models.Repository, pkg string) ([]DatedVersion, bool) {
	merged := make(map[string]time.Time)
	found := false

	for _, subdir := range condaPublishSubdirs {
		for _, comp := range []conda.Compression{conda.CompZstd, conda.CompNone} {
			idx := conda.Path{Kind: conda.KindRepodata, Subdir: subdir, File: conda.RepodataFile(false, comp), Comp: comp}

			cached := s.scanCachedRepodata(ctx, repo, idx, func(plain io.Reader) error {
				times, err := conda.BuildTimes(plain, s.condaIndexLimit(), pkg)
				if err != nil {
					return err
				}

				for v, t := range times {
					if prev, seen := merged[v]; !seen || (!t.IsZero() && (prev.IsZero() || t.Before(prev))) {
						merged[v] = t
					}
				}

				return nil
			})
			if cached {
				found = true

				break
			}
		}
	}

	out := make([]DatedVersion, 0, len(merged))
	for v, t := range merged {
		out = append(out, DatedVersion{Version: v, Published: t})
	}

	return out, found
}

// ansibleVersions lists the versions of the cached version list of the
// collection "namespace.name".
func (s *Service) ansibleVersions(ctx context.Context, repo *models.Repository, collection string) ([]DatedVersion, bool) {
	namespace, name, ok := strings.Cut(collection, ".")
	if !ok {
		return nil, false
	}

	body, ok := s.cachedDocBody(ctx, repo, s.ansibleVersionsDoc(repo, pkgansible.Path{Namespace: namespace, Name: name}))
	if !ok {
		return nil, false
	}

	page, err := pkgansible.ParseVersionPage(bytes.NewReader(body))
	if err != nil {
		return nil, false
	}

	out := make([]DatedVersion, len(page.Versions))
	for i, v := range page.Versions {
		out[i] = DatedVersion{Version: v.Version, Published: galaxyPublished(v.CreatedAt)}
	}

	return out, true
}
