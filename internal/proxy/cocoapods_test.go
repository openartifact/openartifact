// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/curation"
	pkgcocoapods "github.com/openartifact/openartifact/pkg/cocoapods"
)

const cocoapodsBase = "https://oa.example"

var cocoapodsLengths = pkgcocoapods.DefaultPrefixLengths //nolint:gochecknoglobals // read-only fixture

// fakeCDN emulates a CocoaPods CDN spec repository; the source archives live on
// another host, as they do on the real one.
type fakeCDN struct {
	archives    *httptest.Server
	archiveAuth atomic.Value
	archiveHits atomic.Int32
	files       map[string]string // CDN path -> bytes
	archiveBody map[string]string // archive host path -> bytes
}

func cocoapodsPodspec(name, version, source string) string {
	return "{\n  \"name\": \"" + name + "\",\n  \"version\": \"" + version + "\",\n  \"summary\": \"<&> pod\",\n  \"source\": " + source + "\n}\n"
}

func cocoapodsSpecFile(name, version string) string {
	return pkgcocoapods.SpecPath(pkgcocoapods.ShardDir(name, cocoapodsLengths), name, version)
}

func newFakeCDN(t *testing.T) *fakeCDN {
	t.Helper()

	c := &fakeCDN{
		files: map[string]string{pkgcocoapods.VersionFile: "---\nmin: 1.0.0\nlast: 1.16.1\nprefix_lengths:\n- 1\n- 1\n- 1\n"},
		archiveBody: map[string]string{
			"/files/Alamofire-5.9.1.zip": "alamofire 5.9.1 bytes",
			"/files/Alamofire-5.9.0.zip": "alamofire 5.9.0 bytes",
		},
	}

	c.archives = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.archiveAuth.Store(r.Header.Get("Authorization"))
		c.archiveHits.Add(1)

		body, ok := c.archiveBody[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(c.archives.Close)

	hashed := `{"http": "` + c.archives.URL + `/files/Alamofire-5.9.1.zip", "sha256": "` + sum("alamofire 5.9.1 bytes") + `"}`
	unhashed := `{"http": "` + c.archives.URL + `/files/Alamofire-5.9.0.zip"}`
	git := `{"git": "https://github.com/SnapKit/SnapKit.git", "tag": "5.7.1"}`
	svn := `{"svn": "https://svn.example/legacy", "revision": "3"}`

	pods := []struct{ name, version, source string }{
		{"Alamofire", "5.9.0", unhashed}, {"Alamofire", "5.9.1", hashed}, {"SnapKit", "5.7.1", git}, {"Legacy", "1.0", svn},
	}

	shards := map[string][]string{}

	for _, p := range pods {
		c.files[cocoapodsSpecFile(p.name, p.version)] = cocoapodsPodspec(p.name, p.version, p.source)

		file := pkgcocoapods.ShardFile(p.name, cocoapodsLengths)
		line := p.name + "/" + p.version

		if i := slices.IndexFunc(shards[file], func(l string) bool { return strings.HasPrefix(l, p.name+"/") }); i >= 0 {
			shards[file][i] += "/" + p.version
		} else {
			shards[file] = append(shards[file], line)
		}
	}

	for file, lines := range shards {
		c.files[file] = strings.Join(lines, "\n") + "\n"
	}

	c.files[pkgcocoapods.DeprecatedFile] = "Specs/0/0/4/AFCategory/0.1.0/AFCategory.podspec.json\n"
	c.files[pkgcocoapods.AllPodsFile] = "!ProtoCompiler\nAlamofire\nLegacy\nSnapKit\n"

	return c
}

func (c *fakeCDN) handler(w http.ResponseWriter, r *http.Request) {
	body, ok := c.files[strings.TrimPrefix(r.URL.Path, "/")]
	if !ok {
		http.NotFound(w, r)
		return
	}

	_, _ = w.Write([]byte(body))
}

func cocoapodsFixture(t *testing.T, c *fakeCDN) *fixture {
	t.Helper()

	f := newFixture(t, c.handler)
	f.repo.Format = "cocoapods"
	f.repo.Name = "trunk"
	f.repo.UpstreamURL = f.upstream.URL + "/"
	f.svc.baseURL = cocoapodsBase

	return f
}

func cocoapodsSpecPath(name, version string) string { return cocoapodsSpecFile(name, version) }

// cocoapodsArchivePath is the repository-relative path the served podspec of
// name version sends the client to for the archive.
func cocoapodsArchivePath(t *testing.T, f *fixture, name, version string) string {
	t.Helper()

	w := f.do(http.MethodGet, cocoapodsSpecPath(name, version))
	if w.Code != http.StatusOK {
		t.Fatalf("podspec %s %s = %d %s", name, version, w.Code, w.Body.String())
	}

	var doc struct {
		Source struct {
			HTTP string `json:"http"`
		} `json:"source"`
	}

	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("served podspec is not JSON: %v", err)
	}

	rest, ok := strings.CutPrefix(doc.Source.HTTP, cocoapodsBase+"/repos/trunk/")
	if !ok {
		t.Fatalf("source %q does not point at the repository", doc.Source.HTTP)
	}

	return rest
}

