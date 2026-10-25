// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/openartifact/openartifact/internal/pathseg"
	pkgnuget "github.com/openartifact/openartifact/pkg/nuget"
)

// nugetMaxNuspecBytes bounds the .nuspec read out of a package (the server accepts
// at most 4 MiB).
const nugetMaxNuspecBytes = 4 << 20

var errNuGetPackage = errors.New("not a valid NuGet package")

// nugetInvalid reports a package the importer cannot read. The cause is
// flattened into the message on purpose: a wrapped io.ErrUnexpectedEOF would
// make the importer retry a failure that repeating cannot fix.
func nugetInvalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errNuGetPackage, fmt.Sprintf(format, args...))
}

// NuGet packages are identified by the id and version in their own .nuspec,
// which is also what the server files them under; the importer reads the same
// two fields from the spooled archive instead of trusting the source's file
// names (Nexus keeps no extension, Artifactory joins id and version with a
// dot that ids may contain).
func init() { //nolint:gochecknoinits // every format registers itself with the importer
	registerFormat(formatDef{
		format: formatNuGet, nexus: []string{"nuget"}, artifactory: []string{"nuget"},
		importable: importableNuGet, match: matchSHA, present: nugetPresent, components: true,
		upload: nugetUpload,
		remove: nugetRemove,
	})
}

// nugetPresent recognises a package the destination already holds: by content
// digest when the source reports a SHA-256, otherwise by id and version.
// Nexus lists NuGet assets with a SHA-1 and a SHA-512 only, so without this a
// re-run would try every package again and fail each with a conflict. The
// price is that a copy of the same id and version with other bytes is taken
// for present when there is no digest to compare.
func nugetPresent(ix *targetIndex, a sourceAsset) (indexEntry, bool) {
	if a.SHA256 != "" {
		e, ok := ix.bySHA[a.SHA256]

		return e, ok
	}

	id, version := nugetSourceIdentity(a)

	normalized, ok := pkgnuget.Normalize(version)
	if id == "" || !ok {
		return indexEntry{}, false
	}

	// The destination lists <lower-case id>.<normalized version>.nupkg.
	e, found := ix.byPath[strings.ToLower(id)+"."+normalized+".nupkg"]

	return e, found
}

// nugetSourceIdentity is the id and version the source gives an asset without
// opening it: the Nexus component, the Artifactory nuget.id/nuget.version
// properties, else the path (<id>/<version> on Nexus, <id>.<version>.nupkg on
// Artifactory). Empty when the path does not tell.
func nugetSourceIdentity(a sourceAsset) (string, string) {
	if a.Component.Name != "" && a.Component.Version != "" {
		return a.Component.Name, a.Component.Version
	}

	if id, version := nugetProp(a, "nuget.id"), nugetProp(a, "nuget.version"); id != "" && version != "" {
		return id, version
	}

	if !strings.HasSuffix(strings.ToLower(a.Path), ".nupkg") {
		id, version, _ := strings.Cut(a.Path, "/")

		return id, version
	}

	// <id>.<version>: ids contain dots, versions start with a number. The
	// shortest id that leaves a valid version wins.
	file := strings.TrimSuffix(path.Base(a.Path), path.Ext(a.Path))
	for i := 1; i < len(file); i++ {
		if file[i] != '.' {
			continue
		}

		if _, ok := pkgnuget.Normalize(file[i+1:]); ok {
			return file[:i], file[i+1:]
		}
	}

	return "", ""
}

func nugetProp(a sourceAsset, key string) string {
	if v := a.Props[key]; len(v) > 0 {
		return v[0]
	}

	return ""
}

