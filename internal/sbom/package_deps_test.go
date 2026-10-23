// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/licensemeta/archivetest"
	"github.com/openartifact/openartifact/internal/storage"
)

// memBlobs serves stored package files by their sha256 key.
type memBlobs struct {
	storage.BlobStore

	files map[string][]byte
}

func (m *memBlobs) Get(_ context.Context, id string) (io.ReadCloser, error) {
	data, ok := m.files[id]
	if !ok {
		return nil, errors.New("blob not found")
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

// rowDB resolves package-listing row ids as the real store does.
type rowDB struct {
	fakeDB

	blob *models.ComponentBlob
}

func (r *rowDB) GetRepoComponentBlob(_ context.Context, _, id string) (*models.ComponentBlob, error) {
	if r.blob != nil && r.blob.ID == id {
		return r.blob, nil
	}

	return nil, nil
}

const (
	shaTGZ = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaPOM = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	shaJAR = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func npmTarball(t *testing.T) []byte {
	t.Helper()

	return archivetest.TarGz(t,
		archivetest.File("package/package.json", `{"name":"lib","version":"1.0.0","dependencies":{"left-pad":"1.3.0","lodash":"^4.17.0","@babel/core":"7.0.0"}}`),
		archivetest.File("package/npm-shrinkwrap.json", `{"lockfileVersion":3,"packages":{"":{},"node_modules/lodash":{"version":"4.17.21","license":"MIT"}}}`))
}

func cdxComponents(t *testing.T, res *SBOMResult) map[string]cdxComponent {
	t.Helper()

	var bom cdxBOM
	if err := json.Unmarshal(res.Body, &bom); err != nil {
		t.Fatal(err)
	}

	out := map[string]cdxComponent{}
	for _, c := range bom.Components {
		out[c.PURL] = c
	}

	return out
}

func TestSBOMComponentsFromPackageFile(t *testing.T) {
	db := &fakeDB{comps: []*models.Component{
		{Format: "npm", Name: "lib", Version: "1.0.0", Filename: "lib-1.0.0.tgz", SHA256: shaTGZ},
	}}
	blobs := &memBlobs{files: map[string][]byte{shaTGZ: npmTarball(t)}}
	g := newFakeGen(db).WithBlobStore(blobs)

	// A dependency the properties already list keeps the properties' data.
	meta := map[string]any{"dependencies": []any{
		map[string]any{"name": "left-pad", "version": "1.3.0", "license": "WTFPL"},
		map[string]any{"name": "from-props", "version": "9.9.9"},
	}}

	res, err := g.GenerateCycloneDX(context.Background(), "repo", "lib@1.0.0", "npm", meta)
	if err != nil {
		t.Fatal(err)
	}

	comps := cdxComponents(t, res)

	for _, purl := range []string{
		"pkg:npm/left-pad@1.3.0", "pkg:npm/from-props@9.9.9", "pkg:npm/lodash@4.17.21", "pkg:npm/%40babel/core@7.0.0",
	} {
		if _, ok := comps[purl]; !ok {
			t.Errorf("component %s missing from %v", purl, keys(comps))
		}
	}

	if len(res.Data) == 0 || res.Components != 4 {
		t.Errorf("components = %d, want 4", res.Components)
	}

	if lp := comps["pkg:npm/left-pad@1.3.0"]; len(lp.Licenses) != 1 || lp.Licenses[0].License.ID != "WTFPL" {
		t.Errorf("left-pad licenses = %+v, want the property's WTFPL", lp.Licenses)
	}

	if lo := comps["pkg:npm/lodash@4.17.21"]; len(lo.Licenses) != 1 || lo.Licenses[0].License.ID != "MIT" {
		t.Errorf("lodash licenses = %+v", lo.Licenses)
	}

	t.Run("SPDX lists the same packages", func(t *testing.T) {
		out, err := g.GenerateSPDX(context.Background(), "repo", "lib@1.0.0", "npm", nil)
		if err != nil {
			t.Fatal(err)
		}

		for _, want := range []string{"pkg:npm/lodash@4.17.21", "pkg:npm/%40babel/core@7.0.0", "pkg:npm/left-pad@1.3.0"} {
			if !strings.Contains(out.Text, want) {
				t.Errorf("SPDX misses %s", want)
			}
		}
	})
}

func TestSBOMRangeOnlyDependencyHasNoInventedVersion(t *testing.T) {
	tgz := archivetest.TarGz(t, archivetest.File("package/package.json", `{"dependencies":{"lodash":"^4.17.0"}}`))
	db := &fakeDB{comps: []*models.Component{{Format: "npm", Name: "lib", Version: "1.0.0", Filename: "lib-1.0.0.tgz", SHA256: shaTGZ}}}
	g := newFakeGen(db).WithBlobStore(&memBlobs{files: map[string][]byte{shaTGZ: tgz}})

	res, err := g.GenerateCycloneDX(context.Background(), "repo", "lib@1.0.0", "npm", nil)
	if err != nil {
		t.Fatal(err)
	}

	c, ok := cdxComponents(t, res)["pkg:npm/lodash"]
	if !ok {
		t.Fatal("version-less lodash component missing")
	}

	if c.Version != "" || len(c.Properties) != 1 || c.Properties[0].Value != "^4.17.0" {
		t.Errorf("component = %+v", c)
	}
}

func TestSBOMSubjectFromListingRowID(t *testing.T) {
	const rowID = "0b7e7f64-3a4f-4b43-9d0f-7a3a3a3a3a3a"

	db := &rowDB{blob: &models.ComponentBlob{ID: rowID, Format: "npm", Name: "lib", Version: "1.0.0", Filename: "lib-1.0.0.tgz", SHA256: shaTGZ}}
	g := newFakeGen(&db.fakeDB).WithBlobStore(&memBlobs{files: map[string][]byte{shaTGZ: npmTarball(t)}})
	g.db = db

	res, err := g.GenerateCycloneDX(context.Background(), "repo", rowID, "npm", nil)
	if err != nil {
		t.Fatal(err)
	}

	var bom cdxBOM
	if err := json.Unmarshal(res.Body, &bom); err != nil {
		t.Fatal(err)
	}

	root := bom.Metadata.Component
	if root.Name != "lib" || root.Version != "1.0.0" || root.PURL != "pkg:npm/lib@1.0.0" {
		t.Errorf("subject = %+v", root)
	}

	if len(root.Hashes) != 1 || root.Hashes[0].Content != shaTGZ {
		t.Errorf("subject hashes = %+v", root.Hashes)
	}

	if len(bom.Components) != 3 {
		t.Errorf("components = %d, want 3", len(bom.Components))
	}
}

func TestSBOMMavenReadsPomNotJar(t *testing.T) {
	pom := `<project><dependencies><dependency><groupId>g</groupId><artifactId>a</artifactId><version>1.0</version></dependency></dependencies></project>`
	jar := archivetest.Zip(t, archivetest.File("META-INF/maven/x/y/pom.xml",
		`<project><dependencies><dependency><groupId>jar</groupId><artifactId>only</artifactId><version>2</version></dependency></dependencies></project>`))
	db := &fakeDB{comps: []*models.Component{
		{Format: "maven", Name: "org.demo:app", Version: "1.0", Filename: "app-1.0.jar", SHA256: shaJAR},
		{Format: "maven", Name: "org.demo:app", Version: "1.0", Filename: "app-1.0.pom", SHA256: shaPOM},
	}}
	g := newFakeGen(db).WithBlobStore(&memBlobs{files: map[string][]byte{shaPOM: []byte(pom), shaJAR: jar}})

	res, err := g.GenerateCycloneDX(context.Background(), "repo", "org.demo:app@1.0", "maven", nil)
	if err != nil {
		t.Fatal(err)
	}

	comps := cdxComponents(t, res)
	if _, ok := comps["pkg:maven/g/a@1.0"]; !ok || len(comps) != 1 {
		t.Errorf("components = %v, want only pkg:maven/g/a@1.0", keys(comps))
	}
}

func TestSBOMDebDependenciesCarryDistroNamespace(t *testing.T) {
	deb := debPackage(t, "Package: demo\nVersion: 1.0-1ubuntu1\nArchitecture: amd64\nDepends: libc6 (>= 2.31), zlib1g (= 1:1.2.11)\n")
	db := &fakeDB{comps: []*models.Component{{Format: "deb", Name: "demo", Version: "1.0-1ubuntu1", Filename: "demo_1.0-1ubuntu1_amd64.deb", SHA256: shaTGZ}}}
	g := newFakeGen(db).WithBlobStore(&memBlobs{files: map[string][]byte{shaTGZ: deb}})

	res, err := g.GenerateCycloneDX(context.Background(), "repo", "demo@1.0-1ubuntu1", "deb", nil)
	if err != nil {
		t.Fatal(err)
	}

	comps := cdxComponents(t, res)
	if _, ok := comps["pkg:deb/ubuntu/libc6"]; !ok {
		t.Errorf("libc6 missing from %v", keys(comps))
	}

	if _, ok := comps["pkg:deb/ubuntu/zlib1g@1:1.2.11"]; !ok {
		t.Errorf("zlib1g missing from %v", keys(comps))
	}
}

func TestSBOMUnsafeAndUnreadablePackageFiles(t *testing.T) {
	comps := []*models.Component{{Format: "npm", Name: "lib", Version: "1.0.0", Filename: "lib-1.0.0.tgz", SHA256: shaTGZ}}
	meta := map[string]any{"dependencies": []any{map[string]any{"name": "from-props", "version": "1.0.0"}}}

	t.Run("ambiguous manifest fails the SBOM", func(t *testing.T) {
		tgz := archivetest.TarGz(t,
			archivetest.File("a/package.json", `{"dependencies":{"x":"1.0.0"}}`),
			archivetest.File("b/package.json", `{"dependencies":{"y":"1.0.0"}}`))
		g := newFakeGen(&fakeDB{comps: comps}).WithBlobStore(&memBlobs{files: map[string][]byte{shaTGZ: tgz}})

		if _, err := g.GenerateCycloneDX(context.Background(), "repo", "lib@1.0.0", "npm", meta); !errors.Is(err, ErrUnsafeArtifact) {
			t.Fatalf("err = %v, want ErrUnsafeArtifact", err)
		}
	})

	t.Run("damaged file falls back to the properties", func(t *testing.T) {
		g := newFakeGen(&fakeDB{comps: comps}).WithBlobStore(&memBlobs{files: map[string][]byte{shaTGZ: []byte("not a tarball")}})

		res, err := g.GenerateCycloneDX(context.Background(), "repo", "lib@1.0.0", "npm", meta)
		if err != nil {
			t.Fatal(err)
		}

		if got := cdxComponents(t, res); len(got) != 1 {
			t.Errorf("components = %v, want the property dependency only", keys(got))
		}
	})

	t.Run("missing blob falls back to the properties", func(t *testing.T) {
		g := newFakeGen(&fakeDB{comps: comps}).WithBlobStore(&memBlobs{files: map[string][]byte{}})

		if _, err := g.GenerateCycloneDX(context.Background(), "repo", "lib@1.0.0", "npm", meta); err != nil {
			t.Fatal(err)
		}
	})
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}

func TestPickPackageFile(t *testing.T) {
	rows := []*models.Component{
		{Format: "maven", Version: "1", Filename: "a-1-sources.jar", SHA256: "s"},
		{Format: "maven", Version: "1", Filename: "a-1.pom", SHA256: "p"},
		{Format: "maven", Version: "2", Filename: "a-2.pom", SHA256: "q"},
		{Format: "maven", Version: "1", Filename: "a-1.jar.sha1", SHA256: "h"},
	}

	if f := pickPackageFile(rows, "1"); f == nil || f.SHA256 != "p" {
		t.Errorf("picked %+v, want the .pom", f)
	}

	if f := pickPackageFile(rows[:1], "1"); f == nil || f.SHA256 != "s" {
		t.Errorf("without a pom the jar is the fallback, got %+v", f)
	}

	if f := pickPackageFile(rows, "3"); f != nil {
		t.Errorf("unknown version picked %+v", f)
	}
}

// debPackage builds a minimal .deb: an ar archive with a gzip control archive.
func debPackage(t *testing.T, control string) []byte {
	t.Helper()

	member := func(name string, data []byte) []byte {
		var b bytes.Buffer

		hdr := []byte(strings.Repeat(" ", 60))
		copy(hdr[0:], name+"/")
		copy(hdr[16:], "0")
		copy(hdr[28:], "0")
		copy(hdr[34:], "0")
		copy(hdr[40:], "100644")
		copy(hdr[48:], strconv.Itoa(len(data)))
		copy(hdr[58:], "`\n")
		b.Write(hdr)
		b.Write(data)

		if len(data)%2 == 1 {
			b.WriteByte('\n')
		}

		return b.Bytes()
	}

	var out bytes.Buffer

	out.WriteString("!<arch>\n")
	out.Write(member("debian-binary", []byte("2.0\n")))
	out.Write(member("control.tar.gz", archivetest.TarGz(t, archivetest.File("./control", control))))
	out.Write(member("data.tar.gz", archivetest.TarGz(t)))

	return out.Bytes()
}