func TestCocoaPodsProxy_FixedDocumentsAreCachedVerbatim(t *testing.T) {
	f := cocoapodsFixture(t, newFakeCDN(t))

	for path, ctype := range map[string]string{
		pkgcocoapods.VersionFile: "text/yaml", pkgcocoapods.DeprecatedFile: "text/plain", pkgcocoapods.AllPodsFile: "text/plain",
		pkgcocoapods.ShardFile("Alamofire", cocoapodsLengths): "text/plain",
	} {
		w := f.do(http.MethodGet, path)
		if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), ctype) {
			t.Fatalf("%s = %d %q", path, w.Code, w.Header().Get("Content-Type"))
		}

		if f.db.entry("repo-1", path) == nil {
			t.Errorf("%s is not cached under its own path", path)
		}
	}

	hits := f.hits.Load()

	if w := f.do(http.MethodGet, pkgcocoapods.ShardFile("Alamofire", cocoapodsLengths)); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "Alamofire/5.9.0/5.9.1\n") {
		t.Errorf("shard = %d %q", w.Code, w.Body.String())
	}

	if f.hits.Load() != hits {
		t.Error("a cached shard reached the upstream again")
	}
}

func TestCocoaPodsProxy_ServesStaleWhenTheUpstreamIsDown(t *testing.T) {
	f := cocoapodsFixture(t, newFakeCDN(t))
	shard := pkgcocoapods.ShardFile("Alamofire", cocoapodsLengths)

	f.do(http.MethodGet, shard)
	f.db.expire("repo-1", shard)
	f.db.expire("repo-1", pkgcocoapods.VersionFile)
	f.repo.UpstreamURL = "http://127.0.0.1:1/"

	w := f.do(http.MethodGet, shard)
	if w.Code != http.StatusOK || w.Header().Get("Warning") != staleValue {
		t.Errorf("stale shard = %d warning %q", w.Code, w.Header().Get("Warning"))
	}
}

