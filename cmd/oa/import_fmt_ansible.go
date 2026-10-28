// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"

	pkgansible "github.com/openartifact/openartifact/pkg/ansible"
)

// Collections travel as the raw <namespace>-<name>-<version>.tar.gz through the
// Galaxy v3 publish endpoint, which reads the coordinates out of MANIFEST.json.
func init() { //nolint:gochecknoinits // every format registers itself with the importer
	registerFormat(formatDef{
		format:     formatAnsible,
		importable: func(p string) bool { _, _, _, ok := pkgansible.ParseFileName(path.Base(p)); return ok },
		match:      matchSHA,
		upload:     ansibleUpload,
		remove:     ansibleRemove,
	})
}

// ansibleAPI is the Galaxy API root of a repository.
func ansibleAPI(repo string) string { return "/repos/" + url.PathEscape(repo) + "/api/" }

// ansibleUpload publishes one collection: a multipart POST with the tarball and
// its SHA-256, which the server verifies. The 202 answer names an import task
// and carries no digest, so the digest sent is the one stored.
func ansibleUpload(ctx context.Context, t *importTarget, r uploadRequest) (uploadResult, error) {
	file := path.Base(r.Asset.Path)
	if _, _, _, ok := pkgansible.ParseFileName(file); !ok {
		return uploadResult{}, fmt.Errorf("%w: %q is not a collection file (NAMESPACE-NAME-VERSION.tar.gz)", errImportUsage, r.Asset.Path)
	}

	if r.Asset.SHA256 == "" {
		return uploadResult{}, fmt.Errorf("%w: publishing %q needs its sha256", errImportUsage, file)
	}

	pr, contentType := multipartStreamFields([][2]string{{"sha256", r.Asset.SHA256}}, r.Body, "file", file)
	defer func() { _ = pr.Close() }()

	hdr := r.Meta.header()
	hdr.Set("Content-Type", contentType)

	resp, err := t.send(ctx, http.MethodPost, ansibleAPI(r.Dst)+"v3/artifacts/collections/", pr, -1, hdr)
	if err != nil {
		return uploadResult{}, err
	}

	_ = resp.Body.Close()

	return uploadResult{SHA256: r.Asset.SHA256}, nil
}

// ansibleRemove deletes the collection version a file name denotes.
func ansibleRemove(ctx context.Context, t *importTarget, dst string, a sourceAsset, _ uploadResult) error {
	ns, name, version, ok := pkgansible.ParseFileName(path.Base(a.Path))
	if !ok {
		return fmt.Errorf("%w: %q is not a collection file", errImportUsage, a.Path)
	}

	return t.deletePath(ctx, ansibleAPI(dst)+"v3/collections/"+ns+"/"+name+"/versions/"+url.PathEscape(version)+"/")
}
