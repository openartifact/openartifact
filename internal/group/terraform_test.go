// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	tfModVersions  = "net/vpc/versions"
	tfProvVersions = "random/versions"
)

func tfModuleDoc(src string, versions ...string) string {
	entries := make([]string, 0, len(versions))
	for _, v := range versions {
		entries = append(entries, `{"version":"`+v+`"}`)
	}

	return `{"modules":[{"source":"` + src + `","versions":[` + strings.Join(entries, ",") + `]}]}`
}

func tfModuleVersionsOf(t *testing.T, body string) []string {
	t.Helper()

	var doc struct {
		Modules []struct {
			Source   string `json:"source"`
			Versions []struct {
				Version string `json:"version"`
			} `json:"versions"`
		} `json:"modules"`
	}

	if err := json.Unmarshal([]byte(body), &doc); err != nil || len(doc.Modules) != 1 {
		t.Fatalf("not a module version list: %v: %s", err, body)
	}

	out := make([]string, 0, len(doc.Modules[0].Versions))
	for _, v := range doc.Modules[0].Versions {
		out = append(out, v.Version)
	}

	return out
}

func tfGroup(t *testing.T, mergeProxy bool) *fixture {
	t.Helper()

	f := newFixture(t, "terraform", member("hosted", "terraform", "hosted"), member("mirror", "terraform", "proxy"))
	f.grp.MergeProxyVersions = mergeProxy
	f.h.SetBaseURL("http://oa.test/pre/")

	return f
}

func TestTerraformGroup_ModuleVersionsAreMergedLowestFirst(t *testing.T) {
	f := tfGroup(t, true)
	f.set("hosted", tfModVersions, 200, tfModuleDoc("hosted/net/vpc", "1.0.0", "1.10.0"))
	f.set("mirror", tfModVersions, 200, tfModuleDoc("mirror/net/vpc", "1.2.0", "1.0.0", "2.0.0-rc.1"))

	w := f.get(http.MethodGet, tfModVersions)

	got := tfModuleVersionsOf(t, w.Body.String())
	if want := []string{"1.0.0", "1.2.0", "1.10.0", "2.0.0-rc.1"}; w.Code != 200 || !slices.Equal(got, want) {
		t.Errorf("versions = %d %v, want %v", w.Code, got, want)
	}

	if !strings.Contains(w.Body.String(), `"source":"all/net/vpc"`) {
		t.Errorf("source must name the group: %s", w.Body.String())
	}
}

func TestTerraformGroup_ModuleVersionKeepsTheEarliestMembersEntry(t *testing.T) {
	f := tfGroup(t, true)
	f.set("hosted", tfModVersions, 200, `{"modules":[{"source":"x","versions":[{"version":"1.0.0","root":{"hosted":true}}]}]}`)
	f.set("mirror", tfModVersions, 200, `{"modules":[{"source":"x","versions":[{"version":"1.0.0","root":{"hosted":false}}]}]}`)

	if w := f.get(http.MethodGet, tfModVersions); !strings.Contains(w.Body.String(), `"hosted":true`) ||
		strings.Contains(w.Body.String(), `"hosted":false`) {
		t.Errorf("body = %s, want the hosted entry only", w.Body.String())
	}
}

func TestTerraformGroup_ProviderVersionsUnionPlatformsAndProtocols(t *testing.T) {
	f := tfGroup(t, true)
	f.set("hosted", tfProvVersions, 200,
		`{"versions":[{"version":"3.0.0","protocols":["5.0"],"platforms":[{"os":"linux","arch":"amd64"}]}],"warnings":null}`)
	f.set("mirror", tfProvVersions, 200, `{"versions":[
		{"version":"3.0.0","protocols":["5.0","6.0"],"platforms":[{"os":"linux","arch":"amd64"},{"os":"darwin","arch":"arm64"}]},
		{"version":"2.0.0","protocols":["5.0"],"platforms":[{"os":"linux","arch":"amd64"}]}]}`)

	w := f.get(http.MethodGet, tfProvVersions)

	var doc struct {
		Versions []providerVersion `json:"versions"`
	}

	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil || len(doc.Versions) != 2 {
		t.Fatalf("provider versions = %d %s (%v)", w.Code, w.Body.String(), err)
	}

	v3 := doc.Versions[1]
	if doc.Versions[0].Version != "2.0.0" || v3.Version != "3.0.0" || len(v3.Platforms) != 2 ||
		!slices.Equal(v3.Protocols, []string{"5.0", "6.0"}) {
		t.Errorf("merged = %+v", doc.Versions)
	}
}

