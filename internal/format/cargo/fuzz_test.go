// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/licensemeta"
	pkgcargo "github.com/openartifact/openartifact/pkg/cargo"
	"github.com/openartifact/openartifact/pkg/logger"
	"golang.org/x/mod/semver"
)

// fuzzCrate builds a gzip+tar .crate holding the given files.
func fuzzCrate(files ...[2]string) []byte {
	var buf bytes.Buffer

	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for _, f := range files {
		_ = tw.WriteHeader(&tar.Header{Name: f[0], Mode: 0o644, Size: int64(len(f[1])), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(f[1]))
	}

	_ = tw.Close()
	_ = gw.Close()

	return buf.Bytes()
}

// fuzzFrame is the body cargo publish sends: u32le metadata length, the
// metadata JSON, u32le crate length, the .crate.
func fuzzFrame(meta string, crate []byte) []byte {
	var b bytes.Buffer

	_ = binary.Write(&b, binary.LittleEndian, uint32(len(meta)))
	b.WriteString(meta)
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(crate)))
	b.Write(crate)

	return b.Bytes()
}

// fuzzWant is what a publish body must produce: the status the handler answers
// with before the crate's license metadata is looked at, plus the parts found.
type fuzzWant struct {
	status  int
	name    string
	version string
	crate   []byte
}

// fuzzPublishWant decodes a publish body the way the registry protocol
// defines it.
func fuzzPublishWant(body []byte) fuzzWant {
	if len(body) < 4 {
		return fuzzWant{status: http.StatusBadRequest}
	}

	metaLen := binary.LittleEndian.Uint32(body)
	if metaLen > maxPublishMetadataBytes {
		return fuzzWant{status: http.StatusRequestEntityTooLarge}
	}

	rest := body[4:]
	if uint64(len(rest)) < uint64(metaLen) {
		return fuzzWant{status: http.StatusBadRequest}
	}

	var meta map[string]any
	if err := json.Unmarshal(rest[:metaLen], &meta); err != nil {
		return fuzzWant{status: http.StatusBadRequest}
	}

	name, _ := meta["name"].(string)
	version, _ := meta["vers"].(string)

	if !validCrateName(name) || !validCrateVersion(version) {
		return fuzzWant{status: http.StatusBadRequest}
	}

	rest = rest[metaLen:]
	if len(rest) < 4 {
		return fuzzWant{status: http.StatusBadRequest, name: name, version: version}
	}

	crateLen := binary.LittleEndian.Uint32(rest)
	if crateLen > maxPublishCrateBytes {
		return fuzzWant{status: http.StatusRequestEntityTooLarge, name: name, version: version}
	}

	rest = rest[4:]
	if uint64(len(rest)) < uint64(crateLen) {
		return fuzzWant{status: http.StatusBadRequest, name: name, version: version}
	}

	return fuzzWant{status: http.StatusOK, name: name, version: version, crate: rest[:crateLen]}
}

func fuzzPublishSeeds(f *testing.F) {
	f.Helper()

	toml := "[package]\nname = \"demo\"\nversion = \"0.1.0\"\nlicense = \"MIT/Apache-2.0\"\n"
	crate := fuzzCrate([2]string{"demo-0.1.0/Cargo.toml", toml})
	meta := `{"name":"demo","vers":"0.1.0","deps":[{"name":"serde","version_req":"^1","features":["derive"],"optional":false,"default_features":true,"target":null,"kind":"normal","registry":null,"explicit_name_in_toml":"s"}],"features":{"default":["a"],"a":[]},"links":null,"license":"MIT","keywords":["a"],"authors":["x"]}`

	f.Add(fuzzFrame(meta, crate))
	f.Add(fuzzFrame(`{"name":"Demo_X-1","vers":"1.2.3-rc.1+build.5"}`, []byte("not a crate")))
	f.Add(fuzzFrame(`{"name":"demo","vers":"0.1.0"}`, fuzzCrate([2]string{"demo-0.1.0/Cargo.toml", toml}, [2]string{"other-1.0.0/Cargo.toml", toml})))
	f.Add(fuzzFrame(`{"name":"demo","vers":"0.1.0"}`, fuzzCrate([2]string{"demo-0.1.0/Cargo.toml", "[package\n"})))
	f.Add(fuzzFrame(`{"name":"1demo","vers":"0.1.0"}`, nil))
	f.Add(fuzzFrame(`{"name":"demo","vers":"01.0.0"}`, nil))
	f.Add(fuzzFrame(`{"name":"demo"}`, nil))
	f.Add(fuzzFrame(`null`, nil))
	f.Add(fuzzFrame(`[]`, nil))
	f.Add(fuzzFrame(`{"name":"demo","vers":"0.1.0"}`, nil)[:20])
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Add(append(fuzzFrame(`{"name":"demo","vers":"0.1.0"}`, nil)[:4+len(`{"name":"demo","vers":"0.1.0"}`)], 0xff, 0xff, 0xff, 0xff))
	f.Add([]byte{})
}

