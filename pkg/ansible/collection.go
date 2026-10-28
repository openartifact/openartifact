// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ansible

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/openartifact/openartifact/internal/iocopy"
)

// Limits on what Read accepts from an untrusted artifact.
const (
	// maxDocBytes bounds MANIFEST.json and FILES.json.
	maxDocBytes = 8 << 20
	// maxRuntimeBytes bounds meta/runtime.yml.
	maxRuntimeBytes = 1 << 20
	// maxEntries bounds the number of tar entries.
	maxEntries = 100_000
	// MaxUncompressedBytes bounds the total size of the regular files in the
	// archive, which Read has to decompress and hash.
	MaxUncompressedBytes = 4 << 30

	manifestName = "MANIFEST.json"
	filesName    = "FILES.json"
	runtimeName  = "meta/runtime.yml"
)

// ErrInvalid marks an artifact that is not a valid collection; its message
// says what is wrong and is meant for the publisher.
var ErrInvalid = errors.New("invalid collection")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Collection is what a collection artifact says about itself.
type Collection struct {
	Namespace string
	Name      string
	Version   string
	// Dependencies maps "namespace.name" to a version requirement.
	Dependencies map[string]string
	// Info is the collection_info object reduced to the fields Galaxy serves
	// (authors, description, license, tags, links, dependencies, identity).
	Info json.RawMessage
	// Licenses are the license identifiers of collection_info.
	Licenses []string
	// RequiresAnsible is requires_ansible of meta/runtime.yml, "" when absent.
	RequiresAnsible string
}

// manifest is MANIFEST.json.
type manifest struct {
	CollectionInfo map[string]any `json:"collection_info"`
	FileManifest   struct {
		Name     string `json:"name"`
		FType    string `json:"ftype"`
		ChksumT  string `json:"chksum_type"`
		Checksum string `json:"chksum_sha256"`
	} `json:"file_manifest_file"`
	Format int `json:"format"`
}

// filesDoc is FILES.json.
type filesDoc struct {
	Files []struct {
		Name     string  `json:"name"`
		FType    string  `json:"ftype"`
		ChksumT  *string `json:"chksum_type"`
		Checksum *string `json:"chksum_sha256"`
	} `json:"files"`
	Format int `json:"format"`
}

// archive is what the single pass over the tar collects.
type archive struct {
	manifest []byte
	files    []byte
	runtime  []byte
	// sums maps each regular file to its SHA-256.
	sums map[string]string
}

// Read parses the gzipped tar artifact of a collection and checks it: the
// MANIFEST.json (valid namespace, name, semantic version, dependencies), the
// FILES.json it points to by checksum, and every file of the archive against
// the checksum FILES.json lists. An error wraps ErrInvalid when the artifact is
// at fault, and is an I/O error otherwise.
func Read(r io.Reader) (*Collection, error) {
	arc, err := scan(r)
	if err != nil {
		return nil, err
	}

	if arc.manifest == nil {
		return nil, invalid("%s is missing", manifestName)
	}

	if arc.files == nil {
		return nil, invalid("%s is missing", filesName)
	}

	col, err := parseManifest(arc.manifest)
	if err != nil {
		return nil, err
	}

	if err := verifyFiles(arc); err != nil {
		return nil, err
	}

	if arc.runtime != nil {
		col.RequiresAnsible = requiresAnsible(arc.runtime)
	}

	return col, nil
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

// scan reads the archive once: it keeps the three documents and hashes every
// regular file.
func scan(r io.Reader) (*archive, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, invalid("not a gzip-compressed tar archive: %v", err)
	}
	defer func() { _ = zr.Close() }()

	arc := &archive{sums: map[string]string{}}
	tr := tar.NewReader(zr)
	entries := 0

	var total int64

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return arc, nil
		}

		if err != nil {
			return nil, invalid("unreadable tar archive: %v", err)
		}

		if entries++; entries > maxEntries {
			return nil, invalid("more than %d entries", maxEntries)
		}

		if err := arc.entry(tr, hdr, &total); err != nil {
			return nil, err
		}
	}
}

