// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"archive/zip"
	"bytes"
	"context"
	"testing"
)

const sampleNuspec = `<?xml version="1.0" encoding="utf-8"?>
<package>
  <metadata>
    <id>Newtonsoft.Json</id>
    <version>13.0.3</version>
    <title>Json.NET</title>
    <authors>James Newton-King</authors>
    <description>Json.NET is a popular high-performance JSON framework for .NET</description>
  </metadata>
</package>`

func createNupkg(t *testing.T, nuspecName, nuspecContent string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	w, err := zw.Create(nuspecName)
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}

	_, _ = w.Write([]byte(nuspecContent))
	_ = zw.Close()

	return buf.Bytes()
}

func TestNuGetHandler_Parse(t *testing.T) {
	h := NewNuGetHandler()
	ctx := context.Background()

	data := createNupkg(t, "Newtonsoft.Json.nuspec", sampleNuspec)

	result, err := h.Parse(ctx, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result["id"] != "Newtonsoft.Json" {
		t.Errorf("id = %v, want Newtonsoft.Json", result["id"])
	}

	if result["version"] != "13.0.3" {
		t.Errorf("version = %v, want 13.0.3", result["version"])
	}

	if result["authors"] != "James Newton-King" {
		t.Errorf("authors = %v, want James Newton-King", result["authors"])
	}
}

const sampleNuspecWithDeps = `<?xml version="1.0" encoding="utf-8"?>
<package>
  <metadata>
    <id>Example.Pkg</id>
    <version>1.0.0</version>
    <dependencies>
      <dependency id="Legacy.Dep" version="1.0.0" />
      <group targetFramework="net6.0">
        <dependency id="Newtonsoft.Json" version="12.0.3" />
        <dependency id="Serilog" version="[2.10.0, 3.0.0)" />
      </group>
      <group targetFramework="netstandard2.0">
        <dependency id="Newtonsoft.Json" version="12.0.3" />
      </group>
    </dependencies>
  </metadata>
</package>`

func TestNuspecDependencies_DependencyGroups(t *testing.T) {
	data := createNupkg(t, "Example.Pkg.nuspec", sampleNuspecWithDeps)

	nuspec, err := parseNuspec(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("parseNuspec: %v", err)
	}

	groups := nuspec.Metadata.Dependencies.DependencyGroups()
	if len(groups) != 3 {
		t.Fatalf("len(groups) = %d, want 3 (ungrouped + 2 TFM groups)", len(groups))
	}

	// The ungrouped (legacy) dependency forms a group with no
	// targetFramework key at all.
	ungrouped := groups[0]
	if _, hasTFM := ungrouped["targetFramework"]; hasTFM {
		t.Errorf("ungrouped entry has targetFramework = %v, want absent", ungrouped["targetFramework"])
	}

	ungroupedDeps, ok := ungrouped["dependencies"].([]map[string]any)
	if !ok || len(ungroupedDeps) != 1 || ungroupedDeps[0]["id"] != "Legacy.Dep" || ungroupedDeps[0]["range"] != "1.0.0" {
		t.Errorf("ungrouped dependencies = %#v, want [{id: Legacy.Dep, range: 1.0.0}]", ungrouped["dependencies"])
	}

	net6 := groups[1]
	if net6["targetFramework"] != "net6.0" {
		t.Errorf("groups[1] targetFramework = %v, want net6.0", net6["targetFramework"])
	}

	net6Deps, ok := net6["dependencies"].([]map[string]any)
	if !ok || len(net6Deps) != 2 {
		t.Fatalf("net6.0 dependencies = %#v, want 2 entries", net6["dependencies"])
	}

	if net6Deps[0]["id"] != "Newtonsoft.Json" || net6Deps[0]["range"] != "12.0.3" {
		t.Errorf("net6.0 dependency[0] = %#v, want {id: Newtonsoft.Json, range: 12.0.3}", net6Deps[0])
	}

	if net6Deps[1]["id"] != "Serilog" || net6Deps[1]["range"] != "[2.10.0, 3.0.0)" {
		t.Errorf("net6.0 dependency[1] = %#v, want {id: Serilog, range: [2.10.0, 3.0.0)}", net6Deps[1])
	}

	netstd := groups[2]
	if netstd["targetFramework"] != "netstandard2.0" {
		t.Errorf("groups[2] targetFramework = %v, want netstandard2.0", netstd["targetFramework"])
	}
}

func TestNuspecDependencies_DependencyGroupsEmpty(t *testing.T) {
	data := createNupkg(t, "Newtonsoft.Json.nuspec", sampleNuspec)

	nuspec, err := parseNuspec(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("parseNuspec: %v", err)
	}

	if groups := nuspec.Metadata.Dependencies.DependencyGroups(); len(groups) != 0 {
		t.Errorf("groups = %#v, want none for a nuspec with no <dependencies>", groups)
	}
}
