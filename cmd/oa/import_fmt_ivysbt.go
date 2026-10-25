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
	"path"
	"strings"
	"sync"

	"github.com/openartifact/openartifact/internal/pathseg"
)

// Ivy and sbt repositories are Artifactory-only sources (Nexus has neither
// format). OpenArtifact stores whatever path is PUT to
// /repos/{ivy,sbt}/<repo>/<path>: the file keeps its source path, so Ivy
// layouts (`org/module/rev/jars/x.jar`, sbt's `org/module/scala_2.13/sbt_1.0/rev/...`)
// and Maven layouts (`com/acme/lib_2.13/1.0/lib_2.13-1.0.jar`) both land as the
// source has them. Everything that is a file of a module is payload, ivy.xml
// descriptors and POMs included (OpenArtifact serves exactly what is PUT);
// checksum sidecars and maven-metadata.xml are not.
//
// The upload is an overwrite-in-place PUT answering 201 {id, sha256}, so a
// failed upload can always be repeated, and a copy that fails verification is
// deleted again with the format's DELETE.
//
//nolint:gochecknoinits // formats register themselves with the importer (see registerFormat)
func init() {
	registerFormat(ivySBTDef(formatIvy, ivyListedFilename))
	registerFormat(ivySBTDef(formatSBT, sbtListedFilename))
}

func ivySBTDef(f importFormat, listed func(filename string) string) formatDef {
	prefix := "/repos/" + string(f) + "/"

	return formatDef{
		format:      f,
		artifactory: []string{string(f)},
		importable:  importableIvySBT,
		overwrites:  true,
		present:     ivySBTPresence(listed),
		upload: func(ctx context.Context, t *importTarget, r uploadRequest) (uploadResult, error) {
			return putOverwriting(ctx, t, prefix+url.PathEscape(r.Dst)+"/"+escapePath(r.Asset.Path), r)
		},
		remove: func(ctx context.Context, t *importTarget, dst string, a sourceAsset, _ uploadResult) error {
			return t.deletePath(ctx, prefix+url.PathEscape(dst)+"/"+escapePath(a.Path))
		},
	}
}

// putOverwriting PUTs the asset to an endpoint that stores it in place and
// answers 201 {id, sha256} (ivy, sbt, conan). A 409 means the repository keeps
// its releases immutable and the destination holds different bytes at that
// address; it is reported as a conflict with the server's own explanation.
func putOverwriting(ctx context.Context, t *importTarget, apiPath string, r uploadRequest) (uploadResult, error) {
	res, err := t.putFile(ctx, apiPath, r.Asset, r.Body, r.Meta, true)
	if se, ok := errors.AsType[*httpStatusError](err); ok && se.status == http.StatusConflict {
		return uploadResult{}, fmt.Errorf("%w: %w", errUploadConflict, err)
	}

	return res, err
}

// importableIvySBT keeps the files of a module: what Maven treats as payload
// (no checksum sidecars, no maven-metadata.xml, at least four path segments)
// that OpenArtifact also accepts as an address (no empty segment).
func importableIvySBT(p string) bool {
	_, ok := splitIvySBTPath(p)

	return ok && importableMaven(p)
}

// ivySBTCoords is how OpenArtifact reads the address of an ivy or sbt file.
type ivySBTCoords struct {
	group    string
	module   string
	version  string
	filename string // may hold a subdirectory ("jars/lib.jar")
}

// splitIvySBTPath reads a source path the way the server does (see
// SplitArtifactPath in internal/format/base, which a test keeps this in step
// with): Maven layout when the file name starts with <module>-<version>,
// otherwise the first three segments are group, module and version and the
// rest is the file name. It fails below four segments or on a segment
// pathseg.Segments rejects (empty, ".", "..", backslash, NUL).
func splitIvySBTPath(p string) (ivySBTCoords, bool) {
	parts := strings.Split(p, "/")
	if len(parts) < 4 || !pathseg.Segments(parts) {
		return ivySBTCoords{}, false
	}

	if n := len(parts); n > 4 && strings.HasPrefix(parts[n-1], parts[n-3]+"-"+strings.TrimSuffix(parts[n-2], "-SNAPSHOT")) {
		return ivySBTCoords{group: strings.Join(parts[:n-3], "."), module: parts[n-3], version: parts[n-2], filename: parts[n-1]}, true
	}

	return ivySBTCoords{group: parts[0], module: parts[1], version: parts[2], filename: strings.Join(parts[3:], "/")}, true
}

// The package listing does not carry the repository path of an ivy or sbt file
// but its coordinates: name "<group>:<module>", the version and a file name.
// The sbt row lists the stored file name. The ivy row lists the stored file name
// followed by its extension again ("lib-1.0.jar.jar", see the ivy branch of
// internal/database/queries/components.sql): that is the name an ivy asset is
// looked up under.

func sbtListedFilename(filename string) string { return filename }

func ivyListedFilename(filename string) string {
	return filename + "." + strings.TrimPrefix(path.Ext(filename), ".")
}

// ivySBTPresence recognises an asset the destination already holds: a listed
// file with the same coordinates and file name as the asset's path, and the
// same SHA-256. A different digest at the same address is not present: the
// upload replaces it, as OpenArtifact's PUT does.
func ivySBTPresence(listed func(filename string) string) func(*targetIndex, sourceAsset) (indexEntry, bool) {
	var memo ivySBTListingMemo

	return func(ix *targetIndex, a sourceAsset) (indexEntry, bool) {
		c, ok := splitIvySBTPath(a.Path)
		if !ok || a.SHA256 == "" {
			return indexEntry{}, false
		}

		e, ok := memo.table(ix)[ivySBTKey(c.group, c.module, c.version, listed(c.filename))]
		if !ok || e.SHA256 != a.SHA256 {
			return indexEntry{}, false
		}

		return e, true
	}
}

func ivySBTKey(group, module, version, listedFilename string) string {
	return group + "\x00" + module + "\x00" + version + "\x00" + listedFilename
}

// ivySBTListingMemo indexes a repository listing by file coordinates once, so
// that checking every asset costs a map lookup instead of a scan of the whole
// listing. It remembers the last listing it was built for: an import handles
// one repository at a time and its jobs share one targetIndex.
type ivySBTListingMemo struct {
	mu    sync.Mutex
	ix    *targetIndex
	index map[string]indexEntry
}

func (m *ivySBTListingMemo) table(ix *targetIndex) map[string]indexEntry {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.ix != ix {
		m.index = make(map[string]indexEntry, len(ix.all))

		for _, p := range ix.all {
			if group, module, ok := strings.Cut(p.Name, ":"); ok {
				m.index[ivySBTKey(group, module, p.Version, p.Filename)] = indexEntry{ID: p.ID, SHA256: p.SHA256}
			}
		}

		m.ix = ix
	}

	return m.index
}
