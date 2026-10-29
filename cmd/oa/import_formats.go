// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
)

// matchMode says how the importer recognises that the destination already
// holds an asset.
type matchMode int

const (
	// matchPath: the destination lists the asset under its source path with
	// the same SHA-256 (Maven, raw, ivy, sbt: the path is the address).
	matchPath matchMode = iota
	// matchSHA: the destination lists a package with the same SHA-256 anywhere
	// in the repository (the server derives file names from package metadata).
	matchSHA
)

// uploadRequest is what a format's upload function receives.
type uploadRequest struct {
	Dst   string
	Asset sourceAsset
	// Body streams the asset's content from the source. The upload function
	// must read it to the end (the importer digests what is read through it).
	Body io.Reader
	Meta uploadMeta
}

// formatDef is everything the importer knows about one OpenArtifact format:
// which source repositories map to it, which source files are payload, how a
// file is written to the destination and how an earlier copy is recognised.
// Each format registers itself from an init function in its own file.
type formatDef struct {
	format importFormat
	// nexus and artifactory name the source repository formats (Nexus
	// "format", Artifactory "packageType", lower case) that import into this
	// format.
	nexus       []string
	artifactory []string
	// importable reports whether a source path (no leading slash, already
	// filtered for Artifactory dot-directories) is a payload file.
	importable func(p string) bool
	match      matchMode
	// present overrides match when a package is not identified by its path or
	// digest alone (docker tags).
	present func(ix *targetIndex, a sourceAsset) (indexEntry, bool)
	// components asks Nexus sources to attach the component identity
	// (sourceAsset.Component) to every asset.
	components bool
	// overwrites marks formats whose upload is a PUT that replaces in place, so
	// a failed upload can always be repeated.
	overwrites bool
	// units groups the selected assets into ordered transfer units; a unit is
	// transferred sequentially, units run concurrently. nil: one unit per asset.
	units func(assets []sourceAsset) [][]sourceAsset
	// upload stores one asset. It returns the package id and the SHA-256 the
	// server stored (empty when it cannot tell). Use t.findStored when the
	// upload response carries neither.
	upload func(ctx context.Context, t *importTarget, req uploadRequest) (uploadResult, error)
	// remove deletes a copy that failed verification.
	remove func(ctx context.Context, t *importTarget, dst string, a sourceAsset, res uploadResult) error
	// transferUnit, when set, replaces the per-asset transfer of a unit (docker
	// images, which are not single files). It owns reporting through im.rep and
	// resume bookkeeping through im.state.
	transferUnit func(ctx context.Context, im *importer, j importJob)
}

var (
	formatDefs = map[importFormat]formatDef{} //nolint:gochecknoglobals // registry filled by init functions
	// nexusFormats and artifactoryFormats map a source repository format name
	// to the OpenArtifact format it imports into.
	nexusFormats       = map[string]importFormat{} //nolint:gochecknoglobals // registry filled by init functions
	artifactoryFormats = map[string]importFormat{} //nolint:gochecknoglobals // registry filled by init functions
)

// registerFormat adds a format to the importer. It is called from init
// functions only.
func registerFormat(d formatDef) {
	formatDefs[d.format] = d

	for _, n := range d.nexus {
		nexusFormats[n] = d.format
	}

	for _, a := range d.artifactory {
		artifactoryFormats[a] = d.format
	}
}

func lookupSourceFormat(m map[string]importFormat, name string) (importFormat, bool) {
	f, ok := m[name]

	return f, ok
}

// repoFilePath is the native path of an asset in the /repos/<format>/<repo>/<path>
// layout of the formats whose files keep their source path (maven, raw).
func repoFilePath(f importFormat, dst, assetPath string) string {
	return "/repos/" + string(f) + "/" + url.PathEscape(dst) + "/" + escapePath(assetPath)
}

// uploadRepoFile returns the upload of a format in the /repos/<format>/ layout;
// jsonReply says whether the server answers the PUT with JSON.
func uploadRepoFile(f importFormat, jsonReply bool) func(context.Context, *importTarget, uploadRequest) (uploadResult, error) {
	return func(ctx context.Context, t *importTarget, r uploadRequest) (uploadResult, error) {
		return t.putFile(ctx, repoFilePath(f, r.Dst, r.Asset.Path), r.Asset, r.Body, r.Meta, jsonReply)
	}
}

// removeRepoFile returns the removal of a format in the /repos/<format>/ layout.
func removeRepoFile(f importFormat) func(context.Context, *importTarget, string, sourceAsset, uploadResult) error {
	return func(ctx context.Context, t *importTarget, dst string, a sourceAsset, _ uploadResult) error {
		return t.deletePath(ctx, repoFilePath(f, dst, a.Path))
	}
}

func init() { //nolint:gochecknoinits // the built-in formats register themselves with the importer
	registerFormat(formatDef{
		format: formatMaven, nexus: []string{"maven2"}, artifactory: []string{"maven", "gradle"},
		importable: importableMaven, match: matchPath, overwrites: true,
		upload: uploadRepoFile(formatMaven, false),
		remove: removeRepoFile(formatMaven),
	})
	registerFormat(formatDef{
		format: formatRaw, nexus: []string{"raw"}, artifactory: []string{"generic"},
		importable: func(string) bool { return true }, match: matchPath, overwrites: true,
		upload: uploadRepoFile(formatRaw, true),
		remove: removeRepoFile(formatRaw),
	})
	registerFormat(formatDef{
		format: formatDeb, nexus: []string{"apt"}, artifactory: []string{"debian"},
		importable: importableDeb, match: matchSHA,
		upload: func(ctx context.Context, t *importTarget, r uploadRequest) (uploadResult, error) {
			fields := [][2]string{{"distribution", r.Meta.DebDist}, {"component", r.Meta.DebComponent}}
			return t.postPackage(ctx, "/api/v1/repositories/"+url.PathEscape(r.Dst)+"/debs", r.Asset, r.Body, r.Meta, fields)
		},
		remove: func(ctx context.Context, t *importTarget, dst string, _ sourceAsset, res uploadResult) error {
			return t.deleteByID(ctx, "/api/v1/repositories/"+url.PathEscape(dst)+"/debs/", res.ID)
		},
	})
	registerFormat(formatDef{
		format: formatRPM, nexus: []string{"yum"}, artifactory: []string{"rpm"},
		importable: importableRPM, match: matchSHA,
		upload: func(ctx context.Context, t *importTarget, r uploadRequest) (uploadResult, error) {
			return t.postPackage(ctx, "/api/v1/repositories/"+url.PathEscape(r.Dst)+"/packages", r.Asset, r.Body, r.Meta, nil)
		},
		remove: func(ctx context.Context, t *importTarget, dst string, _ sourceAsset, res uploadResult) error {
			return t.deleteByID(ctx, "/api/v1/repositories/"+url.PathEscape(dst)+"/packages/", res.ID)
		},
	})
}

// errNoRemove is returned for formats that cannot delete a stored copy.
func errNoRemove(f importFormat) error {
	return fmt.Errorf("%w: the %s API cannot remove a stored copy", errImportUsage, f)
}
