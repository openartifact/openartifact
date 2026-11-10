// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/M0Rf30/pkgformat/rpm"
)

type UploadResult struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Release   string `json:"release"`
	Arch      string `json:"arch"`
	SHA256    string `json:"sha256"`
	BlobID    string `json:"blob_id"`
	Size      int64  `json:"size"`
	Duplicate bool   `json:"duplicate"`
}

type RepositoryHandler struct{}

func NewRepositoryHandler() *RepositoryHandler {
	return &RepositoryHandler{}
}

func (rh *RepositoryHandler) ValidatePackage(ctx context.Context, data io.Reader) (*rpm.Package, error) {
	pkg, err := rpm.ReadPackage(data)
	if err != nil {
		return nil, fmt.Errorf("invalid package: %w", err)
	}

	if pkg.Name == "" {
		return nil, errors.New("package name is required") //nolint:err113 // static error
	}

	if pkg.Version == "" {
		return nil, errors.New("package version is required") //nolint:err113 // static error
	}

	if pkg.Arch == "" {
		return nil, errors.New("package architecture is required") //nolint:err113 // static error
	}

	return pkg, nil
}

func (rh *RepositoryHandler) GetPackageInfo(pkg *rpm.Package) map[string]any {
	if pkg == nil {
		return make(map[string]any)
	}

	metadata := map[string]any{
		"format":        "rpm",
		"name":          pkg.Name,
		"version":       pkg.Version,
		"release":       pkg.Release,
		"epoch":         pkg.Epoch,
		"architecture":  pkg.Arch,
		"summary":       pkg.Summary,
		"description":   pkg.Description,
		"license":       pkg.License,
		"url":           pkg.URL,
		"vendor":        pkg.Vendor,
		"group":         pkg.Group,
		"packager":      pkg.Packager,
		"sourcerpm":     pkg.SourceRPM,
		"buildhost":     pkg.BuildHost,
		"buildtime":     pkg.BuildTime,
		"packagesize":   pkg.PackageSize,
		"installedsize": pkg.InstalledSize,
		"archivesize":   pkg.ArchiveSize,
	}

	for key, deps := range map[string][]rpm.Dependency{
		"provides": pkg.Provides, "requires": pkg.Requires, "conflicts": pkg.Conflicts, "obsoletes": pkg.Obsoletes,
		"recommends": pkg.Recommends, "suggests": pkg.Suggests, "supplements": pkg.Supplements, "enhances": pkg.Enhances,
	} {
		if len(deps) > 0 {
			metadata[key] = deps
		}
	}

	if len(pkg.Files) > 0 {
		metadata["filecount"] = len(pkg.Files)
	}

	if pkg.FilesTruncated {
		metadata["files_truncated"] = true
	}

	if len(pkg.Changelogs) > 0 {
		metadata["changelogcount"] = len(pkg.Changelogs)
	}

	return metadata
}