func TestTerraformGroup_HostedWinsHidesProxyVersionsAndDownloads(t *testing.T) {
	f := tfGroup(t, false)
	f.set("hosted", tfModVersions, 200, tfModuleDoc("hosted/net/vpc", "1.0.0"))
	f.set("mirror", tfModVersions, 200, tfModuleDoc("mirror/net/vpc", "1.0.0", "9.9.9"))
	f.set("mirror", "net/vpc/9.9.9/download", 204, "")
	f.set("mirror", "other/vpc/versions", 200, tfModuleDoc("mirror/other/vpc", "1.0.0"))
	f.set("mirror", "random/9.9.9/download/linux/amd64", 200, "{}")
	f.set("hosted", tfProvVersions, 200, `{"versions":[{"version":"1.0.0","protocols":[],"platforms":[]}]}`)

	if w := f.get(http.MethodGet, tfModVersions); strings.Contains(w.Body.String(), "9.9.9") {
		t.Errorf("proxy version leaked: %s", w.Body.String())
	}

	if w := f.get(http.MethodGet, "net/vpc/9.9.9/download"); w.Code != http.StatusNotFound {
		t.Errorf("proxy module download = %d, want 404", w.Code)
	}

	if w := f.get(http.MethodGet, "random/9.9.9/download/linux/amd64"); w.Code != http.StatusNotFound {
		t.Errorf("proxy provider download = %d, want 404 (a hosted member holds the type)", w.Code)
	}

	if w := f.get(http.MethodGet, "other/vpc/versions"); w.Code != 200 {
		t.Errorf("a module no hosted member holds = %d, want the proxy's", w.Code)
	}
}

func TestTerraformGroup_HostedWinsIsPerKind(t *testing.T) {
	f := tfGroup(t, false)
	// A hosted provider type named like a proxied module must not hide the module.
	f.set("hosted", "net/versions", 200, `{"versions":[{"version":"1.0.0","protocols":[],"platforms":[]}]}`)
	f.set("mirror", tfModVersions, 200, tfModuleDoc("mirror/net/vpc", "1.0.0"))

	if w := f.get(http.MethodGet, tfModVersions); w.Code != 200 {
		t.Errorf("module = %d, want served by the proxy", w.Code)
	}
}

func TestTerraformGroup_FailingMemberIsPartialAndUncacheable(t *testing.T) {
	f := tfGroup(t, true)
	f.set("hosted", tfModVersions, 200, tfModuleDoc("x", "1.0.0"))
	f.set("mirror", tfModVersions, 503, "")

	w := f.get(http.MethodGet, tfModVersions)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("ETag") != "" {
		t.Errorf("partial = %d cc=%q etag=%q", w.Code, w.Header().Get("Cache-Control"), w.Header().Get("ETag"))
	}

	f.set("hosted", tfModVersions, 404, "")

	if w := f.get(http.MethodGet, tfModVersions); w.Code != http.StatusBadGateway {
		t.Errorf("nobody has it and one failed = %d, want 502", w.Code)
	}

	f.set("mirror", tfModVersions, 404, "")

	if w := f.get(http.MethodGet, tfModVersions); w.Code != http.StatusNotFound {
		t.Errorf("unknown module = %d, want 404", w.Code)
	}
}

