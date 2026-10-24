// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}

	return 0
}

// FuzzVersionOrder compares arbitrary version triples with the scanner-fix
// comparator that decides whether an artifact is reported as fixed: it is
// reflexive, antisymmetric and transitive, unaffected by trailing zero
// components, and versionAtLeast agrees with it.
func FuzzVersionOrder(f *testing.F) {
	for _, s := range [][3]string{
		{"1.0.0", "1.0.1", "1.1"},
		{"1.0", "1.0.0", "2"},
		{"2.0.0-rc1", "2.0.0", "2.0.0-rc2"},
		{"v1.2.3", "1.2.3", "1.2.3-beta"},
		{"1.0.0b1", "1.0.0", "1.0.0a1"},
		{"", "x", "1x"},
		{"99999999999999999999", "1", "1.99999999999999999999"},
	} {
		f.Add(s[0], s[1], s[2])
	}

	f.Fuzz(func(t *testing.T, a, b, c string) {
		va, vb, vc := parseVersion(a), parseVersion(b), parseVersion(c)

		va.checkSelf(t)
		checkVersionPair(t, va, vb)

		if va.ok && vb.ok && vc.ok {
			checkVersionTriple(t, va, vb, vc)
		}
	})
}

type fuzzVersion struct {
	raw  string
	toks []vtoken
	ok   bool
}

func parseVersion(raw string) fuzzVersion {
	toks, ok := versionTokens(raw)

	return fuzzVersion{raw: raw, toks: toks, ok: ok}
}

func (v fuzzVersion) cmp(w fuzzVersion) int { return sign(compareTokens(v.toks, w.toks)) }

// checkSelf: a version equals itself and itself with a trailing zero.
func (v fuzzVersion) checkSelf(t *testing.T) {
	t.Helper()

	if !v.ok {
		return
	}

	if v.cmp(v) != 0 {
		t.Fatalf("%q is not equal to itself", v.raw)
	}

	if padded := parseVersion(v.raw + ".0"); !padded.ok || v.cmp(padded) != 0 {
		t.Fatalf("%q and %q differ by a trailing zero but compare %d", v.raw, v.raw+".0", v.cmp(padded))
	}
}

// checkVersionPair: the order is antisymmetric and versionAtLeast follows it.
func checkVersionPair(t *testing.T, a, b fuzzVersion) {
	t.Helper()

	if !a.ok || !b.ok {
		return
	}

	if a.cmp(b) != -b.cmp(a) {
		t.Fatalf("compare(%q, %q) = %d but compare(%q, %q) = %d", a.raw, b.raw, a.cmp(b), b.raw, a.raw, b.cmp(a))
	}

	// versionAtLeast trims its arguments; the tokens above were cut from them untrimmed.
	if strings.TrimSpace(a.raw) != a.raw || strings.TrimSpace(b.raw) != b.raw {
		return
	}

	if ge, ok := versionAtLeast("npm", a.raw, b.raw); !ok || ge != (a.cmp(b) >= 0) {
		t.Fatalf("versionAtLeast(%q, %q) = %v, %v with compare %d", a.raw, b.raw, ge, ok, a.cmp(b))
	}
}

// checkVersionTriple: the order is transitive and equal versions are
// interchangeable.
func checkVersionTriple(t *testing.T, a, b, c fuzzVersion) {
	t.Helper()

	ab, bc, ac := a.cmp(b), b.cmp(c), a.cmp(c)

	if (ab <= 0 && bc <= 0 && ac > 0) || (ab >= 0 && bc >= 0 && ac < 0) {
		t.Fatalf("not transitive: compare(%q, %q) = %d, compare(%q, %q) = %d, compare(%q, %q) = %d", a.raw, b.raw, ab, b.raw, c.raw, bc, a.raw, c.raw, ac)
	}

	if (ab == 0 && bc != ac) || (bc == 0 && ab != ac) {
		t.Fatalf("equal versions compare differently: compare(%q, %q) = %d, compare(%q, %q) = %d, compare(%q, %q) = %d", a.raw, b.raw, ab, b.raw, c.raw, bc, a.raw, c.raw, ac)
	}
}

