// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cocoapods_test

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/cocoapods"
)

func TestParsePath(t *testing.T) {
	t.Parallel()

	ref := strings.Repeat("ab", 16)

	for _, c := range []struct {
		path string
		want cocoapods.Path
		ok   bool
	}{
		{"CocoaPods-version.yml", cocoapods.Path{Kind: cocoapods.KindVersionFile}, true},
		{"/deprecated_podspecs.txt", cocoapods.Path{Kind: cocoapods.KindDeprecated}, true},
		{"/all_pods.txt", cocoapods.Path{Kind: cocoapods.KindAllPods}, true},
		{"all_pods_versions_d_a_2.txt", cocoapods.Path{Kind: cocoapods.KindShard, Shard: "all_pods_versions_d_a_2.txt"}, true},
		{"all_pods_versions_0f_1.txt", cocoapods.Path{Kind: cocoapods.KindShard, Shard: "all_pods_versions_0f_1.txt"}, true},
		{"all_pods_versions_D_a_2.txt", cocoapods.Path{}, false},
		{"all_pods_versions_.txt", cocoapods.Path{}, false},
		{"all_pods_versions_a/../x.txt", cocoapods.Path{}, false},
		{
			"Specs/d/a/2/Alamofire/5.9.1/Alamofire.podspec.json",
			cocoapods.Path{Kind: cocoapods.KindPodspec, Dir: "d/a/2", Name: "Alamofire", Version: "5.9.1"}, true,
		},
		{
			"/Specs/d/a/2/C%2B%2B/1.0.0%2Bbuild/C++.podspec.json",
			cocoapods.Path{Kind: cocoapods.KindPodspec, Dir: "d/a/2", Name: "C++", Version: "1.0.0+build"}, true,
		},
		{"Specs/Alamofire/5.9.1/Alamofire.podspec.json", cocoapods.Path{Kind: cocoapods.KindPodspec, Name: "Alamofire", Version: "5.9.1"}, true},
		{"Specs/d/a/2/Alamofire/5.9.1/Other.podspec.json", cocoapods.Path{}, false},
		{"Specs/D/a/2/Alamofire/5.9.1/Alamofire.podspec.json", cocoapods.Path{}, false},
		{"Specs/d/a/2/../5.9.1/x.podspec.json", cocoapods.Path{}, false},
		{"Specs/d/a/2/Alamofire/5.9.1/Alamofire.podspec", cocoapods.Path{}, false},
		{"Specs/d/a/2/Alamofire/%zz/Alamofire.podspec.json", cocoapods.Path{}, false},
		{"Specs/Alamofire.podspec.json", cocoapods.Path{}, false},
		{
			"dl/d/a/2/Alamofire/5.9.1/" + ref + "/Alamofire-5.9.1.tar.gz",
			cocoapods.Path{Kind: cocoapods.KindDownload, Dir: "d/a/2", Name: "Alamofire", Version: "5.9.1", Ref: ref, File: "Alamofire-5.9.1.tar.gz"}, true,
		},
		{"dl/d/a/2/Alamofire/5.9.1/short/file.zip", cocoapods.Path{}, false},
		{"dl/d/a/2/Alamofire/5.9.1/" + ref + "/..", cocoapods.Path{}, false},
		{"dl/Alamofire/5.9.1/" + ref, cocoapods.Path{}, false},
		{"Specs", cocoapods.Path{}, false},
		{"elsewhere/file", cocoapods.Path{}, false},
	} {
		got, ok := cocoapods.ParsePath(c.path)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("ParsePath(%q) = %+v, %v; want %+v, %v", c.path, got, ok, c.want, c.ok)
		}
	}
}

func TestShardingMatchesTheTrunk(t *testing.T) {
	t.Parallel()

	// Real trunk paths: the MD5 of "Alamofire" starts da2…, of "AFNetworking" a75….
	if got := cocoapods.ShardDir("Alamofire", cocoapods.DefaultPrefixLengths); got != "d/a/2" {
		t.Errorf("ShardDir(Alamofire) = %q", got)
	}

	if got := cocoapods.ShardFile("Alamofire", cocoapods.DefaultPrefixLengths); got != "all_pods_versions_d_a_2.txt" {
		t.Errorf("ShardFile(Alamofire) = %q", got)
	}

	if got := cocoapods.ShardFile("Alamofire", []int{2, 1}); got != "all_pods_versions_da_2.txt" {
		t.Errorf("ShardFile with prefix lengths [2 1] = %q", got)
	}

	if got := cocoapods.ShardDir("Alamofire", nil); got != "" {
		t.Errorf("ShardDir without prefix lengths = %q", got)
	}

	if got := cocoapods.SpecPath("d/a/2", "Alamofire", "5.9.1"); got != "Specs/d/a/2/Alamofire/5.9.1/Alamofire.podspec.json" {
		t.Errorf("SpecPath = %q", got)
	}
}