// importableNuGet selects package archives: Artifactory names them
// <id>.<version>.nupkg, Nexus <id>/<version> without an extension. Symbol
// packages (.snupkg, .symbols.nupkg) carry the id and version of the package
// they belong to and would collide with it.
func importableNuGet(p string) bool {
	if lower := strings.ToLower(p); strings.HasSuffix(lower, ".nupkg") {
		return !strings.HasSuffix(lower, ".symbols.nupkg")
	}

	id, version, ok := strings.Cut(p, "/")
	if !ok || id == "" || strings.Contains(version, "/") {
		return false
	}

	_, valid := pkgnuget.Normalize(version)

	return valid
}

func nugetUpload(ctx context.Context, t *importTarget, r uploadRequest) (uploadResult, error) {
	sp, err := spool(r.Body)
	if err != nil {
		return uploadResult{}, err
	}

	defer func() { _ = sp.Close() }()

	id, version, err := nugetIdentity(sp.Reader(), sp.Size)
	if err != nil {
		return uploadResult{}, fmt.Errorf("%s: %w", r.Asset.Path, err)
	}

	body, contentType := multipartStreamFields(nil, sp.Reader(), "package", id+"."+version+".nupkg")

	defer func() { _ = body.Close() }()

	hdr := r.Meta.header()
	hdr.Set("Content-Type", contentType)

	resp, err := t.send(ctx, http.MethodPut, "/repos/"+url.PathEscape(r.Dst)+"/v3/package", body, -1, hdr)
	if err != nil {
		return uploadResult{}, err
	}

	_ = resp.Body.Close()

	// The push answers 201 without a body; the listing has the stored digest.
	return t.storedResult(ctx, r.Dst, formatNuGet, id, func(p listedPackage) bool {
		return strings.EqualFold(p.Name, id) && p.Version == version
	})
}

// nugetIdentity returns the package id and the normalized version from the
// root-level .nuspec of a .nupkg.
func nugetIdentity(r io.ReaderAt, size int64) (string, string, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return "", "", nugetInvalid("%v", err)
	}

	var nuspec *zip.File

	for _, f := range zr.File {
		if nugetRootNuspec(f.Name) {
			if nuspec != nil {
				return "", "", nugetInvalid("several root .nuspec files")
			}

			nuspec = f
		}
	}

	if nuspec == nil {
		return "", "", nugetInvalid("no root .nuspec")
	}

	rc, err := nuspec.Open()
	if err != nil {
		return "", "", nugetInvalid("%v", err)
	}

	defer func() { _ = rc.Close() }()

	var doc struct {
		Metadata struct {
			ID      string `xml:"id"`
			Version string `xml:"version"`
		} `xml:"metadata"`
	}

	if err := xml.NewDecoder(io.LimitReader(rc, nugetMaxNuspecBytes)).Decode(&doc); err != nil {
		return "", "", nugetInvalid("reading .nuspec: %v", err)
	}

	id := strings.TrimSpace(doc.Metadata.ID)

	version, ok := pkgnuget.Normalize(doc.Metadata.Version)
	if id == "" || !ok {
		return "", "", nugetInvalid(".nuspec has id %q and version %q", doc.Metadata.ID, doc.Metadata.Version)
	}

	return id, version, nil
}

// nugetRootNuspec reports whether a zip entry is a .nuspec at the package root;
// nested ones are package content.
func nugetRootNuspec(name string) bool {
	return len(name) > len(".nuspec") && pathseg.Name(name) && strings.HasSuffix(strings.ToLower(name), ".nuspec")
}

// nugetRemove deletes the version the upload created, addressed by the id and
// version the listing shows for the package id.
func nugetRemove(ctx context.Context, t *importTarget, dst string, _ sourceAsset, res uploadResult) error {
	p, ok, err := t.findStored(ctx, dst, formatNuGet, "", func(p listedPackage) bool { return p.ID == res.ID })
	if err != nil {
		return err
	}

	if !ok {
		return fmt.Errorf("%w: package %q is not listed in %q", errNotFound, res.ID, dst)
	}

	return t.deletePath(ctx, "/repos/"+url.PathEscape(dst)+"/v3/package/"+url.PathEscape(p.Name)+"/"+url.PathEscape(p.Version))
}
