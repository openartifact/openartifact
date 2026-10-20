// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

type poolDB struct {
	*formattest.DB
}

func (d *poolDB) GetComponentByFilename(_ context.Context, _, filename string) (*models.Component, error) {
	if filename != "hello_1.0_amd64.deb" {
		return nil, nil
	}

	return &models.Component{ID: "pkg-1", SHA256: "abc"}, nil
}

func poolRequest() *http.Request {
	return formattest.Request(http.MethodGet, "/repos/myrepo/pool/main/h/hello/hello_1.0_amd64.deb", nil)
}

func TestPoolFileQuarantine(t *testing.T) {
	for _, key := range []string{"pkg-1", "pool/main/h/hello/hello_1.0_amd64.deb", "hello_1.0_amd64.deb"} {
		db := &poolDB{DB: formattest.NewDB()}
		bs := formattest.NewBlobStore()
		bs.Blobs["abc"] = []byte("deb-bytes")
		db.Keys[key] = true

		h := NewHTTPHandler(formattest.NewLogger(t), db, bs)

		w := formattest.Do(func(r chi.Router) { h.RegisterRepoRoutes(r) }, poolRequest())
		if w.Code != http.StatusForbidden {
			t.Errorf("key %q: status = %d, want 403", key, w.Code)
		}
	}
}

func TestPoolFileNotQuarantined(t *testing.T) {
	db := &poolDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	bs.Blobs["abc"] = []byte("deb-bytes")

	h := NewHTTPHandler(formattest.NewLogger(t), db, bs)

	w := formattest.Do(func(r chi.Router) { h.RegisterRepoRoutes(r) }, poolRequest())
	if w.Code != http.StatusOK || w.Body.String() != "deb-bytes" {
		t.Fatalf("status = %d body = %q", w.Code, w.Body.String())
	}
}