// FuzzPublishFrame sends an arbitrary body to the publish endpoint. The frame
// is the protocol's untrusted input: whatever it holds, the handler answers
// one of the documented statuses and, per the protocol definition, accepts
// exactly the frames that decode into a valid crate name and version with the
// declared bytes present. An accepted crate is then stored under its checksum,
// listed once in the sparse index and downloadable, and the same version
// cannot be published again.
func FuzzPublishFrame(f *testing.F) {
	fuzzPublishSeeds(f)

	log, err := logger.New("error", "json", os.DevNull)
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		db := &cargoDB{DB: formattest.NewDB()}
		bs := formattest.NewBlobStore()
		pub := &formattest.Publisher{}
		h := NewHTTPHandler(log, db, bs, "http://localhost")
		h.SetPublisher(pub)

		w := call(h, http.MethodPut, "/api/v1/crates/new", bytes.NewReader(body))

		want := fuzzPublishWant(body)
		if want.status == http.StatusOK {
			if _, err := licensemeta.ReadBytes(t.Context(), "cargo", want.name+"-"+want.version+".crate", want.crate); licensemeta.Unsafe(err) {
				want.status = http.StatusUnprocessableEntity
			}
		}

		if w.Code != want.status {
			t.Fatalf("publish answered %d, want %d: %s", w.Code, want.status, w.Body.String())
		}

		if want.status != http.StatusOK {
			if len(db.crates) != 0 || len(bs.Blobs) != 0 || db.used != 0 || db.artifacts != 0 || len(pub.Events()) != 0 {
				t.Fatalf("refused publish (%d) left crates=%d blobs=%d used=%d artifacts=%d events=%d",
					want.status, len(db.crates), len(bs.Blobs), db.used, db.artifacts, len(pub.Events()))
			}

			return
		}

		fuzzCheckPublished(t, h, db, bs, want.name, want.version, want.crate)
		fuzzCheckRepublish(t, h, want.name, want.version, want.crate)
	})
}

// fuzzCheckPublished checks what an accepted publish of crate left behind.
func fuzzCheckPublished(t *testing.T, h *HTTPHandler, db *cargoDB, bs *formattest.BlobStore, name, version string, crate []byte) {
	t.Helper()

	sum := sha256.Sum256(crate)
	digest := hex.EncodeToString(sum[:])

	if len(db.crates) != 1 || db.used != int64(len(crate)) || db.artifacts != 1 || !bytes.Equal(bs.Blobs[digest], crate) {
		t.Fatalf("accepted crate stored as crates=%d used=%d artifacts=%d blob=%v", len(db.crates), db.used, db.artifacts, bs.Blobs[digest] != nil)
	}

	if c := db.crates[0]; c.Name != name || c.Version != version || c.Checksum != digest || c.SizeBytes != int64(len(crate)) || !json.Valid(c.MetadataJSON) {
		t.Fatalf("stored row %+v does not match the published %s %s", c, name, version)
	}

	fuzzCheckServed(t, h, name, version, digest, crate)
}

