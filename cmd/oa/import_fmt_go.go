// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	xmod "golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

var errGoModule = errors.New("unusable module coordinates")

func init() { //nolint:gochecknoinits // every format registers itself with the importer
	registerFormat(formatDef{
		format: formatGo, nexus: []string{"go"}, artifactory: []string{"go"},
		importable: importableGo, match: matchSHA,
		upload: uploadGo,
		remove: func(context.Context, *importTarget, string, sourceAsset, uploadResult) error {
			return errNoRemove(formatGo)
		},
	})
}

// importableGo accepts the module zips (<module>/@v/<version>.zip). The .mod and
// .info files, the version lists and the checksum sidecars are derived by the
// target from the zip.
func importableGo(p string) bool {
	module, file, ok := strings.Cut(p, "/@v/")

	return ok && module != "" && strings.HasSuffix(file, ".zip") && len(file) > len(".zip") && !strings.Contains(file, "/")
}

// goCoordinates reads the module path and version out of a source path
// (<module>/@v/<version>.zip). Both are GOPROXY case-encoded in a proxy layout
// ("!x" for "X"); a source that stored them with their capitals is accepted too.
func goCoordinates(p string) (string, string, error) {
	module, file, _ := strings.Cut(p, "/@v/")
	version := strings.TrimSuffix(file, ".zip")

	modulePath, err := goDecodePath(module)
	if err != nil {
		return "", "", err
	}

	v, err := goDecodeVersion(version)
	if err != nil {
		return "", "", err
	}

	return modulePath, v, nil
}

func goDecodePath(escaped string) (string, error) {
	if m, err := xmod.UnescapePath(escaped); err == nil {
		return m, nil
	}

	if err := xmod.CheckPath(escaped); err != nil {
		return "", fmt.Errorf("%w: module path %q: %w", errGoModule, escaped, err)
	}

	return escaped, nil
}

// goDecodeVersion decodes a version and requires what the target accepts: a
// canonical vMAJOR.MINOR.PATCH[-prerelease], optionally +incompatible.
func goDecodeVersion(escaped string) (string, error) {
	v, err := xmod.UnescapeVersion(escaped)
	if err != nil {
		v = escaped
	}

	canonical := semver.Canonical(v)
	if !semver.IsValid(v) || (v != canonical && v != canonical+"+incompatible") {
		return "", fmt.Errorf("%w: %q is not a canonical module version", errGoModule, escaped)
	}

	return v, nil
}

// uploadGo streams a module zip to PUT <module>/@v/<version>. The server
// answers with the module and version only, so the stored digest and the id
// come from the repository listing.
func uploadGo(ctx context.Context, t *importTarget, r uploadRequest) (uploadResult, error) {
	modulePath, version, err := goCoordinates(r.Asset.Path)
	if err != nil {
		return uploadResult{}, err
	}

	escPath, err := xmod.EscapePath(modulePath)
	if err != nil {
		return uploadResult{}, fmt.Errorf("%w: module path: %w", errGoModule, err)
	}

	escVersion, err := xmod.EscapeVersion(version)
	if err != nil {
		return uploadResult{}, fmt.Errorf("%w: module version: %w", errGoModule, err)
	}

	length := r.Asset.Size
	if length <= 0 {
		length = -1
	}

	hdr := r.Meta.header()
	hdr.Set("Content-Type", "application/zip")

	resp, err := t.send(ctx, http.MethodPut,
		"/repos/"+url.PathEscape(r.Dst)+"/"+escapePath(escPath)+"/@v/"+url.PathEscape(escVersion), r.Body, length, hdr)
	if err != nil {
		return uploadResult{}, err
	}

	_ = resp.Body.Close()

	return t.storedResult(ctx, r.Dst, formatGo, modulePath, func(p listedPackage) bool {
		return p.Name == modulePath && p.Version == version
	})
}
