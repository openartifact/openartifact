// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func manifestVersion(version, manifest string, created time.Time) *models.NPMVersion {
	return &models.NPMVersion{
		ID:              "id-" + version,
		PackageID:       "pkg-1",
		Version:         version,
		ManifestJSON:    []byte(manifest),
		Integrity:       "sha512-" + strings.Repeat("A", 86) + "==",
		Shasum:          strings.Repeat("ab", 20),
		TarballFilename: "example-" + version + ".tgz",
		CreatedAt:       created,
	}
}

// edgeVersions covers manifest shapes the byte-level builder must pass through
// or rewrite like the map-based encoder did.
func edgeVersions() []*models.NPMVersion {
	t0 := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)

	return []*models.NPMVersion{
		manifestVersion("1.0.0", "{\n  \"name\": \"x\",\n\t\"version\": \"1.0.0\",\r\n  \"description\": \"a <b> & \\\"q\\\" \\u00e9 \u2028 日本 😀\",\n  \"readme\": \"line1\\nline2\"\n}", t0),
		manifestVersion("1.1.0", `{"name":"x","version":"1.1.0","dist":{"fileCount":12,"unpackedSize":34567,"signatures":[{"keyid":"k","sig":"s"}],"npm-signature":"-----BEGIN PGP-----","tarball":"http://evil/x.tgz","shasum":"bad","integrity":"bad"}}`, t0.Add(time.Hour)),
		manifestVersion("1.2.0", `{"name":"x","version":"1.2.0","dist":"not-an-object"}`, t0.Add(2*time.Hour)),
		manifestVersion("1.3.0", `{"name":"x","version":"1.3.0","dist":null}`, t0.Add(3*time.Hour)),
		manifestVersion("1.4.0", `{"name":"x","version":"1.4.0","d\u0069st":{"x":1,"t\u0061rball":"drop"},"na\u006de":"y"}`, t0.Add(4*time.Hour)),
		manifestVersion("1.5.0", `{"dist":{"a":1},"name":"x","version":"1.5.0","dist":{"b":2,"shasum":"dup"}}`, t0.Add(5*time.Hour)),
		manifestVersion("2.0.0-beta.1", `{"name":"x","version":"2.0.0-beta.1","big":12345678901234567890,"neg":-1.5e-3,"t":true,"f":false,"n":null,"arr":[1,[2,{"k":"v}"}],"]"],"deep":{"a":{"b":{"c":[]}}},"deprecated":"use 3","bin":{"x":"./b.js"},"engines":{"node":">=18"},"_hasShrinkwrap":false,"peerDependenciesMeta":{"r":{"optional":true}},"bundleDependencies":["a"],"directories":{"lib":"l"},"optionalDependencies":{"o":"1"},"scripts":{"test":"echo \"}\""}}`, t0.Add(6*time.Hour)),
		manifestVersion("3.0.0", `{}`, t0.Add(-time.Hour)),
		manifestVersion("10.0.0", `{"name":"x","version":"10.0.0"}`, time.Time{}),
	}
}

func equivalenceCases() map[string][]*models.NPMVersion {
	return map[string][]*models.NPMVersion{
		"empty":     nil,
		"edge":      edgeVersions(),
		"100":       benchNPMVersions(100),
		"reordered": append(benchNPMVersions(5)[3:], benchNPMVersions(5)[:3]...),
	}
}

func decodeJSON(t *testing.T, b []byte) any {
	t.Helper()

	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, b)
	}

	return v
}

func TestPackumentEquivalentToMapEncoder(t *testing.T) {
	tagSets := map[string]map[string]string{
		"tags": {"latest": "1.0.0", "next": "2.0.0-beta.1", "a<b": "x"},
		"nil":  nil,
		"none": {},
	}

	builders := []struct {
		name string
		got  func(string, string, []*models.NPMVersion, map[string]string, string) ([]byte, error)
		want func(string, string, []*models.NPMVersion, map[string]string, string) ([]byte, error)
	}{
		{"full", BuildPackument, refBuildPackument},
		{"abbreviated", BuildAbbreviatedPackument, refBuildAbbreviatedPackument},
	}

	for _, b := range builders {
		for caseName, versions := range equivalenceCases() {
			for tagName, tags := range tagSets {
				t.Run(b.name+"/"+caseName+"/"+tagName, func(t *testing.T) {
					names := [][2]string{{"myrepo", "@scope/example"}, {"r<&>", "pkg\"x"}}
					for _, n := range names {
						want, err := b.want(n[0], n[1], versions, tags, "http://localhost:8080")
						if err != nil {
							t.Fatal(err)
						}

						got, err := b.got(n[0], n[1], versions, tags, "http://localhost:8080")
						if err != nil {
							t.Fatal(err)
						}

						if !reflect.DeepEqual(decodeJSON(t, want), decodeJSON(t, got)) {
							t.Fatalf("documents differ\nwant: %s\ngot:  %s", want, got)
						}
					}
				})
			}
		}
	}
}

