// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/registry"
)

// PURLParts are the components of a Package URL that vary by ecosystem: the
// namespace segments, the package name, the version and the already-escaped
// qualifier suffix ("?distro=...", empty for none). buildPURL percent-encodes
// the names and the version and adds the "pkg:<type>/" prefix.
type PURLParts struct {
	Namespace  []string
	Name       string
	Version    string
	Qualifiers string
}

// Hooks is what the vulnerability scanners need to know about one format.
// Both fields are optional; a format with neither is simply not registered.
type Hooks struct {
	// PURL splits a package into the Package URL components of its
	// ecosystem. nil means the plain form: no namespace, the name and the
	// version as given. It returns errDistroRequired for a package that
	// cannot be expressed without its distribution release.
	PURL func(p Package) (PURLParts, error)

	// TrivyManifest writes into dir the lockfile (or manifest) trivy reads to
	// scan name at version. nil means trivy has no lockfile for the format
	// (it is refused with errUnsupportedEcosystem).
	TrivyManifest func(dir, name, version string) error
}

// hooks holds the Hooks of every registered format. Formats absent from it
// (docker, helm, raw, conda, conan, ivy, sbt, terraform, apk, pacman, nix,
// ansible) have no rewrite of their Package URL and no trivy lockfile.
var hooks registry.Registry[Hooks] //nolint:gochecknoglobals // the format hook table, filled by Register at startup

// Register adds the scanner hooks of a format. It panics when the format is
// registered twice.
func Register(format string, h Hooks) {
	hooks.Register(format, h)
}

// The per-format hooks. Formats with a lockfile trivy reads (npm, pypi, maven,
// cargo, composer, go, nuget, rubygems) have a TrivyManifest; deb and rpm are
// scanned from an SBOM instead.

// NPMHooks: "@scope/name" puts the scope in the namespace.
var NPMHooks = Hooks{PURL: npmPURL, TrivyManifest: npmManifest} //nolint:gochecknoglobals // immutable format hook value
// PyPIHooks: names are normalized (lower case, "_" as "-").
var PyPIHooks = Hooks{PURL: pypiPURL, TrivyManifest: pypiManifest} //nolint:gochecknoglobals // immutable format hook value
// MavenHooks: "group:artifact" coordinates.
var MavenHooks = Hooks{PURL: mavenPURL, TrivyManifest: mavenManifest} //nolint:gochecknoglobals // immutable format hook value
// CargoHooks: the plain Package URL, a Cargo.lock for trivy.
var CargoHooks = Hooks{TrivyManifest: cargoManifest} //nolint:gochecknoglobals // immutable format hook value
// ComposerHooks: "vendor/name", and a leading "v" of the version dropped.
var ComposerHooks = Hooks{PURL: composerPURL, TrivyManifest: composerManifest} //nolint:gochecknoglobals // immutable format hook value
// GoHooks: the module path before its last element is the namespace.
var GoHooks = Hooks{PURL: goPURL, TrivyManifest: goManifest} //nolint:gochecknoglobals // immutable format hook value
// NuGetHooks: the plain Package URL, a packages.lock.json for trivy.
var NuGetHooks = Hooks{TrivyManifest: nugetManifest} //nolint:gochecknoglobals // immutable format hook value
// RubyGemsHooks: the plain Package URL, a Gemfile.lock for trivy.
var RubyGemsHooks = Hooks{TrivyManifest: rubygemsManifest} //nolint:gochecknoglobals // immutable format hook value
// DebHooks: the distribution vendor is the namespace, distro a qualifier.
var DebHooks = Hooks{PURL: debPURL} //nolint:gochecknoglobals // immutable format hook value
// RPMHooks: as DebHooks, plus the epoch qualifier.
var RPMHooks = Hooks{PURL: rpmPURL} //nolint:gochecknoglobals // immutable format hook value

func npmPURL(p Package) (PURLParts, error) {
	// "@scope/name": the scope is the namespace, "@" encoded.
	parts := PURLParts{Name: strings.ToLower(p.Name), Version: p.Version}
	if scope, rest, found := strings.Cut(parts.Name, "/"); found {
		parts.Namespace, parts.Name = []string{scope}, rest
	}

	return parts, nil
}

func pypiPURL(p Package) (PURLParts, error) {
	return PURLParts{Name: strings.ReplaceAll(strings.ToLower(p.Name), "_", "-"), Version: p.Version}, nil
}

func mavenPURL(p Package) (PURLParts, error) {
	// "group:artifact" (the form the maven scanner manifest uses); a bare
	// name is its own group, as in the trivy manifest.
	group, artifact, found := strings.Cut(p.Name, ":")
	if !found {
		group, artifact = p.Name, p.Name
	}

	return PURLParts{Namespace: []string{group}, Name: artifact, Version: p.Version}, nil
}

