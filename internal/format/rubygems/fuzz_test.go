// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rubygems

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/pkg/logger"
	"github.com/openartifact/openartifact/pkg/rubymarshal"
	"go.uber.org/zap"
)

// fuzzTarEntry is one member of a hand-built .gem tar.
type fuzzTarEntry struct {
	name string
	data []byte
}

func fuzzGzip(raw []byte) []byte {
	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(raw)
	_ = zw.Close()

	return buf.Bytes()
}

func fuzzTar(entries ...fuzzTarEntry) []byte {
	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)

	for _, e := range entries {
		_ = tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.data))})
		_, _ = tw.Write(e.data)
	}

	_ = tw.Close()

	return buf.Bytes()
}

// fuzzGem wraps raw metadata the way `gem build` lays out a .gem: a data
// member the parser has to skip, then the gzipped metadata.
func fuzzGem(raw []byte) []byte {
	return fuzzTar(
		fuzzTarEntry{"data.tar.gz", fuzzGzip([]byte("payload"))},
		fuzzTarEntry{"metadata.gz", fuzzGzip(raw)},
	)
}

const fuzzSampleYAML = `--- !ruby/object:Gem::Specification
name: nokogiri
version: !ruby/object:Gem::Version
  version: 1.16.2
platform: x86_64-linux
authors:
- Mike Dalessio
summary: HTML parser
licenses:
- MIT
dependencies:
- !ruby/object:Gem::Dependency
  name: racc
  requirement: !ruby/object:Gem::Requirement
    requirements:
    - - "~>"
      - !ruby/object:Gem::Version
        version: '1.4'
  type: :runtime
  prerelease: false
`

const fuzzSampleJSON = `{"name":"demo","version":"1.0.0","platform":"ruby","summary":"s","authors":["a"],"licenses":["MIT"],` +
	`"dependencies":[{"name":"rack","requirement":"~> 2.0, >= 2.0.1","type":"runtime"}]}`

// fuzzAliasBomb is a YAML document of nine nested levels of nine aliases each.
func fuzzAliasBomb() []byte {
	var b strings.Builder

	b.WriteString("name: bomb\nversion: '1'\nx0: &x0 [\"lol\",\"lol\",\"lol\",\"lol\",\"lol\",\"lol\",\"lol\",\"lol\",\"lol\"]\n")

	for i := 1; i <= 9; i++ {
		prev := "*x" + string(rune('0'+i-1))
		b.WriteString("x" + string(rune('0'+i)) + ": &x" + string(rune('0'+i)) + " [" + strings.TrimSuffix(strings.Repeat(prev+",", 9), ",") + "]\n")
	}

	return []byte(b.String())
}

// fuzzMarshalSeeds returns Marshal-encoded gemspecs for seeding.
func fuzzMarshalSeeds() [][]byte {
	var out [][]byte

	for _, spec := range []rubymarshal.Specification{
		{Name: "demo", Version: "1.0.0", Summary: "s", Authors: []string{"a"}, Licenses: []string{"MIT"}},
		{
			Name: "rich", Version: "2.0.0.pre1", Platform: "java", Description: "d", Homepage: "https://x.example", Email: "e@x.example",
			Dependencies: []rubymarshal.Dependency{{Name: "rake", Requirement: ">= 1.0, < 3", Type: "development"}},
		},
	} {
		if data, err := rubymarshal.EncodeSpecification(spec); err == nil {
			out = append(out, data)
		}
	}

	return out
}

// fuzzMetadataSeeds are the metadata.gz payloads every gem target starts from.
func fuzzMetadataSeeds() [][]byte {
	seeds := [][]byte{
		[]byte(fuzzSampleJSON),
		[]byte(fuzzSampleYAML),
		fuzzAliasBomb(),
		[]byte(`{"name":"a","version":"1"}`),
		[]byte(`{"name":"a/b","version":"1"}`),
		[]byte(`{"name":"../x","version":"1.0"}`),
		[]byte(`{"name":"a","version":"1-1"}`),
		[]byte(`{"name":"a","version":"1","platform":"x86_64-linux"}`),
		[]byte(`{"name":"a","version":"1","platform":"a b"}`),
		[]byte(`{"name":"a","version":"1","dependencies":[{"name":"x","requirement":",","type":""}]}`),
		[]byte(`{"name":"a","version":"1","dependencies":"x","licenses":"MIT","authors":[1,"b"]}`),
		[]byte("name: a\nversion: 1.0\n"),
		[]byte("name: a\nversion: {version: 1}\ndependencies: [{name: x, requirement: {requirements: [[\">=\", {version: 1}]]}, type: ':development'}]\n"),
		[]byte("[]"),
		[]byte("---\n- a\n"),
		[]byte("&a [*a]"),
		{},
	}

	return append(seeds, fuzzMarshalSeeds()...)
}

