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
	"slices"
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
			got, err := buildPURL(Package{Name: tt.pkg, Version: tt.version, Ecosystem: tt.ecosystem})
			if err != nil || got != tt.want {
				t.Errorf("buildPURL(%q, %q, %q) = %q, %v; want %q", tt.ecosystem, tt.pkg, tt.version, got, err, tt.want)
			}
		})
	}
}

// A deb or rpm is a package of one distribution release: the distro qualifier
// carries it, the namespace names the distribution, and an rpm's epoch is a
// qualifier of its own (without it grype reports packages as vulnerable that an
// epoch has already fixed).
func TestBuildPURL_DebAndRPM(t *testing.T) {
	tests := []struct {
		name string
		pkg  Package
		want string
	}{
		{
			"debian", Package{Name: "openssl", Version: "3.0.11-1~deb12u2", Ecosystem: "deb", Distro: "debian-12"},
			"pkg:deb/debian/openssl@3.0.11-1~deb12u2?distro=debian-12",
		},
		{
			"ubuntu", Package{Name: "openssl", Version: "3.0.2-0ubuntu1.10", Ecosystem: "deb", Distro: "ubuntu-22.04"},
			"pkg:deb/ubuntu/openssl@3.0.2-0ubuntu1.10?distro=ubuntu-22.04",
		},
		{
			"ecosystem is case-insensitive", Package{Name: "openssl", Version: "1", Ecosystem: "DEB", Distro: "debian-12"},
			"pkg:deb/debian/openssl@1?distro=debian-12",
		},
		{
			"deb epoch stays in the version", Package{Name: "vim", Version: "2:9.0.1378-2", Ecosystem: "deb", Distro: "debian-12"},
			"pkg:deb/debian/vim@2%3A9.0.1378-2?distro=debian-12",
		},
		{
			"deb name with plus signs", Package{Name: "libstdc++6", Version: "12.2.0-14", Ecosystem: "deb", Distro: "debian-12"},
			"pkg:deb/debian/libstdc%2B%2B6@12.2.0-14?distro=debian-12",
		},
		{
			"rhel is the redhat namespace", Package{Name: "openssl", Version: "3.0.7-24.el9", Ecosystem: "rpm", Distro: "rhel-9"},
			"pkg:rpm/redhat/openssl@3.0.7-24.el9?distro=rhel-9",
		},
		{
			"rpm epoch field", Package{Name: "openssl", Version: "3.0.7-24.el9", Epoch: 1, Ecosystem: "rpm", Distro: "rhel-9"},
			"pkg:rpm/redhat/openssl@3.0.7-24.el9?distro=rhel-9&epoch=1",
		},
		{
			"rpm epoch prefix of the version", Package{Name: "openssl", Version: "1:3.0.7-24.el9", Ecosystem: "rpm", Distro: "rhel-9"},
			"pkg:rpm/redhat/openssl@3.0.7-24.el9?distro=rhel-9&epoch=1",
		},
		{
			"epoch field wins over the prefix", Package{Name: "openssl", Version: "1:3.0.7-24.el9", Epoch: 2, Ecosystem: "rpm", Distro: "rhel-9"},
			"pkg:rpm/redhat/openssl@3.0.7-24.el9?distro=rhel-9&epoch=2",
		},
		{
			"release is appended to a bare version", Package{Name: "openssl", Version: "3.0.7", Release: "24.el9_2", Epoch: 1, Ecosystem: "rpm", Distro: "rhel-9"},
			"pkg:rpm/redhat/openssl@3.0.7-24.el9_2?distro=rhel-9&epoch=1",
		},
		{
			"release is not appended twice", Package{Name: "openssl", Version: "3.0.7-24.el9", Release: "24.el9", Ecosystem: "rpm", Distro: "rhel-9"},
			"pkg:rpm/redhat/openssl@3.0.7-24.el9?distro=rhel-9",
		},
		{
			"fedora", Package{Name: "curl", Version: "8.2.1-3.fc39", Ecosystem: "rpm", Distro: "fedora-39"},
			"pkg:rpm/fedora/curl@8.2.1-3.fc39?distro=fedora-39",
		},
		{
			"amazon linux", Package{Name: "curl", Version: "8.3.0-1.amzn2023.0.4", Ecosystem: "rpm", Distro: "amzn-2023"},
			"pkg:rpm/amzn/curl@8.3.0-1.amzn2023.0.4?distro=amzn-2023",
		},
		{
			"minor release of the distribution", Package{Name: "openssl", Version: "1-1.el9", Ecosystem: "rpm", Distro: "rhel-9.2"},
			"pkg:rpm/redhat/openssl@1-1.el9?distro=rhel-9.2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildPURL(tt.pkg)
			if err != nil || got != tt.want {
				t.Errorf("buildPURL(%+v) = %q, %v; want %q", tt.pkg, got, err, tt.want)
			}
		})
	}
}

