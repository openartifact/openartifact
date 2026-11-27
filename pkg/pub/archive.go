// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pub

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/limitio"
)

// Limits on what ReadArchive accepts from an untrusted upload.
const (
	// MaxArchiveBytes bounds the compressed archive of one release (pub.dev's limit).
	MaxArchiveBytes = 100 << 20
	// MaxUncompressedBytes bounds the total size of the files of an archive.
	MaxUncompressedBytes = 256 << 20
	// MaxPubspecBytes bounds pubspec.yaml (pub.dev's limit).
	MaxPubspecBytes = 128 << 10
	// MaxPubspecJSONBytes bounds the JSON a pubspec expands to; YAML aliases can
	// make it far larger than the source.
	MaxPubspecJSONBytes = 1 << 20
	// maxEntries bounds the number of tar entries.
	maxEntries = 100_000
	// maxDepth bounds the nesting of a pubspec.
	maxDepth = 32
	// tarOverheadSlack is what tar framing may add to the file data of a
	// maxEntries archive.
	tarOverheadSlack = 1<<20 + maxEntries*2048

	pubspecName = "pubspec.yaml"
)

// ErrInvalid marks an archive that is not a valid package; its message says
// what is wrong and is meant for the publisher.
var ErrInvalid = errors.New("invalid package")

// errShape marks a pubspec whose shape cannot be represented as JSON.
var errShape = errors.New("unsupported pubspec shape")

// errTooLarge stops a tar stream that inflates past what the limits allow,
// including bytes no entry accounts for (extension headers).
var errTooLarge = errors.New("tar stream too large")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Package is what a package archive says about itself.
type Package struct {
	Name    string
	Version string
	// Pubspec is pubspec.yaml as JSON.
	Pubspec json.RawMessage
}

// ReadArchive parses the gzipped tar archive of a release and checks it: the
// pubspec.yaml at its root (a valid package name that is not a reserved word,
// and a valid version) and the shape of the tar stream. An error wraps
// ErrInvalid when the archive is at fault, and is an I/O error otherwise.
func ReadArchive(r io.Reader) (*Package, error) {
	data, err := scanArchive(r)
	if err != nil {
		return nil, err
	}

	return ParsePubspec(data)
}

// scanArchive reads the whole tar stream once and returns the pubspec.yaml.
func scanArchive(r io.Reader) ([]byte, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, invalid("not a gzip-compressed tar archive: %v", err)
	}

	defer func() { _ = zr.Close() }()

	tr := tar.NewReader(limitio.Capped(zr, MaxUncompressedBytes+tarOverheadSlack, errTooLarge))

	var (
		spec    []byte
		entries int
		total   int64
	)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if errors.Is(err, errTooLarge) {
			return nil, invalid("contents larger than %d bytes", MaxUncompressedBytes)
		}

		if err != nil {
			return nil, invalid("unreadable tar archive: %v", err)
		}

		if entries++; entries > maxEntries {
			return nil, invalid("more than %d entries", maxEntries)
		}

		data, err := readEntry(tr, hdr, &total)
		if err != nil {
			return nil, err
		}

		if data != nil {
			if spec != nil {
				return nil, invalid("%s appears twice", pubspecName)
			}

			spec = data
		}
	}

	if spec == nil {
		return nil, invalid("%s is missing at the archive root", pubspecName)
	}

	return spec, nil
}

// readEntry accounts for one tar entry and returns its content when it is the
// pubspec.yaml, nil otherwise.
func readEntry(tr *tar.Reader, hdr *tar.Header, total *int64) ([]byte, error) {
	name, ok := cleanName(hdr.Name)
	if !ok {
		return nil, invalid("entry %q has an unsafe name", hdr.Name)
	}

	switch hdr.Typeflag {
	case tar.TypeDir, tar.TypeXGlobalHeader:
		return nil, nil
	case tar.TypeReg:
	default:
		return nil, invalid("entry %q is not a regular file or directory", hdr.Name)
	}

	if *total += hdr.Size; *total > MaxUncompressedBytes {
		return nil, invalid("contents larger than %d bytes", MaxUncompressedBytes)
	}

	if name != pubspecName {
		return nil, drain(tr)
	}

	if hdr.Size > MaxPubspecBytes {
		return nil, invalid("%s is larger than %d bytes", pubspecName, MaxPubspecBytes)
	}

	var buf bytes.Buffer
	if _, err := iocopy.Copy(&buf, tr); err != nil {
		return nil, readError(err)
	}

	return append([]byte{}, buf.Bytes()...), nil
}

