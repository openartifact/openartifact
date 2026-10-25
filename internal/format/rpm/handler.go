// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"context"
	"errors"
	"fmt"
	"io"

	rpm "github.com/openartifact/openartifact/pkg/rpm"
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
	pkg, err := rpm.ParseRPM(data)
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

	return pkg.GetMetadata()
}