func TestParseVersionDoc(t *testing.T) {
	t.Parallel()

	d, err := cocoapods.ParseVersionDoc([]byte("---\nmin: 1.0.0\nlast: 1.16.1\nprefix_lengths:\n- 1\n- 1\n- 1\n"))
	if err != nil || d.Min != "1.0.0" || d.Last != "1.16.1" || !slices.Equal(d.PrefixLengths, []int{1, 1, 1}) {
		t.Errorf("ParseVersionDoc = %+v, %v", d, err)
	}

	if d, err := cocoapods.ParseVersionDoc(
		[]byte("min: 1.0.0\n"),
	); err != nil ||
		!slices.Equal(d.PrefixLengths, cocoapods.DefaultPrefixLengths) {
		t.Errorf("document without prefix lengths = %+v, %v", d, err)
	}

	for _, bad := range []string{
		"", "<html>captive portal</html>", "- a\n- b\n", "min: 1\nprefix_lengths: [0]\n", "min: 1\nprefix_lengths: [20, 20]\n",
		"min: 1\nprefix_lengths: [1, 1, 1, 1, 1, 1, 1, 1, 1]\n", "min: 1\nprefix_lengths: x\n",
	} {
		if _, err := cocoapods.ParseVersionDoc([]byte(bad)); !errors.Is(err, cocoapods.ErrDocument) {
			t.Errorf("ParseVersionDoc(%q) = %v, want ErrDocument", bad, err)
		}
	}
}

const shard = "AAManager/0.0.1/0.0.2\nCKToolbox/0.0.9/0.1.0/0.2.0\r\n\nCocoaPath/0.1.0\n"

func TestShard(t *testing.T) {
	t.Parallel()

	pods, err := cocoapods.ParseShard([]byte(shard))
	if err != nil || len(pods) != 3 || pods[1].Name != "CKToolbox" || !slices.Equal(pods[1].Versions, []string{"0.0.9", "0.1.0", "0.2.0"}) {
		t.Fatalf("ParseShard = %+v, %v", pods, err)
	}

	if v, ok, err := cocoapods.VersionsOf([]byte(shard), "CocoaPath"); err != nil || !ok || !slices.Equal(v, []string{"0.1.0"}) {
		t.Errorf("VersionsOf(CocoaPath) = %v, %v, %v", v, ok, err)
	}

	if _, ok, err := cocoapods.VersionsOf([]byte(shard), "Missing"); err != nil || ok {
		t.Errorf("VersionsOf(Missing) = %v, %v", ok, err)
	}

	if _, err := cocoapods.ParseShard([]byte("ID.me WebVerify/2.0.0\n")); err != nil {
		t.Errorf("a pod name with a space was refused: %v", err)
	}

	for _, bad := range []string{"<html>", "Name//1.0\n", "Name/1.0/\n", "/1.0\n", "Na\tme/1.0\n", "Name/1.\x000\n", "Name/" + strings.Repeat("x", 300) + "\n"} {
		if err := cocoapods.VerifyShard([]byte(bad)); !errors.Is(err, cocoapods.ErrDocument) {
			t.Errorf("VerifyShard(%q) = %v, want ErrDocument", bad, err)
		}
	}
}

func TestFilterShardDropsVersionsAndEmptyPods(t *testing.T) {
	t.Parallel()

	got, err := cocoapods.FilterShard([]byte(shard), func(name, version string) bool {
		return (name != "CKToolbox" || version != "0.1.0") && name != "CocoaPath"
	})
	if err != nil {
		t.Fatal(err)
	}

	if want := "AAManager/0.0.1/0.0.2\nCKToolbox/0.0.9/0.2.0\n"; string(got) != want {
		t.Errorf("filtered shard = %q, want %q", got, want)
	}

	same, err := cocoapods.FilterShard([]byte(shard), nil)
	if err != nil || string(same) != shard {
		t.Errorf("FilterShard(nil) = %q, %v", same, err)
	}
}

