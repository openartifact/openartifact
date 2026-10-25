// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/pkgdeps"
)

// nuspecAgreeMaxField is the length the dependency reader clips names and
// ranges at; longer ones are not compared.
const nuspecAgreeMaxField = 512

// nuspecAgreeFileName is the name of the package file the readers are given:
// the format decides how the metadata is found in it.
const nuspecAgreeFileName = "x.1.0.0.nupkg"

// nuspecAgreeZip is a .nupkg with nuspec as the root nuspec and, depending on
// layout, decoys that a NuGet client does not read: nuspecs below a folder.
func nuspecAgreeZip(nuspec string, layout uint8) []byte {
	decoy := [2]string{"content/decoy.nuspec", fuzzNuspec}
	root := [2]string{"p.nuspec", nuspec}

	switch layout % 5 {
	case 1:
		return fuzzZip(decoy, root)
	case 2:
		return fuzzZip(root, decoy, [2]string{"lib/net8.0/p.dll", "MZ"})
	case 3:
		return fuzzZip(root, [2]string{`sub\decoy.nuspec`, fuzzNuspec})
	case 4:
		return fuzzZip([2]string{"P.NUSPEC", nuspec}, [2]string{"tools/other.nuspec", fuzzNuspec})
	default:
		return fuzzZip(root)
	}
}

// nuspecAgreeDep is a dependency as the format handler lists it.
type nuspecAgreeDep struct{ id, rng string }

// nuspecAgreeDeps are the dependencies of a parsed nuspec in the order of its
// dependency groups: the ungrouped ones first, then each group's.
func nuspecAgreeDeps(spec *Nuspec) []nuspecAgreeDep {
	var out []nuspecAgreeDep

	for _, g := range spec.Metadata.Dependencies.DependencyGroups() {
		items, _ := g["dependencies"].([]map[string]any)
		for _, it := range items {
			id, _ := it["id"].(string)
			rng, _ := it["range"].(string)
			out = append(out, nuspecAgreeDep{id, rng})
		}
	}

	return out
}

// nuspecAgreeCheckDeps checks the dependencies read from the nupkg against the
// ones the format handler lists: the same names (trimmed, once each), and for a
// name that is listed once the same constraint, split into a pinned version or
// a range.
func nuspecAgreeCheckDeps(t *testing.T, want []nuspecAgreeDep, got []pkgdeps.Dep) {
	t.Helper()

	byName := map[string][]string{}

	for _, d := range want {
		name := strings.TrimSpace(d.id)
		if name == "" {
			continue
		}

		if len(name) > nuspecAgreeMaxField || len(d.rng) > nuspecAgreeMaxField {
			return // clipped by the reader, not comparable
		}

		byName[name] = append(byName[name], strings.TrimSpace(d.rng))
	}

	if len(byName) <= pkgdeps.MaxDeps && len(got) != len(byName) {
		t.Fatalf("pkgdeps reads %d dependencies, the nuspec lists %d names: %+v", len(got), len(byName), got)
	}

	for _, d := range got {
		ranges, ok := byName[d.Name]
		if !ok {
			t.Fatalf("pkgdeps reads dependency %q, which the nuspec does not list (%v)", d.Name, byName)
		}

		if len(ranges) == 1 {
			nuspecAgreeCheckConstraint(t, d, ranges[0])
		}
	}
}

// nuspecAgreeCheckConstraint checks that dep is the constraint raw (trimmed):
// a pinned "[x]" gives a version, anything else a range.
func nuspecAgreeCheckConstraint(t *testing.T, dep pkgdeps.Dep, raw string) {
	t.Helper()

	if dep.Version == "" {
		if dep.Range != raw {
			t.Fatalf("dependency %q: range %q, the nuspec says %q", dep.Name, dep.Range, raw)
		}

		return
	}

	inner, ok := strings.CutPrefix(raw, "[")
	if ok {
		inner, ok = strings.CutSuffix(inner, "]")
	}

	if !ok || strings.Contains(inner, ",") || strings.TrimSpace(inner) != dep.Version || dep.Range != "" {
		t.Fatalf("dependency %q pins %q (range %q), the nuspec says %q", dep.Name, dep.Version, dep.Range, raw)
	}
}

