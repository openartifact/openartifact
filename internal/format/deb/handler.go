// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/openartifact/openartifact/internal/format"
	"github.com/openartifact/openartifact/pkg/deb"
)

// DebianHandler implements the FormatHandler interface for Debian packages.
type DebianHandler struct {
	name string
}

// NewDebianHandler creates a new Debian format handler.
func NewDebianHandler() *DebianHandler {
	return &DebianHandler{
		name: "deb",
	}
}

// Name returns the format name.
func (h *DebianHandler) Name() string {
	return h.name
}

// Validate checks if the artifact is a valid .deb file.
func (h *DebianHandler) Validate(ctx context.Context, data io.Reader) error {
	// Try to parse the deb file
	_, err := deb.ParseDebFile(data)
	if err != nil {
		return fmt.Errorf("invalid deb file: %w", err)
	}

	return nil
}

// Parse extracts metadata from a .deb file.
func (h *DebianHandler) Parse(ctx context.Context, data io.Reader) (map[string]any, error) {
	debFile, err := deb.ParseDebFile(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse deb file: %w", err)
	}

	if debFile.Package == nil {
		return nil, errors.New("no package metadata found") //nolint:err113 // static error
	}

	metadata := debFile.GetMetadata()

	// Add format-specific metadata
	metadata["format"] = h.Name()
	metadata["file_type"] = "deb"

	return metadata, nil
}

// GetContentType returns the MIME type for Debian packages.
func (h *DebianHandler) GetContentType() string {
	return "application/x-deb"
}

// GetFileExtension returns the typical file extension.
func (h *DebianHandler) GetFileExtension() string {
	return ".deb"
}

// Ensure DebianHandler implements FormatHandler.
var _ format.FormatHandler = (*DebianHandler)(nil)

// RepositoryHandler handles Debian repository operations.
type RepositoryHandler struct {
	handler *DebianHandler
}

// NewRepositoryHandler creates a new Debian repository handler.
func NewRepositoryHandler() *RepositoryHandler {
	return &RepositoryHandler{
		handler: NewDebianHandler(),
	}
}

// ValidatePackage validates a package for upload.
func (rh *RepositoryHandler) ValidatePackage(ctx context.Context, data io.Reader) (*deb.DebFile, error) {
	debFile, err := deb.ParseDebFile(data)
	if err != nil {
		return nil, fmt.Errorf("invalid package: %w", err)
	}

	if debFile.Package == nil {
		return nil, errors.New("no package metadata found") //nolint:err113 // static error
	}

	// Validate required fields
	if debFile.Package.Package == "" {
		return nil, errors.New("package name is required") //nolint:err113 // static error
	}

	if debFile.Package.Version == "" {
		return nil, errors.New("package version is required") //nolint:err113 // static error
	}

	if debFile.Package.Architecture == "" {
		return nil, errors.New("package architecture is required") //nolint:err113 // static error
	}

	if debFile.Package == nil {
		return nil, errors.New("no package metadata found") //nolint:err113 // static error
	}

	// Validate required fields
	if debFile.Package.Package == "" {
		return nil, errors.New("package name is required") //nolint:err113 // static error
	}

	if debFile.Package.Version == "" {
		return nil, errors.New("package version is required") //nolint:err113 // static error
	}

	if debFile.Package.Architecture == "" {
		return nil, errors.New("package architecture is required") //nolint:err113 // static error
	}

	return debFile, nil
}

// GetPackageInfo returns package information.
func (rh *RepositoryHandler) GetPackageInfo(debFile *deb.DebFile) map[string]any {
	if debFile == nil || debFile.Package == nil {
		return make(map[string]any)
	}

	return debFile.GetMetadata()
}

// GeneratePackagesEntry generates a Packages file entry for a package.
//
//nolint:gocyclo,cyclop // protocol handler: branching mirrors spec state machine
func (rh *RepositoryHandler) GeneratePackagesEntry(debFile *deb.DebFile, filename string, size int64, md5sum, sha1, sha256 string) string {
	if debFile == nil || debFile.Package == nil {
		return ""
	}

	pkg := debFile.Package

	var entry strings.Builder
	fmt.Fprintf(&entry, "Package: %s\n", pkg.Package)
	fmt.Fprintf(&entry, "Version: %s\n", pkg.Version)
	fmt.Fprintf(&entry, "Architecture: %s\n", pkg.Architecture)

	if pkg.Maintainer != "" {
		fmt.Fprintf(&entry, "Maintainer: %s\n", pkg.Maintainer)
	}

	if pkg.InstalledSize > 0 {
		fmt.Fprintf(&entry, "Installed-Size: %d\n", pkg.InstalledSize)
	}

	if pkg.Depends != "" {
		fmt.Fprintf(&entry, "Depends: %s\n", pkg.Depends)
	}

	if pkg.PreDepends != "" {
		fmt.Fprintf(&entry, "Pre-Depends: %s\n", pkg.PreDepends)
	}

	if pkg.Recommends != "" {
		fmt.Fprintf(&entry, "Recommends: %s\n", pkg.Recommends)
	}

	if pkg.Suggests != "" {
		fmt.Fprintf(&entry, "Suggests: %s\n", pkg.Suggests)
	}

	if pkg.Conflicts != "" {
		fmt.Fprintf(&entry, "Conflicts: %s\n", pkg.Conflicts)
	}

	if pkg.Breaks != "" {
		fmt.Fprintf(&entry, "Breaks: %s\n", pkg.Breaks)
	}

	if pkg.Replaces != "" {
		fmt.Fprintf(&entry, "Replaces: %s\n", pkg.Replaces)
	}

	if pkg.Provides != "" {
		fmt.Fprintf(&entry, "Provides: %s\n", pkg.Provides)
	}

	if pkg.Section != "" {
		fmt.Fprintf(&entry, "Section: %s\n", pkg.Section)
	}

	if pkg.Priority != "" {
		fmt.Fprintf(&entry, "Priority: %s\n", pkg.Priority)
	}

	if pkg.Homepage != "" {
		fmt.Fprintf(&entry, "Homepage: %s\n", pkg.Homepage)
	}

	if pkg.Essential {
		entry.WriteString("Essential: yes\n")
	}

	if pkg.MultiArch != "" {
		fmt.Fprintf(&entry, "Multi-Arch: %s\n", pkg.MultiArch)
	}

	if pkg.Source != "" {
		fmt.Fprintf(&entry, "Source: %s\n", pkg.Source)
	}

	if pkg.SourceVersion != "" {
		fmt.Fprintf(&entry, "Source-Version: %s\n", pkg.SourceVersion)
	}

	if pkg.BuiltUsing != "" {
		fmt.Fprintf(&entry, "Built-Using: %s\n", pkg.BuiltUsing)
	}

	// Add file information
	fmt.Fprintf(&entry, "Filename: %s\n", filename)
	fmt.Fprintf(&entry, "Size: %d\n", size)

	if md5sum != "" {
		fmt.Fprintf(&entry, "MD5sum: %s\n", md5sum)
	}

	if sha1 != "" {
		fmt.Fprintf(&entry, "SHA1: %s\n", sha1)
	}

	if sha256 != "" {
		fmt.Fprintf(&entry, "SHA256: %s\n", sha256)
	}

	if pkg.Description != "" {
		fmt.Fprintf(&entry, "Description: %s\n", pkg.Description)
	}

	// Add custom fields
	for k, v := range pkg.Custom {
		fmt.Fprintf(&entry, "%s: %s\n", k, v)
	}

	entry.WriteString("\n")

	return entry.String()
}
