// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/format/terraform"
)

var (
	tfFuzzDocURL  = &url.URL{Scheme: "https", Host: "registry.example", Path: "/.well-known/terraform.json"}
	tfFuzzGetBase = &url.URL{Scheme: "https", Host: "registry.example", Path: "/v1/modules/ns/demo/aws/1.0.0/download"}
)

// FuzzTerraformParsePath checks that an accepted request path names only
// validated segments: they end up in upstream URLs and cache keys.
func FuzzTerraformParsePath(f *testing.F) {
	for _, s := range []string{
		"random/versions", "demo/aws", "demo/aws/versions", "demo/aws/download", "demo/aws/1.2.3", "demo/aws/1.2.3/download",
		"random/3.5.1/download/linux/amd64", "demo/versions/versions", "../x/versions", "a//b", "random/1.0.0/download/%2e%2e/amd64",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, rel string) {
		p, ok := parseTerraformPath(rel)
		if !ok {
			return
		}

		for _, seg := range []string{p.name, p.system, p.os, p.arch} {
			if strings.ContainsAny(seg, "/\\\x00?#%") || seg == ".." || seg == "." {
				t.Fatalf("%q accepted with segment %q", rel, seg)
			}
		}

		if p.name == "" || p.kind == 0 {
			t.Fatalf("%q accepted as %+v", rel, p)
		}

		if p.version != "" && !terraform.ValidVersion(p.version) {
			t.Fatalf("%q accepted with version %q", rel, p.version)
		}
	})
}

// FuzzTerraformDiscovery checks every service URL of an accepted discovery
// document is an absolute http(s) URL without credentials, ending in a slash.
func FuzzTerraformDiscovery(f *testing.F) {
	f.Add([]byte(`{"modules.v1":"/v1/modules/","providers.v1":"/v1/providers/"}`))
	f.Add([]byte(`{"providers.v1":"https://providers.example/p"}`))
	f.Add([]byte(`{"modules.v1":"v1/modules"}`))
	f.Add([]byte(`{"modules.v1":"https://u:p@x/"}`))
	f.Add([]byte(`{"modules.v1":"//other.example/m/"}`))
	f.Add([]byte(`{"login.v1":{"client":"x"}}`))

	f.Fuzz(func(t *testing.T, body []byte) {
		ep, err := parseTerraformDiscovery(body, tfFuzzDocURL)
		if err != nil {
			return
		}

		if ep.modules == nil && ep.providers == nil {
			t.Fatal("a discovery document without services accepted")
		}

		for _, u := range []*url.URL{ep.modules, ep.providers} {
			if u == nil {
				continue
			}

			if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.EscapedPath(), "/") {
				t.Fatalf("service URL %q from %q", u, body)
			}
		}
	})
}

// FuzzTerraformModuleGet checks that only plain archive URLs and github.com
// repositories (mapped to codeload) get through parseModuleGet, whatever a
// registry puts in X-Terraform-Get.
func FuzzTerraformModuleGet(f *testing.F) {
	for _, s := range []string{
		"https://h.example/demo.zip", "https://h.example/demo?archive=tar.gz&x=1", "/modules/demo.zip", "git::https://github.com/org/repo?ref=v1",
		"s3::https://x/y.zip", "github.com/org/repo", "git@github.com:org/repo.git", "https://h/demo.zip//sub", "https://h/demo.zip?checksum=md5:a",
		"https://u:p@h/demo.zip", "file:///etc/passwd", "https://h/%zz.zip", "", "::", "a::b::c",
		"git::https://github.com/org/repo.git?ref=0123456789abcdef0123456789abcdef01234567&depth=1",
		"git::https://github.com/org/repo.git//modules/x?ref=v1.2.3", "github.com/org/repo//a/b?ref=feature/x", "https://github.com/org/repo",
		"git::https://github.com/org/repo?ref=../../x", "git::https://github.com/org/repo?sshkey=abc", "git::ssh://git@github.com/org/repo.git",
		"git::https://gitlab.com/org/repo.git", "https://h/x.tar.gz//wrap/sub", "https://h/x.tar.gz//../x", "https://h/x.zip//a//b", "git::https://github.com/o/r//",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		src, err := parseModuleGet(raw, tfFuzzGetBase, tfCodeloadBase)
		if err != nil {
			return
		}

		u, perr := url.Parse(src.url)
		if perr != nil {
			t.Fatalf("accepted %q as an unparsable URL %q", raw, src.url)
		}

		if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" || strings.Contains(u.Path, "//") {
			t.Fatalf("accepted %q as %q", raw, src.url)
		}

		if !slices.Contains(tfArchiveExts, "."+src.ext) {
			t.Fatalf("accepted %q with extension %q", raw, src.ext)
		}

		if _, q, _ := splitArchiveHint(u.RawQuery); q != u.RawQuery {
			t.Fatalf("accepted %q with a getter archive hint left in %q", raw, src.url)
		}

		if src.subdir != "" {
			if clean, ok := cleanTFSubdir(src.subdir); !ok || clean != src.subdir || !slices.Contains([]string{"zip", "tar.gz", "tgz"}, src.ext) {
				t.Fatalf("accepted %q with subdirectory %q of a %s archive", raw, src.subdir, src.ext)
			}
		}

		if src.strip {
			rest, ok := strings.CutPrefix(src.url, tfCodeloadBase+"/")
			segs := strings.Split(rest, "/")

			if !ok || u.RawQuery != "" || len(segs) < 4 || segs[2] != "tar.gz" || src.ext != "tar.gz" || !validTFGitRef(strings.Join(segs[3:], "/")) {
				t.Fatalf("accepted %q as the codeload URL %q", raw, src.url)
			}
		}
	})
}

