// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ivy

import (
	"context"
	"path"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/format/filerepo"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// HTTPHandler serves hosted Ivy repositories. Ivy publishes with
// overwrite="true", so re-publishing different bytes replaces the file unless
// the repository has immutable releases and the version is a release.
type HTTPHandler struct {
	*filerepo.Handler[models.IvyArtifact]
}

// extOf is the artifact extension the table is keyed by: the file name's last
// dot suffix without the dot.
func extOf(filename string) string { return strings.TrimPrefix(path.Ext(filename), ".") }

var spec = filerepo.Spec[models.IvyArtifact]{ //nolint:gochecknoglobals // immutable format description
	Format:    "ivy",
	TrashKind: models.TrashKindIvy,
	GroupKey:  "org",
	Get: func(ctx context.Context, db database.Database, repoID string, c base.ArtifactPath) (*models.IvyArtifact, error) {
		return db.GetIvyArtifact(ctx, repoID, c.Group, c.Module, c.Version, c.Filename, extOf(c.Filename))
	},
	New: func(repoID string, c base.ArtifactPath, size int64, sha256Hex string) *models.IvyArtifact {
		return &models.IvyArtifact{
			RepositoryID: repoID,
			Organisation: c.Group,
			Module:       c.Module,
			Revision:     c.Version,
			ArtifactName: c.Filename,
			ArtifactType: "jar",
			ArtifactExt:  extOf(c.Filename),
			SizeBytes:    size,
			SHA256:       sha256Hex,
		}
	},
	Create: func(ctx context.Context, db database.Database, a *models.IvyArtifact) error {
		return db.CreateIvyArtifact(ctx, a)
	},
	Delete: func(ctx context.Context, db database.Database, id string) error {
		return db.DeleteIvyArtifact(ctx, id)
	},
	View: func(a *models.IvyArtifact) filerepo.Row {
		return filerepo.Row{ID: a.ID, SHA256: a.SHA256, BlobKey: a.BlobKey, SizeBytes: a.SizeBytes}
	},
	Stamp: func(a *models.IvyArtifact, blobKey string, createdAt time.Time) {
		a.BlobKey = blobKey
		a.CreatedAt = createdAt
	},
}

func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: filerepo.New(spec, log, db, blobStore, baseURL)}
}