// FuzzParseGem feeds arbitrary bytes to the .gem reader behind upload, the
// format handler and licence extraction (tar, then metadata.gz, then the
// gemspec decoders). The three entry points must agree, and what comes out
// must be storable as JSON.
func FuzzParseGem(f *testing.F) {
	for _, raw := range fuzzMetadataSeeds() {
		f.Add(fuzzGem(raw))
	}

	// A metadata.gz that inflates past the cap, a missing metadata.gz, a
	// metadata.gz that is not gzip, a truncated tar.
	f.Add(fuzzGem(bytes.Repeat([]byte{' '}, maxMetadataBytes+1)))
	f.Add(fuzzTar(fuzzTarEntry{"data.tar.gz", []byte("x")}))
	f.Add(fuzzTar(fuzzTarEntry{"metadata.gz", []byte("not gzip")}))
	f.Add(fuzzTar(fuzzTarEntry{"./metadata.gz", fuzzGzip([]byte(fuzzSampleJSON))}))
	f.Add(fuzzGem([]byte(fuzzSampleJSON))[:700])
	f.Add([]byte("not a tar"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		ctx := context.Background()
		h := NewRubyGemsHandler()

		meta, err := h.Parse(ctx, bytes.NewReader(data))
		verr := h.Validate(ctx, bytes.NewReader(data))
		licenses, lerr := ParseLicenses(ctx, bytes.NewReader(data))

		if (err == nil) != (verr == nil) || (err == nil) != (lerr == nil) {
			t.Fatalf("entry points disagree: Parse=%v Validate=%v ParseLicenses=%v", err, verr, lerr)
		}

		if err != nil {
			return
		}

		checkMetadata(t, meta)

		if want := getStringArrayFromMetadata(meta, "licenses"); !reflect.DeepEqual(licenses, want) {
			t.Fatalf("ParseLicenses = %v, metadata licenses = %v", licenses, want)
		}
	})
}

// checkMetadata asserts what the upload path relies on for any parsed gemspec.
func checkMetadata(t *testing.T, meta map[string]any) {
	t.Helper()

	if meta == nil {
		t.Fatal("metadata is nil without an error")
	}

	// mustMarshalJSON falls back to "{}" on error and would silently drop the
	// stored metadata.
	if _, err := json.Marshal(meta); err != nil {
		t.Fatalf("metadata is not JSON-encodable: %v", err)
	}

	// The dependency column is always valid JSON the gemspec route can read.
	deps := mustMarshalJSON(meta["dependencies"])
	if !json.Valid(deps) {
		t.Fatalf("dependencies column %q is not JSON", deps)
	}

	if _, err := rubymarshal.EncodeSpecification(rubymarshal.Specification{
		Name: "x", Version: "1", Dependencies: decodeGemDependencies(deps),
	}); err != nil {
		t.Fatalf("stored dependencies %q cannot be served as a gemspec: %v", deps, err)
	}
}

// FuzzGemspecMetadata feeds arbitrary metadata.gz payloads to the Marshal,
// JSON and YAML gemspec decoders. The YAML path builds the metadata field by
// field, so its shape is checked in full.
func FuzzGemspecMetadata(f *testing.F) {
	for _, raw := range fuzzMetadataSeeds() {
		f.Add(raw)
	}

	f.Add(bytes.Repeat([]byte("- "), 2000))
	f.Add(append([]byte("name: a\nversion: '1'\nauthors: "), bytes.Repeat([]byte("["), 2000)...))
	f.Add([]byte("name: a\nversion: !!binary AAAA\n"))
	f.Add([]byte("? [a]\n: b\nname: x\nversion: '1'\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		meta := parseGemspecMetadata(data)
		checkMetadata(t, meta)

		if raw, ok := meta["raw_metadata"]; ok && (raw != string(data) || len(meta) != 1) {
			t.Fatalf("raw_metadata fallback is not the payload: %v", meta)
		}

		if y := parseYAMLGemspec(data); y != nil {
			checkYAMLShape(t, y)
		}
	})
}

