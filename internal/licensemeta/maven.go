// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta

import (
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/ai/manifest"
	pkgmaven "github.com/openartifact/openartifact/pkg/maven"
)

// ParsePOM returns the license names a pom declares. A license without a name
// contributes its URL when it is a canonical one.
func ParsePOM(pom *pkgmaven.POM) []string {
	if pom == nil {
		return nil
	}

	out := make([]string, 0, len(pom.Licenses))

	for _, l := range pom.Licenses {
		switch name := strings.TrimSpace(l.Name); {
		case name != "":
			out = append(out, name)
		default:
			if id := licenseFromURL(strings.TrimSpace(l.URL)); id != "" {
				out = append(out, id)
			}
		}
	}

	return out
}

// parsePOM reads the licenses of a .pom, or of the pom a jar embeds under
// META-INF/maven. A jar without one declares nothing.
func parsePOM(filename string, parts []manifest.Part) (Result, error) {
	text := ""

	for _, p := range parts {
		if p.Label == "pom.xml" || (p.Label == "" && !strings.HasSuffix(base(filename), ".jar")) {
			text = p.Body
			break
		}
	}

	if text == "" {
		return Result{}, nil
	}

	pom, err := pkgmaven.ParsePOM(strings.NewReader(text))
	if err != nil {
		return Result{}, fmt.Errorf("pom: %w", err)
	}

	return Result{Licenses: ParsePOM(pom), Source: "pom"}, nil
}
