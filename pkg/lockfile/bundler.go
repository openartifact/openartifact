// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package lockfile

import (
	"bufio"
	"bytes"
	"strings"
)

// bundlerSpec is one gem of a Gemfile.lock section: "    name (version)" and,
// below it, "      dependency (requirement)" lines.
type bundlerSpec struct {
	name, version string
	requires      []string
	registry      bool // in a GEM section: installed from a gem server
	project       bool // in a PATH section: the project's own gems
}

// parseBundler reads the gems of a Gemfile.lock installed from a gem server
// (its GEM sections); GIT and PATH gems are not served by a registry. A gem
// locked for several platforms ("1.16.0-x86_64-linux") is one version, the
// number before the platform, as the registry's curation judges it. PATH gems
// are the project's own code and no requirers. Gemfile.lock does not record
// which gems only the development or test groups need: no Dep of it is Dev.
func parseBundler(data []byte) ([]Dep, error) {
	specs, err := bundlerSpecs(data)
	if err != nil {
		return nil, err
	}

	by := requirers{}

	for _, s := range specs {
		if !s.project {
			for _, r := range s.requires {
				by[r] = append(by[r], s.name)
			}
		}
	}

	deps := make([]Dep, 0, len(specs))

	for _, s := range specs {
		number, _, _ := strings.Cut(s.version, "-")
		if s.registry && s.name != "" && fromRegistry(number, "") {
			deps = append(deps, Dep{Name: s.name, Version: number, RequiredBy: by[s.name]})
		}
	}

	return deps, nil
}

// bundlerSpecs reads the specs of every GEM, GIT and PATH section.
func bundlerSpecs(data []byte) ([]bundlerSpec, error) {
	var (
		specs   []bundlerSpec
		section string
		inSpecs bool
		headers int
	)

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, yarnMaxLine)

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")

		switch {
		case strings.TrimSpace(line) == "":
			continue
		case !strings.HasPrefix(line, " "):
			section, inSpecs = line, false
			headers++
		case line == "  specs:":
			inSpecs = true
		case !inSpecs || (section != "GEM" && section != "GIT" && section != "PATH"):
		case strings.HasPrefix(line, "      ") && !strings.HasPrefix(line, "       "):
			if len(specs) > 0 {
				if name, _ := bundlerEntry(line); name != "" {
					specs[len(specs)-1].requires = append(specs[len(specs)-1].requires, name)
				}
			}
		case strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     "):
			name, version := bundlerEntry(line)
			specs = append(specs, bundlerSpec{name: name, version: version, registry: section == "GEM", project: section == "PATH"})
		}
	}

	if err := sc.Err(); err != nil {
		return nil, invalid(err.Error())
	}

	if headers == 0 {
		return nil, invalid("no section")
	}

	return specs, nil
}

// bundlerEntry splits "name (version)" or "name (requirement)"; the part in
// parentheses is "" when there is none ("name" or "name!").
func bundlerEntry(line string) (string, string) {
	line = strings.TrimSpace(line)

	name, rest, found := strings.Cut(line, " (")
	if !found {
		return strings.TrimSuffix(name, "!"), ""
	}

	version, _, _ := strings.Cut(rest, ")")

	return name, strings.TrimSpace(version)
}
