// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	pkgnuget "github.com/openartifact/openartifact/pkg/nuget"
)

const fuzzNuspec = `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd">
  <metadata>
    <id>Demo.Pkg</id>
    <version>1.0.0-Beta.1+meta</version>
    <authors>Contoso</authors>
    <description>demo</description>
    <license type="expression">MIT OR Apache-2.0</license>
    <requireLicenseAcceptance>true</requireLicenseAcceptance>
    <dependencies>
      <dependency id="Legacy.Lib" version="1.0" />
      <group targetFramework="net8.0">
        <dependency id="Other.Lib" version="[1.0.0, )" />
      </group>
      <group targetFramework=".NETStandard2.0" />
    </dependencies>
  </metadata>
</package>`

// fuzzFile is the in-memory multipart file of a pushed package.
type fuzzFile struct{ *bytes.Reader }

func (fuzzFile) Close() error { return nil }

// fuzzZip builds a zip whose entries hold the given name/content pairs.
func fuzzZip(entries ...[2]string) []byte {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for _, e := range entries {
		w, err := zw.Create(e[0])
		if err != nil {
			continue
		}

		_, _ = w.Write([]byte(e[1]))
	}

	_ = zw.Close()

	return buf.Bytes()
}

// fuzzRootNuspecs counts the root-level nuspec entries of a zip the way a
// NuGet client sees them; ok is false when data is not a zip.
func fuzzRootNuspecs(data []byte) (int, bool) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0, false
	}

	n := 0

	for _, f := range zr.File {
		if isRootNuspec(f.Name) {
			n++
		}
	}

	return n, true
}

// FuzzParseNupkg feeds arbitrary bytes to the .nupkg reader behind the push
// handler, the package validation and the dependency group derivation.
func FuzzParseNupkg(f *testing.F) {
	f.Add(fuzzZip([2]string{"Demo.Pkg.nuspec", fuzzNuspec}, [2]string{"lib/net8.0/Demo.dll", "MZ"}))
	f.Add(fuzzZip([2]string{"content/decoy.nuspec", fuzzNuspec}))
	f.Add(fuzzZip([2]string{"a.nuspec", fuzzNuspec}, [2]string{"B.NUSPEC", fuzzNuspec}))
	f.Add(fuzzZip([2]string{`..\x.nuspec`, fuzzNuspec}, [2]string{".nuspec", fuzzNuspec}))
	f.Add(fuzzZip([2]string{"a.nuspec", ""}))
	f.Add([]byte("PK\x05\x06"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		handler := NewNuGetHandler()

		meta, parseErr := handler.Parse(t.Context(), bytes.NewReader(data))
		validErr := handler.Validate(t.Context(), bytes.NewReader(data))

		if (parseErr == nil) != (validErr == nil) {
			t.Fatalf("Parse err = %v but Validate err = %v", parseErr, validErr)
		}

		raw, readErr := readNuspec(bytes.NewReader(data))
		if readErr == nil && len(raw) > maxNuGetNuspecBytes {
			t.Fatalf("readNuspec returned %d bytes, over the %d cap", len(raw), maxNuGetNuspecBytes)
		}

		roots, isZip := fuzzRootNuspecs(data)
		if readErr == nil && (!isZip || roots != 1) {
			t.Fatalf("readNuspec accepted a package with %d root nuspecs (zip: %v)", roots, isZip)
		}

		if parseErr != nil {
			return
		}

		if readErr != nil {
			t.Fatalf("Parse succeeded but readNuspec failed: %v", readErr)
		}

		fuzzCheckPushed(t, meta, data)
	})
}

