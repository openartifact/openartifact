// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gomod

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/openartifact/openartifact/internal/format"
	"github.com/openartifact/openartifact/internal/format/base"
	"golang.org/x/mod/modfile"
)

// maxGoModFileBytes caps the decompressed go.mod entry read from an uploaded
// module zip, preventing a decompression bomb from exhausting memory.
const maxGoModFileBytes = 4 << 20 // 4 MiB

// goModFileName is the name of the module file at the root of a module.
const goModFileName = "go.mod"

var (
	errNoGoMod      = errors.New("zip does not contain a root go.mod file")
	errNoModuleLine = errors.New("go.mod has no module line")
)

type GoModHandler struct{}

func NewGoModHandler() *GoModHandler {
	return &GoModHandler{}
}

func (h *GoModHandler) Name() string {
	return "go"
}

func (h *GoModHandler) GetContentType() string {
	return "application/zip"
}

func (h *GoModHandler) GetFileExtension() string {
	return ".zip"
}

// Validate checks if the artifact is a valid zip containing a root go.mod.
func (h *GoModHandler) Validate(ctx context.Context, r io.Reader) error {
	ra, size, err := base.ReaderAtLimited(r, maxGoModUploadBytes)
	if err != nil {
		return fmt.Errorf("failed to read zip: %w", err)
	}

	_, err = parseModuleZipAt(ra, size)

	return err
}

// Parse extracts the module path and go.mod from the module zip.
func (h *GoModHandler) Parse(ctx context.Context, r io.Reader) (map[string]any, error) {
	ra, size, err := base.ReaderAtLimited(r, maxGoModUploadBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read zip: %w", err)
	}

	mf, err := parseModuleZipAt(ra, size)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"module_path": mf.modulePath,
		"go_mod":      mf.content,
		"go_mod_path": mf.entry,
	}, nil
}

// moduleFile is the root go.mod of a module zip.
type moduleFile struct {
	entry      string // zip entry name, "<module>@<version>/go.mod"
	content    string
	modulePath string // from the module line
}

// parseModuleZip opens a module zip held in memory and reads its root go.mod.
func parseModuleZip(data []byte) (*moduleFile, error) {
	return parseModuleZipAt(bytes.NewReader(data), int64(len(data)))
}

// parseModuleZipAt is parseModuleZip for a zip read in place, so an upload
// spooled to disk is not loaded into memory to find its go.mod.
func parseModuleZipAt(r io.ReaderAt, size int64) (*moduleFile, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("invalid zip file: %w", err)
	}

	f := rootGoMod(zr.File)
	if f == nil {
		return nil, errNoGoMod
	}

	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to open go.mod: %w", err)
	}
	defer func() { _ = rc.Close() }()

	content, err := base.ReadAllLimited(rc, maxGoModFileBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read go.mod: %w", err)
	}

	modulePath := modfile.ModulePath(content)
	if modulePath == "" {
		return nil, errNoModuleLine
	}

	return &moduleFile{entry: f.Name, content: string(content), modulePath: modulePath}, nil
}

// rootGoMod returns the go.mod at the root of a module zip, or nil. A module
// zip stores every file under "<module>@<version>/" and omits nested modules,
// so the root go.mod is the one with the fewest path elements; entries that
// merely end in "go.mod" (such as "foo-go.mod") are not go.mod files.
func rootGoMod(files []*zip.File) *zip.File {
	var root *zip.File

	depth := 0

	for _, f := range files {
		if f.Name != goModFileName && !strings.HasSuffix(f.Name, "/"+goModFileName) {
			continue
		}

		if d := strings.Count(f.Name, "/"); root == nil || d < depth {
			root, depth = f, d
		}
	}

	return root
}

var _ format.FormatHandler = (*GoModHandler)(nil)
