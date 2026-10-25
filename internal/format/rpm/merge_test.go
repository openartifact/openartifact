// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"

	"github.com/openartifact/openartifact/pkg/rpm"
)

func testPackages() []rpm.Package {
	return []rpm.Package{
		{
			Name: "alpha", Version: "1.0", Release: "1", Epoch: 0, Arch: "x86_64", SHA256: "aaaa",
			Summary: "A & B <test>", PackageSize: 100, Files: []rpm.FileEntry{{Path: "/usr/bin/alpha"}},
			Changelogs: []rpm.ChangelogEntry{{Author: "me", Time: 1700000000, Description: "first"}},
		},
		{Name: "beta", Version: "2.0", Release: "3.el9", Epoch: 1, Arch: "noarch", SHA256: "bbbb", Files: []rpm.FileEntry{{Path: "/etc/beta.conf"}}},
	}
}

// TestDocWriterMatchesHostedDocuments: the per-package writer behind merged
// documents must render exactly what the hosted metadata writers always did.
func TestDocWriterMatchesHostedDocuments(t *testing.T) {
	pkgs := testPackages()

	for _, tc := range []struct {
		kind  DocKind
		whole func(io.Writer, []rpm.Package) error
	}{
		{DocPrimary, WritePrimaryXML}, {DocFilelists, WriteFilelistsXML}, {DocOther, WriteOtherXML},
	} {
		var whole, piecewise bytes.Buffer

		if err := tc.whole(&whole, pkgs); err != nil {
			t.Fatal(err)
		}

		d := NewDocWriter(&piecewise, tc.kind, len(pkgs))
		for i := range pkgs {
			d.Package(&pkgs[i])
		}

		if err := d.Close(); err != nil {
			t.Fatal(err)
		}

		if whole.String() != piecewise.String() {
			t.Errorf("kind %d differs:\nwhole:     %s\npiecewise: %s", tc.kind, whole.String(), piecewise.String())
		}
	}
}

func scanAll(t *testing.T, doc string, withElement bool) ([]PackageInfo, []string) {
	t.Helper()

	var (
		infos    []PackageInfo
		elements []string
	)

	err := ScanPackages(strings.NewReader(doc), withElement, func(i PackageInfo, el []byte) error {
		infos = append(infos, i)
		elements = append(elements, string(el))

		return nil
	})
	if err != nil {
		t.Fatalf("ScanPackages: %v", err)
	}

	return infos, elements
}

func TestScanPrimaryIdentifiesPackages(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePrimaryXML(&buf, testPackages()); err != nil {
		t.Fatal(err)
	}

	infos, _ := scanAll(t, buf.String(), false)
	if len(infos) != 2 {
		t.Fatalf("scanned %d packages, want 2", len(infos))
	}

	a, b := infos[0], infos[1]
	if a.Name != "alpha" || a.Arch != "x86_64" || a.Ver != "1.0" || a.Rel != "1" || a.PkgID != "aaaa" || a.Href != "Packages/alpha-1.0-1.x86_64.rpm" {
		t.Errorf("alpha = %+v", a)
	}

	if b.Epoch != "1" || b.NEVRA() != "beta-1:2.0-3.el9.noarch" || b.PkgID != "bbbb" {
		t.Errorf("beta = %+v", b)
	}

	if a.NEVRA() != "alpha-0:1.0-1.x86_64" {
		t.Errorf("an absent epoch is 0: %s", a.NEVRA())
	}
}

func TestScanFilelistsAndOtherUsePkgidAttribute(t *testing.T) {
	for _, write := range []func(io.Writer, []rpm.Package) error{WriteFilelistsXML, WriteOtherXML} {
		var buf bytes.Buffer
		if err := write(&buf, testPackages()); err != nil {
			t.Fatal(err)
		}

		infos, _ := scanAll(t, buf.String(), false)
		if len(infos) != 2 || infos[0].PkgID != "aaaa" || infos[1].PkgID != "bbbb" || infos[0].Name != "alpha" {
			t.Errorf("infos = %+v", infos)
		}
	}
}