// FuzzTerraformSHA256SUMS checks that an accepted checksum file maps safe file
// names to well-formed digests.
func FuzzTerraformSHA256SUMS(f *testing.F) {
	f.Add([]byte(strings.Repeat("a", 64) + "  terraform-provider-x_1.0.0_linux_amd64.zip\n"))
	f.Add([]byte(strings.Repeat("A", 64) + " *x.zip\r\n\r\n" + strings.Repeat("b", 64) + "  y.zip\n"))
	f.Add([]byte(strings.Repeat("a", 64) + "  ../x.zip\n"))
	f.Add([]byte(strings.Repeat("a", 63) + "  x.zip\n"))
	f.Add([]byte("\n\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		sums, err := parseSHA256SUMS(data)
		if err != nil {
			return
		}

		if len(sums) == 0 {
			t.Fatal("an empty set of checksums accepted")
		}

		for name, digest := range sums {
			if name == "" || strings.ContainsAny(name, "/\\\x00") || len(digest) != sha256HexLen || digest != strings.ToLower(digest) {
				t.Fatalf("entry %q = %q from %q", name, digest, data)
			}
		}
	})
}

// FuzzTerraformCanonVersions checks the version list canonicalisers: what they
// output parses again, lists only valid versions and is ascending.
func FuzzTerraformCanonVersions(f *testing.F) {
	f.Add([]byte(`{"modules":[{"versions":[{"version":"1.1.0"},{"version":"1.0.0"},{"version":"x y"}]}]}`))
	f.Add([]byte(`{"versions":[{"version":"3.5.1","protocols":["5.0"],"platforms":[{"os":"linux","arch":"amd64"}]}]}`))
	f.Add([]byte(`{"versions":[{"version":"1.0.0","platforms":[{"os":"../","arch":"x"}]}]}`))
	f.Add([]byte(`{"modules":[]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		if out, err := canonModuleVersions(bytes.NewReader(data)); err == nil {
			versions, perr := parseCanonModuleVersions(out)
			if perr != nil || len(versions) == 0 {
				t.Fatalf("canonical module list %s does not parse: %v", out, perr)
			}

			if !slices.IsSortedFunc(versions, terraform.CompareVersions) {
				t.Fatalf("module versions %v are not ascending", versions)
			}

			for _, v := range versions {
				if !terraform.ValidVersion(v) {
					t.Fatalf("invalid version %q kept", v)
				}
			}
		}

		if out, err := canonProviderVersions(bytes.NewReader(data)); err == nil {
			var doc tfProviderVersionList
			if json.Unmarshal(out, &doc) != nil || len(doc.Versions) == 0 {
				t.Fatalf("canonical provider list %s does not parse", out)
			}

			for _, v := range doc.Versions {
				if !terraform.ValidVersion(v.Version) || len(v.Platforms) == 0 {
					t.Fatalf("unusable provider version %+v kept", v)
				}

				for _, p := range v.Platforms {
					if !terraform.ValidPlatformPart(p.OS) || !terraform.ValidPlatformPart(p.Arch) {
						t.Fatalf("invalid platform %+v kept", p)
					}
				}
			}
		}
	})
}

// FuzzTerraformProviderPackage checks an accepted package document has safe
// file names, a well-formed digest and absolute http(s) URLs.
func FuzzTerraformProviderPackage(f *testing.F) {
	good := `{"protocols":["5.0"],"os":"linux","arch":"amd64","filename":"p_1.0.0_linux_amd64.zip","shasum":"` + strings.Repeat("a", 64) +
		`","download_url":"https://rel.example/p_1.0.0_linux_amd64.zip","shasums_url":"/s/p_1.0.0_SHA256SUMS","shasums_signature_url":"https://rel.example/p_1.0.0_SHA256SUMS.sig","signing_keys":{"gpg_public_keys":[]}}`
	f.Add([]byte(good), "linux", "amd64")
	f.Add([]byte(good), "darwin", "arm64")
	f.Add([]byte(strings.Replace(good, `"filename":"p_1.0.0_linux_amd64.zip"`, `"filename":"../p.zip"`, 1)), "linux", "amd64")
	f.Add([]byte(strings.Replace(good, `"signing_keys":{"gpg_public_keys":[]}`, `"signing_keys":[1]`, 1)), "linux", "amd64")

	f.Fuzz(func(t *testing.T, data []byte, osName, arch string) {
		doc, err := parseProviderPackage(data, tfFuzzDocURL, osName, arch)
		if err != nil {
			return
		}

		if !validTFFileName(doc.Filename) || !strings.HasSuffix(doc.Filename, ".zip") || len(doc.Shasum) != sha256HexLen {
			t.Fatalf("document %+v accepted", doc)
		}

		for _, raw := range []string{doc.DownloadURL, doc.ShasumsURL, doc.ShasumsSignatureURL} {
			u, perr := url.Parse(raw)
			if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
				t.Fatalf("URL %q accepted", raw)
			}
		}

		if names := []string{doc.Filename, urlBase(doc.ShasumsURL), urlBase(doc.ShasumsSignatureURL)}; !validTFFileName(names[1]) || !validTFFileName(names[2]) ||
			names[0] == names[1] || names[0] == names[2] || names[1] == names[2] {
			t.Fatalf("file names %v accepted", names)
		}
	})
}

// FuzzTerraformVerifyArchive feeds arbitrary bodies to the archive check: it
// must never panic, whatever kind is asked for.
func FuzzTerraformVerifyArchive(f *testing.F) {
	f.Add("zip", []byte("PK\x03\x04"))
	f.Add("tar.gz", []byte{0x1f, 0x8b, 0x08})
	f.Add("tar.xz", []byte{0xfd, '7', 'z', 'X', 'Z', 0})
	f.Add("tar", []byte("<html>"))
	f.Add("", []byte{})

	f.Fuzz(func(_ *testing.T, ext string, data []byte) {
		_ = verifyTFArchive(ext, bytes.NewReader(data))
		_ = archiveContentType(ext)
	})
}

// FuzzTerraformUpstreamDocs serves one hostile body as every document an
// upstream registry answers (discovery, version lists, module download
// answers, package documents): the proxy never panics and answers either a
// well-formed JSON document or an error status.
func FuzzTerraformUpstreamDocs(f *testing.F) {
	f.Add([]byte(`{"modules.v1":"/v1/modules/","providers.v1":"/v1/providers/"}`))
	f.Add([]byte(`{"modules":[{"versions":[{"version":"1.0.0"}]}],"versions":[{"version":"1.0.0","platforms":[{"os":"linux","arch":"amd64"}]}],"location":"git::https://x/y"}`))
	f.Add([]byte(`{"os":"linux","arch":"amd64","filename":"p.zip","shasum":"` + strings.Repeat("a", 64) + `","download_url":"/p.zip","shasums_url":"/s","shasums_signature_url":"/s.sig"}`))
	f.Add([]byte(`<html></html>`))

	f.Fuzz(func(t *testing.T, data []byte) {
		fx, release := newFuzzFixture(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/.well-known/terraform.json" {
				_, _ = w.Write([]byte(`{"modules.v1":"/v1/modules/","providers.v1":"/v1/providers/"}`))
				return
			}

			w.Header().Set("X-Terraform-Get", string(data))
			_, _ = w.Write(data)
		})
		defer release()

		fx.repo.Format = "terraform"
		fx.repo.Name = "ns"
		fx.repo.UpstreamURL = fx.upstream.URL
		fx.svc.WithTerraformTicketKey([]byte("0123456789abcdef0123456789abcdef"))
		fx.svc.SetBaseURL(oaBase)

		for _, path := range []string{
			"random/versions", "demo/aws/versions", "demo/aws/1.0.0/download", "random/1.0.0/download/linux/amd64",
		} {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
			w := httptest.NewRecorder()
			fx.svc.Serve(w, req, fx.repo, path)

			if w.Code == http.StatusOK && !json.Valid(w.Body.Bytes()) {
				t.Fatalf("%s answered 200 with %q, not JSON", path, w.Body)
			}
		}
	})
}
