// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
)

const (
	// maxDetailFiles bounds the files listed for one package version.
	maxDetailFiles = 200
	// maxDetailDependencies bounds the dependencies listed for one package version.
	maxDetailDependencies = 500
)

// handlePackageDetail returns one package version (by listing row id) with the
// metadata and dependencies its format stores and the files of the version.
//
//	GET /api/v1/repositories/{name}/packages/{id}
func (h *RepositoriesHandler) handlePackageDetail(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	ctx := r.Context()

	repo := h.readableRepository(w, r, vars["name"])
	if repo == nil {
		return
	}

	detail, err := h.db.GetRepoComponentDetail(ctx, repo.ID, vars["id"])
	if err != nil {
		h.log.Errorw("package detail: db error getting package", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if detail == nil {
		writeError(w, http.StatusNotFound, "not_found", "package not found")
		return
	}

	resp, err := h.packageDetailResponse(ctx, repo, detail)
	if err != nil {
		h.log.Errorw("package detail: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *RepositoriesHandler) packageDetailResponse(ctx context.Context, repo *models.Repository, d *models.ComponentDetail) (*responses.PackageDetailResponse, error) {
	files, err := h.versionFiles(ctx, repo.ID, d)
	if err != nil {
		return nil, err
	}

	rollup, _, err := h.db.ListRepoPackageRollup(ctx, models.PackageRollupOptions{
		RepoID: repo.ID, RepoFormat: repo.Format, Format: d.Format, Name: d.Name, Page: 1, Limit: 1,
	})
	if err != nil {
		return nil, err
	}

	resp := &responses.PackageDetailResponse{
		ID: d.ID, Name: d.Name, Version: d.Version, Format: d.Format, Arch: d.Arch,
		Filename: d.Filename, Size: d.SizeBytes, SHA256: d.SHA256, MD5: d.MD5, SHA1: d.SHA1,
		CreatedAt: formatTime(d.CreatedAt), UpdatedAt: formatTime(d.UpdatedAt),
		Summary: d.Summary, Description: d.Description, License: d.License, Homepage: d.Homepage,
		Metadata:     metadataEntries(d.Info),
		Dependencies: decodeDependencies(d.Format, d.Deps),
		Files:        files,
	}

	if len(rollup) > 0 {
		resp.VersionCount = rollup[0].VersionCount
		resp.LatestVersion = rollup[0].LatestVersion
	}

	return resp, nil
}

// versionFiles lists the rows that share the package name and version of d: the
// files of one version. The listing's substring filter narrows the read, the
// exact version match is made here.
func (h *RepositoriesHandler) versionFiles(ctx context.Context, repoID string, d *models.ComponentDetail) ([]responses.PackageFileResponse, error) {
	components, _, err := h.listComponents(ctx, repoID, database.ComponentListOptions{
		Format: d.Format, Name: d.Name, Query: d.Version, Page: 1, Limit: maxDetailFiles,
	})
	if err != nil {
		return nil, err
	}

	files := []responses.PackageFileResponse{}

	for _, c := range components {
		if c.Version != d.Version {
			continue
		}

		files = append(files, responses.PackageFileResponse{
			ID: c.ID, Filename: c.Filename, Arch: c.Arch, Size: c.PackageSize, SHA256: c.SHA256,
		})
	}

	return files, nil
}

func metadataEntries(info map[string]string) []responses.PackageMetadataEntry {
	entries := make([]responses.PackageMetadataEntry, 0, len(info))

	for _, k := range slices.Sorted(maps.Keys(info)) {
		entries = append(entries, responses.PackageMetadataEntry{Key: k, Value: info[k]})
	}

	return entries
}

// dependencyDecoders turn the raw dependency column of a format into
// dependencies. A format absent here stores none; a column that does not decode
// yields none.
//
//nolint:gochecknoglobals // immutable lookup table
var dependencyDecoders = map[string]func([]byte) []responses.PackageDependencyResponse{
	"npm":      decodeNpmDeps,
	"cargo":    decodeCargoDeps,
	"nuget":    decodeNugetDeps,
	"rubygems": decodeGemDeps,
	"conda":    decodeCondaDeps,
	"deb":      decodeDebDeps,
	"rpm":      decodeRpmDeps,
	"go":       decodeGoDeps,
}

// decodeDependencies returns the declared dependencies of a package version
// from the raw column the format keeps them in, capped at maxDetailDependencies.
func decodeDependencies(format string, raw []byte) []responses.PackageDependencyResponse {
	deps := []responses.PackageDependencyResponse{}

	decode, ok := dependencyDecoders[format]
	if !ok || len(raw) == 0 {
		return deps
	}

	found := decode(raw)

	return append(deps, found[:min(len(found), maxDetailDependencies)]...)
}

func decodeNpmDeps(raw []byte) []responses.PackageDependencyResponse {
	var manifest map[string]json.RawMessage
	if json.Unmarshal(raw, &manifest) != nil {
		return nil
	}

	var out []responses.PackageDependencyResponse

	for _, section := range []struct{ key, kind string }{
		{"dependencies", "runtime"},
		{"devDependencies", "dev"},
		{"peerDependencies", "peer"},
		{"optionalDependencies", "optional"},
	} {
		var deps map[string]string
		if json.Unmarshal(manifest[section.key], &deps) != nil {
			continue
		}

		for _, name := range slices.Sorted(maps.Keys(deps)) {
			out = append(out, responses.PackageDependencyResponse{Name: name, Version: deps[name], Kind: section.kind})
		}
	}

	return out
}

func decodeCargoDeps(raw []byte) []responses.PackageDependencyResponse {
	var meta struct {
		Deps []struct {
			Name       string `json:"name"`
			VersionReq string `json:"version_req"`
			Kind       string `json:"kind"`
		} `json:"deps"`
	}
	if json.Unmarshal(raw, &meta) != nil {
		return nil
	}

	out := make([]responses.PackageDependencyResponse, 0, len(meta.Deps))
	for _, d := range meta.Deps {
		out = append(out, responses.PackageDependencyResponse{Name: d.Name, Version: d.VersionReq, Kind: d.Kind})
	}

	return out
}

func decodeNugetDeps(raw []byte) []responses.PackageDependencyResponse {
	var groups []struct {
		TargetFramework string `json:"targetFramework"` //nolint:tagliatelle // the stored nuget dependency groups are camelCase
		Dependencies    []struct {
			ID    string `json:"id"`
			Range string `json:"range"`
		} `json:"dependencies"`
	}
	if json.Unmarshal(raw, &groups) != nil {
		return nil
	}

	var out []responses.PackageDependencyResponse

	for _, g := range groups {
		for _, d := range g.Dependencies {
			out = append(out, responses.PackageDependencyResponse{Name: d.ID, Version: d.Range, Kind: g.TargetFramework})
		}
	}

	return out
}

func decodeGemDeps(raw []byte) []responses.PackageDependencyResponse {
	var deps []struct {
		Name        string `json:"name"`
		Requirement string `json:"requirement"`
		Type        string `json:"type"`
	}
	if json.Unmarshal(raw, &deps) != nil {
		return nil
	}

	out := make([]responses.PackageDependencyResponse, 0, len(deps))
	for _, d := range deps {
		out = append(out, responses.PackageDependencyResponse{Name: d.Name, Version: d.Requirement, Kind: d.Type})
	}

	return out
}

// decodeCondaDeps reads the "name version build" match specs of a conda package.
func decodeCondaDeps(raw []byte) []responses.PackageDependencyResponse {
	var specs []string
	if json.Unmarshal(raw, &specs) != nil {
		return nil
	}

	out := make([]responses.PackageDependencyResponse, 0, len(specs))

	for _, spec := range specs {
		name, version, _ := strings.Cut(strings.TrimSpace(spec), " ")
		out = append(out, responses.PackageDependencyResponse{Name: name, Version: strings.TrimSpace(version)})
	}

	return out
}

// decodeDebDeps reads the Depends and Pre-Depends control fields of a deb:
// comma-separated entries, each "name (constraint)" or alternatives "a | b".
func decodeDebDeps(raw []byte) []responses.PackageDependencyResponse {
	var fields map[string]string
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}

	var out []responses.PackageDependencyResponse

	for _, field := range []struct{ key, kind string }{{"Pre-Depends", "pre-depends"}, {"Depends", "depends"}} {
		for entry := range strings.SplitSeq(fields[field.key], ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}

			name, constraint, _ := strings.Cut(entry, "(")
			out = append(out, responses.PackageDependencyResponse{
				Name:    strings.TrimSpace(name),
				Version: strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(constraint), ")")),
				Kind:    field.kind,
			})
		}
	}

	return out
}

