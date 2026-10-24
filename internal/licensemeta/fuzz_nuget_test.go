// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta

import (
	"encoding/xml"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

// fuzzNuGetKnownIDs are the licenses knownLicenseURLs can name.
func fuzzNuGetKnownIDs() map[string]bool {
	ids := make(map[string]bool, len(knownLicenseURLs))
	for _, id := range knownLicenseURLs {
		ids[id] = true
	}

	return ids
}

// fuzzNuGetXMLText reports whether s survives an XML round trip: valid UTF-8
// made of characters XML 1.0 allows.
func fuzzNuGetXMLText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}

	for _, r := range s {
		switch {
		case r == '\t', r == '\n', r == '\r':
		case r >= 0x20 && r <= 0xD7FF, r >= 0xE000 && r <= 0xFFFD, r >= 0x10000:
		default:
			return false
		}
	}

	return true
}

// FuzzNuGetNuspec feeds parseNuspec any text: the result is at most one
// license, trimmed, read from the .nuspec, and an error carries no result.
func FuzzNuGetNuspec(f *testing.F) {
	f.Add(`<package><metadata><license type="expression">MIT</license></metadata></package>`)
	f.Add(`<package><metadata><license type="EXPRESSION"> MIT OR Apache-2.0 </license></metadata></package>`)
	f.Add(`<package><metadata><license type="file">LICENSE.txt</license></metadata></package>`)
	f.Add(`<package><metadata><licenseUrl>https://licenses.nuget.org/MIT</licenseUrl></metadata></package>`)
	f.Add(`<package><metadata><licenseUrl>http://www.apache.org/licenses/LICENSE-2.0.html</licenseUrl></metadata></package>`)
	f.Add(`<package><metadata><licenseUrl>https://example.com/eula</licenseUrl></metadata></package>`)
	f.Add(`<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"><metadata><license type="expression"/></metadata></package>`)
	f.Add(`<package><metadata><license type="expression">MIT</license><license type="file">x</license></metadata></package>`)
	f.Add(`<package`)
	f.Add(``)

	f.Fuzz(func(t *testing.T, text string) {
		res, err := parseNuspec(text)
		if err != nil {
			if !res.Empty() || res.Source != "" || res.Parent != nil {
				t.Fatalf("error %v with a result %+v", err, res)
			}

			return
		}

		fuzzNuGetCheckResult(t, res)
	})
}

func fuzzNuGetCheckResult(t *testing.T, res Result) {
	t.Helper()

	if res.Parent != nil {
		t.Fatalf("a nuspec has no parent: %+v", res)
	}

	switch len(res.Licenses) {
	case 0:
		if res.Source != "" {
			t.Fatalf("source %q without a license", res.Source)
		}
	case 1:
		lic := res.Licenses[0]
		if lic == "" || lic != strings.TrimSpace(lic) {
			t.Fatalf("license %q is blank or not trimmed", lic)
		}

		if res.Source != ".nuspec" {
			t.Fatalf("source = %q, want .nuspec", res.Source)
		}
	default:
		t.Fatalf("a nuspec declares one license, got %q", res.Licenses)
	}
}

// fuzzNuGetNuspecOf renders the license elements of a nuspec.
func fuzzNuGetNuspecOf(typ, text, licenseURL string) string {
	var b strings.Builder

	b.WriteString(`<package><metadata><license type="`)
	_ = xml.EscapeText(&b, []byte(typ))
	b.WriteString(`">`)
	_ = xml.EscapeText(&b, []byte(text))
	b.WriteString(`</license><licenseUrl>`)
	_ = xml.EscapeText(&b, []byte(licenseURL))
	b.WriteString(`</licenseUrl></metadata></package>`)

	return b.String()
}

