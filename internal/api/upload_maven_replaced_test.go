// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// mavenReplacingDB reports a replaced row on the write, as the store does when
// a snapshot overwrite takes over content a concurrent upload stored, and
// counts the usage the handler reserves and releases.
type mavenReplacingDB struct {
	*mavenRepoDB

	replaced *models.ReplacedBlob
	used     int64
	count    int
}

func (d *mavenReplacingDB) ReserveStorageUsage(_ context.Context, _ string, size int64, count int) (bool, error) {
	d.used += size
	d.count += count

	return true, nil
}

func (d *mavenReplacingDB) DecrementStorageUsage(_ context.Context, _ string, size int64, count int) error {
	d.used -= size
	d.count -= count

	return nil
}

func (d *mavenReplacingDB) BlobReferenceCount(context.Context, string) (int, error) { return 1, nil }

func (d *mavenReplacingDB) CreateMavenArtifact(ctx context.Context, a *models.MavenArtifact) error {
	if err := d.mavenRepoDB.CreateMavenArtifact(ctx, a); err != nil {
		return err
	}

	a.Replaced = d.replaced

	return nil
}

func TestMavenPutReleasesTheReplacedRow(t *testing.T) {
	e := newMavenParentEnv(t)
	db := &mavenReplacingDB{mavenRepoDB: e.db, replaced: &models.ReplacedBlob{SHA256: "old", SizeBytes: 7}}
	db.used, db.count = 7, 1
	e.handler.db = db

	body := pomXML("com.acme", "app", "1", "", "", "")
	if rec := e.put("com.acme", "app", "1", body); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	// The 7 bytes the write replaced are given back; only the new pom counts.
	if want := int64(len(body)); db.used != want || db.count != 1 {
		t.Errorf("usage = %d bytes / %d artifacts, want %d / 1", db.used, db.count, want)
	}
}
