// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"context"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

type gomodRestoreDB struct {
	*databasetest.BaseMockDB

	created []*models.GoModule
}

func (d *gomodRestoreDB) CreateGoModule(_ context.Context, m *models.GoModule) error {
	d.created = append(d.created, m)

	return nil
}

// A backup taken on <= 0.4.1 has an empty ZipHash and the shared zip's key in
// ZipBlobKey: restoring it would recreate the shared blobs row or serve another
// module's zip, so the row is skipped and reported, and the restore goes on.
func TestRestoreGoModuleSkipsRowsWithoutZipDigest(t *testing.T) {
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	db := &gomodRestoreDB{BaseMockDB: &databasetest.BaseMockDB{}}
	e := NewEngine(db, nil, log, t.TempDir())

	legacy := &models.GoModule{ModulePath: "example.com/b", Version: "v1.0.0", ZipBlobKey: strings.Repeat("a", 64)}
	if err := e.restoreGoModule(context.Background(), legacy); err != nil {
		t.Fatalf("restoreGoModule(legacy) = %v, want the restore to continue", err)
	}

	if len(db.created) != 0 {
		t.Fatalf("legacy row was restored: %+v", db.created)
	}

	if got := e.restoreFailures.Load(); got != 1 {
		t.Errorf("restoreFailures = %d, want 1 (reported as incomplete)", got)
	}

	good := &models.GoModule{ModulePath: "example.com/c", Version: "v1.0.0", ZipHash: strings.Repeat("b", 64), ZipBlobKey: strings.Repeat("b", 64)}
	if err := e.restoreGoModule(context.Background(), good); err != nil || len(db.created) != 1 {
		t.Errorf("restoreGoModule(valid) = %v, created %d; want 1 created", err, len(db.created))
	}
}