// FuzzFixedByAny feeds arbitrary versions and scanner fix lists: an artifact is
// fixed only when one listed fix is at or below its version, and an unknown or
// empty version is never fixed.
func FuzzFixedByAny(f *testing.F) {
	f.Add("npm", "1.2.3", "1.2.9, 2.0.4")
	f.Add("maven", "2.0.0", "2.0.0-rc1")
	f.Add("go", "v1.0.0", "1.0.0-rc.1,")
	f.Add("", "unknown", "1")
	f.Add("pypi", "", "")

	f.Fuzz(func(t *testing.T, format, version, fixedIn string) {
		if !fixedByAny(format, version, fixedIn) {
			return
		}

		if strings.TrimSpace(version) == "" || strings.TrimSpace(version) == "unknown" {
			t.Fatalf("version %q reported as fixed by %q", version, fixedIn)
		}

		for fix := range strings.SplitSeq(fixedIn, ",") {
			if ge, ok := versionAtLeast(format, version, fix); ok && ge {
				return
			}
		}

		t.Fatalf("version %q reported as fixed by %q, but no listed fix is at or below it", version, fixedIn)
	})
}

var purlAllowed = regexp.MustCompile(`^pkg:[a-z]+/[A-Za-z0-9._~:%/-]*(@[A-Za-z0-9._~:%-]*)?$`)

// FuzzPURL builds Package URLs from arbitrary names, namespaces and versions:
// the purl is made of unreserved characters and %XX escapes only, its type and
// version survive decoding, and the name decodes back to the (normalized) name.
func FuzzPURL(f *testing.F) {
	f.Add("npm", "", "@babel/core", "7.0.0")
	f.Add("maven", "", "org.example:lib", "1.0")
	f.Add("pypi", "", "My_Package", "1!2.0")
	f.Add("deb", "debian", "libc6", "2.36-9+deb12u4")
	f.Add("docker", "", "lib/ü/x y", "sha256:abc")
	f.Add("rpm", "fedora", "a@b", "unknown")
	f.Add("", "", "", "")

	f.Fuzz(func(t *testing.T, format, namespace, name, version string) {
		purl := generatePURLNS(format, namespace, name, version)

		if !purlAllowed.MatchString(purl) {
			t.Fatalf("generatePURLNS(%q, %q, %q, %q) = %q holds characters a purl cannot", format, namespace, name, version, purl)
		}

		typ := purlType(format)
		if !strings.HasPrefix(purl, "pkg:"+typ+"/") {
			t.Fatalf("purl %q does not start with its type %q", purl, typ)
		}

		path, ver, hasVersion := strings.Cut(strings.TrimPrefix(purl, "pkg:"+typ+"/"), "@")
		if hasVersion != (version != "" && version != "unknown") {
			t.Fatalf("purl %q: version %q present = %v", purl, version, hasVersion)
		}

		if hasVersion {
			if got, err := url.PathUnescape(ver); err != nil || got != version {
				t.Fatalf("version of purl %q decodes to %q, %v, want %q", purl, got, err, version)
			}
		}

		want := name

		switch typ {
		case "maven":
			if g, a, ok := strings.Cut(name, ":"); ok {
				want = g + "/" + a
			}
		case "pypi":
			want = strings.ToLower(strings.ReplaceAll(name, "_", "-"))
		}

		if namespace != "" && (typ == "deb" || typ == "rpm") {
			want = namespace + "/" + want
		}

		var decoded []string

		for seg := range strings.SplitSeq(path, "/") {
			got, err := url.PathUnescape(seg)
			if err != nil {
				t.Fatalf("segment %q of purl %q is not valid percent-encoding: %v", seg, purl, err)
			}

			decoded = append(decoded, got)
		}

		if got := strings.Join(decoded, "/"); got != want && strings.ToValidUTF8(want, "") == want {
			t.Fatalf("name of purl %q decodes to %q, want %q", purl, got, want)
		}
	})
}

