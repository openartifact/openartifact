// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/licensemeta/archivetest"
	"github.com/openartifact/openartifact/internal/licensepolicy/policytest"
	"github.com/openartifact/openartifact/internal/webhook"
)

// TestTransformCargoIndexDeps exercises the publish-API -> registry-index
// dependency schema translation: "version_req" -> "req", and a
// rename's original/alias names swap between name/explicit_name_in_toml and
// name/package. See https://doc.rust-lang.org/cargo/reference/registry-index.html
func TestTransformCargoIndexDeps(t *testing.T) {
	raw := `[
		{
			"name": "serde",
			"version_req": "^1.0",
			"features": ["derive"],
			"optional": true,
			"default_features": false,
			"target": "cfg(windows)",
			"kind": "dev",
			"registry": null
		},
		{
			"name": "rand",
			"explicit_name_in_toml": "random",
			"version_req": ">=0.8, <0.9"
		}
	]`

	var deps []any
	if err := json.Unmarshal([]byte(raw), &deps); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	got := transformCargoIndexDeps(deps)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}

	serde := got[0]
	if serde["name"] != "serde" {
		t.Errorf("serde name = %v, want serde", serde["name"])
	}

	if serde["req"] != "^1.0" {
		t.Errorf("serde req = %v, want ^1.0 (from version_req)", serde["req"])
	}

	if serde["package"] != nil {
		t.Errorf("serde package = %v, want nil (not renamed)", serde["package"])
	}

	if serde["kind"] != "dev" {
		t.Errorf("serde kind = %v, want dev", serde["kind"])
	}

	renamed := got[1]
	if renamed["name"] != "random" {
		t.Errorf("renamed name = %v, want random (the Cargo.toml alias)", renamed["name"])
	}

	if renamed["package"] != "rand" {
		t.Errorf("renamed package = %v, want rand (the original crate name)", renamed["package"])
	}

	if renamed["req"] != ">=0.8, <0.9" {
		t.Errorf("renamed req = %v, want >=0.8, <0.9", renamed["req"])
	}

	// Fields the publish payload omitted entirely must fall back to the
	// index schema's documented defaults, not be dropped.
	if v, ok := renamed["features"].([]any); !ok || len(v) != 0 {
		t.Errorf("renamed features = %#v, want empty slice default", renamed["features"])
	}

	if renamed["optional"] != false {
		t.Errorf("renamed optional = %v, want false default", renamed["optional"])
	}

	if renamed["default_features"] != true {
		t.Errorf("renamed default_features = %v, want true default", renamed["default_features"])
	}

	if renamed["kind"] != "normal" {
		t.Errorf("renamed kind = %v, want normal default", renamed["kind"])
	}
}

func TestTransformCargoIndexDepsNilOrMalformed(t *testing.T) {
	if got := transformCargoIndexDeps(nil); len(got) != 0 {
		t.Errorf("nil deps = %#v, want empty slice", got)
	}

	if got := transformCargoIndexDeps("not an array"); len(got) != 0 {
		t.Errorf("malformed deps = %#v, want empty slice", got)
	}
}

// cargoDB keeps crates in publish order and looks crate names up
// case-insensitively, as the SQL queries do.
type cargoDB struct {
	*formattest.DB
	quotaFake

	crates []*models.CargoCrate

	// createErr, when set, is returned by CreateCargoCrate (a lost publish race).
	createErr error

	// summaries and total are what SearchCargoCrateSummaries answers with.
	summaries []*models.CargoCrateSummary
	total     int
}

func (d *cargoDB) CreateCargoCrate(_ context.Context, c *models.CargoCrate) error {
	if d.createErr != nil {
		return d.createErr
	}

	d.crates = append(d.crates, c)

	return nil
}

func (d *cargoDB) GetCargoCrate(_ context.Context, _, name, version string) (*models.CargoCrate, error) {
	for _, c := range d.crates {
		if strings.EqualFold(c.Name, name) && c.Version == version {
			return c, nil
		}
	}

	return nil, nil
}

func (d *cargoDB) ListCargoCrateVersions(_ context.Context, _, name string) ([]*models.CargoCrate, error) {
	var out []*models.CargoCrate

	for _, c := range d.crates {
		if strings.EqualFold(c.Name, name) {
			out = append(out, c)
		}
	}

	return out, nil
}

func (d *cargoDB) ListCargoIndexVersions(ctx context.Context, repoID, name string) ([]*models.CargoCrate, error) {
	return d.ListCargoCrateVersions(ctx, repoID, name)
}

