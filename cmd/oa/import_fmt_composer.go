// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	pkgcomposer "github.com/openartifact/openartifact/pkg/composer"
)

// A Composer dist is the zip itself, published with a PUT to the package and the
// version it is stored as. The package name comes from the zip's composer.json;
// the version from its "version" field or, when it has none (the usual case),
// from --version.
func init() { //nolint:gochecknoinits // every format registers itself with the importer
	registerFormat(formatDef{
		format:     formatComposer,
		importable: func(p string) bool { return strings.HasSuffix(p, ".zip") }, // only an OpenArtifact source holds composer repositories
		match:      matchSHA,
		upload:     composerUpload,
	})
}

// composerUploadPath is the publish path of one version of a package.
func composerUploadPath(repo, name, version string) string {
	return "/repos/" + url.PathEscape(repo) + "/packages/upload/" + name + "/" + url.PathEscape(version)
}

// composerUpload stores one dist zip: it reads the package name (and version)
// out of the archive, then sends the same bytes.
func composerUpload(ctx context.Context, t *importTarget, r uploadRequest) (uploadResult, error) {
	sp, err := spool(r.Body)
	if err != nil {
		return uploadResult{}, err
	}

	defer func() { _ = sp.Close() }()

	man, err := pkgcomposer.ReadArchive(sp.Reader(), sp.Size)
	if err != nil {
		return uploadResult{}, fmt.Errorf("%w: %s: %w", errImportUsage, r.Asset.Path, err)
	}

	version := r.Meta.Version
	if version == "" {
		version = man.Version
	}

	if version == "" {
		return uploadResult{}, fmt.Errorf("%w: %s: composer.json has no \"version\"; pass --version", errImportUsage, r.Asset.Path)
	}

	hdr := r.Meta.header()
	hdr.Set("Content-Type", "application/zip")

	apiPath := composerUploadPath(r.Dst, man.Name, version)

	resp, err := t.send(ctx, http.MethodPut, apiPath, sp.Reader(), sp.Size, hdr)
	if err != nil {
		return uploadResult{}, err
	}

	defer func() { _ = resp.Body.Close() }()

	var out struct {
		ID     string `json:"id"`
		SHA256 string `json:"sha256"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return uploadResult{}, fmt.Errorf("PUT %s: decoding response: %w", apiPath, err)
	}

	return uploadResult{ID: out.ID, SHA256: strings.ToLower(out.SHA256)}, nil
}