// entry records one tar entry.
func (a *archive) entry(tr *tar.Reader, hdr *tar.Header, total *int64) error {
	name, ok := cleanName(hdr.Name)
	if !ok {
		return invalid("entry %q has an unsafe name", hdr.Name)
	}

	switch hdr.Typeflag {
	case tar.TypeDir:
		return nil
	case tar.TypeReg:
	default:
		return invalid("entry %q is not a regular file or directory", hdr.Name)
	}

	if *total += hdr.Size; *total > MaxUncompressedBytes {
		return invalid("contents larger than %d bytes", int64(MaxUncompressedBytes))
	}

	if _, dup := a.sums[name]; dup {
		return invalid("entry %q appears twice", name)
	}

	var keep *[]byte

	switch name {
	case manifestName:
		keep = &a.manifest
	case filesName:
		keep = &a.files
	case runtimeName:
		keep = &a.runtime
	}

	h := sha256.New()
	dst := io.Writer(h)

	var buf bytes.Buffer

	if keep != nil {
		limit := int64(maxDocBytes)
		if name == runtimeName {
			limit = maxRuntimeBytes
		}

		if hdr.Size > limit {
			return invalid("%s is larger than %d bytes", name, limit)
		}

		dst = io.MultiWriter(h, &buf)
	}

	if _, err := iocopy.Copy(dst, tr); err != nil {
		return invalid("unreadable tar archive: %v", err)
	}

	a.sums[name] = hex.EncodeToString(h.Sum(nil))

	if keep != nil {
		*keep = buf.Bytes()
		if *keep == nil {
			*keep = []byte{}
		}
	}

	return nil
}

// parseManifest validates MANIFEST.json and returns what it says.
func parseManifest(data []byte) (*Collection, error) {
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, invalid("%s is not valid JSON: %v", manifestName, err)
	}

	if m.Format != 1 {
		return nil, invalid("%s format %d is not supported", manifestName, m.Format)
	}

	if m.CollectionInfo == nil {
		return nil, invalid("%s has no collection_info", manifestName)
	}

	info := m.CollectionInfo
	col := &Collection{
		Namespace: str(info["namespace"]),
		Name:      str(info["name"]),
		Version:   str(info["version"]),
	}

	if !ValidName(col.Namespace) {
		return nil, invalid("namespace %q must be lower-case letters, digits and underscores, not starting with a digit", col.Namespace)
	}

	if !ValidName(col.Name) {
		return nil, invalid("name %q must be lower-case letters, digits and underscores, not starting with a digit", col.Name)
	}

	if !ValidVersion(col.Version) {
		return nil, invalid("version %q is not a semantic version (MAJOR.MINOR.PATCH)", col.Version)
	}

	deps, err := dependencies(info["dependencies"])
	if err != nil {
		return nil, err
	}

	col.Dependencies = deps
	col.Licenses = stringList(info["license"])
	col.Info = reduceInfo(info, deps)

	return col, nil
}

// dependencies reads collection_info.dependencies: namespace.name to a version
// requirement.
func dependencies(v any) (map[string]string, error) {
	out := map[string]string{}

	if v == nil {
		return out, nil
	}

	raw, ok := v.(map[string]any)
	if !ok {
		return nil, invalid("dependencies must be a mapping of namespace.name to a version requirement")
	}

	for key, val := range raw {
		spec, isStr := val.(string)
		if _, _, valid := SplitFQCN(key); !valid {
			return nil, invalid("dependency %q is not a namespace.name collection", key)
		}

		if !isStr || !ValidRequirement(spec) {
			return nil, invalid("dependency %q has an invalid version requirement", key)
		}

		out[key] = spec
	}

	return out, nil
}

