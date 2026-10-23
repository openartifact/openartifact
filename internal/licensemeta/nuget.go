// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta

import (
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
)

// nuspecLicenses are the license elements of a .nuspec.
type nuspecLicenses struct {
	Metadata struct {
		License struct {
			Type string `xml:"type,attr"`
			Text string `xml:",chardata"`
		} `xml:"license"`
		LicenseURL string `xml:"licenseUrl"`
	} `xml:"metadata"`
}

// licensesNuGetOrgPrefix is the URL NuGet gives a package whose nuspec has a
// license expression, for clients that only read licenseUrl.
const licensesNuGetOrgPrefix = "https://licenses.nuget.org/"

// knownLicenseURLs maps license URLs that name exactly one license (compared
// lowercase, without a trailing slash and scheme-insensitively).
//
//nolint:gochecknoglobals // immutable lookup table
var knownLicenseURLs = map[string]string{
	"//www.apache.org/licenses/license-2.0":      "Apache-2.0",
	"//www.apache.org/licenses/license-2.0.html": "Apache-2.0",
	"//www.apache.org/licenses/license-2.0.txt":  "Apache-2.0",
	"//opensource.org/licenses/apache-2.0":       "Apache-2.0",
	"//opensource.org/licenses/mit":              "MIT",
	"//opensource.org/license/mit":               "MIT",
	"//opensource.org/licenses/bsd-3-clause":     "BSD-3-Clause",
	"//opensource.org/licenses/bsd-2-clause":     "BSD-2-Clause",
	"//opensource.org/licenses/ms-pl":            "MS-PL",
	"//opensource.org/licenses/ms-rl":            "MS-RL",
	"//opensource.org/licenses/mpl-2.0":          "MPL-2.0",
	"//www.mozilla.org/mpl/2.0":                  "MPL-2.0",
	"//www.gnu.org/licenses/gpl-3.0":             "GPL-3.0-only",
	"//www.gnu.org/licenses/gpl-3.0.html":        "GPL-3.0-only",
	"//www.gnu.org/licenses/gpl-3.0.txt":         "GPL-3.0-only",
	"//www.gnu.org/licenses/lgpl-3.0":            "LGPL-3.0-only",
	"//www.gnu.org/licenses/lgpl-3.0.html":       "LGPL-3.0-only",
	"//www.gnu.org/licenses/lgpl-3.0.txt":        "LGPL-3.0-only",
}

// parseNuspec reads the license of a .nuspec: <license type="expression"> (an
// SPDX expression) first, then the deprecated <licenseUrl>. A URL names a
// license only when it is licenses.nuget.org/<expression> or a canonical URL of
// one license; any other URL (an EULA, a project page, a repository blob) says
// nothing a policy could match and declares no license. <license type="file">
// names a file in the package, not a license.
func parseNuspec(text string) (Result, error) {
	var spec nuspecLicenses

	if err := xml.Unmarshal([]byte(text), &spec); err != nil {
		return Result{}, fmt.Errorf(".nuspec: %w", err)
	}

	lic := spec.Metadata.License
	if strings.EqualFold(strings.TrimSpace(lic.Type), "expression") {
		if expr := strings.TrimSpace(lic.Text); expr != "" {
			return Result{Licenses: []string{expr}, Source: ".nuspec"}, nil
		}
	}

	if strings.TrimSpace(lic.Type) != "" {
		return Result{}, nil // type="file": the package ships its own text
	}

	raw := strings.TrimSpace(spec.Metadata.LicenseURL)
	if raw == "" {
		return Result{}, nil
	}

	if id := licenseFromURL(raw); id != "" {
		return Result{Licenses: []string{id}, Source: ".nuspec"}, nil
	}

	return Result{}, nil
}

// licenseFromURL maps a license URL to the license it names, or "".
func licenseFromURL(raw string) string {
	if rest, ok := strings.CutPrefix(raw, licensesNuGetOrgPrefix); ok {
		if expr, err := url.PathUnescape(rest); err == nil && strings.TrimSpace(expr) != "" {
			return strings.TrimSpace(expr)
		}
	}

	key := strings.ToLower(strings.TrimRight(raw, "/"))
	key = strings.TrimPrefix(strings.TrimPrefix(key, "https:"), "http:")

	if id, ok := knownLicenseURLs[key]; ok {
		return id
	}

	return ""
}
