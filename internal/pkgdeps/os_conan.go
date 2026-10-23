// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pkgdeps

import (
	"regexp"
	"strings"

	"github.com/openartifact/openartifact/internal/ai/manifest"
)

// fieldValue returns the value of a "Key: value" line of a rendered control
// or header block, including indented continuation lines.
func fieldValue(text, key string) string {
	vals := headerValues(text, key)
	if len(vals) == 0 {
		return ""
	}

	return vals[0]
}

var debName = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*$`)

func parseDeb(parts []manifest.Part) Result {
	var res Result

	for _, p := range parts {
		res.Namespace = debNamespace(fieldValue(p.Body, "Version"), fieldValue(p.Body, "Maintainer"))

		for _, field := range []string{"Pre-Depends", "Depends"} {
			for _, v := range headerValues(p.Body, field) {
				res.Deps = append(res.Deps, debRelations(v)...)
			}
		}
	}

	return res
}

// debNamespace is the purl namespace of a deb: its distribution. The package
// does not record one, so the version suffix and maintainer decide between
// ubuntu and debian.
func debNamespace(version, maintainer string) string {
	v, m := strings.ToLower(version), strings.ToLower(maintainer)
	if strings.Contains(v, "ubuntu") || strings.Contains(m, "ubuntu.com") {
		return "ubuntu"
	}

	return "debian"
}

// debRelations parses "a (>= 1), b | c, d:any". Of a set of alternatives the
// first is listed.
func debRelations(field string) []Dep {
	var out []Dep

	for rel := range strings.SplitSeq(field, ",") {
		alt, _, _ := strings.Cut(rel, "|")
		alt = strings.TrimSpace(alt)

		if i := strings.IndexAny(alt, "[<"); i >= 0 {
			alt = strings.TrimSpace(alt[:i]) // architecture / profile restrictions
		}

		name, constraint, _ := strings.Cut(alt, "(")
		name = strings.TrimSpace(name)

		if i := strings.Index(name, ":"); i >= 0 {
			name = name[:i]
		}

		if !debName.MatchString(name) {
			continue // substitution variables (${misc:Depends}) and junk
		}

		d := Dep{Name: name}
		constraint = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(constraint), ")"))

		if v, ok := strings.CutPrefix(constraint, "= "); ok {
			d.Version = strings.TrimSpace(v)
		} else if constraint != "" {
			d.Range = constraint
		}

		out = append(out, d)
	}

	return out
}

func parseRPM(parts []manifest.Part) Result {
	var res Result

	for _, p := range parts {
		version := fieldValue(p.Body, "Version")
		res.Namespace = rpmNamespace(fieldValue(p.Body, "Vendor"), version)

		inRequires := false

		for line := range strings.SplitSeq(p.Body, "\n") {
			switch {
			case line == "Requires:":
				inRequires = true
			case line != "" && line[0] != ' ':
				inRequires = false
			case inRequires:
				if d, ok := rpmRequire(strings.TrimSpace(line)); ok {
					res.Deps = append(res.Deps, d)
				}
			}
		}
	}

	return res
}

// rpmNamespace is the purl namespace of an rpm: the lower-cased first word of
// its Vendor, or the distribution tag of its release.
func rpmNamespace(vendor, version string) string {
	v := strings.ToLower(vendor)

	switch {
	case strings.Contains(v, "fedora"):
		return "fedora"
	case strings.Contains(v, "red hat"), strings.Contains(v, "redhat"), strings.Contains(v, "centos"):
		return "redhat"
	case strings.Contains(v, "suse"):
		return "opensuse"
	}

	for r := range strings.FieldsSeq(v) {
		if ns := alnum(r); ns != "" {
			return ns
		}
	}

	switch {
	case strings.Contains(version, ".el"):
		return "redhat"
	case strings.Contains(version, ".fc"):
		return "fedora"
	}

	return ""
}

func alnum(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}

		return -1
	}, s)
}

// rpmRequire parses "name", "name GE 1.2" (the rendered header form). Requires
// that are not packages (rpmlib features, file paths, shared-object and boolean
// capabilities) are skipped.
func rpmRequire(line string) (Dep, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return Dep{}, false
	}

	name := fields[0]
	if strings.HasPrefix(name, "rpmlib(") || strings.HasPrefix(name, "/") ||
		strings.ContainsAny(name, "()") || strings.Contains(name, ".so") {
		return Dep{}, false
	}

	d := Dep{Name: name}

	if len(fields) >= 3 {
		switch fields[1] {
		case "EQ":
			d.Version = fields[2]
		default:
			d.Range = map[string]string{"LT": "<", "GT": ">", "LE": "<=", "GE": ">="}[fields[1]] + " " + fields[2]
		}
	}

	return d, true
}

var conanRef = regexp.MustCompile(`["']([A-Za-z0-9_][A-Za-z0-9_.+-]*)/(\[[^\]"']*\]|[^"'@#/\s\[]+)(?:[@#][^"']*)?["']`)

// parseConan scans the recipe for requires declarations; a recipe is Python,
// so only its quoted name/version references are read, never executed.
func parseConan(parts []manifest.Part) Result {
	var res Result

	for _, p := range parts {
		if p.Label != "conanfile.py" {
			continue
		}

		for line := range strings.SplitSeq(p.Body, "\n") {
			if !strings.Contains(line, "requires") || strings.Contains(line, "tool_requires") ||
				strings.Contains(line, "build_requires") || strings.Contains(line, "test_requires") {
				continue
			}

			for _, m := range conanRef.FindAllStringSubmatch(line, -1) {
				d := Dep{Name: m[1]}
				if strings.HasPrefix(m[2], "[") {
					d.Range = m[2]
				} else {
					d.Version = m[2]
				}

				res.Deps = append(res.Deps, d)
			}
		}
	}

	return res
}
