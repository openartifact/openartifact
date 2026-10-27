// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"net/http"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	formatapk "github.com/openartifact/openartifact/internal/format/apk"
	pkgapk "github.com/openartifact/openartifact/pkg/apk"
)

// apkIndexMaxAge is the cache lifetime of a merged index, as the hosted handler's.
const apkIndexMaxAge = 60

// WithApkKeys enables the public key downloads of apk groups: keys resolves the
// key of the group itself (*apkkeys.Service), the one its merged indexes are
// signed with.
func (h *Handler) WithApkKeys(keys formatapk.KeyProvider) *Handler {
	h.apkKeys = keys
	return h
}

// apkPath is the classification of a request path of an apk group.
type apkPath struct {
	class pathClass
	// dir is the <branch>/<repo>/<arch> directory of an index request.
	dir string
}

// classifyApk splits <branch>/<repo>/<arch>/<file>: the index of a directory is
// generated metadata, a <name>-<version>.apk is a package file, the key is
// served from the group's key. Anything else does not exist.
func classifyApk(relPath string) apkPath {
	parts := strings.Split(strings.Trim(relPath, "/"), "/")
	if len(parts) != 4 {
		return apkPath{}
	}

	for _, seg := range parts[:3] {
		if !pkgapk.ValidSegment(seg) {
			return apkPath{}
		}
	}

	if parts[3] == formatapk.IndexName {
		return apkPath{class: classMetadata, dir: strings.Join(parts[:3], "/")}
	}

	if _, _, ok := pkgapk.ParseFileName(parts[3]); ok {
		return apkPath{class: classFile}
	}

	return apkPath{}
}

// serveApk answers a request of an apk group: the group's public key, the
// merged signed index of a directory from the group's own generated rows, and
// package files from the member the merged index lists them for. readable are
// the members the caller may read: the index they are served lists only those
// members' packages.
func (h *Handler) serveApk(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	readable []*models.Repository,
	relPath string,
	be Backend,
) {
	if h.metadata == nil || h.blobs == nil {
		http.Error(w, "apk groups are not available", http.StatusNotImplemented)
		return
	}

	// The merged index lists every member's packages; a caller who can read
	// none of them has no business with it.
	if len(readable) == 0 {
		http.NotFound(w, r)
		return
	}

	if formatapk.IsKeyPath(relPath) {
		formatapk.ServeKey(w, r, h.log, h.apkKeys, grp, strings.Trim(relPath, "/"))
		return
	}

	p := classifyApk(relPath)
	if p.class == classNone {
		http.NotFound(w, r)
		return
	}

	ids := make([]string, len(readable))
	for i, m := range readable {
		ids[i] = m.ID
	}

	scope, ok, err := h.metadata.Scope(r.Context(), grp.ID, ids)
	if err != nil {
		h.log.Errorw("failed to scope group metadata", "group", grp.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if !ok {
		http.NotFound(w, r)
		return
	}

	relPath = strings.Trim(relPath, "/")

	if p.class == classFile {
		h.serveGroupFile(w, r, grp, readable, scope, relPath, be)
		return
	}

	// A miss on an index generates the group's metadata, naming the directory
	// so that a proxy member's is looked for.
	h.serveGroupMetadata(w, r, grp, debRPMPath{
		class: classMetadata, dist: p.dir, anchor: true, contentType: "application/gzip", maxAge: apkIndexMaxAge,
	}, relPath, scope, ids)
}