// checkYAMLShape asserts the typed output of parseYAMLGemspec.
func checkYAMLShape(t *testing.T, m map[string]any) {
	t.Helper()

	for _, key := range []string{"name", "version", "platform"} {
		if s, ok := m[key].(string); !ok || s == "" {
			t.Fatalf("YAML metadata %q = %#v, want a non-empty string", key, m[key])
		}
	}

	for _, key := range []string{"summary", "description", "homepage", "email"} {
		if _, ok := m[key].(string); !ok {
			t.Fatalf("YAML metadata %q = %#v, want a string", key, m[key])
		}
	}

	for _, key := range []string{"authors", "licenses"} {
		list, ok := m[key].([]any)
		if !ok {
			t.Fatalf("YAML metadata %q = %#v, want a list", key, m[key])
		}

		for _, e := range list {
			if _, ok := e.(string); !ok {
				t.Fatalf("YAML metadata %q holds %#v, want strings", key, e)
			}
		}
	}

	deps, ok := m["dependencies"].([]map[string]any)
	if !ok && m["dependencies"] != nil {
		t.Fatalf("YAML dependencies = %#v", m["dependencies"])
	}

	for _, d := range deps {
		for _, key := range []string{"name", "requirement", "type"} {
			if _, ok := d[key].(string); !ok {
				t.Fatalf("YAML dependency %v: %q is not a string", d, key)
			}
		}
	}
}

// fuzzGemsDB is the part of the database the gem routes use.
type fuzzGemsDB struct {
	*formattest.DB

	gems      []*models.RubyGem
	used      int64
	artifacts int
}

func (d *fuzzGemsDB) CreateRubyGem(_ context.Context, g *models.RubyGem) error {
	if err := pgRejects(g); err != nil {
		return err
	}

	g.ID = "gem-1"
	d.gems = append(d.gems, g)

	return nil
}

func (d *fuzzGemsDB) SearchRubyGems(_ context.Context, _, query string) ([]*models.RubyGem, error) {
	var out []*models.RubyGem

	for _, g := range d.gems {
		if strings.Contains(g.Name, query) {
			out = append(out, g)
		}
	}

	return out, nil
}

func (d *fuzzGemsDB) GetRubyGem(_ context.Context, _, name, version, platform string) (*models.RubyGem, error) {
	for _, g := range d.gems {
		if g.Name == name && g.Version == version && g.Platform == platform {
			return g, nil
		}
	}

	return nil, nil
}

func (d *fuzzGemsDB) ListRubyGemSpecs(_ context.Context, _ string) ([]*models.RubyGem, error) {
	var out []*models.RubyGem

	for _, g := range d.gems {
		if !g.Yanked {
			out = append(out, g)
		}
	}

	return out, nil
}

func (d *fuzzGemsDB) RubyGemsSpecsStamp(_ context.Context, _ string) (string, error) {
	var b strings.Builder

	for _, g := range d.gems {
		b.WriteString(g.Name + "@" + g.Version + "@" + g.Platform + ";")
	}

	return b.String(), nil
}

func (d *fuzzGemsDB) ListRubyGemVersions(_ context.Context, _, name string) ([]*models.RubyGem, error) {
	var out []*models.RubyGem

	for _, g := range d.gems {
		if g.Name == name {
			out = append(out, g)
		}
	}

	return out, nil
}

func (d *fuzzGemsDB) ReserveStorageUsage(_ context.Context, _ string, size int64, count int) (bool, error) {
	d.used += size
	d.artifacts += count

	return true, nil
}

func (d *fuzzGemsDB) DecrementStorageUsage(_ context.Context, _ string, size int64, count int) error {
	d.used -= size
	d.artifacts -= count

	return nil
}

var errPostgresRejects = errors.New("postgres rejects the row")

// pgText reports whether a text column can store s: valid UTF-8, no NUL.
func pgText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }

// pgJSONB reports whether a jsonb column can store raw: valid JSON none of
// whose strings or keys holds a NUL (jsonb cannot represent \u0000).
func pgJSONB(raw []byte) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}

	var ok func(any) bool

	ok = func(v any) bool {
		switch t := v.(type) {
		case string:
			return pgText(t)
		case []any:
			return !slices.ContainsFunc(t, func(e any) bool { return !ok(e) })
		case map[string]any:
			for k, e := range t {
				if !pgText(k) || !ok(e) {
					return false
				}
			}
		}

		return true
	}

	return ok(v)
}

