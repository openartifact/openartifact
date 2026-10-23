// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/logger"
)

func TestBuildPURL(t *testing.T) {
	tests := []struct {
		name, ecosystem, pkg, version string
		want                          string
	}{
		{"pypi is lowercased with dashes", "PyPI", "Django_Foo", "3.0", "pkg:pypi/django-foo@3.0"},
		{"npm plain", "npm", "lodash", "4.17.20", "pkg:npm/lodash@4.17.20"},
		{"npm scoped", "npm", "@angular/core", "8.0.0", "pkg:npm/%40angular/core@8.0.0"},
		{"maven group and artifact", "maven", "org.apache.logging.log4j:log4j-core", "2.14.1", "pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1"},
		{"maven bare name is its own group", "maven", "junit", "4.12", "pkg:maven/junit/junit@4.12"},
		{"cargo", "cargo", "regex", "1.5.4", "pkg:cargo/regex@1.5.4"},
		{"rubygems", "rubygems", "nokogiri", "1.10.0", "pkg:gem/nokogiri@1.10.0"},
		{"nuget keeps case", "nuget", "Newtonsoft.Json", "12.0.0", "pkg:nuget/Newtonsoft.Json@12.0.0"},
		{"go module path", "go", "golang.org/x/text", "v0.3.0", "pkg:golang/golang.org/x/text@v0.3.0"},
		{"go deep module path", "go", "github.com/aws/aws-sdk-go-v2/service/s3", "v1.0.0", "pkg:golang/github.com/aws/aws-sdk-go-v2/service/s3@v1.0.0"},
		{"go uppercase letters are case-encoded", "go", "github.com/!azure/go", "v1.0.0", "pkg:golang/github.com/%21azure/go@v1.0.0"},
		{"version with plus and tilde", "pypi", "pkg", "1.0+local~1", "pkg:pypi/pkg@1.0%2Blocal~1"},
		{"epoch colon in version", "pypi", "pkg", "1:2.0", "pkg:pypi/pkg@1%3A2.0"},
		{"flag-like name stays a path segment", "npm", "-h", "1.0.0", "pkg:npm/-h@1.0.0"},
		{"at sign in version", "npm", "pkg", "1.0@x", "pkg:npm/pkg@1.0%40x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildPURL(tt.ecosystem, tt.pkg, tt.version)
			if err != nil || got != tt.want {
				t.Errorf("buildPURL(%q, %q, %q) = %q, %v; want %q", tt.ecosystem, tt.pkg, tt.version, got, err, tt.want)
			}
		})
	}
}

func TestBuildPURL_Refusals(t *testing.T) {
	for _, tt := range []struct {
		name, ecosystem, pkg, version string
		want                          error
	}{
		{"docker is not a grype ecosystem", "docker", "nginx", "1", errUnsupportedEcosystem},
		{"deb needs a distribution", "deb", "openssl", "1.1.0", errUnsupportedEcosystem},
		{"rpm needs a distribution", "rpm", "openssl", "1.0.1", errUnsupportedEcosystem},
		{"scope without a name", "npm", "@scope/", "1", errNoPURL},
		{"maven without an artifact", "maven", "group:", "1", errNoPURL},
		{"go namespace with an empty segment", "go", "github.com//x", "v1", errNoPURL},
		{"empty version", "cargo", "regex", "", errNoPURL},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := buildPURL(tt.ecosystem, tt.pkg, tt.version); !errors.Is(err, tt.want) || got != "" {
				t.Errorf("buildPURL = %q, %v; want error %v", got, err, tt.want)
			}
		})
	}
}

// TestBuildPURL_NeverCarriesRawSpecialCharacters checks the property that makes the result safe as a command
// line argument and as one URL: whatever the input, it starts with pkg: and
// holds nothing but unreserved characters, "%XX", "/", ":" and "@".
func TestBuildPURL_NeverCarriesRawSpecialCharacters(t *testing.T) {
	for _, in := range []string{"a b", "a?x=1", "a#f", "a\nb", "--config=/etc/x", "a%41", "é", "a;b", "a`b"} {
		got, err := buildPURL("npm", in, "1.0.0")
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}

		body := strings.TrimPrefix(got, "pkg:npm/")
		if body == got || strings.ContainsAny(body, " ?#\n;`=&") || strings.Contains(body, "%%") {
			t.Errorf("%q gave %q", in, got)
		}
	}
}