func TestCocoaPodsProxy_RefusesWhatIsNotTheDocument(t *testing.T) {
	c := newFakeCDN(t)
	shard := pkgcocoapods.ShardFile("Alamofire", cocoapodsLengths)
	other := pkgcocoapods.ShardFile("SnapKit", cocoapodsLengths)

	c.files[shard] = "<html>captive portal</html>\n"
	// a shard of another bucket served under this name
	c.files[other] = c.files[pkgcocoapods.ShardFile("Legacy", cocoapodsLengths)]
	c.files[pkgcocoapods.DeprecatedFile] = "<html>captive portal</html>"
	c.files[pkgcocoapods.AllPodsFile] = "Alamofire\n<html>captive portal</html>\n"

	f := cocoapodsFixture(t, c)

	for _, path := range []string{shard, pkgcocoapods.DeprecatedFile, pkgcocoapods.AllPodsFile} {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusBadGateway {
			t.Errorf("%s = %d, want 502", path, w.Code)
		}

		if f.db.entry("repo-1", path) != nil {
			t.Errorf("an unusable %s was cached", path)
		}
	}

	if w := f.do(http.MethodGet, other); w.Code != http.StatusBadGateway {
		t.Errorf("a shard of another bucket = %d, want 502", w.Code)
	}

	c.files[pkgcocoapods.VersionFile] = "<html>captive portal</html>"

	g := cocoapodsFixture(t, c)
	if w := g.do(http.MethodGet, pkgcocoapods.VersionFile); w.Code != http.StatusBadGateway {
		t.Errorf("version file = %d, want 502", w.Code)
	}
}

func TestCocoaPodsProxy_PathsOfAnotherShardingAreNotAskedUpstream(t *testing.T) {
	f := cocoapodsFixture(t, newFakeCDN(t))
	f.do(http.MethodGet, pkgcocoapods.VersionFile)

	hits := f.hits.Load()

	for _, path := range []string{
		"all_pods_versions_da_2.txt",
		"all_pods_versions_d_a_2_0.txt",
		"Specs/d/a/Alamofire/5.9.1/Alamofire.podspec.json",
		"Specs/0/0/0/Alamofire/5.9.1/Alamofire.podspec.json", // not the directory of the name
		"dl/0/0/0/Alamofire/5.9.1/" + strings.Repeat("a", 32) + "/f.zip",
		"Specs/d/a/2/Alamofire/5.9.1/Other.podspec.json",
		"Specs/d/a/2/../../x",
		"index.html",
		"all_pods_versions_%2e%2e.txt",
	} {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, w.Code)
		}
	}

	if f.hits.Load() != hits {
		t.Errorf("%d requests reached the upstream", f.hits.Load()-hits)
	}
}

func TestCocoaPodsProxy_PodspecIsCachedVerbatimAndServedWithItsSourceOnTheProxy(t *testing.T) {
	c := newFakeCDN(t)
	f := cocoapodsFixture(t, c)
	path := cocoapodsSpecPath("Alamofire", "5.9.1")

	w := f.do(http.MethodGet, path)
	if w.Code != http.StatusOK {
		t.Fatalf("podspec = %d %s", w.Code, w.Body.String())
	}

	upstream := c.files[path]
	a, _ := parseTestArchive(t, upstream, "Alamofire", "5.9.1")
	want := strings.Replace(upstream, `"`+a.URL+`"`,
		`"`+cocoapodsBase+`/repos/trunk/`+pkgcocoapods.DownloadPath("d/a/2", "Alamofire", "5.9.1", a.Ref(), a.File)+`"`, 1)

	if w.Body.String() != want || strings.Contains(w.Body.String(), c.archives.URL) {
		t.Errorf("served podspec:\n%s\nwant\n%s", w.Body.String(), want)
	}

	if e := f.db.entry("repo-1", path); e == nil || e.BlobKey != sum(upstream) {
		t.Fatalf("podspec is not cached verbatim under its upstream path: %+v", e)
	}

	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	hits := f.hits.Load()

	if w2 := f.doWith(path, map[string]string{"If-None-Match": etag}); w2.Code != http.StatusNotModified || f.hits.Load() != hits {
		t.Errorf("revalidation = %d with %d new upstream requests", w2.Code, f.hits.Load()-hits)
	}

	f.svc.baseURL = "https://other.example"

	if w3 := f.do(http.MethodGet, path); w3.Header().Get("ETag") == etag || !strings.Contains(w3.Body.String(), "other.example") {
		t.Errorf("rendering did not follow the server URL: %s", w3.Body.String())
	}
}

