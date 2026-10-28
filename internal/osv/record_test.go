// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package osv

import (
	"errors"
	"testing"
)

const sampleRecord = `{
  "id": "GHSA-aaaa-bbbb-cccc",
  "modified": "2024-05-01T10:00:00Z",
  "published": "2024-04-01T10:00:00Z",
  "withdrawn": "2024-06-01T00:00:00Z",
  "aliases": ["CVE-2024-0001", "GHSA-aaaa-bbbb-cccc", ""],
  "summary": "Prototype pollution",
  "database_specific": {"severity": "HIGH", "cwe_ids": ["CWE-1321"]},
  "references": [{"url": "https://example.test/advisory"}],
  "affected": [
    {"package": {"ecosystem": "PyPI", "name": "Django_Rest.Framework"},
     "ranges": [{"type": "ECOSYSTEM", "events": [{"introduced": "0"}, {"fixed": "1.2"}]}]},
    {"package": {"ecosystem": "Debian:11", "name": "django"}, "versions": ["1"]},
    {"package": {"ecosystem": "npm", "name": "left-pad"}, "versions": ["1.0.0"]}
  ]
}`

func TestParseRecord(t *testing.T) {
	t.Parallel()

	adv, err := ParseRecord([]byte(sampleRecord), map[string]bool{"PyPI": true})
	if err != nil {
		t.Fatal(err)
	}

	if adv.ID != "GHSA-aaaa-bbbb-cccc" || adv.Withdrawn == nil || adv.Published == nil {
		t.Fatalf("unexpected advisory header: %+v", adv)
	}

	if len(adv.Aliases) != 1 || adv.Aliases[0] != "CVE-2024-0001" {
		t.Fatalf("aliases = %v, want only CVE-2024-0001 (own id and empty dropped)", adv.Aliases)
	}

	if len(adv.Affected) != 1 {
		t.Fatalf("affected = %d entries, want only the wanted PyPI one", len(adv.Affected))
	}

	if got := adv.Affected[0].NameNorm; got != "django-rest-framework" {
		t.Fatalf("NameNorm = %q, want the PEP 503 form", got)
	}

	if adv.DBSeverity != "HIGH" || len(adv.CWEIDs) != 1 || len(adv.Refs) != 1 {
		t.Fatalf("metadata not kept: %+v", adv)
	}
}

func TestParseRecordErrors(t *testing.T) {
	t.Parallel()

	if _, err := ParseRecord([]byte(`{"modified":"2024-01-01T00:00:00Z"}`), nil); !errors.Is(err, errNoRecordID) {
		t.Fatalf("missing id error = %v, want errNoRecordID", err)
	}

	if _, err := ParseRecord([]byte(`{`), nil); err == nil {
		t.Fatal("malformed JSON must fail")
	}
}

func TestNormalizeName(t *testing.T) {
	t.Parallel()

	tests := []struct{ eco, in, want string }{
		{"PyPI", "Foo_Bar.baz", "foo-bar-baz"},
		{"crates.io", "Serde", "serde"},
		{"RubyGems", "Rails", "rails"},
		{"NuGet", "Newtonsoft.Json", "newtonsoft.json"},
		{"npm", "@Scope/Pkg", "@Scope/Pkg"},
		{"Maven", "org.Foo:Bar", "org.Foo:Bar"},
		{"Go", "github.com/Foo/Bar", "github.com/Foo/Bar"},
	}

	for _, tc := range tests {
		if got := NormalizeName(tc.eco, tc.in); got != tc.want {
			t.Errorf("NormalizeName(%q, %q) = %q, want %q", tc.eco, tc.in, got, tc.want)
		}
	}
}

func TestEcosystemFormats(t *testing.T) {
	t.Parallel()

	for _, eco := range Ecosystems() {
		if format, ok := FormatOf(eco); !ok || format == "" {
			t.Fatalf("FormatOf(%q) = %q, %v", eco, format, ok)
		}
	}

	if Supported("Debian:11") || Supported("Alpine") {
		t.Fatal("distro ecosystems must not be supported by the local mirror")
	}
}

func TestParseBaseURL(t *testing.T) {
	t.Parallel()

	u, err := ParseBaseURL(" http://mirror.internal:8080/osv/ ")
	if err != nil || u.String() != "http://mirror.internal:8080/osv" {
		t.Fatalf("ParseBaseURL = %v, %v", u, err)
	}

	for _, bad := range []string{"", "ftp://x", "https://", "https://u:p@host", "https://host?x=1", "https://host#f"} {
		if _, err := ParseBaseURL(bad); !errors.Is(err, ErrInvalidBaseURL) {
			t.Errorf("ParseBaseURL(%q) err = %v, want ErrInvalidBaseURL", bad, err)
		}
	}
}