// verifyFiles checks FILES.json against the checksum in MANIFEST.json and the
// archive against FILES.json.
func verifyFiles(arc *archive) error {
	var m manifest
	if err := json.Unmarshal(arc.manifest, &m); err != nil {
		return invalid("%s is not valid JSON: %v", manifestName, err)
	}

	want := m.FileManifest.Checksum
	if m.FileManifest.ChksumT != "sha256" || want == "" {
		return invalid("%s does not carry a sha256 checksum of %s", manifestName, filesName)
	}

	if got := arc.sums[filesName]; !strings.EqualFold(got, want) {
		return invalid("%s does not match the checksum in %s", filesName, manifestName)
	}

	var doc filesDoc
	if err := json.Unmarshal(arc.files, &doc); err != nil {
		return invalid("%s is not valid JSON: %v", filesName, err)
	}

	listed := make(map[string]bool, len(doc.Files))

	for _, f := range doc.Files {
		name, ok := cleanName(f.Name)
		if !ok {
			return invalid("%s lists %q, an unsafe name", filesName, f.Name)
		}

		if f.FType == "dir" {
			continue
		}

		if f.FType != "file" || f.ChksumT == nil || *f.ChksumT != "sha256" || f.Checksum == nil {
			return invalid("%s lists %q without a sha256 checksum", filesName, f.Name)
		}

		got, present := arc.sums[name]

		switch {
		case !present:
			return invalid("%q is listed in %s but missing from the archive", f.Name, filesName)
		case !strings.EqualFold(got, *f.Checksum):
			return invalid("%q does not match its checksum in %s", f.Name, filesName)
		}

		listed[name] = true
	}

	for name := range arc.sums {
		if name != manifestName && name != filesName && !listed[name] {
			return invalid("%q is in the archive but not listed in %s", name, filesName)
		}
	}

	return nil
}

// requiresAnsible reads requires_ansible of meta/runtime.yml; a runtime file
// that does not parse counts as having none.
func requiresAnsible(data []byte) string {
	var rt struct {
		RequiresAnsible string `yaml:"requires_ansible"` //nolint:tagliatelle // the key of meta/runtime.yml
	}

	if err := yaml.Unmarshal(data, &rt); err != nil || len(rt.RequiresAnsible) > MaxRequirementLen {
		return ""
	}

	return rt.RequiresAnsible
}

// infoFields are the collection_info fields served as version metadata.
func infoFields() []string {
	return []string{
		"namespace", "name", "version", "authors", "readme", "tags", "description", "license", "license_file",
		"repository", "documentation", "homepage", "issues",
	}
}

// reduceInfo keeps the served fields of collection_info and normalizes their
// types (text fields to strings, lists to string lists), so a hostile
// manifest cannot make the stored document large or oddly shaped.
func reduceInfo(info map[string]any, deps map[string]string) json.RawMessage {
	fields := infoFields()
	out := make(map[string]any, len(fields)+1)

	for _, k := range fields {
		switch k {
		case "authors", "tags", "license":
			out[k] = clip(stringList(info[k]))
		default:
			out[k] = clipText(str(info[k]))
		}
	}

	out["dependencies"] = deps

	b, err := json.Marshal(out)
	if err != nil {
		return json.RawMessage(`{}`)
	}

	return b
}

const (
	maxInfoText  = 4096
	maxInfoItems = 64
)

func clipText(s string) string {
	if len(s) > maxInfoText {
		return s[:maxInfoText]
	}

	return s
}

func clip(l []string) []string {
	l = slices.Clone(l)
	if len(l) > maxInfoItems {
		l = l[:maxInfoItems]
	}

	for i := range l {
		l[i] = clipText(l[i])
	}

	return l
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// stringList reads a JSON string or list of strings (license is either).
func stringList(v any) []string {
	switch x := v.(type) {
	case string:
		if x == "" {
			return []string{}
		}

		return []string{x}
	case []any:
		out := make([]string, 0, len(x))

		for _, e := range x {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}

		return out
	}

	return []string{}
}