func drain(tr *tar.Reader) error {
	if _, err := iocopy.Copy(io.Discard, tr); err != nil {
		return readError(err)
	}

	return nil
}

func readError(err error) error {
	if errors.Is(err, errTooLarge) {
		return invalid("contents larger than %d bytes", MaxUncompressedBytes)
	}

	return invalid("unreadable tar archive: %v", err)
}

// cleanName normalizes a tar entry name; ok is false for a name that is absolute
// or escapes the archive root.
func cleanName(name string) (string, bool) {
	if name == "" || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") {
		return "", false
	}

	cleaned := path.Clean(name)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}

	return cleaned, true
}

// ParsePubspec reads pubspec.yaml and checks the identity it states. The YAML
// is untrusted: it is size-bounded, its nesting is bounded, only the first
// document counts, and the JSON it expands to is bounded too.
func ParsePubspec(data []byte) (*Package, error) {
	if len(data) > MaxPubspecBytes {
		return nil, invalid("%s is larger than %d bytes", pubspecName, MaxPubspecBytes)
	}

	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, invalid("%s is not valid YAML: %v", pubspecName, err)
	}

	norm, err := normalize(doc, 0)
	if err != nil {
		return nil, invalid("%s: %v", pubspecName, err)
	}

	spec, ok := norm.(map[string]any)
	if !ok {
		return nil, invalid("%s is not a mapping", pubspecName)
	}

	name, _ := spec["name"].(string)
	if !ValidName(name) {
		return nil, invalid("the package name %q is not valid: use 1 to %d lower-case letters, digits and underscores,"+
			" not starting with a digit", name, MaxNameLen)
	}

	if IsReserved(name) {
		return nil, invalid("the package name %q is a reserved word", name)
	}

	version, _ := spec["version"].(string)
	if !ValidVersion(version) {
		return nil, invalid("the package version %q is not a valid semantic version", version)
	}

	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, invalid("%s cannot be represented as JSON: %v", pubspecName, err)
	}

	if len(raw) > MaxPubspecJSONBytes {
		return nil, invalid("%s expands to more than %d bytes", pubspecName, MaxPubspecJSONBytes)
	}

	return &Package{Name: name, Version: version, Pubspec: raw}, nil
}

// normalize turns a decoded YAML value into one encoding/json takes: mappings
// get string keys, timestamps become text, and nesting is bounded.
func normalize(v any, depth int) (any, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("%w: nested deeper than %d levels", errShape, maxDepth)
	}

	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))

		for k, e := range t {
			n, err := normalize(e, depth+1)
			if err != nil {
				return nil, err
			}

			out[k] = n
		}

		return out, nil
	case map[any]any:
		out := make(map[string]any, len(t))

		for k, e := range t {
			key, ok := scalarKey(k)
			if !ok {
				return nil, fmt.Errorf("%w: a mapping key is not a scalar", errShape)
			}

			n, err := normalize(e, depth+1)
			if err != nil {
				return nil, err
			}

			out[key] = n
		}

		return out, nil
	case []any:
		out := make([]any, len(t))

		for i, e := range t {
			n, err := normalize(e, depth+1)
			if err != nil {
				return nil, err
			}

			out[i] = n
		}

		return out, nil
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano), nil
	case []byte:
		return string(t), nil
	}

	return v, nil
}

// scalarKey spells a scalar mapping key.
func scalarKey(k any) (string, bool) {
	switch t := k.(type) {
	case string:
		return t, true
	case nil, map[string]any, map[any]any, []any:
		return "", false
	}

	return fmt.Sprint(k), true
}
