// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Sentinel errors for control file parsing.
var (
	ErrInvalidControlFormat = errors.New("invalid control file format")
	ErrMissingRequiredField = errors.New("required field not found in control file")
)

// ControlFile represents a Debian control file (RFC 822 format).
type ControlFile struct {
	Fields map[string]string
}

// ParseControlFile parses a Debian control file in RFC 822 format
// Control files use field: value format with continuation lines starting with space.
func ParseControlFile(r io.Reader) (*ControlFile, error) {
	cf := &ControlFile{
		Fields: make(map[string]string),
	}

	scanner := bufio.NewScanner(r)
	// Depends/Description lines can exceed bufio's 64 KiB default token size.
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)

	var (
		currentField string
		currentValue strings.Builder
	)

	for scanner.Scan() {
		line := scanner.Text()

		// Empty line marks end of control file
		if line == "" {
			if currentField != "" {
				cf.Fields[currentField] = strings.TrimSpace(currentValue.String())
			}

			break
		}

		// Continuation line (starts with space or tab)
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if currentField != "" {
				currentValue.WriteString("\n")
				currentValue.WriteString(line)
			}

			continue
		}

		// New field
		if currentField != "" {
			cf.Fields[currentField] = strings.TrimSpace(currentValue.String())
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("%w: %s", ErrInvalidControlFormat, line)
		}

		currentField = strings.TrimSpace(parts[0])

		currentValue.Reset()
		currentValue.WriteString(strings.TrimSpace(parts[1]))
	}

	// Don't forget the last field
	if currentField != "" {
		cf.Fields[currentField] = strings.TrimSpace(currentValue.String())
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading control file: %w", err)
	}

	return cf, nil
}

// Get retrieves a field value.
func (cf *ControlFile) Get(field string) string {
	return cf.Fields[field]
}

// GetRequired retrieves a required field value.
func (cf *ControlFile) GetRequired(field string) (string, error) {
	value, ok := cf.Fields[field]
	if !ok || value == "" {
		return "", fmt.Errorf("%w: '%s'", ErrMissingRequiredField, field)
	}

	return value, nil
}

// Package represents a Debian package metadata.
type Package struct {
	Package       string
	Version       string
	Architecture  string
	Maintainer    string
	Description   string
	Depends       string
	PreDepends    string
	Recommends    string
	Suggests      string
	Conflicts     string
	Breaks        string
	Replaces      string
	Provides      string
	Section       string
	Priority      string
	Homepage      string
	Size          int64
	InstalledSize int64
	MD5sum        string
	SHA1          string
	SHA256        string
	Filename      string
	Source        string
	SourceVersion string
	Essential     bool
	MultiArch     string
	BuiltUsing    string
	Custom        map[string]string
}

// ParsePackage extracts package metadata from a control file.
func (cf *ControlFile) ParsePackage() (*Package, error) {
	pkg := &Package{
		Custom: make(map[string]string),
	}

	// Required fields
	var err error

	pkg.Package, err = cf.GetRequired("Package")
	if err != nil {
		return nil, err
	}

	pkg.Version, err = cf.GetRequired("Version")
	if err != nil {
		return nil, err
	}

	pkg.Architecture, err = cf.GetRequired("Architecture")
	if err != nil {
		return nil, err
	}

	// Optional fields
	pkg.Maintainer = cf.Get("Maintainer")
	pkg.Description = cf.Get("Description")
	pkg.Depends = cf.Get("Depends")
	pkg.PreDepends = cf.Get("Pre-Depends")
	pkg.Recommends = cf.Get("Recommends")
	pkg.Suggests = cf.Get("Suggests")
	pkg.Conflicts = cf.Get("Conflicts")
	pkg.Breaks = cf.Get("Breaks")
	pkg.Replaces = cf.Get("Replaces")
	pkg.Provides = cf.Get("Provides")
	pkg.Section = cf.Get("Section")
	pkg.Priority = cf.Get("Priority")
	pkg.Homepage = cf.Get("Homepage")
	pkg.InstalledSize, _ = strconv.ParseInt(cf.Get("Installed-Size"), 10, 64)
	pkg.MD5sum = cf.Get("MD5sum")
	pkg.SHA1 = cf.Get("SHA1")
	pkg.SHA256 = cf.Get("SHA256")
	pkg.Filename = cf.Get("Filename")
	pkg.Source = cf.Get("Source")
	pkg.SourceVersion = cf.Get("Source-Version")
	pkg.Essential = cf.Get("Essential") == "yes"
	pkg.MultiArch = cf.Get("Multi-Arch")
	pkg.BuiltUsing = cf.Get("Built-Using")

	// Store any custom fields
	knownFields := map[string]bool{
		"Package": true, "Version": true, "Architecture": true,
		"Maintainer": true, "Description": true, "Depends": true,
		"Pre-Depends": true, "Recommends": true, "Suggests": true,
		"Conflicts": true, "Breaks": true, "Replaces": true,
		"Provides": true, "Section": true, "Priority": true,
		"Homepage": true, "MD5sum": true, "SHA1": true,
		"SHA256": true, "Filename": true, "Source": true,
		"Source-Version": true, "Essential": true, "Multi-Arch": true,
		"Built-Using": true, "Installed-Size": true,
	}

	for field, value := range cf.Fields {
		if !knownFields[field] {
			pkg.Custom[field] = value
		}
	}

	return pkg, nil
}