func parseTestArchive(t *testing.T, doc, name, version string) (pkgcocoapods.Archive, bool) {
	t.Helper()

	p, err := pkgcocoapods.ParsePodspec([]byte(doc), name, version)
	if err != nil {
		t.Fatal(err)
	}

	return p.Archive()
}

func TestCocoaPodsProxy_PodspecThatIsNotThePodIsRefusedAndNotCached(t *testing.T) {
	c := newFakeCDN(t)
	wrong := cocoapodsSpecPath("Alamofire", "5.9.0")
	c.files[wrong] = cocoapodsPodspec("Alamofire", "9.9.9", `{"svn":"x"}`)
	garbage := cocoapodsSpecPath("Legacy", "1.0")
	c.files[garbage] = "<html>captive portal</html>"

	f := cocoapodsFixture(t, c)

	for _, path := range []string{wrong, garbage} {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusBadGateway {
			t.Errorf("%s = %d, want 502", path, w.Code)
		}

		if f.db.entry("repo-1", path) != nil {
			t.Errorf("an unusable %s was cached", path)
		}
	}

	if w := f.do(http.MethodGet, cocoapodsSpecPath("Alamofire", "1.2.3")); w.Code != http.StatusNotFound {
		t.Errorf("podspec the upstream does not have = %d, want 404", w.Code)
	}
}

func TestCocoaPodsProxy_ArchiveIsVerifiedCachedOnceAndServedWithTheUpstreamDown(t *testing.T) {
	c := newFakeCDN(t)
	f := cocoapodsFixture(t, c)
	path := cocoapodsArchivePath(t, f, "Alamofire", "5.9.1")

	if !strings.HasPrefix(path, "dl/d/a/2/Alamofire/5.9.1/") || !strings.HasSuffix(path, "/Alamofire-5.9.1.zip") {
		t.Fatalf("archive path = %q", path)
	}

	if w := f.do(http.MethodGet, path); w.Code != http.StatusOK || w.Body.String() != "alamofire 5.9.1 bytes" {
		t.Fatalf("archive = %d %q", w.Code, w.Body.String())
	}

	parts := strings.Split(path, "/")
	key := pkgcocoapods.ArchiveKey("Alamofire", "5.9.1", parts[len(parts)-2], parts[len(parts)-1])

	e := f.db.entry("repo-1", key)
	if e == nil || e.BlobKey != sum("alamofire 5.9.1 bytes") {
		t.Fatalf("archive not cached content-addressed under %s: %+v", key, e)
	}

	if time.Until(e.ExpiresAt) < 50*365*24*time.Hour {
		t.Errorf("an archive its checksum vouches for expires in %v, want effectively never", time.Until(e.ExpiresAt))
	}

	hits := c.archiveHits.Load()
	c.archives.Close()
	f.repo.UpstreamURL = "http://127.0.0.1:1/"

	if w := f.do(
		http.MethodGet,
		path,
	); w.Code != http.StatusOK || w.Body.String() != "alamofire 5.9.1 bytes" ||
		c.archiveHits.Load() != hits {
		t.Errorf("cached archive = %d %q", w.Code, w.Body.String())
	}
}

func TestCocoaPodsProxy_ArchiveWithoutChecksumIsCachedForTheTTLOnly(t *testing.T) {
	c := newFakeCDN(t)
	f := cocoapodsFixture(t, c)
	path := cocoapodsArchivePath(t, f, "Alamofire", "5.9.0")

	if w := f.do(http.MethodGet, path); w.Code != http.StatusOK || w.Body.String() != "alamofire 5.9.0 bytes" {
		t.Fatalf("archive = %d %q", w.Code, w.Body.String())
	}

	parts := strings.Split(path, "/")

	e := f.db.entry("repo-1", pkgcocoapods.ArchiveKey("Alamofire", "5.9.0", parts[len(parts)-2], parts[len(parts)-1]))
	if e == nil || time.Until(e.ExpiresAt) > time.Duration(f.repo.CacheTTLSeconds)*time.Second {
		t.Errorf("archive without a checksum is cached as %+v, want the repository TTL", e)
	}
}

