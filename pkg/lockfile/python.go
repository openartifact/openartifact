// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package lockfile

import (
	"slices"
	"strings"
)

// pythonName is the normalized form of a Python project name (PEP 503): what
// a package index and a lockfile's requirements agree on.
func pythonName(name string) string {
	var b strings.Builder

	sep := false

	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if r == '-' || r == '_' || r == '.' {
			sep = true
			continue
		}

		if sep && b.Len() > 0 {
			b.WriteByte('-')
		}

		sep = false

		b.WriteRune(r)
	}

	return b.String()
}

// poetryLock is the part of poetry.lock Parse reads.
type poetryLock struct {
	Package  []poetryPackage `toml:"package"`
	Metadata *struct {
		LockVersion string `toml:"lock-version"`
	} `toml:"metadata"`
}

type poetryPackage struct {
	Name         string         `toml:"name"`
	Version      string         `toml:"version"`
	Category     string         `toml:"category"`
	Groups       []string       `toml:"groups"`
	Dependencies map[string]any `toml:"dependencies"`
	Source       *struct {
		Type string `toml:"type"`
	} `toml:"source"`
}

// fromIndex reports whether Poetry installs p from a package index: PyPI (no
// source) or a source of type "legacy" (a custom index), not a git, directory,
// file or URL dependency.
func (p poetryPackage) fromIndex() bool {
	return p.Source == nil || p.Source.Type == "" || p.Source.Type == "legacy"
}

// dev reports whether p is development-only: Poetry 2 lists the dependency
// groups that need it ("main" being the project's own), Poetry before 1.5 a
// category; Poetry 1.5 to 1.8 record neither.
func (p poetryPackage) dev() bool {
	if p.Category != "" {
		return p.Category == "dev"
	}

	return len(p.Groups) > 0 && !slices.Contains(p.Groups, "main")
}

// parsePoetry reads the index packages of a poetry.lock. The project itself
// is not a package of it: what no locked package requires, it requires.
func parsePoetry(data []byte) ([]Dep, error) {
	var lock poetryLock
	if err := decodeTOML(data, &lock); err != nil {
		return nil, err
	}

	if lock.Metadata == nil && lock.Package == nil {
		return nil, invalid("no [metadata] or [[package]]")
	}

	by := requirers{}

	for _, p := range lock.Package {
		name := pythonName(p.Name)
		for required := range p.Dependencies {
			by[pythonName(required)] = append(by[pythonName(required)], name)
		}
	}

	deps := make([]Dep, 0, len(lock.Package))

	for _, p := range lock.Package {
		name := pythonName(p.Name)
		if name == "" || !fromRegistry(p.Version, "") || !p.fromIndex() {
			continue
		}

		deps = append(deps, Dep{Name: name, Version: p.Version, Dev: p.dev(), RequiredBy: by[name]})
	}

	return deps, nil
}

// poetryRecordsDev reports whether a poetry.lock says which packages are
// development-only (written by Poetry 2, or before 1.5).
func poetryRecordsDev(data []byte) bool {
	var lock poetryLock
	if decodeTOML(data, &lock) != nil {
		return false
	}

	return slices.ContainsFunc(lock.Package, func(p poetryPackage) bool { return p.Category != "" || len(p.Groups) > 0 })
}

// uvLock is the part of uv.lock Parse reads.
type uvLock struct {
	Version int         `toml:"version"`
	Package []uvPackage `toml:"package"`
}

type uvPackage struct {
	Name                 string                    `toml:"name"`
	Version              string                    `toml:"version"`
	Source               map[string]any            `toml:"source"`
	Dependencies         []uvDependency            `toml:"dependencies"`
	OptionalDependencies map[string][]uvDependency `toml:"optional-dependencies"`
	DevDependencies      map[string][]uvDependency `toml:"dev-dependencies"`
}

// uvDependency names a locked package; the version is set only when the
// lockfile holds more than one version of the name.
type uvDependency struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
}

// fromIndex reports whether uv installs p from a package index.
func (p uvPackage) fromIndex() bool {
	_, ok := p.Source["registry"]
	return ok
}

// project reports whether p is the project's own code: a workspace member
// (virtual, or editable).
func (p uvPackage) project() bool {
	_, virtual := p.Source["virtual"]
	_, editable := p.Source["editable"]

	return virtual || editable
}

// parseUV reads the index packages of a uv.lock. The workspace members are the
// project: they are no requirers, and what they need through their
// dependencies and extras (not their dependency groups), directly or not, is
// not development-only; the rest is.
func parseUV(data []byte) ([]Dep, error) {
	var lock uvLock
	if err := decodeTOML(data, &lock); err != nil {
		return nil, err
	}

	if lock.Version == 0 {
		return nil, invalid("no version")
	}

	by := requirers{}

	for _, p := range lock.Package {
		if p.project() {
			continue
		}

		name := pythonName(p.Name)
		for _, d := range p.requires() {
			by[pythonName(d.Name)] = append(by[pythonName(d.Name)], name)
		}
	}

	prod := uvReachable(lock.Package)
	deps := make([]Dep, 0, len(lock.Package))

	for i, p := range lock.Package {
		name := pythonName(p.Name)
		if name == "" || !p.fromIndex() || !fromRegistry(p.Version, "") {
			continue
		}

		deps = append(deps, Dep{Name: name, Version: p.Version, Dev: prod != nil && !prod[i], RequiredBy: by[name]})
	}

	return deps, nil
}

// requires is what p needs outside its dependency groups: its dependencies and
// those of every extra.
func (p uvPackage) requires() []uvDependency {
	out := slices.Clone(p.Dependencies)
	for _, extra := range p.OptionalDependencies {
		out = append(out, extra...)
	}

	return out
}

// uvReachable marks the packages (by index) the workspace members need
// outside their dependency groups; nil when the lockfile has no member, so
// nothing can be told development-only.
func uvReachable(pkgs []uvPackage) map[int]bool {
	byName := map[string][]int{}
	for i, p := range pkgs {
		byName[pythonName(p.Name)] = append(byName[pythonName(p.Name)], i)
	}

	prod := map[int]bool{}

	var queue []int

	for i, p := range pkgs {
		if p.project() {
			prod[i] = true
			queue = append(queue, i)
		}
	}

	if len(queue) == 0 {
		return nil
	}

	for len(queue) > 0 {
		p := pkgs[queue[0]]
		queue = queue[1:]

		for _, d := range p.requires() {
			for _, j := range byName[pythonName(d.Name)] {
				if !prod[j] && (d.Version == "" || pkgs[j].Version == d.Version) {
					prod[j] = true
					queue = append(queue, j)
				}
			}
		}
	}

	return prod
}
