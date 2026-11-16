// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package lockfile

import (
	"strings"
)

// cargoLock is the part of Cargo.lock Parse reads.
type cargoLock struct {
	Version int            `toml:"version"`
	Package []cargoPackage `toml:"package"`
}

type cargoPackage struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
	// Source is "registry+<index>" or "sparse+<index>" for a registry crate,
	// "git+<url>" for a git one, and empty for a workspace member or a path
	// dependency.
	Source string `toml:"source"`
	// Dependencies are "name", "name version" or "name version (source)".
	Dependencies []string `toml:"dependencies"`
}

// parseCargo reads the registry crates of a Cargo.lock. A crate without a
// source (a workspace member or a path dependency) is the project's own code
// and no requirer: what only it requires is a direct dependency. Cargo.lock
// does not record which crates only dev-dependencies need: no Dep of it is Dev.
func parseCargo(data []byte) ([]Dep, error) {
	var lock cargoLock
	if err := decodeTOML(data, &lock); err != nil {
		return nil, err
	}

	if lock.Version == 0 && lock.Package == nil {
		return nil, invalid("no version or [[package]]")
	}

	by := requirers{}

	for _, p := range lock.Package {
		if p.Source == "" {
			continue
		}

		for _, d := range p.Dependencies {
			if name, _, _ := strings.Cut(strings.TrimSpace(d), " "); name != "" {
				by[name] = append(by[name], p.Name)
			}
		}
	}

	deps := make([]Dep, 0, len(lock.Package))

	for _, p := range lock.Package {
		registry := strings.HasPrefix(p.Source, "registry+") || strings.HasPrefix(p.Source, "sparse+")
		if p.Name == "" || !registry || !fromRegistry(p.Version, "") {
			continue
		}

		deps = append(deps, Dep{Name: p.Name, Version: p.Version, RequiredBy: by[p.Name]})
	}

	return deps, nil
}