// fuzzCheckServed checks the index and the download of an accepted crate.
func fuzzCheckServed(t *testing.T, h *HTTPHandler, name, version, digest string, crate []byte) {
	t.Helper()

	// The index lists exactly this version of the crate, once.
	idx := call(h, http.MethodGet, "/index/"+pkgcargo.IndexPathFor(strings.ToLower(name)), nil)
	if idx.Code != http.StatusOK {
		t.Fatalf("index of %s answered %d: %s", name, idx.Code, idx.Body.String())
	}

	entries, err := pkgcargo.ParseIndex(idx.Body.Bytes())
	if err != nil || len(entries) != 1 || entries[0].Vers != version {
		t.Fatalf("index of %s is %q (%v), want one entry for %s", name, idx.Body.String(), err, version)
	}

	var line struct {
		Name   string  `json:"name"`
		Cksum  string  `json:"cksum"`
		Yanked bool    `json:"yanked"`
		Deps   *[]any  `json:"deps"`
		Links  *string `json:"links"`
	}

	if err := json.Unmarshal(entries[0].Line, &line); err != nil || line.Name != name || line.Cksum != digest || line.Yanked || line.Deps == nil {
		t.Fatalf("index entry %s is not the published crate (%v)", entries[0].Line, err)
	}

	// And it downloads, byte for byte.
	dl := call(h, http.MethodGet, "/api/v1/crates/"+name+"/"+version+"/download", nil)
	if dl.Code != http.StatusOK || !bytes.Equal(dl.Body.Bytes(), crate) || dl.Header().Get("ETag") != `"`+digest+`"` {
		t.Fatalf("download answered %d with %d bytes, want the %d published", dl.Code, dl.Body.Len(), len(crate))
	}
}

// fuzzCheckRepublish checks that a published version is final: the same
// version, the same name in another case and the same version with other
// build metadata are all conflicts, decided before the crate is read.
func fuzzCheckRepublish(t *testing.T, h *HTTPHandler, name, version string, crate []byte) {
	t.Helper()

	other := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z':
			return r - 'A' + 'a'
		}

		return r
	}, name)

	for _, c := range []struct{ name, version string }{
		{name, version},
		{other, version},
		{name, versionIdentity(version) + "+again"},
	} {
		meta, _ := json.Marshal(map[string]string{"name": c.name, "vers": c.version})
		if w := call(h, http.MethodPut, "/api/v1/crates/new", bytes.NewReader(fuzzFrame(string(meta), crate))); w.Code != http.StatusConflict {
			t.Fatalf("republishing %s %s as %s %s answered %d, want 409: %s", name, version, c.name, c.version, w.Code, w.Body.String())
		}
	}
}

// FuzzCargoHandlerParse feeds an arbitrary body to the format handler's
// validator and parser: they agree, and the memory they use follows the data
// they are given, not the length the first four bytes declare.
func FuzzCargoHandlerParse(f *testing.F) {
	f.Add(fuzzFrame(`{"name":"demo","vers":"0.1.0"}`, nil))
	f.Add(fuzzFrame(`{"name":"demo"}`, nil))
	f.Add(fuzzFrame(`null`, nil))
	f.Add([]byte{3, 0, 0, 0, '{', '}'})
	f.Add([]byte{})

	const budget = 64 << 20

	f.Fuzz(func(t *testing.T, data []byte) {
		h := NewCargoHandler()

		var before, after runtime.MemStats

		runtime.ReadMemStats(&before)

		verr := h.Validate(context.Background(), bytes.NewReader(data))
		meta, perr := h.Parse(context.Background(), bytes.NewReader(data))

		runtime.ReadMemStats(&after)

		if used := after.TotalAlloc - before.TotalAlloc; used > budget+uint64(len(data))*64 {
			t.Fatalf("validating and parsing %d bytes allocated %d bytes", len(data), used)
		}

		if perr != nil && meta != nil {
			t.Fatalf("Parse returned metadata with error %v", perr)
		}

		// The publish endpoint refuses metadata over the cap; so do they.
		if len(data) >= 4 && binary.LittleEndian.Uint32(data) > maxPublishMetadataBytes && (verr == nil || perr == nil) {
			t.Fatalf("a frame declaring %d metadata bytes was accepted (%v, %v)", binary.LittleEndian.Uint32(data), verr, perr)
		}

		// Whatever Validate accepts has the two fields Parse hands back.
		if verr == nil {
			_, hasName := meta["name"]
			_, hasVers := meta["vers"]

			if perr != nil || !hasName || !hasVers {
				t.Fatalf("Validate accepted a body Parse reads as %v (%v)", meta, perr)
			}
		}
	})
}

