// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/curation"
)

// dlRules is the curation rule source of these tests.
type dlRules struct {
	rules []curation.Rule
	err   error
}

func (d *dlRules) ListCurationRules(context.Context, string) ([]curation.Rule, error) {
	return d.rules, d.err
}

func dlRule(action, pkg, version string) curation.Rule {
	return curation.Rule{Action: action, Package: pkg, Version: version, Reason: "test"}
}

// dlLayout describes the files of one package of a format in a fake upstream:
// blocked are the files of the version the denylist blocks and the allowlist
// leaves out, allowed the files of the version the allowlist lists, neutral
// the files that belong to no version (indexes, checksums), stranger a file of
// another package.
type dlLayout struct {
	format   string
	pkg      string
	blockVer string
	allowVer string
	blocked  []string
	allowed  []string
	neutral  []string
	stranger string
}

func (l dlLayout) files() map[string]string {
	files := map[string]string{l.stranger: "stranger"}

	for _, group := range [][]string{l.blocked, l.allowed, l.neutral} {
		for _, p := range group {
			files[p] = "body of " + p
		}
	}

	return files
}

var dlLayouts = []dlLayout{ //nolint:gochecknoglobals // test table
	{
		format: "maven", pkg: "org.acme:lib", blockVer: "1.0", allowVer: "2.0",
		blocked: []string{
			"org/acme/lib/1.0/lib-1.0.jar", "org/acme/lib/1.0/lib-1.0.pom", "org/acme/lib/1.0/lib-1.0.module",
			"org/acme/lib/1.0/lib-1.0-sources.jar", "org/acme/lib/1.0/lib-1.0-bin.tar.gz",
		},
		allowed: []string{"org/acme/lib/2.0/lib-2.0.jar", "org/acme/lib/2.0/lib-2.0.pom"},
		neutral: []string{
			"org/acme/lib/maven-metadata.xml", "org/acme/lib/1.0/lib-1.0.jar.sha1", "org/acme/lib/1.0/lib-1.0.jar.asc",
		},
		stranger: "org/acme/other/1.0/other-1.0.jar",
	},
	{
		format: "ivy", pkg: "org:mod", blockVer: "1.0", allowVer: "2.0",
		blocked:  []string{"org/mod/1.0/jars/mod.jar", "org/mod/1.0/ivy.xml", "org/mod/1.0/poms/mod.pom"},
		allowed:  []string{"org/mod/2.0/jars/mod.jar", "org/mod/2.0/ivy.xml"},
		neutral:  []string{"org/mod/1.0/ivy.xml.sha1", "org/mod/1.0/jars/mod.jar.md5"},
		stranger: "org/else/1.0/jars/else.jar",
	},
	{
		format: "sbt", pkg: "com.example:lib_2.13", blockVer: "1.0.0", allowVer: "1.1.0",
		blocked:  []string{"com/example/lib_2.13/1.0.0/lib_2.13-1.0.0.jar", "com/example/lib_2.13/1.0.0/lib_2.13-1.0.0.pom"},
		allowed:  []string{"com/example/lib_2.13/1.1.0/lib_2.13-1.1.0.jar"},
		neutral:  []string{"com/example/lib_2.13/maven-metadata.xml", "com/example/lib_2.13/1.0.0/lib_2.13-1.0.0.jar.sha1"},
		stranger: "com/example/other_2.13/1.0.0/other_2.13-1.0.0.jar",
	},
	{
		format: "deb", pkg: "hello", blockVer: "1.0-1", allowVer: "2.0-1",
		blocked:  []string{"pool/main/h/hello/hello_1.0-1_amd64.deb", "pool/main/h/hello/hello_1.0-1_all.udeb"},
		allowed:  []string{"pool/main/h/hello/hello_2.0-1_amd64.deb"},
		neutral:  []string{"dists/stable/Release", "dists/stable/main/binary-amd64/Packages.gz", "pool/main/h/hello/hello_1.0-1.dsc"},
		stranger: "pool/main/o/other/other_1.0-1_amd64.deb",
	},
	{
		format: "rpm", pkg: "hello", blockVer: "1.0-1.fc40", allowVer: "2.0-1.fc40",
		blocked:  []string{"Packages/h/hello-1.0-1.fc40.x86_64.rpm", "Packages/h/hello-1.0-1.fc40.src.rpm"},
		allowed:  []string{"Packages/h/hello-2.0-1.fc40.x86_64.rpm"},
		neutral:  []string{"repodata/repomd.xml", "repodata/abc-primary.xml.gz"},
		stranger: "Packages/o/other-1.0-1.fc40.x86_64.rpm",
	},
	{
		format: "pacman", pkg: "hello", blockVer: "1.0-1", allowVer: "1:2.0-1",
		blocked: []string{"extra/os/x86_64/hello-1.0-1-x86_64.pkg.tar.zst", "extra/os/x86_64/hello-1.0-1-any.pkg.tar.xz"},
		allowed: []string{"extra/os/x86_64/hello-1:2.0-1-x86_64.pkg.tar.zst"},
		neutral: []string{
			"extra/os/x86_64/extra.db", "extra/os/x86_64/extra.files.tar.gz",
			"extra/os/x86_64/hello-1.0-1-x86_64.pkg.tar.zst.sig",
		},
		stranger: "extra/os/x86_64/other-1.0-1-x86_64.pkg.tar.zst",
	},
}