// CargoIndexStamp mirrors the real stamp: live versions plus yanked flags.
func (d *cargoDB) CargoIndexStamp(_ context.Context, _, name string) (string, error) {
	var b strings.Builder

	n := 0

	for _, c := range d.crates {
		if strings.EqualFold(c.Name, name) {
			n++

			fmt.Fprintf(&b, "%s@%s@%t;", c.Name, c.Version, c.Yanked)
		}
	}

	if n == 0 {
		return noIndexVersions, nil
	}

	return b.String(), nil
}

func (d *cargoDB) YankCargoCrate(_ context.Context, _, name, version string, yanked bool) error {
	for _, c := range d.crates {
		if strings.EqualFold(c.Name, name) && c.Version == version {
			c.Yanked = yanked
		}
	}

	return nil
}

func (d *cargoDB) SearchCargoCrateSummaries(_ context.Context, _, _ string, _ int) ([]*models.CargoCrateSummary, int, error) {
	return d.summaries, d.total, nil
}

func (d *cargoDB) IncrementCargoDownloadCount(_ context.Context, _, _, _ string) error { return nil }

func newCargoHandler(t *testing.T) (*HTTPHandler, *cargoDB, *formattest.BlobStore, *formattest.Publisher) {
	t.Helper()

	db := &cargoDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	return h, db, bs, pub
}

func TestPublishEmitsEvent(t *testing.T) {
	h, db, bs, pub := newCargoHandler(t)

	meta := []byte(`{"name":"demo","vers":"0.1.0"}`)
	crateData := []byte("crate-bytes")

	var body bytes.Buffer

	_ = binary.Write(&body, binary.LittleEndian, uint32(len(meta)))
	body.Write(meta)
	_ = binary.Write(&body, binary.LittleEndian, uint32(len(crateData)))
	body.Write(crateData)

	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, "/repos/myrepo/api/v1/crates/new", &body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	evs := pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].RepoID != "repo-1" {
		t.Fatalf("events = %+v", evs)
	}

	if len(db.crates) != 1 {
		t.Fatalf("stored crates = %d, want 1", len(db.crates))
	}

	if a := evs[0].Artifact(); a["name"] != "demo" || a["version"] != "0.1.0" || a["sha256"] != db.crates[0].Checksum {
		t.Errorf("artifact = %v", a)
	}

	if _, ok := bs.Blobs[db.crates[0].Checksum]; !ok {
		t.Error("blob not stored")
	}
}

func TestDownloadQuarantine(t *testing.T) {
	h, db, bs, _ := newCargoHandler(t)
	db.crates = []*models.CargoCrate{{Name: "demo", Version: "0.1.0", BlobKey: "abc", Checksum: "abc"}}
	bs.Blobs["abc"] = []byte("xyz")

	get := func() int {
		return formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, "/repos/myrepo/api/v1/crates/demo/0.1.0/download", nil)).Code
	}

	if c := get(); c != http.StatusOK {
		t.Fatalf("status = %d, want 200", c)
	}

	db.Keys["demo-0.1.0.crate"] = true

	if c := get(); c != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", c)
	}
}

func publishCrate(t *testing.T, db database.Database, bs *formattest.BlobStore, pub *formattest.Publisher, meta string) *httptest.ResponseRecorder {
	t.Helper()

	return publishCrateData(t, db, bs, pub, meta, plainCrate(t))
}

// plainCrate is a crate whose Cargo.toml declares no license: the publish
// metadata is the only license information.
func plainCrate(t *testing.T) []byte {
	t.Helper()

	return crateWith(t, "[package]\nname = \"demo\"\nversion = \"0.1.0\"\n")
}

// publishCrateData publishes crate with the given publish metadata JSON, the
// way cargo (or curl) sends it.
func publishCrateData(t *testing.T, db database.Database, bs *formattest.BlobStore, pub *formattest.Publisher, meta string, crateData []byte) *httptest.ResponseRecorder {
	t.Helper()

	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	var body bytes.Buffer

	_ = binary.Write(&body, binary.LittleEndian, uint32(len(meta)))
	body.WriteString(meta)

	_ = binary.Write(&body, binary.LittleEndian, uint32(len(crateData)))
	body.Write(crateData)

	return formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, "/repos/myrepo/api/v1/crates/new", &body))
}

