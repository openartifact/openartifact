// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"
)

// Three source layouts are imported, with "_" for an absent user or channel.
//
// Conan 2, and Conan 1 with revisions enabled, in Artifactory:
//
//	<user>/<name>/<version>/<channel>/<rrev>/export/<file>
//	<user>/<name>/<version>/<channel>/<rrev>/package/<pid>/<prev>/<file>
//
// is PUT to the v2 endpoints, to the recipe or package revision it belongs to
// (revision "0" included, which is how a revisionless Conan 1 server numbers
// its content).
//
// Conan 1 without revisions has no revision directory. Artifactory:
//
//	<user>/<name>/<version>/<channel>/export/<file>
//	<user>/<name>/<version>/<channel>/package/<pid>/<file>
//
// and Nexus, which names the user first below a `conans/` root and keeps the
// six files a Conan 1 client uploads:
//
//	conans/<user>/<name>/<version>/<channel>/<file>
//	conans/<user>/<name>/<version>/<channel>/packages/<pid>/<file>
//
// are PUT to OpenArtifact's v1 file endpoint, which stores them as recipe
// revision "0" and package revision "0", exactly what a Conan 1 client's own
// upload does. The v1 upload_urls handshake is not used: it would attach a
// package to the newest recipe revision of the destination, not to revision 0
// that the source package belongs to.
//
// The index.json files Artifactory keeps at every level, dot-directories and
// any other Nexus asset are not importable: OpenArtifact regenerates them or
// cannot install from them.
//
// A recipe revision becomes visible once its conanmanifest.txt is stored, and a
// package revision can only be uploaded after it; each recipe revision is
// therefore one unit that uploads the recipe files, then every package revision,
// with the manifest of each group last.
//
//nolint:gochecknoinits // formats register themselves with the importer (see registerFormat)
func init() {
	registerFormat(formatDef{
		format:      formatConan,
		nexus:       []string{"conan"},
		artifactory: []string{"conan"},
		importable:  importableConan,
		overwrites:  true,
		present:     presentConan,
		units:       conanUnits,
		upload:      uploadConan,
		// OpenArtifact deletes whole revisions only, which would also destroy
		// files that were stored correctly. A re-run uploads the bad file again
		// over the stored one instead.
		remove: func(context.Context, *importTarget, string, sourceAsset, uploadResult) error {
			return errNoRemove(formatConan)
		},
	})
}

const conanManifest = "conanmanifest.txt"

// conan1Revision numbers the recipe and package revision of a Conan 1 source,
// which has none: it is what OpenArtifact stores a Conan 1 upload as.
const conan1Revision = "0"

// conanFile is one file of a recipe or package revision.
type conanFile struct {
	user, name, version, channel, rrev string
	pid, prev                          string // empty for a recipe file
	file                               string // below export/ or the package revision
	v1                                 bool   // a Conan 1 source: rrev and prev are conan1Revision
}

// parseConanPath reads a source path of any of the layouts of the format
// comment. The shapes cannot be mistaken for one another: a Conan 2 path has
// export or package after its revision, an Artifactory Conan 1 path directly
// after the channel, and a Nexus one starts with conans and has no kind
// directory.
func parseConanPath(p string) (conanFile, bool) {
	parts := strings.Split(p, "/")
	if len(parts) < 6 || slices.Contains(parts, "") {
		return conanFile{}, false
	}

	if c, ok := parseConan2Path(parts); ok {
		return c, true
	}

	if c, ok := parseArtifactoryConan1Path(parts); ok {
		return c, true
	}

	return parseNexusConan1Path(parts)
}

func parseConan2Path(parts []string) (conanFile, bool) {
	if len(parts) < 7 || parts[4] == "export" || parts[4] == "package" {
		return conanFile{}, false
	}

	c := conanFile{user: parts[0], name: parts[1], version: parts[2], channel: parts[3], rrev: parts[4]}

	switch {
	case parts[5] == "export":
		c.file = strings.Join(parts[6:], "/")
	case parts[5] == "package" && len(parts) >= 9:
		c.pid, c.prev, c.file = parts[6], parts[7], strings.Join(parts[8:], "/")
	default:
		return conanFile{}, false
	}

	return c, true
}

func parseArtifactoryConan1Path(parts []string) (conanFile, bool) {
	c := conanFile{user: parts[0], name: parts[1], version: parts[2], channel: parts[3], rrev: conan1Revision, v1: true}

	switch {
	case parts[4] == "export":
		c.file = strings.Join(parts[5:], "/")
	case parts[4] == "package" && len(parts) >= 7:
		c.pid, c.prev, c.file = parts[5], conan1Revision, strings.Join(parts[6:], "/")
	default:
		return conanFile{}, false
	}

	return c, true
}

func parseNexusConan1Path(parts []string) (conanFile, bool) {
	if parts[0] != "conans" {
		return conanFile{}, false
	}

	c := conanFile{user: parts[1], name: parts[2], version: parts[3], channel: parts[4], rrev: conan1Revision, v1: true}

	switch {
	case len(parts) == 6:
		c.file = parts[5]
	case len(parts) == 8 && parts[5] == "packages":
		c.pid, c.prev, c.file = parts[6], conan1Revision, parts[7]
	default:
		return conanFile{}, false
	}

	if !isConan1File(c.file) {
		return conanFile{}, false
	}

	return c, true
}

// isConan1File names the files a Conan 1 client uploads, which are the only
// assets a Nexus conan repository holds.
func isConan1File(name string) bool {
	switch name {
	case "conanfile.py", conanManifest, "conan_export.tgz", "conan_sources.tgz", "conaninfo.txt", "conan_package.tgz":
		return true
	}

	return false
}