func TestCurationDownload_MirroredFormats(t *testing.T) {
	for _, l := range dlLayouts {
		t.Run(l.format, func(t *testing.T) {
			f := newIvyFixture(t, l.format, l.files())
			f.repo.CurationMode = curation.ModeDenylist
			f.svc.WithCuration(&dlRules{rules: []curation.Rule{dlRule(curation.ActionBlock, l.pkg, l.blockVer)}})
			t.Run("denylist", func(t *testing.T) {
				expectDownloads(t, f, http.StatusForbidden, l.blocked)
				expectDownloads(t, f, http.StatusOK, l.allowed, l.neutral, []string{l.stranger})
			})

			f.svc.WithCuration(&dlRules{rules: []curation.Rule{dlRule(curation.ActionAllow, l.pkg, l.allowVer)}})
			f.repo.CurationMode = curation.ModeAllowlist
			t.Run("allowlist", func(t *testing.T) {
				expectDownloads(t, f, http.StatusOK, l.allowed, l.neutral)
				expectDownloads(t, f, http.StatusForbidden, l.blocked, []string{l.stranger})
			})

			f.repo.CurationMode = curation.ModeOff
			t.Run("mode off serves everything", func(t *testing.T) {
				expectDownloads(t, f, http.StatusOK, l.blocked, l.allowed, l.neutral, []string{l.stranger})
			})
		})
	}
}

func expectDownloads(t *testing.T, f *fixture, want int, groups ...[]string) {
	t.Helper()

	for _, group := range groups {
		for _, p := range group {
			if w := f.do(http.MethodGet, p); w.Code != want {
				t.Errorf("GET %s = %d, want %d (%q)", p, w.Code, want, w.Body.String())
			}
		}
	}
}

func TestCurationDownload_CachedFileIsRefusedOnceBlocked(t *testing.T) {
	for _, l := range dlLayouts {
		t.Run(l.format, func(t *testing.T) {
			f := newIvyFixture(t, l.format, l.files())
			f.svc.WithCuration(&dlRules{rules: []curation.Rule{dlRule(curation.ActionBlock, l.pkg, l.blockVer)}})

			expectDownloads(t, f, http.StatusOK, l.blocked) // cached while curation is off

			hits := f.hits.Load()
			f.repo.CurationMode = curation.ModeDenylist

			expectDownloads(t, f, http.StatusForbidden, l.blocked)

			if got := f.hits.Load(); got != hits {
				t.Errorf("refusal reached the upstream: %d -> %d requests", hits, got)
			}

			// HEAD is refused like GET.
			if w := f.do(http.MethodHead, l.blocked[0]); w.Code != http.StatusForbidden {
				t.Errorf("HEAD = %d, want 403", w.Code)
			}
		})
	}
}

func TestCurationDownload_RefusalNamesTheReason(t *testing.T) {
	l := dlLayouts[0]
	f := newIvyFixture(t, l.format, l.files())
	f.repo.CurationMode = curation.ModeDenylist
	f.svc.WithCuration(&dlRules{rules: []curation.Rule{
		{Action: curation.ActionBlock, Package: "org.acme:*", Version: "1.*", Reason: "CVE-2025-0001"},
	}})

	w := f.do(http.MethodGet, l.blocked[0])
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "CVE-2025-0001") {
		t.Fatalf("got %d %q, want 403 naming the reason", w.Code, w.Body.String())
	}

	// A block rule beats an allow rule.
	f.svc.WithCuration(&dlRules{rules: []curation.Rule{
		dlRule(curation.ActionAllow, "org.acme:lib", "*"),
		dlRule(curation.ActionBlock, "org.acme:lib", "1.0"),
	}})
	f.repo.CurationMode = curation.ModeAllowlist

	expectDownloads(t, f, http.StatusForbidden, l.blocked)
	expectDownloads(t, f, http.StatusOK, l.allowed)
}