// decodeRpmDeps reads the requires of an rpm. The stored dependencies carry no
// json tags, so their keys are the Go field names.
func decodeRpmDeps(raw []byte) []responses.PackageDependencyResponse {
	var extra struct {
		Requires []struct {
			Name    string
			Version string
		} `json:"requires"`
	}
	if json.Unmarshal(raw, &extra) != nil {
		return nil
	}

	out := make([]responses.PackageDependencyResponse, 0, len(extra.Requires))
	for _, d := range extra.Requires {
		out = append(out, responses.PackageDependencyResponse{Name: d.Name, Version: d.Version, Kind: "requires"})
	}

	return out
}

// decodeGoDeps reads the require directives of a go.mod (the column holds the
// file as a JSON string).
func decodeGoDeps(raw []byte) []responses.PackageDependencyResponse {
	var mod string
	if json.Unmarshal(raw, &mod) != nil {
		return nil
	}

	var out []responses.PackageDependencyResponse

	inBlock := false

	for line := range strings.SplitSeq(mod, "\n") {
		line, _, _ = strings.Cut(line, "//")
		fields := strings.Fields(line)

		switch {
		case inBlock && len(fields) == 1 && fields[0] == ")":
			inBlock = false
		case inBlock && len(fields) >= 2:
			out = append(out, responses.PackageDependencyResponse{Name: fields[0], Version: fields[1], Kind: "require"})
		case len(fields) == 2 && fields[0] == "require" && fields[1] == "(":
			inBlock = true
		case len(fields) >= 3 && fields[0] == "require":
			out = append(out, responses.PackageDependencyResponse{Name: fields[1], Version: fields[2], Kind: "require"})
		}
	}

	return out
}