func TestPackumentOutputIsCompactAndDeterministic(t *testing.T) {
	versions := edgeVersions()

	first, err := BuildPackument("r", "p", versions, map[string]string{"latest": "1.0.0", "a": "1.1.0"}, "http://h")
	if err != nil {
		t.Fatal(err)
	}

	second, err := BuildPackument("r", "p", versions, map[string]string{"a": "1.1.0", "latest": "1.0.0"}, "http://h")
	if err != nil {
		t.Fatal(err)
	}

	if string(first) != string(second) {
		t.Fatal("output depends on input map order")
	}

	if strings.ContainsAny(strings.NewReplacer(`\n`, "", `\r`, "", `\t`, "").Replace(string(first)), "\n\r\t") {
		t.Fatal("output contains raw whitespace control characters")
	}
}

func TestPackumentTarballRewriting(t *testing.T) {
	out, err := BuildPackument("myrepo", "@scope/example", edgeVersions(), map[string]string{"latest": "1.0.0"}, "https://oa.example")
	if err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Versions map[string]struct {
			Dist map[string]any `json:"dist"`
		} `json:"versions"`
	}

	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}

	for ver, v := range doc.Versions {
		want := "https://oa.example/repos/myrepo/@scope/example/-/example-" + ver + ".tgz"
		if v.Dist["tarball"] != want {
			t.Errorf("%s: tarball = %v, want %s", ver, v.Dist["tarball"], want)
		}

		if v.Dist["integrity"] == "bad" || v.Dist["shasum"] == "bad" || v.Dist["shasum"] == "dup" {
			t.Errorf("%s: stored dist value leaked: %v", ver, v.Dist)
		}
	}

	if got := doc.Versions["1.1.0"].Dist["fileCount"]; got != float64(12) {
		t.Errorf("stored dist.fileCount not preserved: %v", got)
	}
}

func TestPackumentRejectsMalformedManifests(t *testing.T) {
	bad := map[string]string{
		"truncated":  `{"name":"x"`,
		"not object": `["a"]`,
		"null":       `null`,
		"empty":      ``,
		"no colon":   `{"name" "x"}`,
		"bad key":    `{name:"x"}`,
		"unclosed":   `{"a":"b`,
	}

	for name, manifest := range bad {
		t.Run(name, func(t *testing.T) {
			v := []*models.NPMVersion{manifestVersion("1.0.0", manifest, time.Now())}

			for _, build := range []func(string, string, []*models.NPMVersion, map[string]string, string) ([]byte, error){BuildPackument, BuildAbbreviatedPackument} {
				if _, err := build("r", "p", v, nil, "http://h"); err == nil || !strings.Contains(err.Error(), "unmarshal manifest for p@1.0.0") {
					t.Fatalf("want manifest error, got %v", err)
				}
			}
		})
	}
}

func TestPackumentTimes(t *testing.T) {
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	versions := []*models.NPMVersion{
		manifestVersion("1.0.0", `{}`, t0.Add(time.Hour)),
		manifestVersion("0.9.0", `{}`, t0),
		manifestVersion("2.0.0", `{}`, t0.Add(48*time.Hour)),
	}

	out, err := BuildPackument("r", "p", versions, nil, "http://h")
	if err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Time map[string]string `json:"time"`
	}

	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"0.9.0": "2024-01-01T00:00:00Z", "1.0.0": "2024-01-01T01:00:00Z", "2.0.0": "2024-01-03T00:00:00Z",
		"created": "2024-01-01T00:00:00Z", "modified": "2024-01-03T00:00:00Z",
	}
	if !reflect.DeepEqual(doc.Time, want) {
		t.Fatalf("time = %v, want %v", doc.Time, want)
	}
}
