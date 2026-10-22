// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"context"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/logger"
)

func newTestLogger(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	return log
}

//nolint:cyclop,gocyclo // test function covers multiple format scenarios
func TestGenerateCycloneDX(t *testing.T) {
	g := NewGenerator(nil, newTestLogger(t))
	ctx := context.Background()

	t.Run("basic artifact", func(t *testing.T) {
		result, err := g.GenerateCycloneDX(ctx, "repo1", "mylib@1.2.3", "npm", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Format != "cyclonedx" {
			t.Errorf("Format = %q, want cyclonedx", result.Format)
		}

		if result.SpecVersion != "1.4" {
			t.Errorf("SpecVersion = %q, want 1.4", result.SpecVersion)
		}

		if result.ContentType != "application/json" {
			t.Errorf("ContentType = %q, want application/json", result.ContentType)
		}

		if result.Components != 0 {
			t.Errorf("Components = %d, want 0", result.Components)
		}

		meta, ok := result.Data["metadata"].(map[string]any)
		if !ok {
			t.Fatal("missing metadata")
		}

		comp, ok := meta["component"].(map[string]any)
		if !ok {
			t.Fatal("missing component in metadata")
		}

		if comp["name"] != "mylib" {
			t.Errorf("component name = %v, want mylib", comp["name"])
		}

		if comp["version"] != "1.2.3" {
			t.Errorf("component version = %v, want 1.2.3", comp["version"])
		}

		purl, _ := comp["purl"].(string)
		if !strings.Contains(purl, "pkg:npm/mylib@1.2.3") {
			t.Errorf("purl = %q, want to contain pkg:npm/mylib@1.2.3", purl)
		}
	})

	t.Run("artifact without @ separator", func(t *testing.T) {
		result, err := g.GenerateCycloneDX(ctx, "repo1", "justname", "raw", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		meta, ok := result.Data["metadata"].(map[string]any)
		if !ok {
			t.Fatalf("expected map[string]any at key 'metadata', got %T", result.Data["metadata"])
		}

		comp, ok := meta["component"].(map[string]any)
		if !ok {
			t.Fatalf("expected map[string]any at key 'component', got %T", meta["component"])
		}

		if comp["name"] != "justname" {
			t.Errorf("name = %v, want justname", comp["name"])
		}

		if comp["version"] != "unknown" {
			t.Errorf("version = %v, want unknown", comp["version"])
		}
	})

	t.Run("with dependencies", func(t *testing.T) {
		meta := map[string]any{
			"dependencies": []any{
				map[string]any{"name": "lodash", "version": "4.17.21"},
				map[string]any{"name": "axios", "version": "1.0.0"},
			},
		}

		result, err := g.GenerateCycloneDX(ctx, "repo1", "myapp@2.0.0", "npm", meta)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Components != 2 {
			t.Errorf("Components = %d, want 2", result.Components)
		}
	})

	t.Run("purl format mapping", func(t *testing.T) {
		cases := []struct{ format, wantType string }{
			{"maven", "maven"},
			{"pypi", "pypi"},
			{"cargo", "cargo"},
			{"rubygems", "gem"},
			{"nuget", "nuget"},
			{"docker", "docker"},
			{"go", "golang"},
			{"helm", "helm"},
			{"rpm", "rpm"},
			{"deb", "deb"},
			{"raw", "generic"},
		}
		for _, c := range cases {
			r, err := g.GenerateCycloneDX(ctx, "r", "pkg@1.0", c.format, nil)
			if err != nil {
				t.Fatalf("format %s: unexpected error: %v", c.format, err)
			}

			meta, ok := r.Data["metadata"].(map[string]any)
			if !ok {
				t.Fatalf("format %s: expected map[string]any at key 'metadata', got %T", c.format, r.Data["metadata"])
			}

			comp, ok := meta["component"].(map[string]any)
			if !ok {
				t.Fatalf("format %s: expected map[string]any at key 'component', got %T", c.format, meta["component"])
			}

			purl, _ := comp["purl"].(string)
			if !strings.HasPrefix(purl, "pkg:"+c.wantType+"/") {
				t.Errorf("format %s: purl = %q, want prefix pkg:%s/", c.format, purl, c.wantType)
			}
		}
	})
}

func TestGenerateSPDX(t *testing.T) {
	g := NewGenerator(nil, newTestLogger(t))
	ctx := context.Background()

	t.Run("basic artifact", func(t *testing.T) {
		result, err := g.GenerateSPDX(ctx, "repo1", "mylib@1.2.3", "npm", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Format != "spdx" {
			t.Errorf("Format = %q, want spdx", result.Format)
		}

		if result.SpecVersion != "2.3" {
			t.Errorf("SpecVersion = %q, want 2.3", result.SpecVersion)
		}

		if result.ContentType != "text/spdx" {
			t.Errorf("ContentType = %q, want text/spdx", result.ContentType)
		}

		if !strings.Contains(result.Text, "SPDXVersion: SPDX-2.3") {
			t.Errorf("missing SPDXVersion in output")
		}

		if !strings.Contains(result.Text, "PackageName: mylib") {
			t.Errorf("missing PackageName in output")
		}

		if !strings.Contains(result.Text, "PackageVersion: 1.2.3") {
			t.Errorf("missing PackageVersion in output")
		}
	})

	t.Run("with dependencies", func(t *testing.T) {
		meta := map[string]any{
			"dependencies": []any{
				map[string]any{"name": "lodash", "version": "4.17.21"},
			},
		}

		result, err := g.GenerateSPDX(ctx, "repo1", "myapp@2.0.0", "npm", meta)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Components != 1 {
			t.Errorf("Components = %d, want 1", result.Components)
		}

		if !strings.Contains(result.Text, "lodash") {
			t.Errorf("missing dependency 'lodash' in SPDX output")
		}
	})
}

func TestSplitArtifactID_ScopedNpm(t *testing.T) {
	n, v := splitArtifactID("@babel/core@7.0.0")
	if n != "@babel/core" || v != "7.0.0" {
		t.Fatalf("got %q %q", n, v)
	}
	if n, v := splitArtifactID("plain"); n != "plain" || v != "unknown" {
		t.Fatalf("got %q %q", n, v)
	}
}

func TestSpdxSafeStripsNewlines(t *testing.T) {
	if got := spdxSafe("a\nPackageName: evil\r"); got != "aPackageName: evil" {
		t.Fatalf("got %q", got)
	}
}

func TestGenerateSPDX_DependencyInjection(t *testing.T) {
	g := NewGenerator(nil, newTestLogger(t))
	meta := map[string]any{"dependencies": []any{
		map[string]any{"name": "x\nPackageLicenseConcluded: MIT\nPackageName: evil", "version": "1\r\nPackageName: evil2"},
	}}

	res, err := g.GenerateSPDX(context.Background(), "r", "lib@1.0.0", "npm", meta)
	if err != nil {
		t.Fatal(err)
	}

	for line := range strings.SplitSeq(res.Text, "\n") {
		if strings.HasPrefix(line, "PackageLicenseConcluded: MIT") || strings.HasPrefix(line, "PackageName: evil") {
			t.Fatalf("injected tag line: %q", line)
		}
	}
}