func sum256(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func sum1(s string) string   { h := sha1.Sum([]byte(s)); return hex.EncodeToString(h[:]) }

func podspec(source string) []byte {
	return []byte("{\n  \"name\": \"Pod\",\n  \"version\": \"1.2.3\",\n  \"summary\": \"A \\\"pod\\\" <&>\",\n  \"source\": " + source +
		",\n  \"source_files\": \"Sources/**/*.{h,m}\"\n}\n")
}

func parse(t *testing.T, source string) *cocoapods.Podspec {
	t.Helper()

	p, err := cocoapods.ParsePodspec(podspec(source), "Pod", "1.2.3")
	if err != nil {
		t.Fatalf("ParsePodspec(%s): %v", source, err)
	}

	return p
}

func TestHTTPSourceIsRewrittenAndKeepsEverythingElse(t *testing.T) {
	t.Parallel()

	src := `{ "http": "https://dl.example/files/Pod-1.2.3.zip?sig=a&b=1", "sha256": "` + strings.ToUpper(
		sum256("bytes"),
	) + `", "flatten": true }`
	p := parse(t, src)

	a, ok := p.Archive()
	if !ok || a.URL != "https://dl.example/files/Pod-1.2.3.zip?sig=a&b=1" || a.File != "Pod-1.2.3.zip" || a.SHA256 != sum256("bytes") ||
		!a.Pinned {
		t.Fatalf("Archive = %+v, %v", a, ok)
	}

	out, rewritten := p.Rewrite("https://oa.example/repos/pods/dl/x/Pod/1.2.3/" + a.Ref() + "/Pod-1.2.3.zip")
	if !rewritten {
		t.Fatal("source not rewritten")
	}

	want := strings.Replace(string(podspec(src)), `"https://dl.example/files/Pod-1.2.3.zip?sig=a&b=1"`,
		`"https://oa.example/repos/pods/dl/x/Pod/1.2.3/`+a.Ref()+`/Pod-1.2.3.zip"`, 1)
	if string(out) != want {
		t.Errorf("rewritten podspec differs outside the source URL:\n%s\nwant\n%s", out, want)
	}

	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("rewritten podspec is not JSON: %v", err)
	}
}

func TestGitHubGitSourceBecomesATarball(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name, source, url, file string
		pinned                  bool
	}{
		{"tag", `{"git": "https://github.com/Alamofire/Alamofire.git", "tag": "5.9.1"}`,
			"https://codeload.github.com/Alamofire/Alamofire/tar.gz/refs/tags/5.9.1", "Alamofire-5.9.1.tar.gz", false},
		{"git scheme without .git", `{"git": "git://github.com/o/r", "tag": "v1.0"}`,
			"https://codeload.github.com/o/r/tar.gz/refs/tags/v1.0", "r-v1.0.tar.gz", false},
		{"tag with a slash", `{"git": "https://github.com/o/r.git/", "tag": "release/1.0"}`,
			"https://codeload.github.com/o/r/tar.gz/refs/tags/release/1.0", "r-release-1.0.tar.gz", false},
		{"commit", `{"git": "http://www.github.com/o/r.git", "commit": "ABCDEF0123456789abcdef0123456789abcdef01"}`,
			"https://codeload.github.com/o/r/tar.gz/abcdef0123456789abcdef0123456789abcdef01", "r-abcdef0123456789abcdef0123456789abcdef01.tar.gz", true},
	} {
		p := parse(t, c.source)

		a, ok := p.Archive()
		if !ok || a.URL != c.url || a.File != c.file || a.Pinned != c.pinned || a.SHA256 != "" {
			t.Errorf("%s: Archive = %+v, %v", c.name, a, ok)
			continue
		}

		out, rewritten := p.Rewrite("https://oa/x")
		if !rewritten || !bytes.Contains(out, []byte(`"source": {"http":"https://oa/x","type":"tgz"},`)) ||
			bytes.Contains(out, []byte("github.com")) {
			t.Errorf("%s: rewritten = %s", c.name, out)
		}
	}
}

