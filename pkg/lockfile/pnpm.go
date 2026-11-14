// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package lockfile

import (
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// pnpmLock is the part of pnpm-lock.yaml Parse reads. Lockfile version 5 keys
// its packages "/name/1.0.0_peers", version 6 "/name@1.0.0(peers)" and both mark
// development-only packages with dev; version 9 keys them "name@1.0.0", moves the
// resolved peers and the dependency maps to snapshots and records no dev flag.
type pnpmLock struct {
	// A single-project lockfile of version 5 or 6 has its importer at the top.
	pnpmImporter `yaml:",inline"`

	LockfileVersion string                  `yaml:"lockfileVersion"`
	Importers       map[string]pnpmImporter `yaml:"importers"`
	Packages        map[string]pnpmPackage  `yaml:"packages"`
	Snapshots       map[string]pnpmDeps     `yaml:"snapshots"`
}

// pnpmImporter is one project of the workspace ("." for the root): what it
// requires, resolved.
type pnpmImporter struct {
	Dependencies         map[string]pnpmRef `yaml:"dependencies"`
	OptionalDependencies map[string]pnpmRef `yaml:"optionalDependencies"`
	DevDependencies      map[string]pnpmRef `yaml:"devDependencies"`
}

// pnpmRef is the resolved reference of an importer's dependency: a plain string
// in version 5, {specifier, version} from version 6 on.
type pnpmRef string

func (r *pnpmRef) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*r = pnpmRef(n.Value)
		return nil
	}

	var v struct {
		Version string `yaml:"version"`
	}
	if err := n.Decode(&v); err != nil {
		return err
	}

	*r = pnpmRef(v.Version)

	return nil
}

// pnpmDeps are the resolved dependencies of a package (or of a snapshot).
type pnpmDeps struct {
	Dependencies         map[string]string `yaml:"dependencies"`
	OptionalDependencies map[string]string `yaml:"optionalDependencies"`
}

type pnpmPackage struct {
	pnpmDeps `yaml:",inline"`

	Resolution struct {
		Tarball   string `yaml:"tarball"`
		Type      string `yaml:"type"`
		Directory string `yaml:"directory"`
		Repo      string `yaml:"repo"`
	} `yaml:"resolution"`
	Dev *bool `yaml:"dev"`
}

// parsePNPM reads the registry packages of a pnpm-lock.yaml. A git, directory
// or tarball-URL package (its key holds the URL or path as its version) and a
// link (only in the importers) are left out.
func parsePNPM(data []byte) ([]Dep, error) {
	var lock pnpmLock
	if err := decodeYAML(data, &lock); err != nil {
		return nil, err
	}

	if lock.LockfileVersion == "" {
		return nil, fmt.Errorf("%w: no lockfileVersion", ErrInvalid)
	}

	major, _ := strconv.ParseFloat(lock.LockfileVersion, 64)
	f := pnpmFormat{v5: major < 6}

	// The dependency maps: in the snapshots from version 9 on, else in the
	// packages.
	graph := lock.Snapshots
	if graph == nil {
		graph = make(map[string]pnpmDeps, len(lock.Packages))
		for k, p := range lock.Packages {
			graph[k] = p.pnpmDeps
		}
	}

	by := requirers{}
	for k, d := range graph {
		if name, _, ok := f.key(k); ok {
			by.add(name, f.required(d.Dependencies))
			by.add(name, f.required(d.OptionalDependencies))
		}
	}

	// Without a dev flag (version 9), a package is development-only when the
	// importers' dependencies and optional dependencies do not reach it.
	var prod map[string]bool
	if lock.Snapshots != nil {
		prod = pnpmReachable(lock)
	}

	deps := make([]Dep, 0, len(lock.Packages))

	for k, p := range lock.Packages {
		name, version, ok := f.key(k)
		r := p.Resolution

		if !ok || r.Type != "" || r.Directory != "" || r.Repo != "" || !fromRegistry(version, r.Tarball) {
			continue
		}

		dev := p.Dev != nil && *p.Dev
		if prod != nil {
			dev = !prod[name+"@"+version]
		}

		deps = append(deps, Dep{Name: name, Version: version, Dev: dev, RequiredBy: by[name]})
	}

	return deps, nil
}

