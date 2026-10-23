// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pkgdeps

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/mod/modfile"

	"github.com/openartifact/openartifact/internal/ai/manifest"
)

// maxLockDepth bounds the nesting walked in a v1 npm lockfile.
const maxLockDepth = 64

func parseNPM(parts []manifest.Part) (Result, error) {
	var res Result

	if body, ok := part(parts, "package.json"); ok {
		var pkg struct {
			Dependencies         map[string]any `json:"dependencies"`
			OptionalDependencies map[string]any `json:"optionalDependencies"` //nolint:tagliatelle // npm field name
		}

		if err := json.Unmarshal([]byte(body), &pkg); err != nil {
			return Result{}, fmt.Errorf("package.json: %w", err)
		}

		res.Deps = append(res.Deps, npmSpecs(pkg.Dependencies)...)
		res.Deps = append(res.Deps, npmSpecs(pkg.OptionalDependencies)...)
	}

	for _, label := range []string{"npm-shrinkwrap.json", "package-lock.json"} {
		body, ok := part(parts, label)
		if !ok {
			continue
		}

		locked, err := npmLock(body)
		if err != nil {
			return Result{}, fmt.Errorf("%s: %w", label, err)
		}

		res.Deps = merge(res.Deps, locked)

		break
	}

	return res, nil
}

// npmSpecs lists name -> spec maps in name order.
func npmSpecs(m map[string]any) []Dep {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}

	sort.Strings(names)

	out := make([]Dep, 0, len(names))

	for _, name := range names {
		spec, _ := m[name].(string)

		// "npm:real-name@^1.0.0" installs real-name under an alias.
		if alias, ok := strings.CutPrefix(spec, "npm:"); ok {
			if at := strings.LastIndex(alias, "@"); at > 0 {
				name, spec = alias[:at], alias[at+1:]
			} else {
				name, spec = alias, ""
			}
		}

		d := Dep{Name: name}
		if isRegistrySpec(spec) {
			d.Version, d.Range = versionOrRange(spec)
		}

		out = append(out, d)
	}

	return out
}

// isRegistrySpec reports whether an npm spec names a registry version or range
// rather than a git url, file or workspace link.
func isRegistrySpec(spec string) bool {
	for _, p := range []string{"git", "http", "file:", "link:", "workspace:", "github:", "gitlab:", "bitbucket:"} {
		if strings.HasPrefix(spec, p) {
			return false
		}
	}

	return !strings.Contains(spec, "/")
}

type npmLockEntry struct {
	Name         string                  `json:"name"`
	Version      string                  `json:"version"`
	License      any                     `json:"license"`
	Dev          bool                    `json:"dev"`
	Link         bool                    `json:"link"`
	Dependencies map[string]npmLockEntry `json:"dependencies"`
}

// npmLock reads lockfile v2/v3 (`packages`) or v1 (`dependencies`). Dev-only
// packages are left out: they are not part of what the package ships.
func npmLock(body string) ([]Dep, error) {
	var lock struct {
		Packages     map[string]npmLockEntry `json:"packages"`
		Dependencies map[string]npmLockEntry `json:"dependencies"`
	}

	if err := json.Unmarshal([]byte(body), &lock); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	var out []Dep

	if len(lock.Packages) > 0 {
		keys := make([]string, 0, len(lock.Packages))
		for k := range lock.Packages {
			keys = append(keys, k)
		}

		sort.Strings(keys)

		for _, k := range keys {
			e := lock.Packages[k]
			if k == "" || e.Dev || e.Link || e.Version == "" {
				continue
			}

			name := e.Name
			if name == "" {
				var found bool

				if _, name, found = strings.CutLast(k, "node_modules/"); !found {
					continue // a workspace member, not an installed package
				}
			}

			out = append(out, Dep{Name: name, Version: e.Version, License: lockLicense(e.License)})
		}

		return out, nil
	}

	walkNPMv1(lock.Dependencies, 0, &out)

	return out, nil
}