// FuzzParseNuspec feeds arbitrary nuspec XML, wrapped in a well-formed
// package, to the metadata and dependency group extraction.
func FuzzParseNuspec(f *testing.F) {
	f.Add(fuzzNuspec)
	f.Add(`<package><metadata><id>a</id><version>1</version></metadata></package>`)
	f.Add(`<package><metadata><id>a</id><version>1.0.0</version><dependencies><dependency id="x"/><group><dependency/></group></dependencies></metadata></package>`)
	f.Add(`<package><metadata><license type="EXPRESSION"> MIT </license></metadata></package>`)
	f.Add(`<!DOCTYPE x [<!ENTITY e "&e;">]><package>&e;</package>`)
	f.Add(`<package><metadata><id>a</id>`)
	f.Add(``)

	f.Fuzz(func(t *testing.T, nuspec string) {
		data := fuzzZip([2]string{"p.nuspec", nuspec})

		meta, err := NewNuGetHandler().Parse(t.Context(), bytes.NewReader(data))
		if err != nil {
			return
		}

		fuzzCheckPushed(t, meta, data)
	})
}

// fuzzCheckPushed checks the record built for a parsed package: what the push
// handler stores must be what the routes can find again.
func fuzzCheckPushed(t *testing.T, meta map[string]any, data []byte) {
	t.Helper()

	if expr, _ := meta["licenseExpression"].(string); expr != strings.TrimSpace(expr) {
		t.Fatalf("license expression %q is not trimmed", expr)
	}

	sum512 := sha512.New()

	part, err := base.HashPart(fuzzFile{bytes.NewReader(data)}, int64(len(data)), sum512)
	if err != nil {
		t.Fatalf("HashPart: %v", err)
	}

	defer part.Close()

	pkg, err := pushedPackage("repo", meta, part, sum512)
	if err != nil {
		return
	}

	if !validPackageID.MatchString(pkg.PackageID) || pkg.PackageID != strings.ToLower(pkg.PackageID) {
		t.Fatalf("stored package id %q is not a lower-case valid id", pkg.PackageID)
	}

	if pkg.PackageID == "." || pkg.PackageID == ".." {
		t.Fatalf("stored package id %q is a dot segment, no URL can address it", pkg.PackageID)
	}

	if norm, ok := pkgnuget.Normalize(pkg.Version); !ok || norm != pkg.Version {
		t.Fatalf("stored version %q is not normalized (%q, %v)", pkg.Version, norm, ok)
	}

	fuzzCheckDigests(t, pkg, data)
	fuzzCheckDependencyGroups(t, pkg)
	fuzzCheckURLs(t, pkg)
}

func fuzzCheckDigests(t *testing.T, pkg *models.NuGetPackage, data []byte) {
	t.Helper()

	s256 := sha256.Sum256(data)
	s512 := sha512.Sum512(data)

	if pkg.BlobKey != hex.EncodeToString(s256[:]) || pkg.PackageHash != base64.StdEncoding.EncodeToString(s512[:]) ||
		pkg.PackageHashAlgorithm != "SHA512" || pkg.PackageSize != int64(len(data)) {
		t.Fatalf("digests of the stored package do not describe its bytes: %+v", pkg)
	}
}

func fuzzCheckDependencyGroups(t *testing.T, pkg *models.NuGetPackage) {
	t.Helper()

	var groups []struct {
		Dependencies []struct {
			ID    string `json:"id"`
			Range string `json:"range"`
		} `json:"dependencies"`
	}

	if err := json.Unmarshal(pkg.DependencyGroups, &groups); err != nil || groups == nil {
		t.Fatalf("dependency groups %q: %v", pkg.DependencyGroups, err)
	}

	for _, g := range groups {
		if g.Dependencies == nil {
			t.Fatalf("a dependency group without a dependencies array: %s", pkg.DependencyGroups)
		}
	}
}

