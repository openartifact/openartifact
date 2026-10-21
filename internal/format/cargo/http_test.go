// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
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

type cargoDB struct {
	*formattest.DB

	crate *models.CargoCrate
}

func (d *cargoDB) CreateCargoCrate(_ context.Context, c *models.CargoCrate) error {
	d.crate = c
	return nil
}

func (d *cargoDB) GetCargoCrate(_ context.Context, _, _, _ string) (*models.CargoCrate, error) {
	return d.crate, nil
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

	if a := evs[0].Artifact(); a["name"] != "demo" || a["version"] != "0.1.0" || a["sha256"] != db.crate.Checksum {
		t.Errorf("artifact = %v", a)
	}

	if _, ok := bs.Blobs[db.crate.Checksum]; !ok {
		t.Error("blob not stored")
	}
}

func TestDownloadQuarantine(t *testing.T) {
	h, db, bs, _ := newCargoHandler(t)
	db.crate = &models.CargoCrate{Name: "demo", Version: "0.1.0", BlobKey: "abc", Checksum: "abc"}
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

	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	var body bytes.Buffer

	_ = binary.Write(&body, binary.LittleEndian, uint32(len(meta)))
	body.WriteString(meta)

	crateData := []byte("crate-bytes")
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
		if db.crate != nil || len(bs.Blobs) != 0 || len(pub.Events()) != 0 {
			t.Fatalf("rejected upload stored something: crate=%v blobs=%d events=%d", db.crate, len(bs.Blobs), len(pub.Events()))
		}

		return
	}

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	if db.crate == nil || len(bs.Blobs) != 1 || len(pub.Events()) != 1 {
		t.Fatalf("admitted upload not stored: crate=%v blobs=%d events=%d", db.crate, len(bs.Blobs), len(pub.Events()))
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
