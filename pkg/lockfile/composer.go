// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package lockfile

// composerLock is the part of composer.lock Parse reads.
type composerLock struct {
	Packages    []composerPackage `json:"packages"`
	PackagesDev []composerPackage `json:"packages-dev"` //nolint:tagliatelle // composer.lock wire format
}

type composerPackage struct {
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Require map[string]string `json:"require"`
	Dist    *struct {
		Type string `json:"type"`
	} `json:"dist"`
}

// parseComposer reads packages and packages-dev of a composer.lock. A package
// installed from a path repository (dist type "path") is local and left out;
// the version is the one the lockfile records, as the registry names it.
func parseComposer(data []byte) ([]Dep, error) {
	var lock composerLock
	if err := decode(data, &lock); err != nil {
		return nil, err
	}

	deps := make([]Dep, 0, len(lock.Packages)+len(lock.PackagesDev))

	// A package's "require" names what it depends on; the project's own
	// requirements live in composer.json, so a package no locked package
	// requires is a direct one. Platform requirements (php, ext-*) are not
	// packages and never match a locked name.
	by := requirers{}
	for _, p := range append(append([]composerPackage{}, lock.Packages...), lock.PackagesDev...) {
		by.add(p.Name, p.Require)
	}

	add := func(pkgs []composerPackage, dev bool) {
		for _, p := range pkgs {
			if p.Name == "" || p.Version == "" || (p.Dist != nil && p.Dist.Type == "path") {
				continue
			}

			deps = append(deps, Dep{Name: p.Name, Version: p.Version, Dev: dev, RequiredBy: by[p.Name]})
		}
	}

	add(lock.Packages, false)
	add(lock.PackagesDev, true)

	return deps, nil
}
