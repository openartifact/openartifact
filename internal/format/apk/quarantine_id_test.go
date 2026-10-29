// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package apk

import (
	"context"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// apkDB serves stored packages and the quarantined keys.
type apkDB struct {
	HTTPStore

	formattest.Quarantine

	repos *formattest.DB
	pkgs  []*models.ApkPackage
}

func (d *apkDB) GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error) {
	return d.repos.GetRepositoryByName(ctx, name)
}

// IsArtifactQuarantined names the quarantine the fake uses (HTTPStore carries the method too).
func (d *apkDB) IsArtifactQuarantined(ctx context.Context, repoID string, keys []string) (bool, error) {
	return d.Quarantine.IsArtifactQuarantined(ctx, repoID, keys)
}

func (d *apkDB) GetApkPackageFile(_ context.Context, _, _, _, _, name, version string) (*models.ApkPackage, error) {
	for _, p := range d.pkgs {
		if p.Name == name && p.Version == version {
			return p, nil
		}
	}

	return nil, nil
}

func (*apkDB) IncrementApkDownloadCount(context.Context, string, string) error { return nil }

// TestDownloadQuarantinedByRowID: a scan keyed by the package's row id (the id
// the package listing shows) blocks the download of that package only.
func TestDownloadQuarantinedByRowID(t *testing.T) {
	db := &apkDB{repos: formattest.NewDB()}

	for _, v := range []struct{ id, version string }{{"file-1", "1.0-r0"}, {"file-2", "2.0-r0"}} {
		db.pkgs = append(db.pkgs, &models.ApkPackage{
			ID: v.id, Name: "demo", Version: v.version, BlobKey: "abc", SizeBytes: 3,
		})
	}

	bs := formattest.NewBlobStore()
	bs.Blobs["abc"] = []byte("xyz")
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")

	formattest.AssertQuarantinedByID(t, &db.Quarantine, "file-1", formattest.StatusOf(h.RegisterRepoRoutes),
		"/repos/myrepo/v3.19/main/x86_64/demo-1.0-r0.apk", "/repos/myrepo/v3.19/main/x86_64/demo-2.0-r0.apk")
}