func TestPublishLicensePolicy(t *testing.T) {
	scenarios := []struct {
		name    string
		license string
		cases   []policytest.Case
	}{
		{"GPL-3.0", "GPL-3.0", policytest.Cases("GPL-3.0")},
	}

	for _, s := range scenarios {
		for _, c := range s.cases {
			t.Run(s.name+"/"+c.Name, func(t *testing.T) {
				inner := &cargoDB{DB: formattest.NewDB()}
				bs := formattest.NewBlobStore()
				pub := &formattest.Publisher{}

				w := publishCrate(t, c.DB(inner), bs, pub, `{"name":"demo","vers":"0.1.0","license":"`+s.license+`"}`)
				c.AssertResponse(t, w.Code, w.Body.Bytes())

				assertStored(t, c, w.Code, inner, bs, pub)
			})
		}
	}
}

func assertStored(t *testing.T, c policytest.Case, status int, db *cargoDB, bs *formattest.BlobStore, pub *formattest.Publisher) {
	t.Helper()

	if c.Stopped() {
		if len(db.crates) != 0 || len(bs.Blobs) != 0 || len(pub.Events()) != 0 {
			t.Fatalf("rejected upload stored something: crates=%d blobs=%d events=%d", len(db.crates), len(bs.Blobs), len(pub.Events()))
		}

		return
	}

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	if len(db.crates) != 1 || len(bs.Blobs) != 1 || len(pub.Events()) != 1 {
		t.Fatalf("admitted upload not stored: crates=%d blobs=%d events=%d", len(db.crates), len(bs.Blobs), len(pub.Events()))
	}
}

func TestPublishLicensePolicyExpressionWithDeniedIdentifier(t *testing.T) {
	c := policytest.Case{
		Name:     "deny GPL-3.0",
		Want:     policytest.Blocked,
		Policies: []*models.LicensePolicy{policytest.Policy("block", nil, []string{"GPL-3.0"})},
	}

	inner := &cargoDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}

	w := publishCrate(t, c.DB(inner), bs, pub, `{"name":"demo","vers":"0.1.0","license":"MIT OR GPL-3.0"}`)
	c.AssertResponse(t, w.Code, w.Body.Bytes())
	assertStored(t, policytest.Case{Want: policytest.Blocked}, w.Code, inner, bs, pub)
}

