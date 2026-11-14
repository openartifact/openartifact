// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package lockfile reads the package versions a lockfile pins, for `oa check`:
// composer.lock, npm's package-lock.json, pnpm-lock.yaml and yarn.lock. It keeps only what a client
// fetches from a package registry: local, linked, bundled and VCS or file
// dependencies are left out, since no registry serves them.
package lockfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"go.yaml.in/yaml/v3"
)

// Kind is the lockfile format.
type Kind string

// The lockfile formats Parse reads.
const (
	Composer Kind = "composer"
	NPM      Kind = "npm"
	PNPM     Kind = "pnpm"
	// Yarn is Yarn 1's yarn.lock or the YAML one of Yarn 2 and later. It does
	// not record development dependencies: no Dep of it is Dev.
	Yarn Kind = "yarn"
)

// Dep is one package version a lockfile pins. Dev reports that the lockfile
// marks it as a development dependency (only). RequiredBy names the locked
// packages that depend on it, sorted; empty when none does (the project
// requires it itself). A lockfile does not record the project's own
// requirements, so a dependency with requirers may be direct as well.
type Dep struct {
	Name       string   `json:"package"`
	Version    string   `json:"version"`
	Dev        bool     `json:"dev,omitempty"`
	RequiredBy []string `json:"required_by,omitempty"`
}

var (
	// ErrUnknownLockfile is a file name Parse cannot tell the format of.
	ErrUnknownLockfile = errors.New(
		"unknown lockfile: expected composer.lock, package-lock.json, npm-shrinkwrap.json, pnpm-lock.yaml or yarn.lock",
	)
	// ErrInvalid is a lockfile that is not the document its format expects.
	ErrInvalid = errors.New("invalid lockfile")
)

// KindOf returns the format of a lockfile from its file name.
func KindOf(path string) (Kind, error) {
	switch filepath.Base(path) {
	case "composer.lock":
		return Composer, nil
	case "package-lock.json", "npm-shrinkwrap.json":
		return NPM, nil
	case "pnpm-lock.yaml":
		return PNPM, nil
	case "yarn.lock":
		return Yarn, nil
	}

	return "", fmt.Errorf("%w: %s", ErrUnknownLockfile, filepath.Base(path))
}

// Parse reads the dependencies of a lockfile of the given kind, sorted by name
// then version, each name and version once (a version is Dev only when every
// occurrence of it is).
func Parse(kind Kind, data []byte) ([]Dep, error) {
	var (
		deps []Dep
		err  error
	)

	switch kind {
	case Composer:
		deps, err = parseComposer(data)
	case NPM:
		deps, err = parseNPM(data)
	case PNPM:
		deps, err = parsePNPM(data)
	case Yarn:
		deps, err = parseYarn(data)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownLockfile, kind)
	}

	if err != nil {
		return nil, err
	}

	return normalize(deps), nil
}

// requirers inverts a dependency map: for each required name, the names of the
// packages that require it.
type requirers map[string][]string

func (r requirers) add(requirer string, required map[string]string) {
	for name := range required {
		r[name] = append(r[name], requirer)
	}
}

// decode reads data as one JSON object into v.
func decode(data []byte, v any) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return fmt.Errorf("%w: not a JSON object", ErrInvalid)
	}

	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	return nil
}

// decodeYAML reads data as one YAML mapping into v.
func decodeYAML(data []byte, v any) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%w: not a YAML mapping", ErrInvalid)
	}

	if err := doc.Content[0].Decode(v); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	return nil
}

// normalize sorts deps and merges repeated name and version pairs: a version
// is Dev only when every occurrence is, and its RequiredBy is the union.
func normalize(deps []Dep) []Dep {
	type key struct{ name, version string }

	type entry struct {
		dev bool
		by  map[string]bool
	}

	merged := make(map[key]*entry, len(deps))
	for _, d := range deps {
		k := key{d.Name, d.Version}

		e, seen := merged[k]
		if !seen {
			e = &entry{dev: d.Dev, by: map[string]bool{}}
			merged[k] = e
		}

		e.dev = e.dev && d.Dev
		for _, r := range d.RequiredBy {
			if r != "" && r != d.Name {
				e.by[r] = true
			}
		}
	}

	out := make([]Dep, 0, len(merged))
	for k, e := range merged {
		d := Dep{Name: k.name, Version: k.version, Dev: e.dev}
		for r := range e.by {
			d.RequiredBy = append(d.RequiredBy, r)
		}

		sort.Strings(d.RequiredBy)
		out = append(out, d)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}

		return out[i].Version < out[j].Version
	})

	return out
}