// FuzzCrateValidators checks the publish name and version grammars against the
// registry path parser and SemVer: whatever a publish accepts, cargo can ask
// the index and the download endpoint for.
func FuzzCrateValidators(f *testing.F) {
	for _, c := range [][2]string{
		{"demo", "0.1.0"}, {"Demo_X-1", "1.2.3-rc.1+build.5"}, {"a", "0.0.0"}, {"_", "1.0.0-0"}, {"ab", "1.0.0-a.01"},
		{"é", "1.0.0"}, {"a b", "1.0.0"}, {"1a", "1.0.0"}, {"demo", "1.0"}, {"demo", "01.2.3"}, {"demo", "1.2.3+"}, {"", ""},
		{strings.Repeat("a", 65), "1.0.0"},
	} {
		f.Add(c[0], c[1])
	}

	f.Fuzz(func(t *testing.T, name, version string) {
		fuzzCheckCrateName(t, name)
		fuzzCheckCrateVersion(t, version)

		if validCrateName(name) && validCrateVersion(version) {
			p, err := pkgcargo.ParsePath("api/v1/crates/" + name + "/" + version + "/download")
			if err != nil || p.Kind != pkgcargo.KindDownload || p.Crate != name || p.Version != version {
				t.Fatalf("cargo cannot download %q %q: %+v, %v", name, version, p, err)
			}
		}
	})
}

func fuzzCheckCrateName(t *testing.T, name string) {
	t.Helper()

	if !validCrateName(name) {
		return
	}

	if len(name) > 64 || name[0] >= '0' && name[0] <= '9' || name[0] == '-' {
		t.Fatalf("validCrateName(%q)", name)
	}

	lower := strings.ToLower(name)

	p, err := pkgcargo.ParsePath("index/" + pkgcargo.IndexPathFor(lower))
	if err != nil || p.Kind != pkgcargo.KindIndex || p.Crate != lower {
		t.Fatalf("cargo cannot ask for the index of the valid crate %q: %+v, %v", name, p, err)
	}
}

func fuzzCheckCrateVersion(t *testing.T, version string) {
	t.Helper()

	if !validCrateVersion(version) {
		return
	}

	if !semver.IsValid("v"+version) || len(version) > 100 || versionIdentity(version) == "" || strings.Contains(versionIdentity(version), "+") {
		t.Fatalf("validCrateVersion(%q)", version)
	}

	if !validCrateVersion(versionIdentity(version)) {
		t.Fatalf("version %q is valid but its identity %q is not", version, versionIdentity(version))
	}
}

// FuzzTransformCargoIndexDeps translates arbitrary publish "deps" JSON into
// index dependencies: always a list that marshals, and one entry per object.
func FuzzTransformCargoIndexDeps(f *testing.F) {
	f.Add(`[{"name":"serde","version_req":"^1.0","features":["derive"],"optional":true,"default_features":false,"target":"cfg(windows)","kind":"dev","registry":null},{"name":"rand","explicit_name_in_toml":"random","version_req":">=0.8"}]`)
	f.Add(`[null,1,"x",[],{}]`)
	f.Add(`{"name":"x"}`)
	f.Add(`null`)
	f.Add(`[{"name":5,"explicit_name_in_toml":"","features":null,"kind":null}]`)

	f.Fuzz(func(t *testing.T, raw string) {
		var deps any
		if json.Unmarshal([]byte(raw), &deps) != nil {
			return
		}

		got := transformCargoIndexDeps(deps)
		if got == nil {
			t.Fatalf("transformCargoIndexDeps(%s) = nil, marshals as null", raw)
		}

		objects := 0

		if list, ok := deps.([]any); ok {
			for _, d := range list {
				if _, isObj := d.(map[string]any); isObj {
					objects++
				}
			}
		}

		if len(got) != objects {
			t.Fatalf("%d index dependencies for %d dependency objects in %s", len(got), objects, raw)
		}

		for _, d := range got {
			for _, key := range []string{"name", "req", "features", "optional", "default_features", "target", "kind", "registry", "package"} {
				if _, ok := d[key]; !ok {
					t.Fatalf("index dependency %v lacks %q", d, key)
				}
			}
		}

		if _, err := json.Marshal(got); err != nil {
			t.Fatalf("index dependencies of %s do not marshal: %v", raw, err)
		}
	})
}