// FuzzCanonicalSPDXExpression feeds arbitrary license strings from uploaded
// package metadata to the SPDX expression canonicalization: an accepted
// expression is in canonical form (re-parsing returns it unchanged, spacing is
// normalized), and the conjunction of accepted expressions is an accepted
// expression.
func FuzzCanonicalSPDXExpression(f *testing.F) {
	for _, s := range []string{
		"MIT", "mit", "Apache-2.0 OR MIT", "(MIT OR Apache-2.0) AND BSD-3-Clause",
		"GPL-2.0-or-later WITH Classpath-exception-2.0", "GPL-2.0+", "MIT AND", "(MIT",
		"MIT)", "MIT OR\tApache-2.0", "MIT\nAND\nISC", "WITH", "MIT WITH", "", "((((MIT))))",
	} {
		f.Add(s, "ISC")
	}

	f.Fuzz(func(t *testing.T, expr, other string) {
		canon, ok := canonicalSPDXExpression(expr)
		if !ok {
			if canon != "" {
				t.Fatalf("rejected %q but returned %q", expr, canon)
			}

			return
		}

		if again, ok := canonicalSPDXExpression(canon); !ok || again != canon {
			t.Fatalf("canonical form %q of %q is not stable: %q, %v", canon, expr, again, ok)
		}

		if strings.ContainsAny(canon, "\t\r\n") || strings.Contains(canon, "  ") || canon != strings.TrimSpace(canon) {
			t.Fatalf("canonical form %q of %q has stray whitespace", canon, expr)
		}

		if oc, ok := canonicalSPDXExpression(other); ok {
			joined := joinExpressions([]string{canon, oc})
			if again, ok := canonicalSPDXExpression(joined); !ok || again != joined {
				t.Fatalf("conjunction %q of %q and %q is not an accepted expression: %q, %v", joined, canon, oc, again, ok)
			}
		}
	})
}

func fuzzGenerator() *Generator {
	g := NewGenerator(nil, nil)
	g.now = func() time.Time { return time.Unix(1_700_000_000, 0) }

	return g
}

var (
	spdxRefRE    = regexp.MustCompile(`^SPDXRef-[A-Za-z0-9.-]+$`)
	licenseRefRE = regexp.MustCompile(`LicenseRef-[A-Za-z0-9.-]+`)
	tagValueLine = regexp.MustCompile(`^(SPDXVersion|DataLicense|SPDXID|DocumentName|DocumentNamespace|Creator|Created|PackageName|PackageVersion|PackageDownloadLocation|FilesAnalyzed|PackageChecksum|PackageLicenseConcluded|PackageLicenseDeclared|PackageCopyrightText|PrimaryPackagePurpose|ExternalRef|Relationship|LicenseID|ExtractedText|LicenseName): `)
)

