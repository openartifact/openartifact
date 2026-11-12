// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package lockfile reads the package versions a lockfile pins, for `oa check`:
// composer.lock and npm's package-lock.json. It keeps only what a client
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
)

// Kind is the lockfile format.
type Kind string

// The lockfile formats Parse reads.
const (
	Composer Kind = "composer"
	NPM      Kind = "npm"
)

// Dep is one package version a lockfile pins. Dev reports that the lockfile
// marks it as a development dependency (only).
type Dep struct {
	Name    string `json:"package"`
	Version string `json:"version"`
	Dev     bool   `json:"dev,omitempty"`
}

var (
	// ErrUnknownLockfile is a file name Parse cannot tell the format of.
	ErrUnknownLockfile = errors.New("unknown lockfile: expected composer.lock or package-lock.json")
	// ErrInvalid is a lockfile that is not the JSON document its format expects.
	ErrInvalid = errors.New("invalid lockfile")
)

// KindOf returns the format of a lockfile from its file name.
func KindOf(path string) (Kind, error) {
	switch filepath.Base(path) {
	case "composer.lock":
		return Composer, nil
	case "package-lock.json", "npm-shrinkwrap.json":
		return NPM, nil
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
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownLockfile, kind)
	}

	if err != nil {
		return nil, err
	}

	return normalize(deps), nil
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

// normalize sorts deps and merges repeated name and version pairs.
func normalize(deps []Dep) []Dep {
	type key struct{ name, version string }

	merged := make(map[key]bool, len(deps)) // value: every occurrence is dev
	for _, d := range deps {
		k := key{d.Name, d.Version}
		if dev, seen := merged[k]; seen {
			merged[k] = dev && d.Dev
		} else {
			merged[k] = d.Dev
		}
	}

	out := make([]Dep, 0, len(merged))
	for k, dev := range merged {
		out = append(out, Dep{Name: k.name, Version: k.version, Dev: dev})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}

		return out[i].Version < out[j].Version
	})

	return out
}