// TestScannedElementsRoundTripThroughDocWriter: copying the elements of an
// upstream document through the scanner reproduces the document, which is what
// a merged repodata is made of.
func TestScannedElementsRoundTripThroughDocWriter(t *testing.T) {
	for _, tc := range []struct {
		kind  DocKind
		write func(io.Writer, []rpm.Package) error
	}{
		{DocPrimary, WritePrimaryXML}, {DocFilelists, WriteFilelistsXML}, {DocOther, WriteOtherXML},
	} {
		var src bytes.Buffer
		if err := tc.write(&src, testPackages()); err != nil {
			t.Fatal(err)
		}

		var out bytes.Buffer

		d := NewDocWriter(&out, tc.kind, 2)

		err := ScanPackages(bytes.NewReader(src.Bytes()), true, func(_ PackageInfo, el []byte) error {
			d.Raw(el)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		if err := d.Close(); err != nil {
			t.Fatal(err)
		}

		reScanned, _ := scanAll(t, out.String(), false)
		orig, _ := scanAll(t, src.String(), false)

		if len(reScanned) != 2 || reScanned[0] != orig[0] || reScanned[1] != orig[1] {
			t.Errorf("kind %d: rescanned %+v, original %+v\n%s", tc.kind, reScanned, orig, out.String())
		}

		if tc.kind == DocPrimary && !strings.Contains(out.String(), "A &amp; B &lt;test&gt;") {
			t.Errorf("escaping lost in %s", out.String())
		}
	}
}

func TestScanDropsXMLBaseAndKeepsPrefixes(t *testing.T) {
	doc := `<?xml version="1.0"?>
<metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm" packages="1">
<package type="rpm"><name>pkg</name><arch>noarch</arch><version epoch="0" ver="1" rel="1"/><checksum type="sha256" pkgid="YES">cafe</checksum>
<location xml:base="http://elsewhere/" href="Packages/p/pkg-1-1.noarch.rpm"/>
<format><rpm:license>MIT</rpm:license><rpm:provides><rpm:entry name="pkg"/></rpm:provides></format></package>
</metadata>`

	infos, els := scanAll(t, doc, true)
	if len(infos) != 1 || infos[0].PkgID != "cafe" || infos[0].Href != "Packages/p/pkg-1-1.noarch.rpm" {
		t.Fatalf("infos = %+v", infos)
	}

	el := els[0]
	if strings.Contains(el, "xml:base") || strings.Contains(el, "elsewhere") {
		t.Errorf("xml:base must be dropped (it would redirect clients to another host): %s", el)
	}

	if !strings.Contains(el, "<rpm:license>MIT</rpm:license>") || !strings.Contains(el, `<rpm:entry name="pkg"></rpm:entry>`) {
		t.Errorf("rpm: prefixes not preserved: %s", el)
	}
}

func TestScanMalformedDocument(t *testing.T) {
	err := ScanPackages(strings.NewReader("<metadata><package><name>x</nam>"), false, func(PackageInfo, []byte) error { return nil })
	if err == nil {
		t.Error("a malformed document must be an error")
	}
}

func TestSafeLocation(t *testing.T) {
	for in, want := range map[string]string{
		"Packages/foo-1.rpm":             "Packages/foo-1.rpm",
		"Packages/f/foo%2B1-1.rpm":       "Packages/f/foo+1-1.rpm",
		"./Packages//foo-1.rpm":          "Packages/foo-1.rpm",
		"Packages/../Packages/foo-1.rpm": "Packages/foo-1.rpm",
		"Packages/..x/foo-1.rpm":         "Packages/..x/foo-1.rpm",
	} {
		if got, ok := SafeLocation(in); !ok || got != want {
			t.Errorf("SafeLocation(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}

	for _, in := range []string{"", "..", "../etc/passwd", "/abs/path.rpm", "a/../../b.rpm", "http://x/y.rpm/..", "a\\b.rpm", "a%00b.rpm", "%zz"} {
		if got, ok := SafeLocation(in); ok && (strings.HasPrefix(got, "/") || strings.HasPrefix(got, "..")) {
			t.Errorf("SafeLocation(%q) accepted %q", in, got)
		}
	}

	for _, in := range []string{"", "..", "../etc/passwd", "/abs/path.rpm", "a/../../b.rpm", "a\\b.rpm", "a%00b.rpm", "%zz", "a%5Cb.rpm", ".", "./", "Packages/..", "%2e%2e/x.rpm", "a/%2e%2e/%2e%2e/b.rpm"} {
		if _, ok := SafeLocation(in); ok {
			t.Errorf("SafeLocation(%q) must be rejected", in)
		}
	}
}

func TestParseRepomd(t *testing.T) {
	doc := `<?xml version="1.0"?><repomd xmlns="http://linux.duke.edu/metadata/repo">
<data type="primary"><location href="repodata/a-primary.xml.zst"/></data>
<data type="filelists"><location href="repodata/b-filelists.xml.gz"/></data>
<data type="modules"><location href="repodata/c-modules.yaml.gz"/></data>
<data type="primary"><location href="repodata/dup.xml.gz"/></data></repomd>`

	got, err := ParseRepomd(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}

	if got["primary"] != "repodata/a-primary.xml.zst" || got["filelists"] != "repodata/b-filelists.xml.gz" || got["modules"] != "repodata/c-modules.yaml.gz" || got["other"] != "" {
		t.Errorf("got %v", got)
	}

	if _, err := ParseRepomd(strings.NewReader("<repomd><data")); err == nil {
		t.Error("malformed repomd must be an error")
	}
}

func TestOpenMetadataDecompressesByExtension(t *testing.T) {
	const payload = "<metadata/>"

	var gzb, xzb, zsb bytes.Buffer

	gw := gzip.NewWriter(&gzb)
	_, _ = gw.Write([]byte(payload))
	_ = gw.Close()

	xw, _ := xz.NewWriter(&xzb)
	_, _ = xw.Write([]byte(payload))
	_ = xw.Close()

	zw, _ := zstd.NewWriter(&zsb)
	_, _ = zw.Write([]byte(payload))
	_ = zw.Close()

	for name, tc := range map[string]struct {
		file string
		data []byte
	}{
		"gz": {"x-primary.xml.gz", gzb.Bytes()}, "xz": {"x-primary.xml.xz", xzb.Bytes()},
		"zst": {"x-primary.xml.zst", zsb.Bytes()}, "none": {"x-primary.xml", []byte(payload)},
	} {
		rc, err := OpenMetadata(io.NopCloser(bytes.NewReader(tc.data)), tc.file)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		out, err := io.ReadAll(rc)
		_ = rc.Close()

		if err != nil || string(out) != payload {
			t.Errorf("%s: %q, %v", name, out, err)
		}
	}
}

const modulesA = `---
document: modulemd
version: 2
data:
  name: nodejs
  stream: "18"
  version: 1
  context: abc
  summary: from A
...
---
document: modulemd-defaults
version: 1
data:
  module: nodejs
  stream: "18"
...
`

const modulesB = `---
document: modulemd
version: 2
data:
  name: nodejs
  stream: "18"
  version: 1
  context: abc
  summary: from B (duplicate, must lose)
...
---
document: modulemd
version: 2
data:
  name: nodejs
  stream: "20"
  version: 2
  context: def
  summary: from B
...
---
document: modulemd-defaults
version: 1
data:
  module: nodejs
  stream: "20"
...
---
this: is: not valid: yaml: [
`

func TestMergeModulesFirstSourceWins(t *testing.T) {
	var out bytes.Buffer

	n, err := MergeModules(&out, []io.Reader{strings.NewReader(modulesA), strings.NewReader(modulesB)})
	if err != nil {
		t.Fatal(err)
	}

	got := out.String()

	if n != 3 {
		t.Errorf("merged %d documents, want 3 (A's module and defaults + B's stream 20)\n%s", n, got)
	}

	if !strings.Contains(got, "from A") || strings.Contains(got, "from B (duplicate") || !strings.Contains(got, "summary: from B") {
		t.Errorf("precedence wrong:\n%s", got)
	}

	if strings.Count(got, "document: modulemd-defaults") != 1 || strings.Contains(got, "not valid") {
		t.Errorf("defaults must be one per module and invalid documents dropped:\n%s", got)
	}

	if !strings.HasPrefix(got, "---\n") {
		t.Errorf("each document must start with ---:\n%s", got)
	}
}