// nuspecAgreeCheckLicense checks the license read from the nupkg against the
// metadata the format handler parsed: an SPDX expression is read as it is, and
// a license from any other source is only there when the nuspec could name one.
func nuspecAgreeCheckLicense(t *testing.T, spec *Nuspec, res licensemeta.Result) {
	t.Helper()

	lic := spec.Metadata.License
	expr := lic.Expression()

	if expr != "" {
		if len(res.Licenses) != 1 || res.Licenses[0] != expr || res.Source != ".nuspec" {
			t.Fatalf("license expression %q is read as %+v", expr, res)
		}

		return
	}

	if len(res.Licenses) == 0 {
		return
	}

	if len(res.Licenses) != 1 || res.Licenses[0] != strings.TrimSpace(res.Licenses[0]) || res.Source != ".nuspec" {
		t.Fatalf("license read as %+v from a nuspec without an expression", res)
	}

	if strings.TrimSpace(lic.Type) != "" || strings.TrimSpace(spec.Metadata.LicenseURL) == "" {
		t.Fatalf("license %+v read from a nuspec with type %q and licenseUrl %q", res, lic.Type, spec.Metadata.LicenseURL)
	}
}

// FuzzNuspecReadersAgree reads one .nuspec with the three readers of it: the
// format handler's, the dependency reader of the AI and policy side
// (pkgdeps) and the license reader (licensemeta). A package the handler accepts
// is read by the others from the same file, with the same dependencies (names
// trimmed once each, a constraint split into pinned version or range) and the
// same license expression.
func FuzzNuspecReadersAgree(f *testing.F) {
	f.Add(fuzzNuspec, uint8(0))
	f.Add(fuzzNuspec, uint8(1))
	f.Add(fuzzNuspec, uint8(2))
	f.Add(fuzzNuspec, uint8(3))
	f.Add(fuzzNuspec, uint8(4))
	f.Add(`<package><metadata><id>a</id><version>1</version><license type="expression"> MIT </license></metadata></package>`, uint8(0))
	f.Add(`<package><metadata><licenseUrl> https://licenses.nuget.org/MIT </licenseUrl></metadata></package>`, uint8(0))
	f.Add(`<package><metadata><license type="file">LICENSE</license><licenseUrl>https://opensource.org/licenses/MIT</licenseUrl></metadata></package>`, uint8(0))
	f.Add(`<package><metadata><license type="expression">MIT</license><license>Apache-2.0</license></metadata></package>`, uint8(0))
	f.Add(`<package><metadata><dependencies><dependency id="a" version="[1.0.0]"/><dependency id=" a " version="2"/><dependency id="b" version=" [ 1 ] "/><dependency id="c" version="[1,2]"/><dependency id="d"/></dependencies></metadata></package>`, uint8(0))
	f.Add(`<package><metadata><dependencies><group targetFramework="net8.0"><dependency id="g" version="[1.0.0, 2.0.0)"/></group><dependency id="u" version="1"/></dependencies></metadata></package>`, uint8(0))
	f.Add(`<package><metadata><dependencies><dependency id="" version="1"/><dependency id="  "/></dependencies></metadata></package>`, uint8(0))
	f.Add(`<other><metadata><license type="expression">MIT</license></metadata></other>`, uint8(0))
	f.Add(`<package><metadata><id>a</id>`, uint8(0))
	f.Add(``, uint8(0))

	f.Fuzz(nuspecAgree)
}

func nuspecAgree(t *testing.T, nuspec string, layout uint8) {
	// The readers of the other packages see text, not bytes: what a .nuspec
	// with NUL or invalid UTF-8 reads as there is theirs to decide.
	if !utf8.ValidString(nuspec) || strings.ContainsRune(nuspec, 0) {
		return
	}

	data := nuspecAgreeZip(nuspec, layout)

	spec, err := parseNuspec(bytes.NewReader(data))
	if err != nil {
		return
	}

	open := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }

	deps, err := pkgdeps.Read(context.Background(), pkgdeps.Source{
		Format:   "nuget",
		Filename: nuspecAgreeFileName,
		Open:     open,
	})
	if err != nil {
		t.Fatalf("the handler reads the package, pkgdeps does not: %v\n%s", err, nuspec)
	}

	nuspecAgreeCheckDeps(t, nuspecAgreeDeps(spec), deps.Deps)

	lic, err := licensemeta.Read(context.Background(), "nuget", nuspecAgreeFileName, open)
	if err != nil {
		t.Fatalf("the handler reads the package, licensemeta does not: %v\n%s", err, nuspec)
	}

	nuspecAgreeCheckLicense(t, spec, lic)
}