// pgRejects mirrors the rubygems_gems column types (migrations/001): the
// varchar lengths count characters, text and text[] need valid text, and
// dependencies and metadata_json are jsonb.
func pgRejects(g *models.RubyGem) error {
	bad := utf8.RuneCountInString(g.Name) > 255 || utf8.RuneCountInString(g.Version) > 100 || utf8.RuneCountInString(g.Platform) > 50 ||
		!pgText(g.Name) || !pgText(g.Version) || !pgText(g.Platform) ||
		!pgText(g.Description) || !pgText(g.Summary) || !pgText(g.Homepage) ||
		slices.ContainsFunc(g.Authors, func(s string) bool { return !pgText(s) }) ||
		slices.ContainsFunc(g.Licenses, func(s string) bool { return !pgText(s) }) ||
		!pgJSONB(g.Dependencies) || !pgJSONB(g.MetadataJSON)
	if bad {
		return errPostgresRejects
	}

	return nil
}

func newFuzzGemHandler() (*HTTPHandler, *fuzzGemsDB, *formattest.BlobStore) {
	db := &fuzzGemsDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	h := NewHTTPHandler(&logger.Logger{SugaredLogger: zap.NewNop().Sugar()}, db, bs, "http://localhost")
	h.SetPublisher(&formattest.Publisher{})

	return h, db, bs
}

// fuzzGemRequest sends one request to the gem routes with the path set
// verbatim (no client-side escaping), so hostile names reach the router.
func fuzzGemRequest(h *HTTPHandler, method, tail string, body []byte) *httptest.ResponseRecorder {
	path, query, _ := strings.Cut(tail, "?")

	req := formattest.Request(method, "/", bytes.NewReader(body))
	req.URL.Path = "/repos/myrepo/" + path
	req.URL.RawQuery = query

	return formattest.Do(h.RegisterRepoRoutes, req)
}

