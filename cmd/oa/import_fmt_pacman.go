// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"
)

func init() { //nolint:gochecknoinits // every format registers itself with the importer
	registerFormat(formatDef{
		format:     formatPacman,
		importable: importablePacman, match: matchSHA, overwrites: true,
		upload: func(ctx context.Context, t *importTarget, r uploadRequest) (uploadResult, error) {
			p, ok := pacmanPath(r.Dst, r.Asset.Path)
			if !ok {
				return uploadResult{}, fmt.Errorf("%w: %q is not a pacman package file", errImportUsage, r.Asset.Path)
			}

			return t.putFile(ctx, p, r.Asset, r.Body, r.Meta, false)
		},
		remove: func(ctx context.Context, t *importTarget, dst string, a sourceAsset, _ uploadResult) error {
			p, ok := pacmanPath(dst, a.Path)
			if !ok {
				return fmt.Errorf("%w: %q is not a pacman package file", errImportUsage, a.Path)
			}

			return t.deletePath(ctx, p)
		},
	})
}

// pacmanExts are the package file extensions of a pacman repository.
func pacmanExts() []string { return []string{".pkg.tar.zst", ".pkg.tar.xz", ".pkg.tar.gz"} }

// pacmanPackageFile reports whether name is a package file or its detached
// signature (<package>.sig).
func pacmanPackageFile(name string) bool {
	name = strings.TrimSuffix(name, ".sig")

	for _, ext := range pacmanExts() {
		if strings.HasSuffix(name, ext) && len(name) > len(ext) {
			return true
		}
	}

	return false
}

// importablePacman accepts package files; the signatures and the db/files
// databases are not payload (the target regenerates the databases).
func importablePacman(p string) bool {
	return pacmanPackageFile(path.Base(p)) && !strings.HasSuffix(p, ".sig")
}

// pacmanArch returns the architecture a package file name ends in
// (<name>-<pkgver>-<pkgrel>-<arch>.pkg.tar.<comp>[.sig]).
func pacmanArch(file string) (string, bool) {
	file = strings.TrimSuffix(file, ".sig")

	for _, ext := range pacmanExts() {
		stem, ok := strings.CutSuffix(file, ext)
		if !ok {
			continue
		}

		i := strings.LastIndexByte(stem, '-')
		if i < 0 || i == len(stem)-1 {
			return "", false
		}

		return stem[i+1:], true
	}

	return "", false
}

// pacmanPath is the repository path of a package file or signature: the
// architecture comes from the file name.
func pacmanPath(repo, assetPath string) (string, bool) {
	file := path.Base(assetPath)

	arch, ok := pacmanArch(file)
	if !ok {
		return "", false
	}

	return "/repos/" + url.PathEscape(repo) + "/os/" + url.PathEscape(arch) + "/" + url.PathEscape(file), true
}