func goPURL(p Package) (PURLParts, error) {
	// The module path "github.com/org/mod": everything before the last
	// element is the namespace.
	parts := PURLParts{Name: p.Name, Version: p.Version}
	if mod, last, found := strings.CutLast(p.Name, "/"); found {
		parts.Namespace, parts.Name = strings.Split(mod, "/"), last
	}

	return parts, nil
}

func composerPURL(p Package) (PURLParts, error) {
	// "vendor/name": the vendor is the namespace; a leading "v" of a tag
	// ("v1.2.3") is not part of the version advisories are written against.
	parts := PURLParts{Name: p.Name, Version: composerVersion(p.Version)}
	if vendor, name, found := strings.Cut(p.Name, "/"); found {
		parts.Namespace, parts.Name = []string{vendor}, name
	}

	return parts, nil
}

// distroPURL is the part deb and rpm share: the vendor namespace and the
// distro qualifier, or errDistroRequired.
func distroPURL(p Package) (PURLParts, error) {
	if !validDistro(p.Distro) {
		return PURLParts{}, errDistroRequired
	}

	// Qualifiers are sorted by key: distro, then epoch.
	return PURLParts{
		Namespace:  []string{distroVendor(p.Distro)},
		Name:       p.Name,
		Version:    p.Version,
		Qualifiers: "?distro=" + purlEscape(p.Distro),
	}, nil
}

func debPURL(p Package) (PURLParts, error) { return distroPURL(p) }

func rpmPURL(p Package) (PURLParts, error) {
	parts, err := distroPURL(p)
	if err != nil {
		return PURLParts{}, err
	}

	var epoch int

	epoch, parts.Version = rpmEVR(p)
	if epoch > 0 {
		parts.Qualifiers += "&epoch=" + strconv.Itoa(epoch)
	}

	return parts, nil
}

// Trivy reads lockfiles, not the manifests that declare ranges: a bare
// package.json, Cargo.toml, Gemfile or .csproj yields no packages and so a
// clean result that means nothing. These ecosystems get a lockfile that pins
// exactly the package being scanned.

func npmManifest(dir, name, version string) error {
	data, err := json.Marshal(map[string]any{
		"name":            "scan-pkg",
		"lockfileVersion": 3,
		"requires":        true,
		"packages": map[string]any{
			"":                     map[string]any{"name": "scan-pkg", "dependencies": map[string]string{name: version}},
			"node_modules/" + name: map[string]any{"version": version},
		},
	})
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, "package-lock.json"), data, 0600)
}

func pypiManifest(dir, name, version string) error {
	content := name + "==" + version + "\n"

	return os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte(content), 0600)
}

func mavenManifest(dir, name, version string) error {
	// For maven, name may be in groupId:artifactId format
	groupID, artifactID := name, name
	if parts := strings.SplitN(name, ":", 2); len(parts) == 2 {
		groupID = parts[0]
		artifactID = parts[1]
	}

	content := fmt.Sprintf(`<project>
  <modelVersion>4.0.0</modelVersion>
  <groupId>app</groupId>
  <artifactId>app</artifactId>
  <version>1.0.0</version>
  <dependencies>
    <dependency>
      <groupId>%s</groupId>
      <artifactId>%s</artifactId>
      <version>%s</version>
    </dependency>
  </dependencies>
</project>`, groupID, artifactID, version)

	return os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(content), 0600)
}

func cargoManifest(dir, name, version string) error {
	content := fmt.Sprintf("version = 3\n\n[[package]]\nname = %q\nversion = %q\n", name, version)

	return os.WriteFile(filepath.Join(dir, "Cargo.lock"), []byte(content), 0600)
}

func composerManifest(dir, name, version string) error {
	data, err := json.Marshal(map[string]any{
		"packages":     []map[string]string{{"name": name, "version": composerVersion(version)}},
		"packages-dev": []map[string]string{},
	})
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, "composer.lock"), data, 0600)
}

func goManifest(dir, name, version string) error {
	content := fmt.Sprintf(`module scan-pkg

go 1.21

require %s %s
`, name, version)

	return os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0600)
}

func rubygemsManifest(dir, name, version string) error {
	content := fmt.Sprintf(
		"GEM\n  remote: https://rubygems.org/\n  specs:\n    %s (%s)\n\nPLATFORMS\n  ruby\n\nDEPENDENCIES\n  %s (= %s)\n",
		name,
		version,
		name,
		version,
	)

	return os.WriteFile(filepath.Join(dir, "Gemfile.lock"), []byte(content), 0600)
}

func nugetManifest(dir, name, version string) error {
	data, err := json.Marshal(map[string]any{
		"version": 1,
		"dependencies": map[string]any{"net6.0": map[string]any{
			name: map[string]any{"type": "Direct", "requested": "[" + version + ", )", "resolved": version},
		}},
	})
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, "packages.lock.json"), data, 0600)
}