func importableConan(p string) bool {
	if _, ok := parseConanPath(p); !ok || path.Base(p) == "index.json" {
		return false
	}

	return !slices.ContainsFunc(strings.Split(p, "/"), func(s string) bool { return strings.HasPrefix(s, ".") })
}

func (c conanFile) isManifest() bool { return c.file == conanManifest }

func (c conanFile) revisionKey() string {
	return strings.Join([]string{c.user, c.name, c.version, c.channel, c.rrev}, "/")
}

// listedFilename is the file name of the package listing's row for the
// recipe revision: "<name>/<version>@<user>/<channel>#<rrev>".
func (c conanFile) listedFilename() string {
	return c.name + "/" + c.version + "@" + c.user + "/" + c.channel + "#" + c.rrev
}

// uploadPath is the OpenArtifact address of the file in repository dst.
func (c conanFile) uploadPath(dst string) string {
	if c.v1 {
		return c.v1UploadPath(dst)
	}

	var b strings.Builder

	b.WriteString("/repos/" + url.PathEscape(dst) + "/v2/conans/")
	b.WriteString(url.PathEscape(c.name) + "/" + url.PathEscape(c.version) + "/")
	b.WriteString(url.PathEscape(c.user) + "/" + url.PathEscape(c.channel))
	b.WriteString("/revisions/" + url.PathEscape(c.rrev))

	if c.pid != "" {
		b.WriteString("/packages/" + url.PathEscape(c.pid) + "/revisions/" + url.PathEscape(c.prev))
	}

	b.WriteString("/files/" + escapePath(c.file))

	return b.String()
}

// v1UploadPath is the address a Conan 1 client PUTs the file to, the URL the
// server's upload_urls answer would name: name/version/user/channel first, then
// the revision and export/ or package/<pid>/<prev>/.
func (c conanFile) v1UploadPath(dst string) string {
	var b strings.Builder

	b.WriteString("/repos/" + url.PathEscape(dst) + "/v1/files/")

	for _, seg := range []string{c.name, c.version, c.user, c.channel, c.rrev} {
		b.WriteString(url.PathEscape(seg) + "/")
	}

	if c.pid == "" {
		b.WriteString("export/")
	} else {
		b.WriteString("package/" + url.PathEscape(c.pid) + "/" + url.PathEscape(c.prev) + "/")
	}

	b.WriteString(escapePath(c.file))

	return b.String()
}

// presentConan: the package listing has one row per recipe revision, carrying
// the digest of its conanmanifest.txt, so only that file can be recognised as
// already imported. Every other file is stored again: the PUT replaces it in
// place with identical bytes.
func presentConan(ix *targetIndex, a sourceAsset) (indexEntry, bool) {
	c, ok := parseConanPath(a.Path)
	if !ok || c.pid != "" || !c.isManifest() || a.SHA256 == "" {
		return indexEntry{}, false
	}

	e, ok := ix.byPath[c.listedFilename()]
	if !ok || e.SHA256 != a.SHA256 {
		return indexEntry{}, false
	}

	return e, true
}

// conanUnits makes one unit of every recipe revision with all its package
// revisions, ordered so that nothing is uploaded before what OpenArtifact
// requires: the recipe files, its manifest last, then each package revision's
// files, its manifest last. Units keep the order of their first asset.
func conanUnits(assets []sourceAsset) [][]sourceAsset {
	type member struct {
		asset sourceAsset
		file  conanFile
	}

	var (
		keys   []string
		groups = map[string][]member{}
		units  [][]sourceAsset
	)

	for _, a := range assets {
		c, ok := parseConanPath(a.Path)
		if !ok {
			units = append(units, []sourceAsset{a})
			continue
		}

		k := c.revisionKey()
		if _, seen := groups[k]; !seen {
			keys = append(keys, k)
		}

		groups[k] = append(groups[k], member{a, c})
	}

	for _, k := range keys {
		ms := groups[k]
		slices.SortStableFunc(ms, func(x, y member) int { return compareConanUploadOrder(x.file, y.file) })

		unit := make([]sourceAsset, len(ms))
		for i, m := range ms {
			unit[i] = m.asset
		}

		units = append(units, unit)
	}

	return units
}

// compareConanUploadOrder sorts the recipe files (no package id) before the
// package revisions, package revisions by id, and the manifest after the other
// files of its group.
func compareConanUploadOrder(a, b conanFile) int {
	return cmp.Or(
		strings.Compare(a.pid, b.pid),
		strings.Compare(a.prev, b.prev),
		cmp.Compare(a.manifestRank(), b.manifestRank()),
		strings.Compare(a.file, b.file),
	)
}

// manifestRank is 1 for the manifest of a group, 0 for its other files.
func (c conanFile) manifestRank() int {
	if c.isManifest() {
		return 1
	}

	return 0
}

// uploadConan PUTs the file to its recipe or package revision. The answer names
// the file's own record; for a recipe revision's manifest that record is the
// row the package listing shows, so its properties show on the revision.
func uploadConan(ctx context.Context, t *importTarget, r uploadRequest) (uploadResult, error) {
	c, ok := parseConanPath(r.Asset.Path)
	if !ok {
		return uploadResult{}, fmt.Errorf("%w: %q is not a Conan file path", errImportUsage, r.Asset.Path)
	}

	return putOverwriting(ctx, t, c.uploadPath(r.Dst), r)
}
