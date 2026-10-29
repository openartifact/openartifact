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
	"slices"
	"strings"
)

// A Terraform repository holds modules and providers, both published with a PUT
// below /_api/. The source path names what the file is:
//
//	modules/<name>/<system>/<version>
//	providers/<type>/<version>/<os>/<arch>
//
// (the body is the module archive, or the provider's platform zip). Only an
// OpenArtifact repository holds them. The provider's checksums file and its
// signature are not stored by the registry: it answers the provider download
// protocol from the platform zips it has.
func init() { //nolint:gochecknoinits // every format registers itself with the importer
	registerFormat(formatDef{
		format:     formatTerraform,
		importable: importableTerraform,
		match:      matchSHA,
		upload:     terraformUpload,
		remove: func(ctx context.Context, t *importTarget, dst string, a sourceAsset, _ uploadResult) error {
			c, ok := parseTerraformPath(a.Path)
			if !ok {
				return fmt.Errorf("%w: cannot determine what to remove", errImportUsage)
			}

			return t.deletePath(ctx, c.apiPath(dst))
		},
	})
}

func importableTerraform(p string) bool {
	_, ok := parseTerraformPath(p)

	return ok
}

// terraformProtocolsKey is the sourceAsset.Extra key that carries the plugin
// protocols of a provider platform ("5.0,6.0").
const terraformProtocolsKey = "terraform.protocols"

// terraformCoords is the address of one module version or provider platform.
type terraformCoords struct {
	module         bool
	name, system   string // module: name and system; provider: name is the type
	version        string
	osName, goArch string
}

func parseTerraformPath(p string) (terraformCoords, bool) {
	parts := strings.Split(p, "/")
	if slices.Contains(parts, "") || slices.Contains(parts, "..") {
		return terraformCoords{}, false
	}

	switch {
	case len(parts) == 4 && parts[0] == "modules":
		return terraformCoords{module: true, name: parts[1], system: parts[2], version: parts[3]}, true
	case len(parts) == 5 && parts[0] == "providers":
		return terraformCoords{name: parts[1], version: parts[2], osName: parts[3], goArch: parts[4]}, true
	}

	return terraformCoords{}, false
}

// apiPath is the publish path of the module version or provider platform.
func (c terraformCoords) apiPath(dst string) string {
	base := "/repos/" + url.PathEscape(dst) + "/_api/"
	if c.module {
		return base + "modules/" + url.PathEscape(c.name) + "/" + url.PathEscape(c.system) + "/" + url.PathEscape(c.version)
	}

	return base + "providers/" + url.PathEscape(c.name) + "/" + url.PathEscape(c.version) + "/" +
		url.PathEscape(c.osName) + "/" + url.PathEscape(c.goArch)
}

func terraformUpload(ctx context.Context, t *importTarget, r uploadRequest) (uploadResult, error) {
	c, ok := parseTerraformPath(r.Asset.Path)
	if !ok {
		return uploadResult{}, fmt.Errorf("%w: %q is not a terraform module or provider", errImportUsage, r.Asset.Path)
	}

	apiPath := c.apiPath(r.Dst)
	if protocols := r.Asset.Extra[terraformProtocolsKey]; protocols != "" && !c.module {
		apiPath += "?protocols=" + url.QueryEscape(protocols)
	}

	hdr := r.Meta.header()
	hdr.Set("Content-Type", "application/octet-stream")

	length := r.Asset.Size
	if length <= 0 {
		length = -1
	}

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
