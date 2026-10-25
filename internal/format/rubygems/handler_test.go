// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rubygems

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"testing"

	"github.com/openartifact/openartifact/pkg/rubymarshal"
)

func createGemFile(t *testing.T, metadata map[string]any) []byte {
	t.Helper()

	// Create metadata.gz
	var metaBuf bytes.Buffer

	gw := gzip.NewWriter(&metaBuf)

	if metadata != nil {
		data, _ := json.Marshal(metadata)
		_, _ = gw.Write(data)
	}

	_ = gw.Close()

	// Create outer tar containing metadata.gz
	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)
	metaBytes := metaBuf.Bytes()
	_ = tw.WriteHeader(&tar.Header{Name: "metadata.gz", Mode: 0644, Size: int64(len(metaBytes))})
	_, _ = tw.Write(metaBytes)
	_ = tw.Close()

	return buf.Bytes()
}

func TestRubyGemsHandler_Parse(t *testing.T) {
	h := NewRubyGemsHandler()
	ctx := context.Background()

	t.Run("gem with JSON metadata", func(t *testing.T) {
		data := createGemFile(t, map[string]any{"name": "rails", "version": "7.0.0"})

		result, err := h.Parse(ctx, bytes.NewReader(data))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result["name"] != "rails" {
			t.Errorf("name = %v, want rails", result["name"])
		}
	})

	t.Run("gem without metadata.gz returns default", func(t *testing.T) {
		// Empty tar
		var buf bytes.Buffer

		tw := tar.NewWriter(&buf)
		_ = tw.Close()

		result, err := h.Parse(ctx, bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result["name"] != "unknown" {
			t.Errorf("name = %v, want unknown", result["name"])
		}
	})
}

// createGemFileWithMarshal builds a .gem tar whose metadata.gz is a real
// Marshal 4.8 Gem::Specification dump, matching what parseGemMetadata must
// decode (not only JSON), so real gems yield their actual name/version.
func createGemFileWithMarshal(t *testing.T, spec rubymarshal.Specification) []byte {
	t.Helper()

	specBytes, err := rubymarshal.EncodeSpecification(spec)
	if err != nil {
		t.Fatalf("EncodeSpecification: %v", err)
	}

	var metaBuf bytes.Buffer

	gw := gzip.NewWriter(&metaBuf)
	_, _ = gw.Write(specBytes)
	_ = gw.Close()

	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)
	metaBytes := metaBuf.Bytes()
	_ = tw.WriteHeader(&tar.Header{Name: "metadata.gz", Mode: 0644, Size: int64(len(metaBytes))})
	_, _ = tw.Write(metaBytes)
	_ = tw.Close()

	return buf.Bytes()
}

func TestRubyGemsHandler_Parse_MarshalMetadata(t *testing.T) {
	h := NewRubyGemsHandler()
	ctx := context.Background()

	data := createGemFileWithMarshal(t, rubymarshal.Specification{
		Name:     "rack",
		Version:  "2.2.8",
		Platform: "ruby",
		Summary:  "a modular Ruby webserver interface",
		Authors:  []string{"Leah Neukirchen"},
		Licenses: []string{"MIT"},
		Dependencies: []rubymarshal.Dependency{
			{Name: "rexml", Requirement: ">= 3.0", Type: "runtime"},
		},
	})

	result, err := h.Parse(ctx, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A Marshal-encoded payload must be decoded rather than falling back to
	// {"raw_metadata": ...}.
	if result["name"] != "rack" {
		t.Errorf("name = %v, want rack (not unknown)", result["name"])
	}

	if result["version"] != "2.2.8" {
		t.Errorf("version = %v, want 2.2.8 (not 0.0.0)", result["version"])
	}

	deps, ok := result["dependencies"].([]map[string]any)
	if !ok || len(deps) != 1 || deps[0]["name"] != "rexml" {
		t.Errorf("dependencies = %#v, want [{name: rexml, ...}]", result["dependencies"])
	}
}