// FuzzGenerateDocuments builds every SBOM flavour (CycloneDX, SPDX 2.3 JSON and
// tag-value, SPDX 3.0) of an artifact from arbitrary uploaded metadata: the
// documents are well-formed and self-consistent whatever the metadata says,
// and untrusted text cannot add a line to the tag-value document.
func FuzzGenerateDocuments(f *testing.F) {
	f.Add("npm", "@babel/core@7.0.0", `{"license":"MIT","dependencies":[{"name":"a","version":"1","sha256":"`+strings.Repeat("a", 64)+`","license":["MIT","Custom <b>"]}]}`, uint8(1))
	f.Add("maven", "g:a@1", `{"licenses":[{"type":"Apache 2.0"},{"name":"X"}],"dependencies":[{"name":"g:b","purl":"pkg:maven/g/b@1"},{"name":"g:b","purl":"pkg:maven/g/b@1"},5,null]}`, uint8(0))
	f.Add("docker", "img@sha256:abc", `{"license":"MIT AND (ISC OR BSD-2-Clause)","dependencies":[{"name":"a\nSPDXID: x","version":"\u2028"}]}`, uint8(2))
	f.Add("", "", `null`, uint8(0))

	f.Fuzz(func(t *testing.T, format, artifactID, metadata string, spec uint8) {
		var meta map[string]any
		if json.Unmarshal([]byte(metadata), &meta) != nil {
			return
		}

		g := fuzzGenerator()
		ctx := context.Background()

		cdx, err := g.GenerateCycloneDX(ctx, "repo", artifactID, format, meta, []string{CycloneDX14, CycloneDX15, CycloneDX16}[int(spec)%3])
		spdx, err2 := g.GenerateSPDXJSON(ctx, "repo", artifactID, format, meta)
		tv, err3 := g.GenerateSPDX(ctx, "repo", artifactID, format, meta)
		spdx3, err4 := g.GenerateSPDX3JSON(ctx, "repo", artifactID, format, meta)

		for _, e := range []error{err, err2, err3, err4} {
			if e != nil {
				t.Fatalf("generating the documents of %q failed: %v", artifactID, e)
			}
		}

		for _, r := range []*SBOMResult{cdx, spdx, tv, spdx3} {
			if r.Components != cdx.Components {
				t.Fatalf("%s lists %d components, CycloneDX %d", r.Format, r.Components, cdx.Components)
			}
		}

		checkCycloneDX(t, cdx)
		checkSPDXJSON(t, spdx)
		checkTagValue(t, tv)
		checkSPDX3(t, spdx3)
	})
}

func checkCycloneDX(t *testing.T, r *SBOMResult) {
	t.Helper()

	var doc struct {
		Metadata struct {
			Component cdxComponent `json:"component"`
		} `json:"metadata"`
		Components   []cdxComponent  `json:"components"`
		Dependencies []cdxDependency `json:"dependencies"`
	}

	if err := json.Unmarshal(r.Body, &doc); err != nil {
		t.Fatalf("CycloneDX body is not JSON: %v", err)
	}

	if len(doc.Components) != r.Components {
		t.Fatalf("CycloneDX holds %d components, result says %d", len(doc.Components), r.Components)
	}

	refs := map[string]struct{}{doc.Metadata.Component.BOMRef: {}}

	for _, c := range append([]cdxComponent{doc.Metadata.Component}, doc.Components...) {
		refs[c.BOMRef] = struct{}{}

		for _, l := range c.Licenses {
			if (l.License == nil) == (l.Expression == "") {
				t.Fatalf("CycloneDX license entry %+v needs exactly one of license and expression", l)
			}

			if l.License != nil && (l.License.ID == "") == (l.License.Name == "") {
				t.Fatalf("CycloneDX license %+v needs exactly one of id and name", l.License)
			}
		}
	}

	for _, d := range doc.Dependencies {
		if _, ok := refs[d.Ref]; !ok {
			t.Fatalf("CycloneDX dependency %q has no component", d.Ref)
		}

		for _, to := range d.DependsOn {
			if _, ok := refs[to]; !ok {
				t.Fatalf("CycloneDX dependency %q -> %q has no component", d.Ref, to)
			}
		}
	}
}