func TestTerraformGroup_UnreadableMemberDocumentFailsThatMember(t *testing.T) {
	f := tfGroup(t, true)
	f.set("hosted", tfModVersions, 200, tfModuleDoc("x", "1.0.0"))
	f.set("mirror", tfModVersions, 200, `{"modules":[{"versions":[{"nope":1}]}]}`)

	w := f.get(http.MethodGet, tfModVersions)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" ||
		!slices.Equal(tfModuleVersionsOf(t, w.Body.String()), []string{"1.0.0"}) {
		t.Errorf("= %d %s cc=%q", w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
	}
}

func TestTerraformGroup_LatestIsOfTheMergedListAndNamesTheGroup(t *testing.T) {
	f := tfGroup(t, true)
	f.set("hosted", tfModVersions, 200, tfModuleDoc("x", "1.0.0"))
	f.set("mirror", tfModVersions, 200, tfModuleDoc("x", "1.5.0", "2.0.0-beta.1"))
	f.set("mirror", "net/vpc/1.5.0", 200, `{"id":"mirror/net/vpc/1.5.0","namespace":"mirror","name":"vpc","version":"1.5.0"}`)

	w := f.get(http.MethodGet, "net/vpc")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"namespace":"all"`) ||
		!strings.Contains(w.Body.String(), `"id":"all/net/vpc/1.5.0"`) {
		t.Errorf("latest details = %d %s", w.Code, w.Body.String())
	}

	w = f.get(http.MethodGet, "net/vpc/download")
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/pre/repos/all/net/vpc/1.5.0/download" {
		t.Errorf("latest download = %d %q", w.Code, w.Header().Get("Location"))
	}

	if w := f.get(http.MethodGet, "gone/vpc/download"); w.Code != http.StatusNotFound {
		t.Errorf("unknown module latest download = %d, want 404", w.Code)
	}
}

func TestTerraformGroup_DownloadsComeFromTheOwningMemberUntouched(t *testing.T) {
	f := tfGroup(t, true)
	f.set("hosted", tfModVersions, 200, tfModuleDoc("x", "1.0.0"))

	be := f.backend()
	be.ServeMember = func(w http.ResponseWriter, _ *http.Request, m *models.Repository, p string) {
		f.asked = append(f.asked, m.Name)

		switch {
		case m.Name == "mirror" && p == "net/vpc/1.0.0/download":
			w.Header().Set("X-Terraform-Get", "http://oa.test/terraform/dl/ticket/vpc.zip")
			w.WriteHeader(http.StatusNoContent)
		case m.Name == "mirror" && p == "random/1.0.0/download/linux/amd64":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"filename":"x.zip","signing_keys":{"gpg_public_keys":[{"key_id":"MIRROR"}]}}`)
		default:
			http.NotFound(w, nil)
		}
	}

	serve := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		f.h.Serve(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil), f.grp, path, be)

		return w
	}

	w := serve("net/vpc/1.0.0/download")
	if w.Code != http.StatusNoContent || w.Header().Get("X-Terraform-Get") != "http://oa.test/terraform/dl/ticket/vpc.zip" {
		t.Errorf("module download = %d %v", w.Code, w.Header())
	}

	if w := serve("random/1.0.0/download/linux/amd64"); w.Code != 200 || !strings.Contains(w.Body.String(), "MIRROR") {
		t.Errorf("provider download = %d %s", w.Code, w.Body.String())
	}

	if w := serve("random/1.0.0/download/linux/arm64"); w.Code != http.StatusNotFound {
		t.Errorf("platform nobody has = %d, want 404", w.Code)
	}
}

func TestTerraformGroup_VersionDetailsOfAHostedModuleDoNotUseTheProxy(t *testing.T) {
	f := tfGroup(t, false)
	f.set("hosted", tfModVersions, 200, tfModuleDoc("x", "1.0.0"))
	f.set("hosted", "net/vpc/1.0.0", 200, `{"id":"hosted/net/vpc/1.0.0","namespace":"hosted"}`)
	f.set("mirror", "net/vpc/9.9.9", 200, `{"id":"mirror/net/vpc/9.9.9","namespace":"mirror"}`)

	if w := f.get(http.MethodGet, "net/vpc/9.9.9"); w.Code != http.StatusNotFound {
		t.Errorf("proxy details of a hosted module = %d, want 404", w.Code)
	}

	if w := f.get(http.MethodGet, "net/vpc/1.0.0"); w.Code != 200 || !strings.Contains(w.Body.String(), `"namespace":"all"`) {
		t.Errorf("hosted details = %d %s", w.Code, w.Body.String())
	}
}

func TestTerraformGroup_ReadOnlyAndUnknownPaths(t *testing.T) {
	f := tfGroup(t, true)

	if w := f.get(http.MethodPost, tfModVersions); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", w.Code)
	}

	if w := f.get(http.MethodGet, "a/b/c/d/e/f"); w.Code != http.StatusNotFound {
		t.Errorf("deep path = %d, want 404", w.Code)
	}
}

