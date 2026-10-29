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
)

// An Alpine package is stored at <branch>/<repo>/<arch>/<name>-<version>.apk:
// the branch and the repo directory are part of the address, so the source path
// is the address and is PUT as it is. Only an OpenArtifact repository holds apk
// packages (the path layout is OpenArtifact's own). The APKINDEX and the signing
// key are the destination's: the index is regenerated for what is stored and
// signed with the destination repository's key, which clients have to trust
// (apk-key of the repository).
func init() { //nolint:gochecknoinits // every format registers itself with the importer
	registerFormat(formatDef{
		format:     formatAPK,
		importable: importableAPK,
		match:      matchSHA,
		upload:     apkUpload,
		remove: func(ctx context.Context, t *importTarget, dst string, a sourceAsset, _ uploadResult) error {
			return t.deletePath(ctx, apkPath(dst, a.Path))
		},
	})
}

// importableAPK accepts <branch>/<repo>/<arch>/<file>.apk.
func importableAPK(p string) bool {
	parts := strings.Split(p, "/")

	return len(parts) == 4 && !strings.Contains(p, "..") && strings.HasSuffix(p, ".apk")
}

func apkPath(dst, assetPath string) string {
	return "/repos/" + url.PathEscape(dst) + "/" + escapePath(assetPath)
}

func apkUpload(ctx context.Context, t *importTarget, r uploadRequest) (uploadResult, error) {
	hdr := r.Meta.header()
	hdr.Set("Content-Type", "application/vnd.alpine.package")

	length := r.Asset.Size
	if length <= 0 {
		length = -1
	}

	apiPath := apkPath(r.Dst, r.Asset.Path)

	resp, err := t.send(ctx, http.MethodPut, apiPath, r.Body, length, hdr)
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
