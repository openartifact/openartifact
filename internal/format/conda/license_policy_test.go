// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"net/http"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/licensepolicy/policytest"
)

func infoTar(t *testing.T, indexJSON string) []byte {
	t.Helper()

	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "info/index.json", Mode: 0o644, Size: int64(len(indexJSON))}); err != nil {
		t.Fatal(err)
	}

	_, _ = tw.Write([]byte(indexJSON))
	_ = tw.Close()

	return buf.Bytes()
}

// condaArchive builds a .conda (zip with info-*.tar.zst).
func condaArchive(t *testing.T, indexJSON string) []byte {
	t.Helper()

	var zst bytes.Buffer

	enc, err := zstd.NewWriter(&zst)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = enc.Write(infoTar(t, indexJSON))
	_ = enc.Close()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	fw, _ := zw.Create("info-demo-1.0-py_0.tar.zst")
	_, _ = fw.Write(zst.Bytes())
	_ = zw.Close()

	return buf.Bytes()
}

// indexOf reads the package index of an in-memory archive.
func indexOf(ext string, data []byte) packageIndex {
	return readPackageIndex(ext, bytes.NewReader(data), int64(len(data)))
}

func TestDeclaredLicense(t *testing.T) {
	const index = `{"name":"demo","license":"GPL-3.0-only","license_family":"GPL"}`

	if got := indexOf(models.CondaExtConda, condaArchive(t, index)).License; got != "GPL-3.0-only" {
		t.Errorf(".conda license = %q", got)
	}

	for ext, data := range map[string][]byte{
		models.CondaExtTarBz2: []byte("not an archive"),
		models.CondaExtConda:  []byte("not an archive"),
	} {
		if got := indexOf(ext, data).License; got != "" {
			t.Errorf("garbage %s: license = %q, want empty", ext, got)
		}
	}
}

func TestUploadLicensePolicy(t *testing.T) {
	archive := condaArchive(t, `{"name":"demo","version":"1.0","license":"GPL-3.0-only","license_family":"GPL"}`)

	for _, c := range policytest.Cases("GPL-3.0") {
		t.Run(c.Name, func(t *testing.T) {
			db := &condaDB{DB: formattest.NewDB()}
			bs := formattest.NewBlobStore()
			pub := &formattest.Publisher{}
			h := NewHTTPHandler(formattest.NewLogger(t), c.DB(db), bs, "http://localhost")
			h.SetPublisher(pub)

			w := formattest.Do(h.RegisterRepoRoutes,
				formattest.Request(http.MethodPost, "/repos/myrepo/linux-64?filename=demo-1.0-py_0.conda", bytes.NewReader(archive)))
			c.AssertResponse(t, w.Code, w.Body.Bytes())

			if c.Want == policytest.Admitted {
				if w.Code != http.StatusCreated || db.pkg == nil || len(bs.Blobs) != 1 || len(pub.Events()) != 1 {
					t.Fatalf("not stored: status=%d pkg=%v blobs=%d events=%d", w.Code, db.pkg, len(bs.Blobs), len(pub.Events()))
				}

				return
			}

			if db.pkg != nil || len(bs.Blobs) != 0 || len(pub.Events()) != 0 {
				t.Fatalf("rejected upload stored something: pkg=%v blobs=%d events=%d", db.pkg, len(bs.Blobs), len(pub.Events()))
			}
		})
	}
}

func TestUploadWithoutLicenseAdmittedUnderLicensePolicy(t *testing.T) {
	db := &condaDB{DB: formattest.NewDB()}
	policy := policytest.Policy("block", []string{"MIT"}, nil)
	h := NewHTTPHandler(formattest.NewLogger(t), &policytest.DB{Database: db, Policies: []*models.LicensePolicy{policy}}, formattest.NewBlobStore(), "http://localhost")

	w := formattest.Do(h.RegisterRepoRoutes,
		formattest.Request(http.MethodPost, "/repos/myrepo/linux-64?filename=demo-1.0-py_0.conda", bytes.NewReader(condaArchive(t, `{"name":"demo"}`))))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
}