func TestParseTerraformPath(t *testing.T) {
	for path, want := range map[string]terraformKind{
		"net/vpc/versions":                   terraformModuleVersions,
		"random/versions":                    terraformProviderVersions,
		"net/vpc":                            terraformModuleLatest,
		"net/vpc/download":                   terraformModuleLatestDownload,
		"net/vpc/1.0.0":                      terraformModuleVersion,
		"net/vpc/1.0.0/download":             terraformModuleDownload,
		"random/1.0.0/download/linux/amd64":  terraformProviderDownload,
		"random/1.0.0/other/linux/amd64":     terraformOther,
		"net/vpc/1.0.0/other":                terraformOther,
		"/net/vpc/versions/":                 terraformModuleVersions,
		"net/../versions":                    terraformOther,
		"net/vp%63/versions":                 terraformOther,
		"":                                   terraformOther,
		"net":                                terraformOther,
		"a/b/c/d/e/f":                        terraformOther,
		"random/1.0.0/download/linux/amd64/": terraformProviderDownload,
	} {
		if got := parseTerraformPath(path).kind; got != want {
			t.Errorf("parseTerraformPath(%q) = %d, want %d", path, got, want)
		}
	}
}

func TestLatestTerraformVersion(t *testing.T) {
	for _, c := range []struct {
		versions []string
		want     string
	}{
		{nil, ""},
		{[]string{"1.0.0", "2.0.0-rc.1", "1.5.0"}, "1.5.0"},
		{[]string{"2.0.0-rc.1", "2.0.0-rc.2"}, "2.0.0-rc.2"},
		{[]string{"1.10.0", "1.9.0"}, "1.10.0"},
	} {
		if got := latestTerraformVersion(c.versions); got != c.want {
			t.Errorf("latest(%v) = %q, want %q", c.versions, got, c.want)
		}
	}
}

func FuzzParseTerraformPath(f *testing.F) {
	for _, p := range []string{
		"net/vpc/versions", "random/versions", "net/vpc", "net/vpc/download", "net/vpc/1.0.0",
		"net/vpc/1.0.0/download", "random/1.0.0/download/linux/amd64", "/a/b/", "../..", "a//b", "", "a/b/c/d/e/f", "%2e%2e/x",
	} {
		f.Add(p)
	}

	f.Fuzz(func(t *testing.T, relPath string) {
		p := parseTerraformPath(relPath)
		if p.kind == terraformOther {
			if _, ok := terraformRef(relPath); ok {
				t.Fatalf("terraformRef(%q) names a package for an unclassified path", relPath)
			}

			return
		}

		for _, s := range []string{p.name, p.system, p.version} {
			if s != "" && !terraformSegment(s) {
				t.Fatalf("parseTerraformPath(%q) yields the invalid coordinate %q", relPath, s)
			}
		}

		if p.name == "" {
			t.Fatalf("parseTerraformPath(%q) = %+v without a name", relPath, p)
		}

		if ref, ok := terraformRef(relPath); !ok || !strings.HasSuffix(ref.probe, "/versions") || strings.Contains(ref.probe, "..") {
			t.Fatalf("terraformRef(%q) = %+v, %v", relPath, ref, ok)
		}
	})
}

func FuzzMergeTerraformVersions(f *testing.F) {
	f.Add(
		tfModuleDoc("x", "1.0.0", "2.0.0-rc.1"),
		`{"versions":[{"version":"1.0.0","protocols":["5.0"],"platforms":[{"os":"linux","arch":"amd64"}]}]}`,
	)
	f.Add(`{"modules":[]}`, `{"versions":null}`)
	f.Add(`{"modules":[{"versions":[{"version":""}]}]}`, `{"versions":[{"version":"v1"}]}`)
	f.Add(`[`, `{"versions":[{"version":"1.0.0","platforms":null}]}`)

	f.Fuzz(func(t *testing.T, modDoc, provDoc string) {
		if l, err := parseModuleVersions([]byte(modDoc)); err == nil {
			out := mergeModuleVersions(l, l)
			if len(out) > len(l) {
				t.Fatalf("merge of a list with itself grew: %d > %d", len(out), len(l))
			}

			if body, err := renderModuleVersions("g/n/s", out); err != nil || !json.Valid(body) {
				t.Fatalf("rendered module list is not JSON for %q: %v", modDoc, err)
			}
		}

		if l, err := parseProviderVersions([]byte(provDoc)); err == nil {
			out := mergeProviderVersions(l, l)
			if len(out) > len(l) {
				t.Fatalf("merge of a list with itself grew: %d > %d", len(out), len(l))
			}

			if body, err := renderProviderVersions(out); err != nil || !json.Valid(body) {
				t.Fatalf("rendered provider list is not JSON for %q: %v", provDoc, err)
			}
		}

		if out, err := rewriteModuleDetails([]byte(modDoc), "grp"); err == nil && !json.Valid(out) {
			t.Fatalf("rewritten details are not JSON for %q", modDoc)
		}
	})
}