func TestCurationDownload_UnreadableRulesFailClosed(t *testing.T) {
	l := dlLayouts[0]

	for name, src := range map[string]CurationSource{
		"rule store fails": &dlRules{err: errors.New("db down")},
		"no rule source":   nil,
	} {
		t.Run(name, func(t *testing.T) {
			f := newIvyFixture(t, l.format, l.files())
			f.repo.CurationMode = curation.ModeDenylist

			if src != nil {
				f.svc.WithCuration(src)
			}

			expectDownloads(t, f, http.StatusInternalServerError, l.blocked, l.allowed)
			// What is no package file is not asked.
			expectDownloads(t, f, http.StatusOK, l.neutral)
		})
	}
}

func TestCurationDownload_APK(t *testing.T) {
	m := newApkMirror(t)
	f := newApkFixture(t, m)

	f.repo.CurationMode = curation.ModeDenylist
	f.svc.WithCuration(&dlRules{rules: []curation.Rule{dlRule(curation.ActionBlock, "hello", "1.0-r0")}})

	if w := f.do(http.MethodGet, apkPkgKey); w.Code != http.StatusForbidden {
		t.Errorf("blocked package = %d, want 403", w.Code)
	}

	if w := f.do(http.MethodGet, apkIndexK); w.Code != http.StatusOK {
		t.Errorf("index = %d, want 200: the index still lists the blocked package", w.Code)
	}

	// Another version of the package, and another package, are served.
	f.svc.WithCuration(&dlRules{rules: []curation.Rule{dlRule(curation.ActionBlock, "hello", "2.*")}})

	if w := f.do(http.MethodGet, apkPkgKey); w.Code != http.StatusOK {
		t.Errorf("unblocked version = %d, want 200", w.Code)
	}

	// Cached now: a rule added afterwards still refuses it.
	f.svc.WithCuration(&dlRules{rules: []curation.Rule{dlRule(curation.ActionBlock, "hel*", "")}})

	hits := f.hits.Load()

	if w := f.do(http.MethodGet, apkPkgKey); w.Code != http.StatusForbidden || f.hits.Load() != hits {
		t.Errorf("cached package = %d (upstream %d -> %d), want 403 without upstream", w.Code, hits, f.hits.Load())
	}

	f.svc.WithCuration(&dlRules{rules: []curation.Rule{dlRule(curation.ActionAllow, "hello", "2.0-r0")}})
	f.repo.CurationMode = curation.ModeAllowlist

	if w := f.do(http.MethodGet, apkPkgKey); w.Code != http.StatusForbidden {
		t.Errorf("package outside the allowlist = %d, want 403", w.Code)
	}

	f.svc.WithCuration(&dlRules{rules: []curation.Rule{dlRule(curation.ActionAllow, "hello", "1.0-r*")}})

	if w := f.do(http.MethodGet, apkPkgKey); w.Code != http.StatusOK {
		t.Errorf("allowed package = %d, want 200", w.Code)
	}

	f.repo.CurationMode = curation.ModeOff
	f.svc.WithCuration(&dlRules{rules: []curation.Rule{dlRule(curation.ActionBlock, "*", "")}})

	if w := f.do(http.MethodGet, apkPkgKey); w.Code != http.StatusOK {
		t.Errorf("mode off = %d, want 200", w.Code)
	}
}