// FuzzUploadGem uploads a gem whose metadata.gz is the fuzzed payload and
// checks the whole accept path: nothing below 500, nothing persisted on a
// rejection, and an accepted gem is a well-formed identity that every read
// route (download, gemspec, specs indexes, info) then serves.
func FuzzUploadGem(f *testing.F) {
	for _, raw := range fuzzMetadataSeeds() {
		f.Add(raw)
	}

	f.Add([]byte("{\"name\":\"a\xff\",\"version\":\"1\"}"))
	f.Add([]byte(`{"name":"a.b","version":"1.0.pre"}`))
	f.Add([]byte(`{"name":"a-1","version":"2","platform":"p-3"}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		h, db, bs := newFuzzGemHandler()
		gem := fuzzGem(raw)

		w := fuzzGemRequest(h, http.MethodPost, "api/v1/gems", gem)
		if w.Code >= http.StatusInternalServerError {
			t.Fatalf("upload answered %d: %s", w.Code, w.Body.String())
		}

		if w.Code != http.StatusCreated {
			if len(db.gems) != 0 || len(bs.Blobs) != 0 || db.used != 0 || db.artifacts != 0 {
				t.Fatalf("upload answered %d but left gems=%d blobs=%d used=%d artifacts=%d",
					w.Code, len(db.gems), len(bs.Blobs), db.used, db.artifacts)
			}

			return
		}

		checkAcceptedGem(t, h, db, gem)
	})
}

func checkAcceptedGem(t *testing.T, h *HTTPHandler, db *fuzzGemsDB, gem []byte) {
	t.Helper()

	if len(db.gems) != 1 {
		t.Fatalf("upload stored %d gems, want 1", len(db.gems))
	}

	g := db.gems[0]

	for _, s := range []string{g.Name, g.Version, g.Platform} {
		if !validGemIdent(s) || !utf8.ValidString(s) {
			t.Fatalf("accepted gem has the invalid identity part %q (%s %s %s)", s, g.Name, g.Version, g.Platform)
		}
	}

	if !validGemVersion(g.Version) {
		t.Fatalf("accepted version %q", g.Version)
	}

	file := strings.TrimSuffix(gemFilename(g), ".gem")

	if w := fuzzGemRequest(h, http.MethodGet, "gems/"+file+".gem", nil); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), gem) {
		t.Fatalf("download of %q = %d (%d bytes, want %d)", file, w.Code, w.Body.Len(), len(gem))
	}

	checkServedGemspec(t, h, g, file)
	checkServedSpecs(t, h, g)

	if w := fuzzGemRequest(h, http.MethodPost, "api/v1/gems", gem); w.Code != http.StatusConflict {
		t.Fatalf("second upload of %q = %d, want 409", file, w.Code)
	}
}

func checkServedGemspec(t *testing.T, h *HTTPHandler, g *models.RubyGem, file string) {
	t.Helper()

	w := fuzzGemRequest(h, http.MethodGet, "quick/Marshal.4.8/"+file+".gemspec.rz", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("gemspec of %q = %d: %s", file, w.Code, w.Body.String())
	}

	zr, err := zlib.NewReader(w.Body)
	if err != nil {
		t.Fatalf("gemspec of %q is not zlib: %v", file, err)
	}

	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gemspec of %q: %v", file, err)
	}

	spec, err := rubymarshal.DecodeSpecification(data)
	if err != nil {
		t.Fatalf("gemspec of %q does not decode: %v", file, err)
	}

	if spec.Name != g.Name || spec.Version != g.Version || spec.Platform != g.Platform {
		t.Fatalf("gemspec of %q is %s %s %s, stored %s %s %s", file, spec.Name, spec.Version, spec.Platform, g.Name, g.Version, g.Platform)
	}
}

// checkServedSpecs asserts that the gem sits in exactly the specs indexes its
// version calls for.
func checkServedSpecs(t *testing.T, h *HTTPHandler, g *models.RubyGem) {
	t.Helper()

	pre := isPrerelease(g.Version)

	for _, c := range []struct {
		file string
		want bool
	}{
		{"specs.4.8.gz", !pre},
		{"latest_specs.4.8.gz", !pre},
		{"prerelease_specs.4.8.gz", pre},
	} {
		w := fuzzGemRequest(h, http.MethodGet, c.file, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d", c.file, w.Code)
		}

		zr, err := gzip.NewReader(w.Body)
		if err != nil {
			t.Fatalf("%s is not gzip: %v", c.file, err)
		}

		data, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}

		if got := specsListHas(t, data, g); got != c.want {
			t.Fatalf("%s lists %s %s %s = %v, want %v", c.file, g.Name, g.Version, g.Platform, got, c.want)
		}
	}
}

// specsListHas decodes a specs index and reports whether it lists g.
func specsListHas(t *testing.T, data []byte, g *models.RubyGem) bool {
	t.Helper()

	root, err := rubymarshal.Decode(data)
	if err != nil {
		t.Fatalf("specs index does not decode: %v", err)
	}

	list, ok := root.([]any)
	if !ok {
		t.Fatalf("specs index is %T, want an array", root)
	}

	found := false

	for _, e := range list {
		entry, ok := e.([]any)
		if !ok || len(entry) != 3 {
			t.Fatalf("specs entry %#v is not [name, version, platform]", e)
		}

		name, _ := entry[0].(string)
		platform, _ := entry[2].(string)

		ver, ok := entry[1].(*rubymarshal.UserMarshal)
		if !ok || ver.Class != "Gem::Version" {
			t.Fatalf("specs entry version %#v is not a Gem::Version", entry[1])
		}

		payload, _ := ver.Payload.([]any)
		version := ""

		if len(payload) == 1 {
			version, _ = payload[0].(string)
		}

		if name == g.Name && version == g.Version && platform == g.Platform {
			found = true
		}
	}

	return found
}

// FuzzGemIdentity feeds arbitrary gem names, versions and platforms to the
// validators and to the file-name resolver: whatever the upload gate accepts
// must be resolvable again from the "{name}-{version}[-{platform}].gem" name
// it is published under, and never be shadowed.
func FuzzGemIdentity(f *testing.F) {
	f.Add("rails", "7.0.0", "ruby")
	f.Add("nokogiri", "1.16.2", "x86_64-linux")
	f.Add("a-1", "2", "ruby")
	f.Add("a", "1", "2")
	f.Add("a", "1", "p-2-q")
	f.Add("a-1-b", "2", "java")
	f.Add("a", "1.0.pre", "ruby")
	f.Add("a.b", "1", "x86-mingw32")
	f.Add("", "1", "ruby")
	f.Add("..", "1", "ruby")
	f.Add("a/b", "1", "ruby")
	f.Add("a", "-1", "ruby")
	f.Add("a", "1-1", "ruby")
	f.Add("a b", "1", "ruby")

	f.Fuzz(func(t *testing.T, name, version, platform string) {
		if validGemVersion(version) {
			if !validGemIdent(version) || version[0] < '0' || version[0] > '9' || strings.Contains(version, "-") {
				t.Fatalf("validGemVersion(%q) accepts a version findGem cannot split on", version)
			}
		}

		if !validGemIdent(name) || !validGemVersion(version) || !validGemIdent(platform) {
			return
		}

		for _, s := range []string{name, version, platform} {
			if !utf8.ValidString(s) {
				t.Fatalf("identity part %q is accepted but is not UTF-8", s)
			}
		}

		h, db, _ := newFuzzGemHandler()
		g := &models.RubyGem{Name: name, Version: version, Platform: platform}
		db.gems = append(db.gems, g)

		file := strings.TrimSuffix(gemFilename(g), ".gem")

		found, err := h.findGem(context.Background(), "repo-1", file)
		if err != nil || found != g {
			t.Fatalf("findGem(%q) = %v, %v; want the gem %s %s %s", file, found, err, name, version, platform)
		}

		named, err := h.gemsNamed(context.Background(), "repo-1", file)
		if err != nil || len(named) != 1 {
			t.Fatalf("gemsNamed(%q) = %d gems, %v; want 1", file, len(named), err)
		}
	})
}

var fuzzSegmentRE = regexp.MustCompile(`[0-9]+|[A-Za-z]+`)

// fuzzRefCompare is Gem::Version#<=> over [0-9A-Za-z.] versions, written
// straight from the rules (numeric segments by value, string segments before
// numbers, missing segments are 0) with arbitrary-size integers.
func fuzzRefCompare(a, b string) int {
	x, y := fuzzSegmentRE.FindAllString(a, -1), fuzzSegmentRE.FindAllString(b, -1)

	for i := 0; i < len(x) || i < len(y); i++ {
		sx, sy := "0", "0"
		if i < len(x) {
			sx = x[i]
		}

		if i < len(y) {
			sy = y[i]
		}

		xNum, yNum := sx[0] >= '0' && sx[0] <= '9', sy[0] >= '0' && sy[0] <= '9'

		switch {
		case xNum && yNum:
			bx, _ := new(big.Int).SetString(sx, 10)
			by, _ := new(big.Int).SetString(sy, 10)

			if c := bx.Cmp(by); c != 0 {
				return c
			}
		case xNum:
			return 1
		case yNum:
			return -1
		default:
			if c := strings.Compare(sx, sy); c != 0 {
				return c
			}
		}
	}

	return 0
}

func fuzzPlainVersion(v string) bool {
	for _, r := range v {
		if r != '.' && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}

	return true
}

// FuzzGemVersionOrder feeds arbitrary versions to Gem::Version ordering and to
// the specs index selection built on it.
func FuzzGemVersionOrder(f *testing.F) {
	f.Add("1.0", "1.0.0", "1.0.a")
	f.Add("10.0", "9.0", "1.0.rc1")
	f.Add("1.2147483648", "1.2147483649", "1.9")
	f.Add("1.a", "1.b", "1.1")
	f.Add("", "0", "1-1")
	f.Add("1.\u0663", "1.3", "1.\u00e9")

	f.Fuzz(func(t *testing.T, a, b, c string) {
		vs := []string{a, b, c}

		for _, x := range vs {
			if compareVersions(x, x) != 0 {
				t.Fatalf("compareVersions(%q, %q) != 0", x, x)
			}

			for _, y := range vs {
				if compareVersions(x, y) != -compareVersions(y, x) {
					t.Fatalf("compareVersions not antisymmetric for %q, %q", x, y)
				}

				if fuzzPlainVersion(x) && fuzzPlainVersion(y) && compareVersions(x, y) != fuzzRefCompare(x, y) {
					t.Fatalf("compareVersions(%q, %q) = %d, want %d", x, y, compareVersions(x, y), fuzzRefCompare(x, y))
				}

				for _, z := range vs {
					if compareVersions(x, y) <= 0 && compareVersions(y, z) <= 0 && compareVersions(x, z) > 0 {
						t.Fatalf("compareVersions not transitive for %q <= %q <= %q", x, y, z)
					}
				}
			}
		}

		checkSelectSpecs(t, vs)
	})
}

// checkSelectSpecs asserts the partition and the latest rule of the specs
// indexes over gems of one name and two platforms.
func checkSelectSpecs(t *testing.T, versions []string) {
	t.Helper()

	gems := []*models.RubyGem{{Name: "y", Version: versions[0], Platform: "ruby", Yanked: true}}

	for i, v := range versions {
		platform := "ruby"
		if i == 2 {
			platform = "java"
		}

		gems = append(gems, &models.RubyGem{Name: "x", Version: v, Platform: platform})
	}

	released, latest, pre := selectSpecs(gems, specsReleased), selectSpecs(gems, specsLatest), selectSpecs(gems, specsPrerelease)

	if len(released)+len(pre) != len(gems)-1 {
		t.Fatalf("released %d + prerelease %d does not partition the %d listed gems", len(released), len(pre), len(gems)-1)
	}

	for _, g := range released {
		if g.Yanked || isPrerelease(g.Version) {
			t.Fatalf("specs.4.8 lists %s %s", g.Name, g.Version)
		}
	}

	for _, g := range pre {
		if g.Yanked || !isPrerelease(g.Version) {
			t.Fatalf("prerelease_specs.4.8 lists %s %s", g.Name, g.Version)
		}
	}

	seen := map[string]bool{}

	for i, g := range latest {
		key := g.Name + ":" + g.Platform
		if seen[key] {
			t.Fatalf("latest_specs.4.8 lists %s twice", key)
		}

		seen[key] = true

		if i > 0 && latest[i-1].Name+"\x00"+latest[i-1].Platform > g.Name+"\x00"+g.Platform {
			t.Fatalf("latest_specs.4.8 is not ordered by name and platform")
		}

		for _, r := range released {
			if r.Name == g.Name && r.Platform == g.Platform && compareVersions(r.Version, g.Version) > 0 {
				t.Fatalf("latest_specs.4.8 lists %s %s although %s is newer", key, g.Version, r.Version)
			}
		}
	}

	for _, r := range released {
		if !seen[r.Name+":"+r.Platform] {
			t.Fatalf("latest_specs.4.8 misses %s:%s", r.Name, r.Platform)
		}
	}
}

// FuzzMarshalRoundTrip encodes arbitrary gem fields into the two Marshal
// documents this registry serves (a gemspec and a specs index) and decodes
// them again: what the registry writes it must be able to read back, and a
// second round trip must not change anything.
func FuzzMarshalRoundTrip(f *testing.F) {
	f.Add("rails", "7.0.0", "ruby", "summary", "desc", "https://x.example", "me@x.example", "me", "MIT", "rack", "~> 2.0, >= 2.0.1", "runtime")
	f.Add("a", "1", "java", "", "", "", "", "", "", "x", ">=", "development")
	f.Add("a\xff", "1.\x00", "", "\xc3\x28", "\n", "", "", "", "", "", ",,", "")
	f.Add("a", "1", "ruby", "", "", "", "", "", "", "d", "= >1", ":x")

	f.Fuzz(func(t *testing.T, name, version, platform, summary, desc, home, email, author, license, depName, depReq, depType string) {
		spec := rubymarshal.Specification{
			Name: name, Version: version, Platform: platform, Summary: summary, Description: desc, Homepage: home, Email: email,
			Authors: []string{author}, Licenses: []string{license},
			Dependencies: []rubymarshal.Dependency{{Name: depName, Requirement: depReq, Type: depType}},
		}

		first := fuzzRoundTripSpec(t, spec)

		wantPlatform := platform
		if wantPlatform == "" {
			wantPlatform = "ruby"
		}

		if first.Name != name || first.Version != version || first.Platform != wantPlatform || first.Summary != summary ||
			first.Description != desc || first.Homepage != home || first.Email != email ||
			!reflect.DeepEqual(first.Authors, spec.Authors) || !reflect.DeepEqual(first.Licenses, spec.Licenses) ||
			len(first.Dependencies) != 1 || first.Dependencies[0].Name != depName {
			t.Fatalf("gemspec round trip changed %+v into %+v", spec, first)
		}

		// A clause that is an operator without a version has no Marshal
		// reading and decodes to "": it is not a requirement to keep stable.
		if first.Dependencies[0].Requirement == "" {
			first.Dependencies[0].Requirement = ">= 0"
		}

		if second := fuzzRoundTripSpec(t, *first); !reflect.DeepEqual(first, second) {
			t.Fatalf("second gemspec round trip changed %+v into %+v", first, second)
		}

		checkSpecsListRoundTrip(t, name, version, platform)
	})
}

func fuzzRoundTripSpec(t *testing.T, spec rubymarshal.Specification) *rubymarshal.Specification {
	t.Helper()

	data, err := rubymarshal.EncodeSpecification(spec)
	if err != nil {
		t.Fatalf("EncodeSpecification(%+v): %v", spec, err)
	}

	got, err := rubymarshal.DecodeSpecification(data)
	if err != nil {
		t.Fatalf("DecodeSpecification of the encoding of %+v: %v", spec, err)
	}

	return got
}

func checkSpecsListRoundTrip(t *testing.T, name, version, platform string) {
	t.Helper()

	g := &models.RubyGem{Name: name, Version: version, Platform: platform}

	data, err := marshalSpecsList([]*models.RubyGem{g, g})
	if err != nil {
		t.Fatalf("marshalSpecsList: %v", err)
	}

	list, ok := mustDecodeList(t, data)
	if !ok || len(list) != 2 || !specsListHas(t, data, g) {
		t.Fatalf("specs index of %q %q %q does not round trip: %#v", name, version, platform, list)
	}
}

func mustDecodeList(t *testing.T, data []byte) ([]any, bool) {
	t.Helper()

	root, err := rubymarshal.Decode(data)
	if err != nil {
		t.Fatalf("specs index does not decode: %v", err)
	}

	list, ok := root.([]any)

	return list, ok
}

// FuzzServeGemGet sends arbitrary GET paths to a repository holding a few gems.
// Every answer is 200, 404 or the router's 405 (the upload and yank routes
// share paths with reads), and a 200 for a gem file is that stored gem.
func FuzzServeGemGet(f *testing.F) {
	h, db, bs := newFuzzGemHandler()
	stored := map[string][]byte{}

	for _, c := range [][3]string{
		{"demo", "1.0.0", "ruby"},
		{"demo", "1.1.0.pre1", "ruby"},
		{"nokogiri", "1.16.2", "x86_64-linux"},
		{"a-1", "2", "ruby"},
	} {
		g := &models.RubyGem{Name: c[0], Version: c[1], Platform: c[2], BlobKey: "blob-" + c[0] + c[1] + c[2]}
		bs.Blobs[g.BlobKey] = []byte(gemFilename(g))
		db.gems = append(db.gems, g)
		stored[gemFilename(g)] = bs.Blobs[g.BlobKey]

		file := strings.TrimSuffix(gemFilename(g), ".gem")

		f.Add("gems/" + file + ".gem")
		f.Add("quick/Marshal.4.8/" + file + ".gemspec.rz")
		f.Add("api/v1/gems/" + c[0] + ".json")
	}

	for _, p := range []string{
		"specs.4.8.gz", "latest_specs.4.8.gz", "prerelease_specs.4.8.gz", "api/v1/search.json?query=demo",
		"gems/.gem", "gems/-1.gem", "gems/demo-.gem", "gems/demo-1.0.0-.gem", "gems/demo-1.0.0.gem.gem", "gems/../gems/demo-1.0.0.gem",
		"quick/Marshal.4.8/.gemspec.rz", "api/v1/gems/.json", "api/v1/gems/demo.json.json",
	} {
		f.Add(p)
	}

	f.Fuzz(func(t *testing.T, tail string) {
		w := fuzzGemRequest(h, http.MethodGet, tail, nil)

		switch w.Code {
		case http.StatusNotFound, http.StatusMethodNotAllowed:
		case http.StatusOK:
			if file, ok := strings.CutPrefix(tail, "gems/"); ok && !bytes.Equal(w.Body.Bytes(), stored[file]) {
				t.Fatalf("GET %q served %q, which is not the stored gem of that name", tail, w.Body.Bytes())
			}
		default:
			t.Fatalf("GET %q answered %d: %s", tail, w.Code, w.Body.String())
		}
	})
}

// FuzzYankForm feeds arbitrary query strings and form bodies to the yank
// request parser: the body is capped, and a body parameter wins over the same
// query parameter.
func FuzzYankForm(f *testing.F) {
	f.Add("gem_name=a&version=1", "gem_name=b&version=2")
	f.Add("", "gem_name=%zz")
	f.Add("platform=x", "gem_name=a;version=1")

	f.Fuzz(func(t *testing.T, query, body string) {
		// Seeding an oversize body would slow the engine to a crawl, so the
		// cap is checked on every input padded past it.
		huge := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/",
			strings.NewReader(body+strings.Repeat("&", maxYankFormBytes+1)))
		huge.URL.RawQuery = query

		if _, err := yankForm(huge); err == nil {
			t.Fatalf("a body of %d bytes passed the %d byte cap", len(body)+maxYankFormBytes+1, maxYankFormBytes)
		}

		req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/", strings.NewReader(body))
		req.URL.RawQuery = query

		form, err := yankForm(req)
		if err != nil && len(body) <= maxYankFormBytes {
			if _, perr := url.ParseQuery(body); perr == nil {
				t.Fatalf("yankForm rejected the valid %d byte body %q: %v", len(body), body, err)
			}
		}

		if err != nil {
			return
		}

		want, perr := url.ParseQuery(body)
		if perr != nil {
			t.Fatalf("yankForm accepted the body %q that ParseQuery rejects: %v", body, perr)
		}

		for k, vs := range want {
			if form.Get(k) != vs[0] {
				t.Fatalf("form[%q] = %q, the body says %q", k, form.Get(k), vs[0])
			}
		}
	})
}
