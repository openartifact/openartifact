// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbt

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
)

const artifactURL = "/repos/myrepo/org.example/mod/1.0/mod-1.0.jar"

type sbtDB struct {
	*formattest.DB

	art *models.SBTArtifact
}

func (d *sbtDB) CreateSBTArtifact(_ context.Context, a *models.SBTArtifact) error {
	a.ID = "a-1"
	d.art = a

	return nil
}

func (d *sbtDB) GetSBTArtifact(_ context.Context, _, _, _, _, _ string) (*models.SBTArtifact, error) {
	return d.art, nil
}

func (d *sbtDB) DeleteSBTArtifact(_ context.Context, _ string) error {
	d.art = nil
	return nil
}

func newTestHandler(t *testing.T) (*HTTPHandler, *sbtDB, *formattest.BlobStore, *formattest.Publisher) {
	t.Helper()

	db := &sbtDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	return h, db, bs, pub
}

func TestUploadAndDeleteEmitEvents(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)

	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, artifactURL, strings.NewReader("data")))
	if w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", w.Code, w.Body.String())
	}

	evs := pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].RepoID != "repo-1" {
		t.Fatalf("events = %+v", evs)
	}

	if a := evs[0].Artifact(); a["name"] != "org.example/mod" || a["version"] != "1.0" || a["path"] != "org.example/mod/1.0/mod-1.0.jar" {
		t.Errorf("artifact = %v", a)
	}

	if _, ok := bs.Blobs[db.art.SHA256]; !ok {
		t.Fatal("blob not stored")
	}

	if w = formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodDelete, artifactURL, nil)); w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", w.Code)
	}

	evs = pub.Events()
	if len(evs) != 2 || evs[1].Type != webhook.EventArtifactDeleted {
		t.Fatalf("events after delete = %+v", evs)
	}

	if len(bs.Blobs) != 0 {
		t.Error("blob not removed after delete")
	}
}

func TestDownloadQuarantine(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.art = &models.SBTArtifact{ID: "a", BlobKey: "abc", SHA256: "abc", SizeBytes: 3}
	bs.Blobs["abc"] = []byte("xyz")

	get := func() int {
		return formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, artifactURL, nil)).Code
	}

	if c := get(); c != http.StatusOK {
		t.Fatalf("status = %d, want 200", c)
	}

	db.Keys["org.example/mod/1.0/mod-1.0.jar"] = true

	if c := get(); c != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", c)
	}
}
