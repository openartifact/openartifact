// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pkgdeps

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/openartifact/openartifact/internal/ai/manifest"
)

var (
	pyReq     = regexp.MustCompile(`^\s*([A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?)\s*(?:\[[^\]]*\])?\s*(.*)$`)
	pyExactEq = regexp.MustCompile(`^\(?\s*===?\s*([^\s,;)*]+)\s*\)?$`)
)

// parsePyPI reads the Requires-Dist headers of a METADATA / PKG-INFO. A
// requirement gated on an extra is optional and not part of the base install.
func parsePyPI(parts []manifest.Part) Result {
	var res Result

	for _, p := range parts {
		for _, v := range headerValues(p.Body, "Requires-Dist") {
			req, marker, _ := strings.Cut(v, ";")
			if strings.Contains(marker, "extra") {
				continue
			}

			m := pyReq.FindStringSubmatch(req)
			if m == nil {
				continue
			}

			d := Dep{Name: m[1]}

			spec := strings.TrimSpace(m[2])
			if e := pyExactEq.FindStringSubmatch(spec); e != nil {
				d.Version = e[1]
			} else {
				d.Range = strings.Trim(spec, "() ")
			}

			res.Deps = append(res.Deps, d)
		}
	}

	return res
}

// headerValues returns the values of an RFC 822 style header, joining folded
// continuation lines and stopping at the end of the header block.
func headerValues(text, key string) []string {
	var (
		out []string
		cur *string
	)

	prefix := strings.ToLower(key) + ":"

	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			break
		}

		if line[0] == ' ' || line[0] == '\t' {
			if cur != nil {
				*cur += " " + strings.TrimSpace(line)
			}

			continue
		}

		cur = nil

		if strings.HasPrefix(strings.ToLower(line), prefix) {
			out = append(out, strings.TrimSpace(line[len(prefix):]))
			cur = &out[len(out)-1]
		}
	}

	return out
}

func parseGem(parts []manifest.Part) (Result, error) {
	var res Result

	for _, p := range parts {
		var doc struct {
			Dependencies []struct {
				Name        string `yaml:"name"`
				Type        string `yaml:"type"`
				Requirement struct {
					Requirements [][]any `yaml:"requirements"`
				} `yaml:"requirement"`
			} `yaml:"dependencies"`
		}

		if err := yaml.Unmarshal([]byte(p.Body), &doc); err != nil {
			return Result{}, fmt.Errorf("gemspec: %w", err)
		}

		for _, d := range doc.Dependencies {
			if strings.TrimPrefix(d.Type, ":") != "runtime" && d.Type != "" {
				continue
			}

			dep := Dep{Name: d.Name}

			var clauses []string

			for _, r := range d.Requirement.Requirements {
				if len(r) != 2 {
					continue
				}

				op, _ := r[0].(string)
				clauses = append(clauses, op+" "+gemVersion(r[1]))

				if len(d.Requirement.Requirements) == 1 && op == "=" {
					dep.Version = gemVersion(r[1])
				}
			}

			if dep.Version == "" {
				if rng := strings.Join(clauses, ", "); rng != ">= 0" {
					dep.Range = rng
				}
			}

			res.Deps = append(res.Deps, dep)
		}
	}

	return res, nil
}

// gemVersion unwraps a Gem::Version node ({version: "1.0"}).
func gemVersion(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		s, _ := t["version"].(string)
		return s
	}

	return ""
}

type helmDeps struct {
	Dependencies []struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
	} `yaml:"dependencies"`
}

func parseHelm(parts []manifest.Part) (Result, error) {
	var res Result

	for _, label := range []string{"Chart.yaml", "requirements.yaml"} {
		body, ok := part(parts, label)
		if !ok {
			continue
		}

		var doc helmDeps
		if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
			return Result{}, fmt.Errorf("%s: %w", label, err)
		}

		for _, d := range doc.Dependencies {
			v, r := versionOrRange(d.Version)
			res.Deps = append(res.Deps, Dep{Name: d.Name, Version: v, Range: r})
		}
	}

	if body, ok := part(parts, "Chart.lock"); ok {
		var doc helmDeps
		if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
			return Result{}, fmt.Errorf("Chart.lock: %w", err) //nolint:staticcheck // file name
		}

		locked := make([]Dep, 0, len(doc.Dependencies))
		for _, d := range doc.Dependencies {
			locked = append(locked, Dep{Name: d.Name, Version: d.Version})
		}

		res.Deps = merge(res.Deps, locked)
	}

	return res, nil
}

const condaIndexMarker = "### info/index.json\n"

// condaIndex returns the body of info/index.json: its own part for a .tar.bz2,
// a section of the joined text for a .conda.
func condaIndex(parts []manifest.Part) (string, bool) {
	if body, ok := part(parts, "info/index.json"); ok {
		return body, true
	}

	for _, p := range parts {
		_, rest, ok := strings.Cut(p.Body, condaIndexMarker)
		if !ok {
			continue
		}

		body, _, _ := strings.Cut(rest, "\n### ")

		return body, true
	}

	return "", false
}

func parseConda(parts []manifest.Part) (Result, error) {
	body, ok := condaIndex(parts)
	if !ok {
		return Result{}, nil
	}

	var idx struct {
		Depends []string `json:"depends"`
	}

	if err := json.Unmarshal([]byte(body), &idx); err != nil {
		return Result{}, fmt.Errorf("info/index.json: %w", err)
	}

	var res Result

	for _, spec := range idx.Depends {
		fields := strings.Fields(spec)
		if len(fields) == 0 {
			continue
		}

		d := Dep{Name: fields[0]}

		if len(fields) > 1 {
			if v, ok := strings.CutPrefix(fields[1], "=="); ok && !strings.ContainsAny(v, "*,|<>=!~") {
				d.Version = v
			} else {
				d.Range = fields[1]
			}
		}

		res.Deps = append(res.Deps, d)
	}

	return res, nil
}
