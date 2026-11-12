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
	Name        string `json:"name"`
	Version     string `json:"version"`
	Resolved    string `json:"resolved"`
	Link        bool   `json:"link"`
	InBundle    bool   `json:"inBundle"` //nolint:tagliatelle // package-lock.json wire format
	Dev         bool   `json:"dev"`
	DevOptional bool   `json:"devOptional"` //nolint:tagliatelle // package-lock.json wire format
}

type npmV1Dep struct {
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

	npmV1(lock.Dependencies, 0, &deps)

	return deps, nil
}

// npmPackages reads the packages map of lockfileVersion 2 and 3: an entry under
// node_modules/ is an installed package, named by its "name" field (an alias
// installs a package under another folder name) or else by its folder. The
// root ("") and workspace folders are the project's own code; links and bundled
// packages do not come from the registry.
func npmPackages(pkgs map[string]npmPackage) []Dep {
	deps := make([]Dep, 0, len(pkgs))

	for path, p := range pkgs {
		_, folder, nested := strings.CutLast(path, npmModules)
		if !nested || p.Link || p.InBundle || !fromRegistry(p.Version, p.Resolved) {
			continue
		}

		name := p.Name
		if name == "" {
			name = folder
		}

		if name == "" {
			continue
		}

		deps = append(deps, Dep{Name: name, Version: p.Version, Dev: p.Dev || p.DevOptional})
	}

	return deps
}

// npmV1 walks the dependency tree of lockfileVersion 1. An alias is recorded as
// "npm:<name>@<version>".
func npmV1(tree map[string]npmV1Dep, depth int, deps *[]Dep) {
	if depth > npmMaxDepth {
		return
	}

	for name, d := range tree {
		version := d.Version
		if alias, ok := strings.CutPrefix(version, "npm:"); ok {
			if at := strings.LastIndex(alias, "@"); at > 0 {
				name, version = alias[:at], alias[at+1:]
			}
		}

		if name != "" && !d.Bundled && fromRegistry(version, d.Resolved) {
			*deps = append(*deps, Dep{Name: name, Version: version, Dev: d.Dev})
		}

		npmV1(d.Dependencies, depth+1, deps)
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