func checkSPDXJSON(t *testing.T, r *SBOMResult) {
	t.Helper()

	var doc spdxDoc
	if err := json.Unmarshal(r.Body, &doc); err != nil {
		t.Fatalf("SPDX body is not JSON: %v", err)
	}

	if len(doc.Packages) != r.Components+1 {
		t.Fatalf("SPDX holds %d packages for %d dependencies", len(doc.Packages), r.Components)
	}

	ids := map[string]struct{}{doc.SPDXID: {}}

	for _, p := range doc.Packages {
		if !spdxRefRE.MatchString(p.SPDXID) {
			t.Fatalf("SPDX package id %q is not a valid SPDXRef", p.SPDXID)
		}

		if _, dup := ids[p.SPDXID]; dup {
			t.Fatalf("SPDX id %q is used twice", p.SPDXID)
		}

		ids[p.SPDXID] = struct{}{}
	}

	for _, rel := range doc.Relationships {
		_, from := ids[rel.Element]
		_, to := ids[rel.Related]

		if !from || !to {
			t.Fatalf("SPDX relationship %+v names an unknown element", rel)
		}
	}

	declared := map[string]struct{}{}

	for _, x := range doc.ExtractedLicenses {
		if !licenseRefRE.MatchString(x.LicenseID) || licenseRefRE.FindString(x.LicenseID) != x.LicenseID {
			t.Fatalf("SPDX extracted license id %q is not a valid LicenseRef", x.LicenseID)
		}

		declared[x.LicenseID] = struct{}{}
	}

	for _, p := range doc.Packages {
		for _, expr := range []string{p.LicenseConcluded, p.LicenseDeclared} {
			for _, ref := range licenseRefRE.FindAllString(expr, -1) {
				if _, ok := declared[ref]; !ok {
					t.Fatalf("SPDX package %q uses undeclared %q in %q", p.SPDXID, ref, expr)
				}
			}
		}
	}

	if strings.Contains(doc.DocumentNamespace, "#") {
		t.Fatalf("SPDX document namespace %q has a fragment", doc.DocumentNamespace)
	}
}

func checkTagValue(t *testing.T, r *SBOMResult) {
	t.Helper()

	var packages, relationships, licenseIDs, texts int

	for line := range strings.SplitSeq(r.Text, "\n") {
		if line == "" {
			continue
		}

		if !tagValueLine.MatchString(line) {
			t.Fatalf("SPDX tag-value line %q is not a known tag", line)
		}

		if i := strings.IndexFunc(line, func(c rune) bool { return c < 0x20 || c == 0x7f || c == '\u2028' || c == '\u2029' }); i >= 0 {
			t.Fatalf("SPDX tag-value line %q holds the line break or control character at %d", line, i)
		}

		switch {
		case line == "FilesAnalyzed: false": // written for every package, whatever its name
			packages++
		case strings.HasPrefix(line, "Relationship: "):
			relationships++
		case strings.HasPrefix(line, "LicenseID: "):
			licenseIDs++
		case strings.HasPrefix(line, "ExtractedText: "):
			texts++

			body, ok := strings.CutPrefix(line, "ExtractedText: <text>")
			if !ok || !strings.HasSuffix(body, "</text>") || strings.Contains(strings.TrimSuffix(body, "</text>"), "</text>") {
				t.Fatalf("SPDX extracted text line %q does not hold exactly one closed <text> element", line)
			}
		}
	}

	if packages != r.Components+1 || relationships != r.Components+1 || licenseIDs != texts {
		t.Fatalf("SPDX tag-value has %d packages, %d relationships, %d license ids and %d texts for %d dependencies",
			packages, relationships, licenseIDs, texts, r.Components)
	}
}

func checkSPDX3(t *testing.T, r *SBOMResult) {
	t.Helper()

	var doc struct {
		Graph []map[string]any `json:"@graph"`
	}

	if err := json.Unmarshal(r.Body, &doc); err != nil {
		t.Fatalf("SPDX 3 body is not JSON: %v", err)
	}

	ids := map[string]struct{}{"_:creationinfo": {}}

	for _, el := range doc.Graph {
		id, _ := el["spdxId"].(string)
		if id == "" {
			continue
		}

		if _, dup := ids[id]; dup {
			t.Fatalf("SPDX 3 element id %q is used twice", id)
		}

		ids[id] = struct{}{}
	}

	packages := 0

	for _, el := range doc.Graph {
		if el["type"] == "software_Package" {
			packages++
		}

		for _, key := range []string{"element", "rootElement", "to"} {
			list, _ := el[key].([]any)
			for _, v := range list {
				if id, _ := v.(string); id != "" {
					if _, ok := ids[id]; !ok {
						t.Fatalf("SPDX 3 %s of %v names the unknown element %q", key, el["spdxId"], id)
					}
				}
			}
		}

		if from, _ := el["from"].(string); from != "" {
			if _, ok := ids[from]; !ok {
				t.Fatalf("SPDX 3 relationship %v starts at the unknown element %q", el["spdxId"], from)
			}
		}
	}

	if packages != r.Components+1 {
		t.Fatalf("SPDX 3 holds %d packages for %d dependencies", packages, r.Components)
	}
}

