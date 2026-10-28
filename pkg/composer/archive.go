// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package composer

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// Limits on what ReadArchive accepts from an untrusted dist zip. Only
// composer.json is ever decompressed, so these bound the central directory
// parse and that one member.
const (
	// maxEntries bounds the number of entries of the archive.
	maxEntries = 200_000
	// MaxManifestBytes bounds composer.json.
	MaxManifestBytes = 4 << 20

	manifestName = "composer.json"
)

// ErrInvalidArchive marks a dist zip that cannot be published; its message says
// what is wrong and is meant for the publisher.
var ErrInvalidArchive = errors.New("invalid dist archive")

func invalidArchive(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidArchive, fmt.Sprintf(format, args...))
}

// Manifest is what a dist zip says about its package: the composer.json it
// carries, verbatim, and the fields a repository reads from it.
type Manifest struct {
	// JSON is composer.json as stored in the archive (a JSON object).
	JSON json.RawMessage
	// Name is the "name" field, a valid package name.
	Name string
	// Version is the optional "version" field, "" when absent.
	Version string
	// Licenses are the identifiers of the "license" field (a string or a list).
	Licenses []string
}

// ReadArchive reads composer.json from a dist zip: from the archive root, or,
// when the root has none, from the single top-level directory the whole archive
// lives in (the layout of GitHub zipballs). It validates the package name.
func ReadArchive(r io.ReaderAt, size int64) (*Manifest, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, invalidArchive("not a zip archive: %v", err)
	}

	if len(zr.File) > maxEntries {
		return nil, invalidArchive("too many entries (%d)", len(zr.File))
	}

	f := findManifest(zr.File)
	if f == nil {
		return nil, invalidArchive("no %s at the archive root or in its single top-level directory", manifestName)
	}

	raw, err := readMember(f)
	if err != nil {
		return nil, err
	}

	return parseManifest(raw)
}

// findManifest locates composer.json: at the root first, then inside the one
// top-level directory every entry of the archive shares.
func findManifest(files []*zip.File) *zip.File {
	var (
		root, nested *zip.File
		rootFiles    bool
		tops         = map[string]bool{}
	)

	for _, f := range files {
		name := path.Clean(strings.ReplaceAll(f.Name, "\\", "/"))
		if name == "." || name == "__MACOSX" || strings.HasPrefix(name, "__MACOSX/") {
			continue
		}

		dir := f.FileInfo().IsDir()
		first, rest, inDir := strings.Cut(name, "/")

		switch {
		case inDir:
			tops[first] = true

			if !dir && rest == manifestName {
				nested = f
			}
		case dir:
			tops[first] = true
		default:
			rootFiles = true

			if name == manifestName {
				root = f
			}
		}
	}

	if root != nil {
		return root
	}

	if !rootFiles && len(tops) == 1 {
		return nested
	}

	return nil
}

// readMember decompresses one zip member under MaxManifestBytes.
func readMember(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > MaxManifestBytes {
		return nil, invalidArchive("%s is larger than %d bytes", manifestName, MaxManifestBytes)
	}

	rc, err := f.Open()
	if err != nil {
		return nil, invalidArchive("cannot open %s: %v", manifestName, err)
	}

	defer func() { _ = rc.Close() }()

	raw, err := io.ReadAll(io.LimitReader(rc, MaxManifestBytes+1))
	if err != nil {
		return nil, invalidArchive("cannot read %s: %v", manifestName, err)
	}

	if len(raw) > MaxManifestBytes {
		return nil, invalidArchive("%s is larger than %d bytes", manifestName, MaxManifestBytes)
	}

	return raw, nil
}

// parseManifest validates the bytes of a composer.json.
func parseManifest(raw []byte) (*Manifest, error) {
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, invalidArchive("%s is not a JSON object", manifestName)
	}

	var m Manifest

	if err := stringField(fields, "name", &m.Name); err != nil || m.Name == "" {
		return nil, invalidArchive("%s has no \"name\"", manifestName)
	}

	if !ValidName(m.Name) {
		return nil, invalidArchive("the package name %q is not a valid lower-case vendor/package name", m.Name)
	}

	if err := stringField(fields, "version", &m.Version); err != nil {
		return nil, invalidArchive("the \"version\" of %s is not a string", manifestName)
	}

	licenses, err := licenseField(fields["license"])
	if err != nil {
		return nil, invalidArchive("the \"license\" of %s is neither a string nor a list of strings", manifestName)
	}

	m.JSON = bytes.Clone(raw)
	m.Licenses = licenses

	return &m, nil
}

// stringField reads an optional string member.
func stringField(fields map[string]json.RawMessage, key string, dst *string) error {
	raw, ok := fields[key]
	if !ok || string(raw) == "null" {
		return nil
	}

	return json.Unmarshal(raw, dst) //nolint:wrapcheck // the caller reports the field
}

// licenseField reads composer.json's "license": one identifier or a list.
func licenseField(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var one string
	if json.Unmarshal(raw, &one) == nil {
		return nonEmpty([]string{one}), nil
	}

	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, fmt.Errorf("license: %w", err)
	}

	return nonEmpty(many), nil
}

func nonEmpty(in []string) []string {
	out := in[:0]

	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}

	return out
}
