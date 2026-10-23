// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pkgdeps

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/openartifact/openartifact/internal/ai/manifest"
)

// xmlLimit bounds the XML read while decoding one descriptor.
const xmlLimit = 8 << 20

type pomDep struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
	Scope      string `xml:"scope"`
}

type pomProp struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}

type pomDoc struct {
	XMLName xml.Name `xml:"project"`
	GroupID string   `xml:"groupId"`
	Version string   `xml:"version"`
	Parent  struct {
		GroupID string `xml:"groupId"`
		Version string `xml:"version"`
	} `xml:"parent"`
	Properties struct {
		Items []pomProp `xml:",any"`
	} `xml:"properties"`
	Managed []pomDep `xml:"dependencyManagement>dependencies>dependency"`
	Deps    []pomDep `xml:"dependencies>dependency"`
}

type ivyDoc struct {
	XMLName xml.Name `xml:"ivy-module"`
	Deps    []struct {
		Org  string `xml:"org,attr"`
		Name string `xml:"name,attr"`
		Rev  string `xml:"rev,attr"`
	} `xml:"dependencies>dependency"`
}

func parseJVM(filename string, parts []manifest.Part) (Result, error) {
	var body string

	for _, p := range parts {
		if p.Label == "pom.xml" || (p.Label == "" && !strings.HasSuffix(strings.ToLower(filename), ".jar")) {
			body = p.Body
			break
		}
	}

	if body == "" {
		return Result{}, nil
	}

	dec := xml.NewDecoder(io.LimitReader(strings.NewReader(body), xmlLimit))

	// The root element tells a pom from an ivy descriptor.
	for {
		tok, err := dec.Token()
		if err != nil {
			return Result{}, fmt.Errorf("pom: %w", err)
		}

		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}

		if start.Name.Local == "ivy-module" {
			var doc ivyDoc
			if err := dec.DecodeElement(&doc, &start); err != nil {
				return Result{}, fmt.Errorf("ivy.xml: %w", err)
			}

			return ivyDeps(doc), nil
		}

		var doc pomDoc
		if err := dec.DecodeElement(&doc, &start); err != nil {
			return Result{}, fmt.Errorf("pom: %w", err)
		}

		return pomDeps(&doc), nil
	}
}

func ivyDeps(doc ivyDoc) Result {
	res := Result{Deps: make([]Dep, 0, len(doc.Deps))}

	for _, d := range doc.Deps {
		v, r := versionOrRange(d.Rev)
		if v == "" && r != "" && !strings.ContainsAny(r, "[](),+*") {
			v, r = r, "" // ivy revisions are plain versions unless a range syntax is used
		}

		res.Deps = append(res.Deps, Dep{Name: d.Org + ":" + d.Name, Version: v, Range: r})
	}

	return res
}

// pomDeps lists the compile and runtime dependencies of a pom. Test, provided
// and system scopes are not part of what the package needs at run time.
func pomDeps(doc *pomDoc) Result {
	props := map[string]string{}

	for _, p := range doc.Properties.Items {
		props[p.XMLName.Local] = strings.TrimSpace(p.Value)
	}

	version := doc.Version
	if version == "" {
		version = doc.Parent.Version
	}

	group := doc.GroupID
	if group == "" {
		group = doc.Parent.GroupID
	}

	for _, k := range []string{"project.version", "pom.version", "version"} {
		props[k] = version
	}

	for _, k := range []string{"project.groupId", "pom.groupId", "groupId"} {
		props[k] = group
	}

	props["project.parent.version"] = doc.Parent.Version
	props["parent.version"] = doc.Parent.Version

	resolve := func(s string) string { return expandProps(strings.TrimSpace(s), props) }

	managed := map[string]string{}

	for _, m := range doc.Managed {
		managed[resolve(m.GroupID)+":"+resolve(m.ArtifactID)] = resolve(m.Version)
	}

	var res Result

	for _, d := range doc.Deps {
		switch strings.TrimSpace(d.Scope) {
		case "test", "provided", "system":
			continue
		}

		g, a := resolve(d.GroupID), resolve(d.ArtifactID)
		if g == "" || a == "" || strings.Contains(g+a, "${") {
			continue
		}

		v := resolve(d.Version)
		if v == "" {
			v = managed[g+":"+a]
		}

		dep := Dep{Name: g + ":" + a}

		switch {
		case strings.Contains(v, "${"):
			// an unresolved property is not a version
		case strings.ContainsAny(v, "[](),"):
			dep.Range = v
		default:
			dep.Version = v
		}

		res.Deps = append(res.Deps, dep)
	}

	return res
}

// expandProps substitutes ${name} references, repeating for properties that
// refer to other properties (bounded, so a cycle cannot loop).
func expandProps(s string, props map[string]string) string {
	for range 8 {
		start := strings.Index(s, "${")
		if start < 0 {
			return s
		}

		end := strings.Index(s[start:], "}")
		if end < 0 {
			return s
		}

		val, ok := props[s[start+2:start+end]]
		if !ok {
			return s
		}

		s = s[:start] + val + s[start+end+1:]
	}

	return s
}

type nuspecDoc struct {
	XMLName xml.Name `xml:"package"`
	Deps    []struct {
		ID      string `xml:"id,attr"`
		Version string `xml:"version,attr"`
	} `xml:"metadata>dependencies>dependency"`
	Groups []struct {
		Deps []struct {
			ID      string `xml:"id,attr"`
			Version string `xml:"version,attr"`
		} `xml:"dependency"`
	} `xml:"metadata>dependencies>group"`
}

func parseNuGet(parts []manifest.Part) (Result, error) {
	body, ok := part(parts, ".nuspec")
	if !ok {
		return Result{}, nil
	}

	var doc nuspecDoc
	if err := xml.NewDecoder(io.LimitReader(strings.NewReader(body), xmlLimit)).Decode(&doc); err != nil {
		return Result{}, fmt.Errorf(".nuspec: %w", err)
	}

	var res Result

	add := func(id, version string) {
		d := Dep{Name: id}
		version = strings.TrimSpace(version)

		// "[1.2.3]" pins a version; a bare "1.2.3" is a minimum.
		if inner, ok := strings.CutPrefix(version, "["); ok {
			if inner, ok = strings.CutSuffix(inner, "]"); ok && !strings.Contains(inner, ",") {
				d.Version = strings.TrimSpace(inner)
			}
		}

		if d.Version == "" {
			d.Range = version
		}

		res.Deps = append(res.Deps, d)
	}

	for _, d := range doc.Deps {
		add(d.ID, d.Version)
	}

	for _, g := range doc.Groups {
		for _, d := range g.Deps {
			add(d.ID, d.Version)
		}
	}

	return res, nil
}