func TestCurationDownload_Docker(t *testing.T) {
	f := newOCIFixture(t)
	dg := f.reg.addManifest("library/alpine", "3.20", alpineManifest)
	layer := f.reg.addBlob("library/alpine", "layer-bytes")
	f.reg.addManifest("library/alpine", "3.19", alpineManifest)
	f.reg.addManifest("library/busybox", "1", alpineManifest)

	const (
		tag320   = "hub/library/alpine/manifests/3.20"
		tag319   = "hub/library/alpine/manifests/3.19"
		busybox  = "hub/library/busybox/manifests/1"
		byDigest = "hub/library/alpine/manifests/"
		blobPath = "hub/library/alpine/blobs/"
	)

	f.svc.WithCuration(&dlRules{rules: []curation.Rule{dlRule(curation.ActionBlock, "library/alpine", "3.19")}})
	f.repo.CurationMode = curation.ModeDenylist

	t.Run("denylist", func(t *testing.T) {
		w := f.do(http.MethodGet, tag319)
		if w.Code != http.StatusForbidden || ociErrorCode(t, w) != "DENIED" {
			t.Fatalf("blocked tag = %d %q, want 403 DENIED", w.Code, w.Body.String())
		}

		if w := f.do(http.MethodHead, tag319); w.Code != http.StatusForbidden {
			t.Errorf("HEAD blocked tag = %d, want 403", w.Code)
		}

		for _, p := range []string{tag320, busybox, byDigest + dg, blobPath + layer} {
			if w := f.do(http.MethodGet, p); w.Code != http.StatusOK {
				t.Errorf("GET %s = %d, want 200", p, w.Code)
			}
		}
	})

	t.Run("a digest rule blocks that manifest and its blobs stay reachable", func(t *testing.T) {
		f.svc.WithCuration(&dlRules{rules: []curation.Rule{dlRule(curation.ActionBlock, "library/alpine", dg)}})

		if w := f.do(http.MethodGet, byDigest+dg); w.Code != http.StatusForbidden {
			t.Errorf("blocked digest = %d, want 403", w.Code)
		}

		if w := f.do(http.MethodGet, blobPath+layer); w.Code != http.StatusOK {
			t.Errorf("blob = %d, want 200: blobs are not gated", w.Code)
		}
	})

	t.Run("allowlist", func(t *testing.T) {
		f.svc.WithCuration(&dlRules{rules: []curation.Rule{
			dlRule(curation.ActionAllow, "library/alpine", "3.20"),
			dlRule(curation.ActionAllow, "library/alpine", "sha256:*"),
		}})
		f.repo.CurationMode = curation.ModeAllowlist

		for p, want := range map[string]int{
			tag320: http.StatusOK, byDigest + dg: http.StatusOK, tag319: http.StatusForbidden, busybox: http.StatusForbidden,
		} {
			if w := f.do(http.MethodGet, p); w.Code != want {
				t.Errorf("GET %s = %d, want %d", p, w.Code, want)
			}
		}
	})

	t.Run("a cached manifest is refused once blocked", func(t *testing.T) {
		f.svc.WithCuration(&dlRules{rules: []curation.Rule{dlRule(curation.ActionBlock, "library/*", "3.20")}})
		f.repo.CurationMode = curation.ModeDenylist

		before := f.reg.count("")
		if w := f.do(http.MethodGet, tag320); w.Code != http.StatusForbidden {
			t.Errorf("cached blocked tag = %d, want 403", w.Code)
		}

		if got := f.reg.count(""); got != before {
			t.Errorf("refusal reached the registry: %d -> %d requests", before, got)
		}
	})

	t.Run("mode off serves", func(t *testing.T) {
		f.repo.CurationMode = curation.ModeOff

		if w := f.do(http.MethodGet, tag320); w.Code != http.StatusOK {
			t.Errorf("mode off = %d, want 200", w.Code)
		}
	})
}

func TestCurationDownload_ConanRecipe(t *testing.T) {
	f, _ := conanFixture(t)
	f.repo.CurationMode = curation.ModeDenylist

	f.svc.WithCuration(&dlRules{rules: []curation.Rule{dlRule(curation.ActionBlock, "zlib", "1.3.1")}})

	for _, p := range []string{conanBase + "/latest", conanRRoot + "/files", conanPRoot + "/files/conan_package.tgz"} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusForbidden {
			t.Errorf("GET %s = %d, want 403", p, w.Code)
		}
	}

	f.svc.WithCuration(&dlRules{rules: []curation.Rule{dlRule(curation.ActionBlock, "zlib", "1.2.*")}})

	if w := f.do(http.MethodGet, conanRRoot+"/files"); w.Code != http.StatusOK {
		t.Errorf("unblocked version = %d, want 200", w.Code)
	}
}

