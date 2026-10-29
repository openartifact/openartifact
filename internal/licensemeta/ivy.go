// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta

import (
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/ai/manifest"
	pkgmaven "github.com/openartifact/openartifact/pkg/maven"
)

// IvyHooks reads the licenses of ivy: the <info> of the module descriptor. The
// descriptor is the authority, so it is expected whenever it is supported.
var IvyHooks = Hooks{ //nolint:gochecknoglobals // immutable format hook value
	Filter:      isIvyDescriptor,
	Expected:    expectSupported,
	MemberLimit: pkgmaven.MaxPOMBytes,
	Parse:       func(_ string, parts []manifest.Part) (Result, error) { return parseIvy(parts[0].Body) },
}

// isIvyDescriptor reports whether the file name is a module descriptor of an
// Ivy repository: ivy.xml, or ivy-<revision>.xml as the default Ivy pattern
// writes it. Other .xml files of a module (reports, settings) are plain
// artifacts. The table keys a file by its full name plus its extension, so a
// stored descriptor is listed as "ivy.xml.xml"; that doubled suffix is the
// same file.
func isIvyDescriptor(filename string) bool {
	name := strings.TrimSuffix(base(filename), ".xml")
	if !strings.HasSuffix(name, ".xml") {
		name = base(filename)
	}

	return name == "ivy.xml" || (strings.HasPrefix(name, "ivy-") && strings.HasSuffix(name, ".xml"))
}

// parseIvy reads the <license> elements of the <info> of an ivy.xml. Like a
// pom license, one without a name contributes its URL when it is a canonical
// one. Nothing but the root-level <info> counts: Ivy has no other place for a
// module's own license.
func parseIvy(text string) (Result, error) {
	var doc struct {
		Info struct {
			Licenses []struct {
				Name string `xml:"name,attr"`
				URL  string `xml:"url,attr"`
			} `xml:"license"`
		} `xml:"info"`
	}

	if err := xml.Unmarshal([]byte(text), &doc); err != nil {
		return Result{}, fmt.Errorf("ivy.xml: %w", err)
	}

	res := Result{Source: "ivy.xml"}

	for _, l := range doc.Info.Licenses {
		switch name := strings.TrimSpace(l.Name); {
		case name != "":
			res.Licenses = append(res.Licenses, name)
		default:
			if id := licenseFromURL(strings.TrimSpace(l.URL)); id != "" {
				res.Licenses = append(res.Licenses, id)
			}
		}
	}

	return res, nil
}
