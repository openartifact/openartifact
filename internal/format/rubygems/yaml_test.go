// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rubygems

import (
	"bytes"
	"context"
	"testing"
)

const sampleGemYAML = `--- !ruby/object:Gem::Specification
name: nokogiri
version: !ruby/object:Gem::Version
  version: 1.16.2
platform: x86_64-linux
authors:
- Mike Dalessio
summary: HTML parser
description: desc
homepage: https://nokogiri.org
licenses:
- MIT
dependencies:
- !ruby/object:Gem::Dependency
  name: racc
  requirement: !ruby/object:Gem::Requirement
    requirements:
    - - "~>"
      - !ruby/object:Gem::Version
        version: '1.4'
  type: :runtime
  prerelease: false
`

func TestParseYAMLGemspec(t *testing.T) {
	m := parseGemspecMetadata([]byte(sampleGemYAML))
	if m["name"] != "nokogiri" || m["version"] != "1.16.2" || m["platform"] != "x86_64-linux" {
		t.Fatalf("unexpected metadata: %#v", m)
	}

	deps, _ := m["dependencies"].([]map[string]any)
	if len(deps) != 1 || deps[0]["requirement"] != "~> 1.4" || deps[0]["type"] != "runtime" {
		t.Fatalf("unexpected deps: %#v", m["dependencies"])
	}
}

func TestCompareGemVersions(t *testing.T) {
	if compareVersions("10.0", "9.0") != 1 || compareVersions("1.0.a", "1.0") != -1 || compareVersions("1.0", "1.0.0") != 0 {
		t.Fatal("bad comparison")
	}

	if !isPrerelease("1.0.rc1") || isPrerelease("1.0.0") {
		t.Fatal("bad prerelease detection")
	}
}

func TestParseLicenses(t *testing.T) {
	got, err := ParseLicenses(context.Background(), bytes.NewReader(gemWithMetadata(t, []byte(sampleGemYAML))))
	if err != nil || len(got) != 1 || got[0] != "MIT" {
		t.Fatalf("ParseLicenses = %v, %v; want [MIT]", got, err)
	}
}