func TestCocoaPodsProxy_ArchiveWithWrongChecksumIsRefusedAndNotCached(t *testing.T) {
	c := newFakeCDN(t)
	c.archiveBody["/files/Alamofire-5.9.1.zip"] = "tampered"
	f := cocoapodsFixture(t, c)
	path := cocoapodsArchivePath(t, f, "Alamofire", "5.9.1")

	for range 2 {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusBadGateway {
			t.Fatalf("mismatching archive = %d %q, want 502", w.Code, w.Body.String())
		}
	}

	parts := strings.Split(path, "/")
	if f.db.entry("repo-1", pkgcocoapods.ArchiveKey("Alamofire", "5.9.1", parts[len(parts)-2], parts[len(parts)-1])) != nil {
		t.Error("an archive that does not match its checksum was cached")
	}
}

func TestCocoaPodsProxy_ArchiveHostGetsNoCredentials(t *testing.T) {
	c := newFakeCDN(t)
	f := cocoapodsFixture(t, c)
	f.repo.UpstreamUsername = "robot"

	if w := f.do(http.MethodGet, cocoapodsArchivePath(t, f, "Alamofire", "5.9.1")); w.Code != http.StatusOK {
		t.Fatalf("archive = %d", w.Code)
	}

	if auth, _ := c.archiveAuth.Load().(string); auth != "" || c.archiveHits.Load() == 0 {
		t.Errorf("archive host received credentials %q after %d requests", auth, c.archiveHits.Load())
	}
}

func TestCocoaPodsProxy_ForgedOrUnpublishedArchivesAreNotFetched(t *testing.T) {
	c := newFakeCDN(t)
	f := cocoapodsFixture(t, c)
	good := cocoapodsArchivePath(t, f, "Alamofire", "5.9.1")
	parts := strings.Split(good, "/")

	forgedRef := strings.Join(append(append([]string{}, parts[:len(parts)-2]...), strings.Repeat("a", 32), parts[len(parts)-1]), "/")
	forgedFile := strings.Join(append(append([]string{}, parts[:len(parts)-1]...), "Evil.zip"), "/")
	otherVersion := strings.Replace(good, "/5.9.1/", "/5.9.0/", 1)
	unknown := strings.Replace(good, "/Alamofire/", "/Nothing/", 1)

	before := c.archiveHits.Load()

	for _, path := range []string{forgedRef, forgedFile, otherVersion, unknown} {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, w.Code)
		}
	}

	if c.archiveHits.Load() != before {
		t.Error("a forged archive path reached the archive host")
	}
}

func TestCocoaPodsProxy_GitHubTagSourceIsServedAsACodeloadTarball(t *testing.T) {
	c := newFakeCDN(t)
	f := cocoapodsFixture(t, c)
	path := cocoapodsSpecPath("SnapKit", "5.7.1")

	w := f.do(http.MethodGet, path)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "github.com") ||
		!strings.Contains(w.Body.String(), `"source": {"http":"`+cocoapodsBase+`/repos/trunk/dl/`) ||
		!strings.Contains(w.Body.String(), `/SnapKit-5.7.1.tar.gz","type":"tgz"}`) {
		t.Fatalf("podspec = %d %s", w.Code, w.Body.String())
	}

	// The download is located from the cached podspec; it is not fetched here (the host is GitHub's).
	p, _ := pkgcocoapods.ParsePath(cocoapodsArchivePathOf(t, w.Body.String()))

	d, err := f.svc.locateCocoaPodsArchive(context.Background(), f.repo, p)
	if err != nil || d.url != "https://codeload.github.com/SnapKit/SnapKit/tar.gz/refs/tags/5.7.1" || !d.opts.unverified {
		t.Errorf("locate = %+v, %v", d, err)
	}
}