// fuzzNuGetWant is what the documented rules give for the three fields.
func fuzzNuGetWant(typ, text, licenseURL string) []string {
	if strings.EqualFold(strings.TrimSpace(typ), "expression") {
		if expr := strings.TrimSpace(text); expr != "" {
			return []string{expr}
		}
	}

	if strings.TrimSpace(typ) != "" {
		return nil // type="file": a file of the package, not a license
	}

	raw := strings.TrimSpace(licenseURL)
	if raw == "" {
		return nil
	}

	if id := licenseFromURL(raw); id != "" {
		return []string{id}
	}

	return nil
}

// FuzzNuGetNuspecFields builds a well-formed nuspec from the license type, its
// text and the licenseUrl: the license read must be the documented one, an
// expression before a URL, and a type="file" license never falls back to the URL.
func FuzzNuGetNuspecFields(f *testing.F) {
	f.Add("expression", "MIT", "")
	f.Add("", "", "https://licenses.nuget.org/Apache-2.0")
	f.Add("file", "LICENSE", "https://licenses.nuget.org/MIT")
	f.Add("expression", "  ", "https://opensource.org/licenses/MIT")
	f.Add(" Expression ", " GPL-3.0-only ", "")
	f.Add("", "ignored text", "https://licenses.nuget.org/MIT%20OR%20Apache-2.0")

	f.Fuzz(func(t *testing.T, typ, text, licenseURL string) {
		if !fuzzNuGetXMLText(typ) || !fuzzNuGetXMLText(text) || !fuzzNuGetXMLText(licenseURL) {
			return
		}

		res, err := parseNuspec(fuzzNuGetNuspecOf(typ, text, licenseURL))
		if err != nil {
			t.Fatalf("a well-formed nuspec is refused: %v", err)
		}

		fuzzNuGetCheckResult(t, res)

		want := fuzzNuGetWant(typ, text, licenseURL)
		if len(res.Licenses) != len(want) || (len(want) == 1 && res.Licenses[0] != want[0]) {
			t.Fatalf("type %q text %q url %q: licenses = %q, want %q", typ, text, licenseURL, res.Licenses, want)
		}
	})
}

// FuzzNuGetLicenseURL checks licenseFromURL: a licenses.nuget.org URL gives its
// unescaped expression, any other URL one of the licenses the table names, and a
// trailing slash changes nothing for the latter.
func FuzzNuGetLicenseURL(f *testing.F) {
	for key := range knownLicenseURLs {
		f.Add("https:" + key)
	}

	f.Add("https://licenses.nuget.org/MIT")
	f.Add("https://licenses.nuget.org/MIT%20OR%20Apache-2.0")
	f.Add("https://licenses.nuget.org/%zz")
	f.Add("https://licenses.nuget.org/%20")
	f.Add("https://licenses.nuget.org/")
	f.Add("https://licenses.nuget.org")
	f.Add("HTTP://WWW.APACHE.ORG/LICENSES/LICENSE-2.0/")
	f.Add("")

	known := fuzzNuGetKnownIDs()

	f.Fuzz(func(t *testing.T, raw string) {
		got := licenseFromURL(raw)

		if got != strings.TrimSpace(got) {
			t.Fatalf("licenseFromURL(%q) = %q is not trimmed", raw, got)
		}

		if rest, ok := strings.CutPrefix(raw, licensesNuGetOrgPrefix); ok {
			want := ""
			if expr, err := url.PathUnescape(rest); err == nil {
				want = strings.TrimSpace(expr)
			}

			if got != want {
				t.Fatalf("licenseFromURL(%q) = %q, want the unescaped expression %q", raw, got, want)
			}

			return
		}

		if got != "" && !known[got] {
			t.Fatalf("licenseFromURL(%q) = %q is not a license the table names", raw, got)
		}

		if strings.HasPrefix(raw+"/", licensesNuGetOrgPrefix) {
			return
		}

		if slash := licenseFromURL(raw + "/"); slash != got {
			t.Fatalf("a trailing slash changes the license of %q: %q then %q", raw, got, slash)
		}
	})
}
