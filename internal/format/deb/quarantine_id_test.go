// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"context"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// poolFilesDB serves two pool files, each with its own row id.
type poolFilesDB struct {
	*formattest.DB

	rows map[string]*models.Component
}

func (d *poolFilesDB) GetComponentByFilename(_ context.Context, _, filename string) (*models.Component, error) {
	return d.rows[filename], nil
}

// TestPoolFileQuarantinedByRowID: a scan keyed by the package's row id (the id
// the package listing shows) blocks the download of that file only.
func TestPoolFileQuarantinedByRowID(t *testing.T) {
	db := &poolFilesDB{DB: formattest.NewDB(), rows: map[string]*models.Component{
		"hello_1.0_amd64.deb": {ID: "file-1", SHA256: "abc"},
		"hello_1.1_amd64.deb": {ID: "file-2", SHA256: "abc"},
	}}
	bs := formattest.NewBlobStore()
	bs.Blobs["abc"] = []byte("deb-bytes")
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs)

	formattest.AssertQuarantinedByID(t, &db.Quarantine, "file-1",
		formattest.StatusOf(func(r chi.Router) { h.RegisterRepoRoutes(r) }),
		"/repos/myrepo/pool/main/h/hello/hello_1.0_amd64.deb", "/repos/myrepo/pool/main/h/hello/hello_1.1_amd64.deb")
}