// fuzzCheckURLs checks that the URLs the server advertises for a stored package
// name exactly the segments the download and registration routes read.
func fuzzCheckURLs(t *testing.T, pkg *models.NuGetPackage) {
	t.Helper()

	const base = "http://oa.test/repos/r"

	for _, c := range []struct {
		url  string
		want []string // path segments after /repos/r/
	}{
		{packageContentURL(base, pkg.PackageID, pkg.Version), []string{"v3", "package", pkg.PackageID, pkg.Version, nupkgName(pkg.PackageID, pkg.Version)}},
		{registrationIndexURL(base, pkg.PackageID), []string{"v3", "registration", pkg.PackageID, "index.json"}},
		{registrationLeafURL(base, pkg.PackageID, pkg.Version), []string{"v3", "registration", pkg.PackageID, pkg.Version + ".json"}},
	} {
		u, err := url.Parse(c.url)
		if err != nil {
			t.Fatalf("%q: %v", c.url, err)
		}

		segs := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/repos/r/"), "/")
		if len(segs) != len(c.want) {
			t.Fatalf("%q has %d path segments, want %d", c.url, len(segs), len(c.want))
		}

		for i, seg := range segs {
			if got, err := url.PathUnescape(seg); err != nil || got != c.want[i] {
				t.Fatalf("%q segment %d = %q, want %q", c.url, i, got, c.want[i])
			}
		}
	}

	name := nupkgName(pkg.PackageID, pkg.Version)
	if classifyPackageFile(strings.ToUpper(name), pkg.PackageID, pkg.Version, pkg.Version) != fileNupkg ||
		classifyPackageFile(pkg.PackageID+".nuspec", pkg.PackageID, pkg.Version, pkg.Version) != fileNuspec {
		t.Fatalf("the files of %s %s are not served under their own names", pkg.PackageID, pkg.Version)
	}
}

// FuzzRegistrationIndex feeds arbitrary package ids and version lists to the
// registration index renderer.
func FuzzRegistrationIndex(f *testing.F) {
	f.Add("Demo.Pkg", "1.0.0\n2.0.0-beta\n1.0\n10.0.0\n2.0.0")
	f.Add("a/b c%2F", "1.0.0+x\nnope\n 3.1 ")
	f.Add("x", strings.Repeat("1.0.0\n", 3)+strings.Repeat("9.9.9-r.1\n", 70))
	f.Add("", "")

	f.Fuzz(func(t *testing.T, id, versions string) {
		id = strings.ToLower(id)

		var pkgs []*models.NuGetPackage

		seen := map[string]bool{}

		for line := range strings.SplitSeq(versions, "\n") {
			v, ok := pkgnuget.Normalize(line)
			if !ok || seen[v] {
				continue // the database holds one row per normalized version
			}

			seen[v] = true

			pkgs = append(pkgs, &models.NuGetPackage{
				ID: "row-" + strconv.Itoa(len(pkgs)), PackageID: id, Version: v, DependencyGroups: json.RawMessage("[]"),
			})
		}

		if len(pkgs) == 0 {
			return
		}

		fuzzCheckRegistration(t, id, pkgs)
	})
}

func fuzzCheckRegistration(t *testing.T, id string, pkgs []*models.NuGetPackage) {
	t.Helper()

	const base = "http://oa.test/repos/r"

	rendered, err := (&HTTPHandler{}).renderRegistrationIndex(t.Context(), base, id, pkgs)
	if err != nil || rendered.Transient {
		t.Fatalf("renderRegistrationIndex: %v (transient %v)", err, rendered != nil && rendered.Transient)
	}

	var index struct {
		Count int `json:"count"`
		Items []struct {
			Count int    `json:"count"`
			Lower string `json:"lower"`
			Upper string `json:"upper"`
			Items []struct {
				ID           string `json:"@id"`
				CatalogEntry struct {
					Version string `json:"version"`
				} `json:"catalogEntry"` //nolint:tagliatelle // NuGet V3 wire format is camelCase
			} `json:"items"`
		} `json:"items"`
	}

	if err := json.Unmarshal(rendered.Body, &index); err != nil {
		t.Fatalf("registration index is not JSON: %v", err)
	}

	var versions []string

	if wantPages := (len(pkgs) + registrationPageSize - 1) / registrationPageSize; index.Count != len(index.Items) || len(index.Items) != wantPages {
		t.Fatalf("%d versions rendered as %d pages (count %d), want %d", len(pkgs), len(index.Items), index.Count, wantPages)
	}

	for _, page := range index.Items {
		if page.Count != len(page.Items) || len(page.Items) == 0 || len(page.Items) > registrationPageSize ||
			page.Lower != page.Items[0].CatalogEntry.Version || page.Upper != page.Items[len(page.Items)-1].CatalogEntry.Version {
			t.Fatalf("inconsistent page %q..%q with %d items (count %d)", page.Lower, page.Upper, len(page.Items), page.Count)
		}

		for _, it := range page.Items {
			if it.ID != registrationLeafURL(base, id, it.CatalogEntry.Version) {
				t.Fatalf("item %q is not the leaf of version %q", it.ID, it.CatalogEntry.Version)
			}

			versions = append(versions, it.CatalogEntry.Version)
		}
	}

	if len(versions) != len(pkgs) {
		t.Fatalf("%d versions rendered as %d items", len(pkgs), len(versions))
	}

	for i := 1; i < len(versions); i++ {
		if pkgnuget.CompareStrings(versions[i-1], versions[i]) > 0 {
			t.Fatalf("versions out of order: %q before %q", versions[i-1], versions[i])
		}
	}
}