func TestGrypeScan_PassesAPackageURL(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")
	argsFile := filepath.Join(t.TempDir(), "args")

	installFakeScanner(t, "grype", `printf '%s\n' "$@" > `+argsFile+`
echo '{"matches":[]}'`)

	backend := testRegistry(t, log).Backend("grype")

	if _, err := backend.Scan(context.Background(), "@angular/core", "8.0.0", "npm"); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}

	args := strings.Fields(string(raw))
	if len(args) < 3 || args[0] != "pkg:npm/%40angular/core@8.0.0" || args[1] != "-o" || args[2] != "json" {
		t.Errorf("grype was run with %v", args)
	}

	// --quiet would also hide the reason of a failure from the log.
	for _, a := range args {
		if a == "--quiet" || a == "-q" {
			t.Errorf("grype is run with %s", a)
		}
	}
}

func TestGrypeScan_RefusesWithoutRunningTheBinary(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")
	ran := filepath.Join(t.TempDir(), "ran")

	installFakeScanner(t, "grype", "touch "+ran+"\necho '{\"matches\":[]}'")

	backend := testRegistry(t, log).Backend("grype")

	for _, tt := range []struct {
		name, ecosystem, pkg string
		want                 FailureKind
	}{
		{"deb", "deb", "openssl", FailureUnsupported},
		{"rpm", "rpm", "openssl", FailureUnsupported},
		{"docker", "docker", "nginx", FailureUnsupported},
		{"name with a space", "npm", "a b", FailureInternal},
		{"name with a quote", "npm", `a"b`, FailureInternal},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := backend.Scan(context.Background(), tt.pkg, "1.0.0", tt.ecosystem)
			if se := asScanError(t, err); se.Kind != tt.want {
				t.Errorf("kind = %q, want %q", se.Kind, tt.want)
			}
		})
	}

	if _, err := os.Stat(ran); err == nil {
		t.Error("grype was run for a package it cannot scan")
	}
}

// Trivy reads lockfiles: a bare package.json, Cargo.toml, Gemfile or .csproj
// gives an empty, clean-looking result, so these ecosystems get a lockfile.
func TestTrivyManifest_UsesLockfilesTrivyReads(t *testing.T) {
	tests := []struct {
		ecosystem, name, version, file string
		contains                       []string
	}{
		{"npm", "lodash", "4.17.20", "package-lock.json", []string{`"node_modules/lodash":{"version":"4.17.20"}`, `"lockfileVersion":3`}},
		{"npm", "@angular/core", "8.0.0", "package-lock.json", []string{`"node_modules/@angular/core":{"version":"8.0.0"}`}},
		{"cargo", "regex", "1.5.4", "Cargo.lock", []string{"[[package]]\nname = \"regex\"\nversion = \"1.5.4\""}},
		{"rubygems", "nokogiri", "1.10.0", "Gemfile.lock", []string{"    nokogiri (1.10.0)", "DEPENDENCIES"}},
		{"nuget", "Newtonsoft.Json", "12.0.0", "packages.lock.json", []string{`"Newtonsoft.Json":{"requested":"[12.0.0, )","resolved":"12.0.0","type":"Direct"}`}},
		{"pypi", "django", "3.0", "requirements.txt", []string{"django==3.0"}},
		{"go", "golang.org/x/text", "v0.3.0", "go.mod", []string{"require golang.org/x/text v0.3.0"}},
		{"maven", "org.apache.logging.log4j:log4j-core", "2.14.1", "pom.xml", []string{"<artifactId>log4j-core</artifactId>", "<version>2.14.1</version>"}},
	}

	for _, tt := range tests {
		t.Run(tt.ecosystem+" "+tt.name, func(t *testing.T) {
			dir := t.TempDir()

			if err := (&TrivyScanner{}).writeManifest(dir, tt.name, tt.version, tt.ecosystem); err != nil {
				t.Fatal(err)
			}

			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 || entries[0].Name() != tt.file {
				t.Fatalf("wrote %v, want only %s", entries, tt.file)
			}

			raw, err := os.ReadFile(filepath.Join(dir, tt.file))
			if err != nil {
				t.Fatal(err)
			}

			for _, want := range tt.contains {
				if !strings.Contains(string(raw), want) {
					t.Errorf("%s lacks %q:\n%s", tt.file, want, raw)
				}
			}

			if strings.HasSuffix(tt.file, ".json") && !json.Valid(raw) {
				t.Errorf("%s is not valid JSON:\n%s", tt.file, raw)
			}
		})
	}
}
