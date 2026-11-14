// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package lockfile

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// yarnMaxLine bounds one line of a classic yarn.lock.
const yarnMaxLine = 1 << 20

// parseYarn reads the registry packages of a yarn.lock: the YAML of Yarn 2 and
// later (it has a __metadata entry) or the format of Yarn 1. Neither records
// which packages are development-only.
func parseYarn(data []byte) ([]Dep, error) {
	if berry(data) {
		return parseYarnBerry(data)
	}

	return parseYarnClassic(data)
}

// berry reports whether a yarn.lock is the YAML of Yarn 2 and later.
func berry(data []byte) bool {
	for line := range bytes.Lines(data) {
		if bytes.HasPrefix(line, []byte("__metadata:")) {
			return true
		}
	}

	return false
}

// yarnBerryEntry is one resolution of a Yarn 2+ lockfile.
type yarnBerryEntry struct {
	Resolution           string            `yaml:"resolution"`
	Dependencies         map[string]string `yaml:"dependencies"`
	OptionalDependencies map[string]string `yaml:"optionalDependencies"`
	PeerDependencies     map[string]string `yaml:"peerDependencies"`
}

// parseYarnBerry reads the entries resolved through the npm protocol
// ("name@npm:1.0.0"); workspaces, patches, git, file, link and portal
// resolutions are not served by a registry. The project's workspaces are not
// requirers: what only they require is a direct dependency.
func parseYarnBerry(data []byte) ([]Dep, error) {
	var lock map[string]yarnBerryEntry
	if err := decodeYAML(data, &lock); err != nil {
		return nil, err
	}

	delete(lock, "__metadata")

	by := requirers{}
	for _, e := range lock {
		name, protocol, _ := yarnBerryResolution(e.Resolution)
		if name == "" || protocol == "workspace" {
			continue
		}

		by.add(name, yarnRequired(e.Dependencies))
		by.add(name, yarnRequired(e.OptionalDependencies))
		by.add(name, yarnRequired(e.PeerDependencies))
	}

	deps := make([]Dep, 0, len(lock))

	for _, e := range lock {
		name, protocol, version := yarnBerryResolution(e.Resolution)
		if name == "" || protocol != "npm" || !fromRegistry(version, "") {
			continue
		}

		deps = append(deps, Dep{Name: name, Version: version, RequiredBy: by[name]})
	}

	return deps, nil
}

// yarnBerryResolution splits "name@protocol:reference" (the name possibly
// scoped) into its parts; name is empty when it is not of that form.
func yarnBerryResolution(res string) (string, string, string) {
	name, rest, ok := splitName(res)
	if !ok {
		return "", "", ""
	}

	protocol, reference, ok := strings.Cut(rest, ":")
	if !ok {
		return "", "", ""
	}

	return name, protocol, reference
}

// splitName splits "name@rest" at the first "@" after a scope's: the name is
// "pkg" or "@scope/pkg", rest everything after (a range, a version or a
// protocol and its reference, which may hold more "@").
func splitName(s string) (string, string, bool) {
	if len(s) < 2 {
		return "", "", false
	}

	at := strings.IndexByte(s[1:], '@')
	if at < 0 {
		return "", "", false
	}

	name, _, ok := validNameVersion(s[:at+1], "-")

	return name, s[at+2:], ok
}

// yarnRequired maps a dependency map (name to range) to the names of the
// packages it installs: an "npm:other@^1.0.0" alias installs other.
func yarnRequired(deps map[string]string) map[string]string {
	out := make(map[string]string, len(deps))
	for name, rng := range deps {
		out[yarnAliasTarget(name, rng)] = rng
	}

	return out
}

// yarnAliasTarget is the package a dependency on name with range rng installs.
func yarnAliasTarget(name, rng string) string {
	if alias, ok := strings.CutPrefix(rng, "npm:"); ok {
		if target, _, ok := splitName(alias); ok {
			return target
		}
	}

	return name
}

