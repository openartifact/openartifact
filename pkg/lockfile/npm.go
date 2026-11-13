// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package lockfile

import (
	"strings"
)

// npmMaxDepth bounds the nesting of a lockfileVersion 1 dependency tree.
const npmMaxDepth = 256

// npmLock is the part of package-lock.json (or npm-shrinkwrap.json) Parse reads:
// packages for lockfileVersion 2 and 3, dependencies for version 1 (version 2
// carries both; packages wins).
type npmLock struct {
	Packages     map[string]npmPackage `json:"packages"`
	Dependencies map[string]npmV1Dep   `json:"dependencies"`
}

type npmPackage struct {
	Dependencies         map[string]string `json:"dependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"` //nolint:tagliatelle // package-lock.json wire format
	PeerDependencies     map[string]string `json:"peerDependencies"`     //nolint:tagliatelle // package-lock.json wire format

	Name        string `json:"name"`
	Version     string `json:"version"`
	Resolved    string `json:"resolved"`
	Link        bool   `json:"link"`
	InBundle    bool   `json:"inBundle"` //nolint:tagliatelle // package-lock.json wire format
	Dev         bool   `json:"dev"`
	DevOptional bool   `json:"devOptional"` //nolint:tagliatelle // package-lock.json wire format
}

type npmV1Dep struct {
	Requires     map[string]string   `json:"requires"`
	Version      string              `json:"version"`
	Resolved     string              `json:"resolved"`
	Bundled      bool                `json:"bundled"`
	Dev          bool                `json:"dev"`
	Dependencies map[string]npmV1Dep `json:"dependencies"`
}

const npmModules = "node_modules/"

// parseNPM reads the installed packages of a package-lock.json.
func parseNPM(data []byte) ([]Dep, error) {
	var lock npmLock
	if err := decode(data, &lock); err != nil {
		return nil, err
	}

	if lock.Packages != nil {
		return npmPackages(lock.Packages), nil
	}

	var deps []Dep

	by := requirers{}
	npmV1Requirers(lock.Dependencies, 0, by)
	npmV1(lock.Dependencies, 0, by, &deps)

	return deps, nil
}

// npmPackages reads the packages map of lockfileVersion 2 and 3: an entry under
// node_modules/ is an installed package, named by its "name" field (an alias
// installs a package under another folder name) or else by its folder. The
// root ("") and workspace folders are the project's own code; links and bundled
// packages do not come from the registry.
func npmPackages(pkgs map[string]npmPackage) []Dep {
	deps := make([]Dep, 0, len(pkgs))

	// Who depends on what: only installed packages count as requirers; the
	// root and workspace folders are the project, so what only they require
	// is a direct dependency.
	by := requirers{}
	for path, p := range pkgs {
		if name, ok := npmInstalledName(path, p); ok {
			by.add(name, p.Dependencies)
			by.add(name, p.OptionalDependencies)
			by.add(name, p.PeerDependencies)
		}
	}

	for path, p := range pkgs {
		name, ok := npmInstalledName(path, p)
		if !ok || p.Link || p.InBundle || !fromRegistry(p.Version, p.Resolved) {
			continue
		}

		// A dependency map names an alias by its folder, not by the package it
		// installs: look up both.
		requiredBy := by[name]
		if _, folder, _ := strings.CutLast(path, npmModules); folder != name {
			requiredBy = append(append([]string{}, requiredBy...), by[folder]...)
		}

		deps = append(deps, Dep{Name: name, Version: p.Version, Dev: p.Dev || p.DevOptional, RequiredBy: requiredBy})
	}

	return deps
}

// npmInstalledName is the package an entry of the packages map installs: its
// "name" field (an alias installs a package under another folder name) or else
// its folder below the last node_modules/. ok is false for the root and
// workspace folders, which are the project's own code.
func npmInstalledName(path string, p npmPackage) (string, bool) {
	_, folder, nested := strings.CutLast(path, npmModules)
	if !nested {
		return "", false
	}

	if p.Name != "" {
		return p.Name, true
	}

	return folder, folder != ""
}

// npmV1 walks the dependency tree of lockfileVersion 1. An alias is recorded as
// "npm:<name>@<version>".
// npmV1Requirers records, for every entry of a lockfileVersion 1 tree, the
// packages its "requires" names (the aliased name of an npm: alias).
func npmV1Requirers(tree map[string]npmV1Dep, depth int, by requirers) {
	if depth > npmMaxDepth {
		return
	}

	for name, d := range tree {
		name, _ = npmV1Alias(name, d.Version)
		by.add(name, d.Requires)
		npmV1Requirers(d.Dependencies, depth+1, by)
	}
}

// npmV1Alias resolves an "npm:<name>@<version>" alias of a lockfileVersion 1
// entry to the package it installs.
func npmV1Alias(name, version string) (string, string) {
	if alias, ok := strings.CutPrefix(version, "npm:"); ok {
		if at := strings.LastIndex(alias, "@"); at > 0 {
			return alias[:at], alias[at+1:]
		}
	}

	return name, version
}

func npmV1(tree map[string]npmV1Dep, depth int, by requirers, deps *[]Dep) {
	if depth > npmMaxDepth {
		return
	}

	for name, d := range tree {
		name, version := npmV1Alias(name, d.Version)

		if name != "" && !d.Bundled && fromRegistry(version, d.Resolved) {
			*deps = append(*deps, Dep{Name: name, Version: version, Dev: d.Dev, RequiredBy: by[name]})
		}

		npmV1(d.Dependencies, depth+1, by, deps)
	}
}

// fromRegistry reports whether a locked version comes from a package registry:
// it has a version that is not a git, file, link or URL specifier, and a
// resolved location, if any, that is an HTTP(S) tarball.
func fromRegistry(version, resolved string) bool {
	if version == "" || strings.Contains(version, ":") || strings.Contains(version, "/") {
		return false
	}

	return resolved == "" || strings.HasPrefix(resolved, "https://") || strings.HasPrefix(resolved, "http://")
}
