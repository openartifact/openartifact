// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
	pkgrpm "github.com/openartifact/openartifact/pkg/rpm"
)

type weakDepsDB struct {
	databasetest.BaseMockDB

	repoID, sha string
	extra       []byte
}

func (d *weakDepsDB) UpdateRpmPackageExtra(_ context.Context, repoID, sha string, extra []byte) error {
	d.repoID, d.sha, d.extra = repoID, sha, extra
	return nil
}

func TestBackfillWeakDepsReadsBlob(t *testing.T) {
	data, err := os.ReadFile("../../pkg/rpm/testdata/weakdeps.rpm")
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])

	if _, err := store.Put(context.Background(), bytes.NewReader(data), map[string]string{"sha256": sha}); err != nil {
		t.Fatal(err)
	}

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	db := &weakDepsDB{}
	w := NewRPMetadataWorker(db, store, nil, log, nil)
	pkgs := []pkgrpm.Package{{Name: "oa-weakdep-test", SHA256: sha}, {Name: "done", SHA256: "x", WeakDepsParsed: true, SizesParsed: true}}

	w.backfillFromBlob(context.Background(), "repo1", pkgs)

	if len(pkgs[0].Recommends) != 1 || pkgs[0].Recommends[0].Name != "oa-recommended" || !pkgs[0].WeakDepsParsed {
		t.Errorf("recommends not backfilled: %+v", pkgs[0])
	}

	if db.repoID != "repo1" || db.sha != sha || !bytes.Contains(db.extra, []byte(`"weak_deps":true`)) {
		t.Errorf("extra not persisted: %q %q %s", db.repoID, db.sha, db.extra)
	}
}