func TestPublishLicensePolicyNoLicenseAdmitted(t *testing.T) {
	policies := []*models.LicensePolicy{policytest.Policy("block", []string{"MIT"}, nil)}

	for name, meta := range map[string]string{
		"no license":   `{"name":"demo","vers":"0.1.0"}`,
		"license file": `{"name":"demo","vers":"0.1.0","license_file":"LICENSE"}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := policytest.Case{Want: policytest.Admitted, Policies: policies}
			inner := &cargoDB{DB: formattest.NewDB()}
			bs := formattest.NewBlobStore()
			pub := &formattest.Publisher{}

			w := publishCrate(t, c.DB(inner), bs, pub, meta)
			c.AssertResponse(t, w.Code, w.Body.Bytes())
			assertStored(t, c, w.Code, inner, bs, pub)
		})
	}
}

func TestPublishRejectsOversizedDeclaredLengths(t *testing.T) {
	meta := []byte(`{"name":"demo","vers":"0.1.0"}`)

	cases := map[string]func(*bytes.Buffer){
		"metadata": func(b *bytes.Buffer) {
			_ = binary.Write(b, binary.LittleEndian, uint32(0xFFFFFFFF))
		},
		"crate": func(b *bytes.Buffer) {
			_ = binary.Write(b, binary.LittleEndian, uint32(len(meta)))
			b.Write(meta)
			_ = binary.Write(b, binary.LittleEndian, uint32(0xFFFFFFFF))
		},
	}

	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, bs, pub := newCargoHandler(t)

			var body bytes.Buffer

			build(&body)

			w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, "/repos/myrepo/api/v1/crates/new", &body))
			if w.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413: %s", w.Code, w.Body.String())
			}

			if len(bs.Blobs) != 0 || len(pub.Events()) != 0 {
				t.Error("oversized publish must not store or emit anything")
			}
		})
	}
}

func crateWith(t *testing.T, cargoToml string) []byte {
	t.Helper()

	return archivetest.TarGz(t,
		archivetest.File("demo-0.1.0/src/lib.rs", ""),
		archivetest.File("demo-0.1.0/Cargo.toml", cargoToml),
	)
}

// The publish metadata's license is client-supplied; policies must judge the
// Cargo.toml inside the crate.
func TestPublishLicensePolicyReadsTheCrate(t *testing.T) {
	deny := []*models.LicensePolicy{policytest.Policy("block", nil, []string{"GPL-3.0-only"})}

	tests := []struct {
		name string
		toml string
		meta string
		want policytest.Outcome
	}{
		{"no license in the publish metadata", "[package]\nname = \"demo\"\nlicense = \"GPL-3.0-only\"\n", `{"name":"demo","vers":"0.1.0"}`, policytest.Blocked},
		{"publish metadata claims MIT, crate is GPL", "[package]\nname = \"demo\"\nlicense = \"GPL-3.0-only\"\n", `{"name":"demo","vers":"0.1.0","license":"MIT"}`, policytest.Blocked},
		{"publish metadata claims GPL, crate is MIT", "[package]\nname = \"demo\"\nlicense = \"MIT\"\n", `{"name":"demo","vers":"0.1.0","license":"GPL-3.0-only"}`, policytest.Admitted},
		{"legacy slash expression", "[package]\nname = \"demo\"\nlicense = \"MIT/GPL-3.0-only\"\n", `{"name":"demo","vers":"0.1.0"}`, policytest.Blocked},
		{"crate declares nothing: publish metadata is the fallback", "[package]\nname = \"demo\"\n", `{"name":"demo","vers":"0.1.0","license":"GPL-3.0-only"}`, policytest.Blocked},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := policytest.Case{Want: tc.want, Policies: deny}
			inner := &cargoDB{DB: formattest.NewDB()}
			bs := formattest.NewBlobStore()
			pub := &formattest.Publisher{}

			w := publishCrateData(t, c.DB(inner), bs, pub, tc.meta, crateWith(t, tc.toml))
			c.AssertResponse(t, w.Code, w.Body.Bytes())
			assertStored(t, c, w.Code, inner, bs, pub)
		})
	}
}

func TestPublishRejectsUnsafeCrates(t *testing.T) {
	crates := map[string][]byte{
		"oversized Cargo.toml": crateWith(t, "[package]\nname = \"demo\"\nlicense = \"MIT\"\n# "+strings.Repeat("A", 2<<20)),
		"two Cargo.toml files": archivetest.TarGz(t,
			archivetest.File("demo-0.1.0/Cargo.toml", "[package]\nlicense = \"MIT\"\n"),
			archivetest.File("other/Cargo.toml", "[package]\nlicense = \"GPL-3.0-only\"\n")),
	}

	for name, crate := range crates {
		t.Run(name, func(t *testing.T) {
			inner := &cargoDB{DB: formattest.NewDB()}
			bs := formattest.NewBlobStore()
			pub := &formattest.Publisher{}

			w := publishCrateData(t, inner, bs, pub, `{"name":"demo","vers":"0.1.0","license":"MIT"}`, crate)
			if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "LICENSE_METADATA_UNSAFE") {
				t.Fatalf("want 422 LICENSE_METADATA_UNSAFE, got %d: %s", w.Code, w.Body.String())
			}

			if len(inner.crates) != 0 || len(bs.Blobs) != 0 || len(pub.Events()) != 0 {
				t.Fatal("rejected publish stored something")
			}
		})
	}
}

// A crate whose licenses cannot be derived is judged on the publish metadata's
// license only when no blocking policy applies; with one it is refused whatever
// the metadata claims.
func TestPublishUnderivableLicenses(t *testing.T) {
	crates := map[string][]byte{
		"not a tarball":      []byte("junk"),
		"no Cargo.toml":      archivetest.TarGz(t, archivetest.File("demo-0.1.0/src/lib.rs", "")),
		"invalid Cargo.toml": crateWith(t, "[package\nlicense ="),
	}

	for name, crate := range crates {
		for _, c := range policytest.UnderivableCases() {
			t.Run(name+"/"+c.Name, func(t *testing.T) {
				inner := &cargoDB{DB: formattest.NewDB()}
				bs := formattest.NewBlobStore()
				pub := &formattest.Publisher{}

				w := publishCrateData(t, c.DB(inner), bs, pub, `{"name":"demo","vers":"0.1.0","license":"MIT"}`, crate)
				c.AssertResponse(t, w.Code, w.Body.Bytes())

				stored := len(inner.crates) != 0 || len(bs.Blobs) != 0 || len(pub.Events()) != 0
				if c.Stopped() && stored {
					t.Fatal("rejected publish stored something")
				}

				if !c.Stopped() && len(inner.crates) != 1 {
					t.Fatal("admitted publish not stored")
				}
			})
		}
	}
}