// FuzzGenerateVEX builds the vulnerability documents (OpenVEX, CycloneDX VEX,
// and the vulnerabilities of the SBOMs) of an artifact whose scan reports
// arbitrary findings: every finding carries a valid VEX status, "fixed" only
// where a reported fix is at or below the artifact version, and the documents
// are well-formed.
func FuzzGenerateVEX(f *testing.F) {
	f.Add("npm", "lib@1.2.3", `[{"id":"CVE-2021-1","fixed_in":"1.2.3","severity":"HIGH","cvss":7.5,"cwes":["CWE-79","x"]},{"id":"cve-2021-1"},{"id":"GHSA-xxxx","fixed_in":"2.0.0, 1.0.0"},{"id":""}]`, uint8(0))
	f.Add("maven", "g:a@1.0", `[{"id":"CVE-1","fixed_in":"1.0-SNAPSHOT","cvss":-1,"references":["http://x"],"published_at":"bad"}]`, uint8(1))
	f.Add("", "", `[]`, uint8(0))

	f.Fuzz(func(t *testing.T, format, artifactID, vulns string, flags uint8) {
		var found []models.Vulnerability
		if json.Unmarshal([]byte(vulns), &found) != nil {
			return
		}

		g := fuzzGenerator()
		in := newInput(format, artifactID, nil)
		in.Scan = &models.VulnScanResult{Scanner: "trivy,osv", Vulnerabilities: found}
		in.Quarantined = flags&1 != 0

		distinct := map[string]struct{}{}

		for _, v := range found {
			if v.ID != "" {
				distinct[strings.ToUpper(v.ID)] = struct{}{}
			}
		}

		findings := in.Findings()
		if len(findings) != len(distinct) {
			t.Fatalf("%d findings for %d distinct vulnerability ids", len(findings), len(distinct))
		}

		for _, fd := range findings {
			if !ValidVEXStatus(fd.Status) {
				t.Fatalf("finding %q has the status %q", fd.Vuln.ID, fd.Status)
			}

			if fd.Status == models.VEXFixed && !fixedByAny(in.Format, in.Version, fd.Vuln.FixedIn) {
				t.Fatalf("finding %q is fixed by %q although version %q is not at or above it", fd.Vuln.ID, fd.Vuln.FixedIn, in.Version)
			}
		}

		open, err := g.GenerateOpenVEX([]*Input{in})
		if err != nil {
			t.Fatalf("OpenVEX: %v", err)
		}

		if open.Components != len(findings) {
			t.Fatalf("OpenVEX: %d statements for %d findings", open.Components, len(findings))
		}

		cdxVEX, err := g.GenerateCycloneDXVEX([]*Input{in}, []string{CycloneDX14, CycloneDX15, CycloneDX16}[int(flags>>1)%3])
		if err != nil {
			t.Fatalf("CycloneDX VEX: %v", err)
		}

		if cdxVEX.Components != len(findings) {
			t.Fatalf("CycloneDX VEX: %d vulnerabilities for %d findings", cdxVEX.Components, len(findings))
		}

		var doc struct {
			Vulnerabilities []cdxVuln `json:"vulnerabilities"`
		}

		if err := json.Unmarshal(cdxVEX.Body, &doc); err != nil || len(doc.Vulnerabilities) != len(findings) {
			t.Fatalf("CycloneDX VEX body: %d vulnerabilities, %v", len(doc.Vulnerabilities), err)
		}

		// The vulnerabilities of an SPDX 3 document are built from the same findings.
		if b := g.spdx3Document(in); len(b.graph) == 0 {
			t.Fatal("SPDX 3 graph is empty")
		}
	})
}