// yarnClassicEntry is one entry of a Yarn 1 lockfile: the descriptors of its
// header line and its fields.
type yarnClassicEntry struct {
	descriptor string // the first one, "name@range"
	version    string
	resolved   string
	deps       map[string]string
}

// parseYarnClassic reads a Yarn 1 lockfile: a header line of descriptors
// ("name@^1.0.0", "name@^1.1.0":) per resolution, then indented fields, and
// indented dependencies and optionalDependencies blocks. An entry is from the
// registry when its range is one (not a git, GitHub, file, link or URL
// specifier) and it resolved to an HTTP(S) tarball.
func parseYarnClassic(data []byte) ([]Dep, error) {
	entries, err := yarnClassicEntries(data)
	if err != nil {
		return nil, err
	}

	by := requirers{}

	for _, e := range entries {
		if name, _, ok := yarnClassicDescriptor(e.descriptor); ok {
			by.add(name, yarnRequired(e.deps))
		}
	}

	deps := make([]Dep, 0, len(entries))

	for _, e := range entries {
		name, rng, ok := yarnClassicDescriptor(e.descriptor)
		if !ok || !fromRegistry(rng, "") || !fromRegistry(e.version, e.resolved) {
			continue
		}

		deps = append(deps, Dep{Name: name, Version: e.version, RequiredBy: by[name]})
	}

	return deps, nil
}

// yarnClassicEntries splits a Yarn 1 lockfile into its entries.
func yarnClassicEntries(data []byte) ([]yarnClassicEntry, error) {
	var (
		entries []yarnClassicEntry
		cur     *yarnClassicEntry
		section string
	)

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), yarnMaxLine)

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)

		switch {
		case trimmed == "" || strings.HasPrefix(trimmed, "#"):
			continue
		case indent == 0:
			header, ok := strings.CutSuffix(line, ":")
			if !ok {
				return nil, fmt.Errorf("%w: line %q is not an entry header", ErrInvalid, line)
			}

			first, _, _ := strings.Cut(header, ", ")
			entries = append(entries, yarnClassicEntry{descriptor: yarnUnquote(first), deps: map[string]string{}})
			cur, section = &entries[len(entries)-1], ""
		case cur == nil:
			return nil, fmt.Errorf("%w: indented line before the first entry", ErrInvalid)
		case indent == 2:
			key, value := yarnField(trimmed)
			section = ""

			switch {
			case value == "" && strings.HasSuffix(trimmed, ":"):
				section = strings.TrimSuffix(key, ":")
			case key == "version":
				cur.version = value
			case key == "resolved":
				cur.resolved = value
			}
		case section == "dependencies" || section == "optionalDependencies":
			name, rng := yarnField(trimmed)
			cur.deps[name] = rng
		}
	}

	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	return entries, nil
}

// yarnClassicDescriptor splits "name@range" (the name possibly scoped) and
// resolves an "npm:other@range" alias to other.
func yarnClassicDescriptor(desc string) (string, string, bool) {
	name, rng, ok := splitName(desc)
	if !ok {
		return "", "", false
	}

	if alias, isAlias := strings.CutPrefix(rng, "npm:"); isAlias {
		return splitName(alias)
	}

	return name, rng, true
}

// yarnField splits an indented line into its key and value, either of which
// may be quoted.
func yarnField(s string) (string, string) {
	key, rest := yarnToken(s)
	value, _ := yarnToken(strings.TrimLeft(rest, " "))

	return key, value
}

// yarnToken reads one possibly quoted token from the start of s.
func yarnToken(s string) (string, string) {
	if strings.HasPrefix(s, `"`) {
		if q, err := strconv.QuotedPrefix(s); err == nil {
			return yarnUnquote(q), s[len(q):]
		}
	}

	token, rest, _ := strings.Cut(s, " ")

	return token, rest
}

// yarnUnquote removes the quotes of a quoted token.
func yarnUnquote(s string) string {
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}

	return s
}
