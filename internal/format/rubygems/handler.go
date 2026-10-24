// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rubygems

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/openartifact/openartifact/internal/format"
	"github.com/openartifact/openartifact/pkg/rubymarshal"
	"go.yaml.in/yaml/v3"
)

type RubyGemsHandler struct{}

func NewRubyGemsHandler() *RubyGemsHandler {
	return &RubyGemsHandler{}
}

func (h *RubyGemsHandler) Name() string {
	return "rubygems"
}

func (h *RubyGemsHandler) GetContentType() string {
	return "application/octet-stream"
}

func (h *RubyGemsHandler) GetFileExtension() string {
	return ".gem"
}

// Validate checks if the artifact is a valid .gem file (tar.gz format).
func (h *RubyGemsHandler) Validate(ctx context.Context, r io.Reader) error {
	_, err := h.parseGemMetadata(r)
	if err != nil {
		return fmt.Errorf("invalid gem file: %w", err)
	}

	return nil
}

// Parse extracts metadata from the .gem file.
func (h *RubyGemsHandler) Parse(ctx context.Context, r io.Reader) (map[string]any, error) {
	metadata, err := h.parseGemMetadata(r)
	if err != nil {
		return nil, fmt.Errorf("failed to parse gem file: %w", err)
	}

	return metadata, nil
}

// parseGemMetadata extracts metadata from a .gem file.
//
// .gem files are tar archives containing metadata.gz and data.tar.gz. The
// metadata.gz payload is a Ruby Marshal 4.8 dump of the Gem::Specification
// for gems built the way this registry's own upload/legacy-index endpoints
// expect (see pkg/rubymarshal). Real modern `gem build`/`gem push` instead
// write metadata.gz as YAML (parsed by parseYAMLGemspec); JSON is kept as a
// further fallback and a raw_metadata passthrough is the last resort.
func (h *RubyGemsHandler) parseGemMetadata(r io.Reader) (map[string]any, error) {
	tr := tar.NewReader(r)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("failed to read tar: %w", err)
		}

		// Look for metadata.gz file
		if header.Name == "metadata.gz" {
			gzr, err := gzip.NewReader(tr)
			if err != nil {
				return nil, fmt.Errorf("failed to read metadata.gz: %w", err)
			}
			defer func() { _ = gzr.Close() }()

			data, err := io.ReadAll(io.LimitReader(gzr, maxMetadataBytes+1))
			if err != nil {
				return nil, fmt.Errorf("failed to read metadata content: %w", err)
			}

			if len(data) > maxMetadataBytes {
				return nil, errMetadataTooLarge
			}

			return parseGemspecMetadata(data), nil
		}
	}

	// If no metadata.gz found, return minimal metadata
	return map[string]any{
		"name": "unknown",
	}, nil
}

// parseGemspecMetadata decodes a metadata.gz payload, preferring Marshal 4.8
// (a real Gem::Specification dump), falling back to JSON, and finally to a
// raw passthrough. See parseGemMetadata's doc comment for scope.
func parseGemspecMetadata(data []byte) map[string]any {
	if spec, err := rubymarshal.DecodeSpecification(data); err == nil {
		return gemspecToMetadata(spec)
	}

	// A JSON null decodes without error but leaves the map nil: not an object,
	// so not a gemspec.
	metadata := make(map[string]any)
	if err := json.Unmarshal(data, &metadata); err == nil && metadata != nil {
		return metadata
	}

	if m := parseYAMLGemspec(data); m != nil {
		return m
	}

	return map[string]any{"raw_metadata": string(data)}
}

func gemspecToMetadata(spec *rubymarshal.Specification) map[string]any {
	deps := make([]map[string]any, len(spec.Dependencies))
	for i, d := range spec.Dependencies {
		deps[i] = map[string]any{
			"name":        d.Name,
			"requirement": d.Requirement,
			"type":        d.Type,
		}
	}

	authors := make([]any, len(spec.Authors))
	for i, a := range spec.Authors {
		authors[i] = a
	}

	licenses := make([]any, len(spec.Licenses))
	for i, l := range spec.Licenses {
		licenses[i] = l
	}

	return map[string]any{
		"name":         spec.Name,
		"version":      spec.Version,
		"platform":     spec.Platform,
		"summary":      spec.Summary,
		"description":  spec.Description,
		"homepage":     spec.Homepage,
		"email":        spec.Email,
		"authors":      authors,
		"licenses":     licenses,
		"dependencies": deps,
	}
}

var _ format.FormatHandler = (*RubyGemsHandler)(nil)

const maxMetadataBytes = 4 << 20

var errMetadataTooLarge = errors.New("gem metadata.gz exceeds maximum decompressed size")

// parseYAMLGemspec decodes the YAML Gem::Specification that `gem build`
// writes (tagged with !ruby/object:... nodes) into the metadata shape used by
// gemspecToMetadata. It returns nil when the document has no gem name.
func parseYAMLGemspec(data []byte) map[string]any {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil
	}

	name, _ := doc["name"].(string)
	version := yamlVersion(doc["version"])

	if name == "" || version == "" {
		return nil
	}

	platform, _ := doc["platform"].(string)
	if platform == "" {
		platform = "ruby"
	}

	var deps []map[string]any

	if list, ok := doc["dependencies"].([]any); ok {
		for _, item := range list {
			d, ok := item.(map[string]any)
			if !ok {
				continue
			}

			dn, _ := d["name"].(string)
			typ, _ := d["type"].(string)

			deps = append(deps, map[string]any{
				"name":        dn,
				"requirement": yamlRequirement(d["requirement"]),
				"type":        strings.TrimPrefix(typ, ":"),
			})
		}
	}

	return map[string]any{
		"name":         name,
		"version":      version,
		"platform":     platform,
		"summary":      yamlString(doc["summary"]),
		"description":  yamlString(doc["description"]),
		"homepage":     yamlString(doc["homepage"]),
		"email":        yamlString(doc["email"]),
		"authors":      yamlStringList(doc["authors"]),
		"licenses":     yamlStringList(doc["licenses"]),
		"dependencies": deps,
	}
}

func yamlString(v any) string {
	s, _ := v.(string)
	return s
}

func yamlStringList(v any) []any {
	switch t := v.(type) {
	case []any:
		out := make([]any, 0, len(t))

		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}

		return out
	case string:
		return []any{t}
	}

	return []any{}
}

// yamlVersion unwraps a Gem::Version node ({version: "1.0"}) or plain string.
func yamlVersion(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if s, ok := t["version"].(string); ok {
			return s
		}
	}

	return ""
}

// yamlRequirement renders a Gem::Requirement node
// ({requirements: [[">=", {version: "1"}], ...]}) as "op v, op v".
func yamlRequirement(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ">= 0"
	}

	reqs, _ := m["requirements"].([]any)

	parts := make([]string, 0, len(reqs))

	for _, r := range reqs {
		pair, ok := r.([]any)
		if !ok || len(pair) != 2 {
			continue
		}

		op, _ := pair[0].(string)
		parts = append(parts, op+" "+yamlVersion(pair[1]))
	}

	if len(parts) == 0 {
		return ">= 0"
	}

	return strings.Join(parts, ", ")
}

// ParseLicenses returns the licenses a .gem file declares in its gemspec
// (metadata.gz). It is how a stored gem's licenses are read after upload; the
// upload path reads the same field from the same metadata.
func ParseLicenses(ctx context.Context, r io.Reader) ([]string, error) {
	metadata, err := NewRubyGemsHandler().Parse(ctx, r)
	if err != nil {
		return nil, err
	}

	return getStringArrayFromMetadata(metadata, "licenses"), nil
}