// A deb or rpm without a usable distro is refused, never turned into a
// Package URL grype answers with "no match" (which would read as clean).
func TestBuildPURL_DebAndRPMNeedADistro(t *testing.T) {
	for _, tt := range []struct{ name, ecosystem, distro string }{
		{"deb without a distro", "deb", ""},
		{"rpm without a distro", "rpm", ""},
		{"codename without a version", "deb", "bookworm"},
		{"distro without an id", "deb", "12"},
		{"upper case", "deb", "Debian-12"},
		{"qualifier injection", "deb", "debian-12&epoch=9"},
		{"path injection", "deb", "debian/../x-12"},
		{"trailing dot", "deb", "debian-12."},
		{"space", "rpm", "rhel 9"},
		{"newline", "rpm", "rhel-9\n"},
		{"too long", "rpm", strings.Repeat("a", 70) + "-9"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildPURL(Package{Name: "openssl", Version: "1.0", Ecosystem: tt.ecosystem, Distro: tt.distro})
			if !errors.Is(err, errDistroRequired) || got != "" {
				t.Errorf("buildPURL = %q, %v; want %v", got, err, errDistroRequired)
			}
		})
	}
}

// Reports have no distribution to name, so deb and rpm stay generic there.
func TestPackageURL_DebAndRPMStayGeneric(t *testing.T) {
	for ecosystem, want := range map[string]string{
		"deb": "pkg:generic/openssl@1.0?ecosystem=deb",
		"rpm": "pkg:generic/openssl@1.0?ecosystem=rpm",
	} {
		if got := PackageURL(ecosystem, "openssl", "1.0"); got != want {
			t.Errorf("PackageURL(%q) = %q, want %q", ecosystem, got, want)
		}
	}
}

func TestBuildPURL_Refusals(t *testing.T) {
	for _, tt := range []struct {
		name, ecosystem, pkg, version string
		want                          error
	}{
		{"docker is not a grype ecosystem", "docker", "nginx", "1", errUnsupportedEcosystem},
		{"scope without a name", "npm", "@scope/", "1", errNoPURL},
		{"maven without an artifact", "maven", "group:", "1", errNoPURL},
		{"go namespace with an empty segment", "go", "github.com//x", "v1", errNoPURL},
		{"empty version", "cargo", "regex", "", errNoPURL},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := buildPURL(Package{Name: tt.pkg, Version: tt.version, Ecosystem: tt.ecosystem}); !errors.Is(err, tt.want) || got != "" {
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
		got, err := buildPURL(Package{Name: in, Version: "1.0.0", Ecosystem: "npm"})
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

	if _, err := backend.Scan(context.Background(), Package{Name: "@angular/core", Version: "8.0.0", Ecosystem: "npm"}); err != nil {
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
		name string
		pkg  Package
		want FailureKind
	}{
		{"deb without a distro", Package{Name: "openssl", Version: "1.0.0", Ecosystem: "deb"}, FailureUnsupported},
		{"rpm without a distro", Package{Name: "openssl", Version: "1.0.0", Ecosystem: "rpm"}, FailureUnsupported},
		{"deb with a codename instead of a distro", Package{Name: "openssl", Version: "1.0.0", Ecosystem: "deb", Distro: "bookworm"}, FailureUnsupported},
		{"docker", leftPad("docker"), FailureUnsupported},
		{"name with a space", Package{Name: "a b", Version: "1.0.0", Ecosystem: "npm"}, FailureInternal},
		{"name with a quote", Package{Name: `a"b`, Version: "1.0.0", Ecosystem: "npm"}, FailureInternal},
		{"release with a space", Package{Name: "openssl", Version: "1.0.0", Release: "1 el9", Ecosystem: "rpm", Distro: "rhel-9"}, FailureInternal},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := backend.Scan(context.Background(), tt.pkg)
			if se := asScanError(t, err); se.Kind != tt.want {
				t.Errorf("kind = %q, want %q", se.Kind, tt.want)
			}
		})
	}

	if _, err := os.Stat(ran); err == nil {
		t.Error("grype was run for a package it cannot scan")
	}
}

// A deb or rpm with its distribution release is scanned: grype gets the Package
// URL with the distro qualifier (and an rpm's epoch), which is what makes it
// match against the advisories of that release.
func TestGrypeScan_DebAndRPMGetTheirDistro(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	tests := []struct {
		name string
		pkg  Package
		want string
	}{
		{
			"debian", Package{Name: "openssl", Version: "3.0.11-1~deb12u2", Ecosystem: "deb", Distro: "debian-12"},
			"pkg:deb/debian/openssl@3.0.11-1~deb12u2?distro=debian-12",
		},
		{
			"rhel with epoch and release", Package{Name: "openssl", Version: "3.0.7", Release: "24.el9", Epoch: 1, Ecosystem: "rpm", Distro: "rhel-9"},
			"pkg:rpm/redhat/openssl@3.0.7-24.el9?distro=rhel-9&epoch=1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argsFile := filepath.Join(t.TempDir(), "args")

			installFakeScanner(t, "grype", `printf '%s\n' "$@" > `+argsFile+`
echo '{"matches":[{"vulnerability":{"id":"CVE-2024-0001","severity":"High"}}]}'`)

			out := Run(context.Background(), log, []ScannerBackend{testRegistry(t, log).Backend("grype")}, tt.pkg)
			if out.Status != StatusCompleted || !slices.Equal(vulnIDs(out.Vulnerabilities), []string{"CVE-2024-0001"}) {
				t.Fatalf("status=%q (%s) vulns=%v", out.Status, out.PublicError(), vulnIDs(out.Vulnerabilities))
			}

			raw, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}

			if args := strings.Fields(string(raw)); len(args) < 1 || args[0] != tt.want {
				t.Errorf("grype was run with %v, want the target %q", args, tt.want)
			}
		})
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