func cocoapodsArchivePathOf(t *testing.T, podspec string) string {
	t.Helper()

	var doc struct {
		Source struct {
			HTTP string `json:"http"`
		} `json:"source"`
	}

	if err := json.Unmarshal([]byte(podspec), &doc); err != nil {
		t.Fatal(err)
	}

	return strings.TrimPrefix(doc.Source.HTTP, cocoapodsBase+"/repos/trunk/")
}

func TestCocoaPodsProxy_SourcesItDoesNotServeAreLeftToTheClient(t *testing.T) {
	c := newFakeCDN(t)
	f := cocoapodsFixture(t, c)
	path := cocoapodsSpecPath("Legacy", "1.0")

	if w := f.do(http.MethodGet, path); w.Code != http.StatusOK || w.Body.String() != c.files[path] {
		t.Errorf("podspec = %d %s, want it as published", w.Code, w.Body.String())
	}

	ref := strings.Repeat("a", 32)

	if w := f.do(
		http.MethodGet,
		"dl/"+pkgcocoapods.ShardDir("Legacy", cocoapodsLengths)+"/Legacy/1.0/"+ref+"/Legacy.zip",
	); w.Code != http.StatusNotFound {
		t.Errorf("archive of an svn source = %d, want 404", w.Code)
	}
}

func cocoapodsServedShard(t *testing.T, f *fixture, name string) []string {
	t.Helper()

	w := f.do(http.MethodGet, pkgcocoapods.ShardFile(name, cocoapodsLengths))
	if w.Code != http.StatusOK {
		t.Fatalf("shard = %d %s", w.Code, w.Body.String())
	}

	pods, err := pkgcocoapods.ParseShard(w.Body.Bytes())
	if err != nil {
		t.Fatalf("served shard: %v\n%s", err, w.Body.String())
	}

	var out []string

	for _, p := range pods {
		for _, v := range p.Versions {
			out = append(out, p.Name+"/"+v)
		}
	}

	return out
}

func TestCocoaPodsCuration_ShardsAndDownloadsFollowTheRules(t *testing.T) {
	tests := []struct {
		name  string
		mode  string
		rules []curation.Rule
		want  []string
		specs map[string]int
	}{
		{
			name:  "denylist drops the version",
			mode:  curation.ModeDenylist,
			rules: []curation.Rule{blockRule("Alamofire", "5.9.1")},
			want:  []string{"Alamofire/5.9.0"},
			specs: map[string]int{"5.9.1": http.StatusForbidden, "5.9.0": http.StatusOK},
		},
		{
			name:  "version glob",
			mode:  curation.ModeDenylist,
			rules: []curation.Rule{blockRule("Alamofire", "5.9.*")},
			want:  nil,
			specs: map[string]int{"5.9.1": http.StatusForbidden, "5.9.0": http.StatusForbidden},
		},
		{
			name:  "allowlist of one version",
			mode:  curation.ModeAllowlist,
			rules: []curation.Rule{allowRule("Alamofire", "5.9.0")},
			want:  []string{"Alamofire/5.9.0"},
			specs: map[string]int{"5.9.1": http.StatusForbidden, "5.9.0": http.StatusOK},
		},
		{
			name:  "mode off leaves the rules inert",
			mode:  curation.ModeOff,
			rules: []curation.Rule{blockRule("Alamofire", "")},
			want:  []string{"Alamofire/5.9.0", "Alamofire/5.9.1"},
			specs: map[string]int{"5.9.1": http.StatusOK},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newFakeCDN(t)
			f := cocoapodsFixture(t, c)
			f.useCuration(tt.mode, tt.rules...)

			var got []string

			for _, l := range cocoapodsServedShard(t, f, "Alamofire") {
				if strings.HasPrefix(l, "Alamofire/") {
					got = append(got, l)
				}
			}

			if !slices.Equal(got, tt.want) {
				t.Errorf("shard lists %v, want %v", got, tt.want)
			}

			for version, want := range tt.specs {
				if w := f.do(http.MethodGet, cocoapodsSpecPath("Alamofire", version)); w.Code != want {
					t.Errorf("podspec %s = %d, want %d", version, w.Code, want)
				}
			}
		})
	}
}

