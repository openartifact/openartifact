// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbt

import (
	"context"
	"time"

	"github.com/openartifact/openartifact/internal/artifactpath"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/filerepo"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Store is the database the sbt handler needs: the file repository basics and
// the sbt artifact rows.
type Store interface {
	filerepo.HandlerStore
	CreateSBTArtifact(ctx context.Context, artifact *models.SBTArtifact) error
	GetSBTArtifact(ctx context.Context, repoID, groupID, artifactID, version, filename string) (*models.SBTArtifact, error)
	DeleteSBTArtifact(ctx context.Context, id string) error
}

// HTTPHandler serves hosted sbt repositories: different bytes replace a file
// unless the repository has immutable releases and the version is a release.
type HTTPHandler struct {
	*filerepo.Handler[models.SBTArtifact, Store]
}

var spec = filerepo.Spec[models.SBTArtifact, Store]{ //nolint:gochecknoglobals // immutable format description
	Format:    "sbt",
	TrashKind: models.TrashKindSBT,
	GroupKey:  "group",
	Get: func(ctx context.Context, db Store, repoID string, c artifactpath.Path) (*models.SBTArtifact, error) {
		return db.GetSBTArtifact(ctx, repoID, c.Group, c.Module, c.Version, c.Filename)
	},
	New: func(repoID string, c artifactpath.Path, size int64, sha256Hex string) *models.SBTArtifact {
		return &models.SBTArtifact{
			RepositoryID: repoID,
			GroupID:      c.Group,
			ArtifactID:   c.Module,
			Version:      c.Version,
			ScalaVersion: "",
			Filename:     c.Filename,
			SizeBytes:    size,
			SHA256:       sha256Hex,
		}
	},
	Create: func(ctx context.Context, db Store, a *models.SBTArtifact) error {
		return db.CreateSBTArtifact(ctx, a)
	},
	Delete: func(ctx context.Context, db Store, id string) error {
		return db.DeleteSBTArtifact(ctx, id)
	},
	View: func(a *models.SBTArtifact) filerepo.Row {
		return filerepo.Row{ID: a.ID, SHA256: a.SHA256, BlobKey: a.BlobKey, SizeBytes: a.SizeBytes}
	},
	Stamp: func(a *models.SBTArtifact, blobKey string, createdAt time.Time) {
		a.BlobKey = blobKey
		a.CreatedAt = createdAt
	},
}

func NewHTTPHandler(log *logger.Logger, db Store, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: filerepo.New(spec, log, db, blobStore, baseURL)}
}