// pnpmFormat reads the keys and references of one lockfile version: version 5
// ("/name/1.0.0_peers") or a later one ("/name@1.0.0(peers)", "name@1.0.0").
type pnpmFormat struct{ v5 bool }

// key reads a package key into the package name and version, without peers.
// A version 5 key without the leading slash names a tarball or git package.
func (f pnpmFormat) key(k string) (string, string, bool) {
	if f.v5 {
		k, ok := strings.CutPrefix(k, "/")
		if !ok {
			return "", "", false
		}

		segments := 1
		if strings.HasPrefix(k, "@") {
			segments = 2
		}

		parts := strings.SplitN(k, "/", segments+1)
		if len(parts) != segments+1 {
			return "", "", false
		}

		version, _, _ := strings.Cut(parts[segments], "_")

		return validNameVersion(strings.Join(parts[:segments], "/"), version)
	}

	k = strings.TrimPrefix(k, "/")
	k, _, _ = strings.Cut(k, "(")

	// The version starts at the first "@" after a scope's.
	if len(k) < 2 {
		return "", "", false
	}

	at := strings.IndexByte(k[1:], '@')
	if at < 0 {
		return "", "", false
	}

	return validNameVersion(k[:at+1], k[at+2:])
}

// target is the package a resolved dependency reference installs: an alias
// names it ("other@1.0.0", or "/other/1.0.0" and "/other@1.0.0" up to version 6),
// any other reference is a version of name. ok is false for a link or a local
// directory.
func (f pnpmFormat) target(name, ref string) (string, string, bool) {
	if strings.HasPrefix(ref, "link:") || strings.HasPrefix(ref, "file:") {
		return "", "", false
	}

	if pnpmAlias(ref) {
		return f.key(ref)
	}

	if f.v5 {
		return f.key("/" + name + "/" + ref)
	}

	return f.key(name + "@" + ref)
}

// required maps a dependency map to the names of the packages it installs.
func (f pnpmFormat) required(deps map[string]string) map[string]string {
	out := make(map[string]string, len(deps))
	for name, ref := range deps {
		if target, _, ok := f.target(name, ref); ok {
			out[target] = ref
		}
	}

	return out
}

// pnpmAlias reports whether a resolved reference names the package it installs
// instead of only its version.
func pnpmAlias(ref string) bool {
	base, _, _ := strings.Cut(ref, "(")

	return strings.HasPrefix(base, "/") || strings.Contains(strings.TrimPrefix(base, "@"), "@")
}

// pnpmReachable is the set of "name@version" a version 9 lockfile's importers
// reach through their dependencies and optional dependencies, walking the
// snapshots (keyed "name@version(peers)").
func pnpmReachable(lock pnpmLock) map[string]bool {
	f := pnpmFormat{}
	prod := map[string]bool{}
	seen := map[string]bool{}

	var queue []string

	push := func(name, ref string) {
		if strings.HasPrefix(ref, "link:") || strings.HasPrefix(ref, "file:") {
			return
		}

		snapshot := name + "@" + ref
		if pnpmAlias(ref) {
			snapshot = ref
		}

		if !seen[snapshot] {
			seen[snapshot] = true
			queue = append(queue, snapshot)
		}
	}

	importers := []pnpmImporter{lock.pnpmImporter}
	for _, imp := range lock.Importers {
		importers = append(importers, imp)
	}

	for _, imp := range importers {
		for _, deps := range []map[string]pnpmRef{imp.Dependencies, imp.OptionalDependencies} {
			for name, ref := range deps {
				push(name, string(ref))
			}
		}
	}

	for len(queue) > 0 {
		snapshot := queue[0]
		queue = queue[1:]

		if name, version, ok := f.key(snapshot); ok {
			prod[name+"@"+version] = true
		}

		d := lock.Snapshots[snapshot]
		for _, deps := range []map[string]string{d.Dependencies, d.OptionalDependencies} {
			for name, ref := range deps {
				push(name, ref)
			}
		}
	}

	return prod
}

// validNameVersion returns name and version when both are set and the name is
// not a bare scope.
func validNameVersion(name, version string) (string, string, bool) {
	if name == "" || version == "" || strings.HasSuffix(name, "/") || name == "@" {
		return "", "", false
	}

	return name, version, true
}