// FuzzDepCache drives the dependency group cache with arbitrary operation
// sequences and compares it with a list-based LRU model.
func FuzzDepCache(f *testing.F) {
	f.Add(byte(2), []byte{0x00, 0x04, 0x08, 0x01, 0x0c, 0x05, 0x00, 0x09})
	f.Add(byte(0), []byte{0x00, 0x04, 0x01, 0x05})
	f.Add(byte(255), []byte{})

	f.Fuzz(func(t *testing.T, size byte, ops []byte) {
		capacity := int(size % 5)
		c := newDepCache(capacity)
		limit := max(capacity, 1)

		var (
			recent []string // most recently used first
			values = map[string]string{}
		)

		for i, op := range ops {
			key := string(rune('a' + op>>2%8))

			if op&1 == 0 {
				val := strconv.Itoa(i)
				c.put(key, json.RawMessage(val))

				values[key] = val
				recent = append([]string{key}, fuzzWithout(recent, key)...)

				if len(recent) > limit {
					delete(values, recent[limit])
					recent = recent[:limit]
				}

				continue
			}

			got, hit := c.get(key)
			want, inModel := values[key]

			if hit != inModel || (hit && string(got) != want) {
				t.Fatalf("op %d get %q = %q, %v; model has %q, %v", i, key, got, hit, want, inModel)
			}

			if hit {
				recent = append([]string{key}, fuzzWithout(recent, key)...)
			}
		}

		if len(c.items) != len(recent) || c.order.Len() != len(recent) || len(recent) > limit {
			t.Fatalf("cache holds %d/%d entries, model %d (limit %d)", len(c.items), c.order.Len(), len(recent), limit)
		}
	})
}

func fuzzWithout(keys []string, key string) []string {
	out := make([]string, 0, len(keys))

	for _, k := range keys {
		if k != key {
			out = append(out, k)
		}
	}

	return out
}

// FuzzParseIntParams feeds arbitrary query parameter values to the paging
// helpers of the search endpoints.
func FuzzParseIntParams(f *testing.F) {
	f.Add("20", 20, 0, 100)
	f.Add("-1", 0, 0, 1)
	f.Add("9223372036854775808", 5, 1, 10)
	f.Add("", 3, 3, 3)
	f.Add("1e3", -1, 10, 2)

	f.Fuzz(func(t *testing.T, s string, def, lo, hi int) {
		if got := parseIntMin(s, def, lo); s != "" && got < lo && got != def {
			t.Fatalf("parseIntMin(%q, %d, %d) = %d", s, def, lo, got)
		}

		if lo > hi {
			return
		}

		got := parseIntClamp(s, def, lo, hi)
		if s != "" && (got < lo || got > hi) && got != def {
			t.Fatalf("parseIntClamp(%q, %d, %d, %d) = %d", s, def, lo, hi, got)
		}
	})
}
