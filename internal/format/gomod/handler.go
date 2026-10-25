// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gomod

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"strings"

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

// moduleFile is the root go.mod of a module zip.
type moduleFile struct {
	entry      string // zip entry name, "<module>@<version>/go.mod"
	content    string
	modulePath string // from the module line
}

// parseModuleZipAt reads the root go.mod of a module zip in place, so an upload
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
