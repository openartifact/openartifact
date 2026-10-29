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

			return t.putFile(ctx, p, r.Asset, r.Body, r.Meta, true)
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

// pacmanDefaultDir is the architecture directory an "any" package is uploaded
// to when its source does not name one: the destination refuses os/any, and
// serves an "any" package under every architecture whichever directory holds it.
const pacmanDefaultDir = "x86_64"

// pacmanAnyDir is the architecture directory for an "any" package: the one the
// source path names (<prefix>/os/<arch>/<file>), else pacmanDefaultDir. The
// OpenArtifact listing carries no directory, so its packages land in the default.
func pacmanAnyDir(assetPath string) string {
	dir := path.Dir(assetPath)
	if arch := path.Base(dir); arch != "." && arch != "/" && arch != "any" && path.Base(path.Dir(dir)) == "os" {
		return arch
	}

	return pacmanDefaultDir
}

// pacmanPath is the repository path of a package file or signature: the
// architecture comes from the file name, except for an "any" package, which has
// to go into the directory of a real architecture (see pacmanAnyDir).
func pacmanPath(repo, assetPath string) (string, bool) {
	file := path.Base(assetPath)

	arch, ok := pacmanArch(file)
	if !ok {
		return "", false
	}

	if arch == "any" {
		arch = pacmanAnyDir(assetPath)
	}

	return "/repos/" + url.PathEscape(repo) + "/os/" + url.PathEscape(arch) + "/" + url.PathEscape(file), true
}