func walkNPMv1(m map[string]npmLockEntry, depth int, out *[]Dep) {
	if depth > maxLockDepth {
		return
	}

	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}

	sort.Strings(names)

	for _, n := range names {
		e := m[n]
		if !e.Dev && e.Version != "" && isRegistrySpec(e.Version) {
			*out = append(*out, Dep{Name: n, Version: e.Version})
		}

		walkNPMv1(e.Dependencies, depth+1, out)
	}
}

func lockLicense(v any) string {
	s, _ := v.(string)
	return s
}

// cargoDep is a Cargo.toml dependency: a version string or a table.
type cargoTable map[string]any

func parseCargo(parts []manifest.Part) (Result, error) {
	var (
		res      Result
		rootName string
	)

	if body, ok := part(parts, "Cargo.toml"); ok {
		var m struct {
			Package struct {
				Name string `toml:"name"`
			} `toml:"package"`
			Dependencies      cargoTable `toml:"dependencies"`
			BuildDependencies cargoTable `toml:"build-dependencies"`
			Target            map[string]struct {
				Dependencies      cargoTable `toml:"dependencies"`
				BuildDependencies cargoTable `toml:"build-dependencies"`
			} `toml:"target"`
		}

		if err := toml.Unmarshal([]byte(body), &m); err != nil {
			return Result{}, fmt.Errorf("Cargo.toml: %w", err) //nolint:staticcheck // file name
		}

		rootName = m.Package.Name
		res.Deps = append(res.Deps, cargoDeps(m.Dependencies)...)
		res.Deps = append(res.Deps, cargoDeps(m.BuildDependencies)...)

		targets := make([]string, 0, len(m.Target))
		for t := range m.Target {
			targets = append(targets, t)
		}

		sort.Strings(targets)

		for _, t := range targets {
			res.Deps = append(res.Deps, cargoDeps(m.Target[t].Dependencies)...)
			res.Deps = append(res.Deps, cargoDeps(m.Target[t].BuildDependencies)...)
		}
	}

	if body, ok := part(parts, "Cargo.lock"); ok {
		var lock struct {
			Package []struct {
				Name    string `toml:"name"`
				Version string `toml:"version"`
			} `toml:"package"`
		}

		if err := toml.Unmarshal([]byte(body), &lock); err != nil {
			return Result{}, fmt.Errorf("Cargo.lock: %w", err) //nolint:staticcheck // file name
		}

		locked := make([]Dep, 0, len(lock.Package))

		for _, p := range lock.Package {
			if p.Name != "" && p.Name != rootName {
				locked = append(locked, Dep{Name: p.Name, Version: p.Version})
			}
		}

		res.Deps = merge(res.Deps, locked)
	}

	return res, nil
}

func cargoDeps(t cargoTable) []Dep {
	names := make([]string, 0, len(t))
	for n := range t {
		names = append(names, n)
	}

	sort.Strings(names)

	out := make([]Dep, 0, len(names))

	for _, key := range names {
		d := Dep{Name: key}

		switch v := t[key].(type) {
		case string:
			d.Version, d.Range = cargoReq(v)
		case map[string]any:
			if pkg, ok := v["package"].(string); ok && pkg != "" {
				d.Name = pkg
			}

			if req, ok := v["version"].(string); ok {
				d.Version, d.Range = cargoReq(req)
			}
		}

		out = append(out, d)
	}

	return out
}

// cargoReq returns the version a requirement pins: only "=1.2.3" does ("1.2.3"
// alone means ^1.2.3).
func cargoReq(req string) (string, string) {
	req = strings.TrimSpace(req)
	if rest, ok := strings.CutPrefix(req, "="); ok {
		if v := exactVersion(strings.TrimSpace(rest)); v != "" {
			return v, ""
		}
	}

	return "", req
}

func parseGoMod(filename string, parts []manifest.Part) (Result, error) {
	var body string

	for _, p := range parts {
		body = p.Body
		break
	}

	if body == "" {
		return Result{}, nil
	}

	f, err := modfile.ParseLax(filename, []byte(body), nil)
	if err != nil {
		return Result{}, fmt.Errorf("go.mod: %w", err)
	}

	res := Result{Deps: make([]Dep, 0, len(f.Require))}

	for _, r := range f.Require {
		res.Deps = append(res.Deps, Dep{Name: r.Mod.Path, Version: r.Mod.Version})
	}

	return res, nil
}
