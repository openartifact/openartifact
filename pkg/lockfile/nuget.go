// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package lockfile

import (
	"strings"
)

// nugetLock is the part of packages.lock.json Parse reads: per target
// framework (and runtime), the locked packages by id.
type nugetLock struct {
	Version      int                                `json:"version"`
	Dependencies map[string]map[string]nugetPackage `json:"dependencies"`
}

type nugetPackage struct {
	// Type is Direct, Transitive, CentralTransitive or Project.
	Type         string            `json:"type"`
	Resolved     string            `json:"resolved"`
	Dependencies map[string]string `json:"dependencies"`
}

// parseNuGet reads the packages of a packages.lock.json, of every target
// framework and runtime. Project references are the solution's own code: no
// registry serves them and they are no requirers. NuGet ids ignore case, so a
// requirement names its package in any case; a package keeps the spelling of
// its entry. NuGet does not record which packages only tests need: no Dep of
// it is Dev.
func parseNuGet(data []byte) ([]Dep, error) {
	var lock nugetLock
	if err := decode(data, &lock); err != nil {
		return nil, err
	}

	if lock.Version == 0 {
		return nil, invalid("no version")
	}

	by := requirers{}

	for _, target := range lock.Dependencies {
		for id, p := range target {
			if p.Type == "Project" {
				continue
			}

			for required := range p.Dependencies {
				by[strings.ToLower(required)] = append(by[strings.ToLower(required)], id)
			}
		}
	}

	var deps []Dep

	for _, target := range lock.Dependencies {
		for id, p := range target {
			if p.Type != "Project" && id != "" && fromRegistry(p.Resolved, "") {
				deps = append(deps, Dep{Name: id, Version: p.Resolved, RequiredBy: by[strings.ToLower(id)]})
			}
		}
	}

	return deps, nil
}