func TestCocoaPodsCuration_RefusedArchiveNamesTheReasonAndIsNeverFetched(t *testing.T) {
	c := newFakeCDN(t)
	f := cocoapodsFixture(t, c)
	path := cocoapodsArchivePath(t, f, "Alamofire", "5.9.1")

	f.useCuration(
		curation.ModeDenylist,
		curation.Rule{Action: curation.ActionBlock, Package: "Alamofire", Version: "5.9.1", Reason: "CVE-2099-7"},
	)

	w := f.do(http.MethodGet, path)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "CVE-2099-7") {
		t.Errorf("archive = %d %q, want 403 naming the reason", w.Code, w.Body.String())
	}

	if c.archiveHits.Load() != 0 {
		t.Error("a curated archive was fetched")
	}

	if !CuratesFromPath("cocoapods") {
		t.Error("CuratesFromPath(cocoapods) = false")
	}
}

func TestCocoaPodsCuration_DropsAPodWithNoVersionLeftAndFollowsTheRules(t *testing.T) {
	c := newFakeCDN(t)
	f := cocoapodsFixture(t, c)
	shard := pkgcocoapods.ShardFile("Alamofire", cocoapodsLengths)

	plain := f.do(http.MethodGet, shard).Header().Get("ETag")
	src := f.useCuration(curation.ModeDenylist, blockRule("Alamofire", ""))

	w := f.do(http.MethodGet, shard)
	if strings.Contains(w.Body.String(), "Alamofire") {
		t.Errorf("a pod with no version left is still listed: %q", w.Body.String())
	}

	first := w.Header().Get("ETag")

	src.set(blockRule("Alamofire", "5.9.0"))

	second := f.do(http.MethodGet, shard).Header().Get("ETag")
	if first == second || first == plain || second == plain {
		t.Errorf("validators %q %q %q; want three different ones", plain, first, second)
	}

	if w := f.doWith(shard, map[string]string{"If-None-Match": second}); w.Code != http.StatusNotModified {
		t.Errorf("revalidating with the current validator = %d, want 304", w.Code)
	}
}

func TestCocoaPodsVersionsAndCachedVersionsComeFromTheCache(t *testing.T) {
	c := newFakeCDN(t)
	f := cocoapodsFixture(t, c)
	ctx := context.Background()

	if _, ok := f.svc.CachedVersions(ctx, f.repo, "Alamofire"); ok {
		t.Fatal("versions listed before anything is cached")
	}

	f.do(http.MethodGet, pkgcocoapods.ShardFile("Alamofire", cocoapodsLengths))

	versions, ok := f.svc.CachedVersions(ctx, f.repo, "Alamofire")
	if !ok || len(versions) != 2 || versions[0].Version != "5.9.0" || versions[1].Version != "5.9.1" || !versions[0].Published.IsZero() {
		t.Errorf("versions = %+v, %v", versions, ok)
	}

	if _, ok := f.svc.CachedVersions(ctx, f.repo, "Nothing"); ok {
		t.Error("a pod the shard does not list has versions")
	}

	if name, version, ok := PackageCoordinates(
		"cocoapods",
		"dl/Alamofire/5.9.1/"+strings.Repeat("a", 32)+"/Alamofire-5.9.1.zip",
	); !ok || name != "Alamofire" ||
		version != "5.9.1" {
		t.Errorf("PackageCoordinates = %q %q %v", name, version, ok)
	}

	if _, _, ok := PackageCoordinates("cocoapods", "Specs/d/a/2/Alamofire/5.9.1/Alamofire.podspec.json"); ok {
		t.Error("a podspec is a package")
	}
}