func TestDownloadCoordinates(t *testing.T) {
	type coord struct{ pkg, version string }

	cases := []struct {
		name string
		fn   func(string) (string, string, bool)
		key  string
		want *coord
	}{
		{"maven jar", mavenDownloadCoordinates, "org/acme/lib/1.0/lib-1.0.jar", &coord{"org.acme:lib", "1.0"}},
		{"maven module", mavenDownloadCoordinates, "org/acme/lib/1.0/lib-1.0.module", &coord{"org.acme:lib", "1.0"}},
		{"maven snapshot", mavenDownloadCoordinates, "g/a/1.0-SNAPSHOT/a-1.0-20240101.120000-3.module", &coord{"g:a", "1.0-SNAPSHOT"}},
		{"maven checksum", mavenDownloadCoordinates, "g/a/1.0/a-1.0.jar.sha1", nil},
		{"maven snapshot metadata", mavenDownloadCoordinates, "g/a/1.0-SNAPSHOT/maven-metadata.xml", nil},
		{"maven foreign file", mavenDownloadCoordinates, "g/a/1.0/b-1.0.jar", nil},
		{"maven dot segment", mavenDownloadCoordinates, "g/../1.0/a-1.0.module", nil},
		{"ivy ivy.xml", ivyDownloadCoordinates, "org/mod/1.0/ivy.xml", &coord{"org:mod", "1.0"}},
		{"ivy jar", ivyDownloadCoordinates, "org/mod/1.0/jars/mod.jar", &coord{"org:mod", "1.0"}},
		{"sbt maven layout", ivyDownloadCoordinates, "com/example/lib/1.0/lib-1.0.jar", &coord{"com.example:lib", "1.0"}},
		{"ivy metadata", ivyDownloadCoordinates, "com/example/lib/maven-metadata.xml", nil},
		{"ivy checksum", ivyDownloadCoordinates, "org/mod/1.0/ivy.xml.sha1", nil},
		{"deb", debCoordinates, "pool/main/h/hello/hello_1.0-1_amd64.deb", &coord{"hello", "1.0-1"}},
		{"deb flat", debCoordinates, "hello_1.0-1_amd64.deb", &coord{"hello", "1.0-1"}},
		{"deb source", debCoordinates, "pool/main/h/hello/hello_1.0.orig.tar.xz", nil},
		{"deb odd name", debCoordinates, "pool/main/h/hello/hello.deb", nil},
		{"rpm", rpmCoordinates, "Packages/h/hello-1.0-1.fc40.x86_64.rpm", &coord{"hello", "1.0-1.fc40"}},
		{"rpm dashed name", rpmCoordinates, "p/python3-dateutil-2.8.2-1.noarch.rpm", &coord{"python3-dateutil", "2.8.2-1"}},
		{"rpm delta", rpmCoordinates, "Packages/h/hello-1.0-1.fc40_2.0-1.fc40.x86_64.drpm", nil},
		{"rpm repodata", rpmCoordinates, "repodata/a-1-1.x.rpm", nil},
		{"apk", apkCoordinates, "v3.20/main/x86_64/hello-1.0-r0.apk", &coord{"hello", "1.0-r0"}},
		{"apk index", apkCoordinates, "v3.20/main/x86_64/APKINDEX.tar.gz", nil},
		{"pacman", pacmanCoordinates, "extra/os/x86_64/oa-hello-1:1.0-2-x86_64.pkg.tar.zst", &coord{"oa-hello", "1:1.0-2"}},
		{"pacman bz2", pacmanCoordinates, "x/a-1-1-any.pkg.tar.bz2", &coord{"a", "1-1"}},
		{"pacman signature", pacmanCoordinates, "x/a-1-1-any.pkg.tar.zst.sig", nil},
		{"pacman database", pacmanCoordinates, "extra/os/x86_64/extra.db.tar.gz", nil},
		{"conan files", conanCoordinates, conanRRoot + "/files", &coord{"zlib", "1.3.1"}},
		{"conan search", conanCoordinates, "v2/conans/search", nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pkg, version, ok := c.fn(c.key)

			switch {
			case c.want == nil && ok:
				t.Fatalf("got %q %q, want no coordinates", pkg, version)
			case c.want != nil && (!ok || pkg != c.want.pkg || version != c.want.version):
				t.Fatalf("got %q %q %v, want %q %q", pkg, version, ok, c.want.pkg, c.want.version)
			}
		})
	}
}