func TestSourcesTheProxyLeavesAlone(t *testing.T) {
	t.Parallel()

	for _, src := range []string{
		`{"git": "https://github.com/o/r.git", "tag": "1.0", "submodules": true}`,
		`{"git": "https://github.com/o/r.git", "branch": "main"}`,
		`{"git": "https://github.com/o/r.git"}`,
		`{"git": "https://github.com/o/r.git", "tag": "1.0", "commit": "abcdef0"}`,
		`{"git": "https://github.com/o/r.git", "commit": "not-a-commit"}`,
		`{"git": "https://github.com/o/r.git", "tag": "../x"}`,
		`{"git": "https://gitlab.com/o/r.git", "tag": "1.0"}`,
		`{"git": "git@github.com:o/r.git", "tag": "1.0"}`,
		`{"git": "https://github.com/o/r/extra.git", "tag": "1.0"}`,
		`{"git": "https://user@github.com/o/r.git", "tag": "1.0"}`,
		`{"git": "https://github.com:8443/o/r.git", "tag": "1.0"}`,
		`{"git": 7, "tag": "1.0"}`,
		`{"svn": "https://svn.example/r", "revision": "3"}`,
		`{"hg": "https://hg.example/r"}`,
		`{"http": "https://dl.example/a.zip", "headers": ["Authorization: Bearer x"]}`,
		`{"http": "ftp://dl.example/a.zip"}`,
		`{"http": "/relative/a.zip"}`,
		`{"http": "https://dl.example/a.zip", "sha256": "xyz"}`,
		`{"http": "https://dl.example/a.zip", "sha1": 5}`,
		`{"http": "https://dl.example/a.zip", "git": "https://github.com/o/r.git", "tag": "1"}`,
		`"just a string"`,
		`{}`,
		`null`,
	} {
		p := parse(t, src)

		if a, ok := p.Archive(); ok {
			t.Errorf("source %s is served: %+v", src, a)
		}

		if out, rewritten := p.Rewrite("https://oa/x"); rewritten || !bytes.Equal(out, podspec(src)) {
			t.Errorf("source %s was changed: %s", src, out)
		}
	}

	// A podspec without any source passes untouched.
	p, err := cocoapods.ParsePodspec([]byte(`{"name":"Pod","version":"1.2.3"}`), "Pod", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := p.Archive(); ok {
		t.Error("a podspec without source has an archive")
	}
}

func TestArchiveNames(t *testing.T) {
	t.Parallel()

	for url, want := range map[string]string{
		"https://dl.example/dl/geosdk/abc/GoogleMaps-8.0.0.tar.gz": "GoogleMaps-8.0.0.tar.gz",
		"https://dl.example/download":                              "download",
		"https://dl.example/":                                      "source",
		"https://dl.example/a%20b.zip":                             "a_b.zip",
		"https://dl.example/a%2Fb.zip":                             "b.zip",
		"https://dl.example/" + strings.Repeat("x", 300) + ".zip":  strings.Repeat("x", 116) + ".zip",
	} {
		a, ok := parse(t, `{"http": "`+url+`"}`).Archive()
		if !ok || a.File != want {
			t.Errorf("file name of %s = %q (%v), want %q", url, a.File, ok, want)
		}
	}
}

func TestRefFollowsTheSource(t *testing.T) {
	t.Parallel()

	a := cocoapods.Archive{URL: "https://a/x.zip", File: "x.zip"}

	if !cocoapods.ValidRef(a.Ref()) {
		t.Fatalf("Ref() = %q is not a valid reference", a.Ref())
	}

	for _, b := range []cocoapods.Archive{
		{URL: "https://b/x.zip", File: "x.zip"}, {URL: "https://a/x.zip", File: "y.zip"},
		{URL: "https://a/x.zip", File: "x.zip", SHA256: sum256("a")}, {URL: "https://a/x.zip", File: "x.zip", SHA1: sum1("a")},
	} {
		if b.Ref() == a.Ref() {
			t.Errorf("%+v shares the reference of %+v", b, a)
		}
	}
}

func TestVerify(t *testing.T) {
	t.Parallel()

	a := cocoapods.Archive{SHA256: sum256("bytes"), SHA1: sum1("bytes")}

	if err := a.Verify(strings.NewReader("bytes")); err != nil {
		t.Errorf("matching archive: %v", err)
	}

	for _, bad := range []cocoapods.Archive{{SHA256: sum256("other")}, {SHA1: sum1("other")}, {SHA256: sum256("bytes"), SHA1: sum1("other")}} {
		if err := bad.Verify(strings.NewReader("bytes")); !errors.Is(err, cocoapods.ErrChecksum) {
			t.Errorf("Verify(%+v) = %v, want ErrChecksum", bad, err)
		}
	}

	if err := (cocoapods.Archive{}).Verify(strings.NewReader("anything")); err != nil {
		t.Errorf("archive without checksum: %v", err)
	}
}

func TestParsePodspecRefusesWhatIsNotThePod(t *testing.T) {
	t.Parallel()

	for _, doc := range []string{
		``, `[]`, `null`, `{`, `{"name":"Pod","version":"1.2.3"} trailing`, `{"name":"Pod"}`, `{"version":"1.2.3"}`,
		`{"name":"Other","version":"1.2.3"}`, `{"name":"Pod","version":"9.9.9"}`, `{"name":"Pod","version":1}`,
		`<html>captive portal</html>`,
	} {
		if _, err := cocoapods.ParsePodspec([]byte(doc), "Pod", "1.2.3"); !errors.Is(err, cocoapods.ErrDocument) {
			t.Errorf("ParsePodspec(%q) = %v, want ErrDocument", doc, err)
		}
	}
}

func TestDuplicateSourceKeepsTheLastLikeRuby(t *testing.T) {
	t.Parallel()

	doc := []byte(`{"name":"Pod","version":"1.2.3","source":{"http":"https://a/one.zip"},"source":{"http":"https://a/two.zip"}}`)

	p, err := cocoapods.ParsePodspec(doc, "Pod", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}

	if a, ok := p.Archive(); !ok || a.URL != "https://a/two.zip" {
		t.Errorf("Archive = %+v, %v", a, ok)
	}
}

func TestArchiveKey(t *testing.T) {
	t.Parallel()

	ref := strings.Repeat("0", 32)
	key := cocoapods.ArchiveKey("Pod", "1.0", ref, "Pod.zip")

	if name, version, ok := cocoapods.ParseArchiveKey(key); !ok || name != "Pod" || version != "1.0" {
		t.Errorf("ParseArchiveKey(%q) = %q %q %v", key, name, version, ok)
	}

	if !strings.HasPrefix(key, cocoapods.ArchiveKeyPrefix("Pod")) {
		t.Errorf("key %q lacks the prefix %q", key, cocoapods.ArchiveKeyPrefix("Pod"))
	}

	for _, bad := range []string{"", "dl/Pod/1.0", "dl/Pod/1.0/" + ref, "dl/Pod/1.0/short/f.zip", "p2/Pod/1.0/" + ref + "/f", "dl/Pod/1.0/" + ref + "/a/b"} {
		if _, _, ok := cocoapods.ParseArchiveKey(bad); ok {
			t.Errorf("ParseArchiveKey(%q) accepted", bad)
		}
	}

	if got := cocoapods.DownloadPath("d/a/2", "C++", "1.0+b", ref, "a b.zip"); got != "dl/d/a/2/C++/1.0+b/"+ref+"/a%20b.zip" {
		t.Errorf("DownloadPath = %q", got)
	}
}

func TestVerifyPodLists(t *testing.T) {
	t.Parallel()

	if err := cocoapods.VerifyAllPods([]byte("!ProtoCompiler\nAlamofire\n\nC++Pod\r\nID.me WebVerify\n")); err != nil {
		t.Errorf("a pod list was refused: %v", err)
	}

	for _, bad := range []string{"<html>captive portal</html>\n", "a/b\n", "..\n", "ok\nwith\ttab\n"} {
		if err := cocoapods.VerifyAllPods([]byte(bad)); !errors.Is(err, cocoapods.ErrDocument) {
			t.Errorf("VerifyAllPods(%q) = %v, want ErrDocument", bad, err)
		}
	}

	if err := cocoapods.VerifyDeprecated([]byte("Specs/0/0/4/A/0.1.0/A.podspec.json\n")); err != nil {
		t.Errorf("a deprecated list was refused: %v", err)
	}

	if err := cocoapods.VerifyDeprecated(
		[]byte("Specs/0/0/4/A/0.1.0/A.podspec.json\nnot a path\n"),
	); !errors.Is(
		err,
		cocoapods.ErrDocument,
	) {
		t.Errorf("a bad deprecated list = %v, want ErrDocument", err)
	}
}
