// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/gitlfs"
)

// GitLFSStore owns the Git LFS objects.
//
// Mapping of an object onto the generic package tables. An LFS object has no
// name or version beyond its content id, so each object is its own package; the
// listing, search, trash, quota and cleanup then see every object as one item
// (cleanup ranks and ages objects across the repository, see
// formats.gitlfsStorage):
//
//	packages          name = name_key = the oid (lower-case hex sha256), attrs {}
//	package_versions  version = version_key = '', metadata {}
//	package_files     path objects/ab/cd/<oid>, filename <oid>, the blob whose
//	                  sha256 is the oid (the oid is the digest, so the blob and
//	                  the row cannot disagree), role primary, listed, arch '',
//	                  part '', attrs {}
//
// Objects are immutable and uploading one again is idempotent: a live object is
// ErrDuplicate. An object that was trashed and is uploaded again is a new file
// under the same package.
type GitLFSStore struct{ *core }

// PutGitLFSObject stores the object's row. The blob must already be stored; an
// object that already exists live is ErrDuplicate.
func (p *GitLFSStore) PutGitLFSObject(ctx context.Context, obj *models.GitLFSObject) error {
	out, err := p.writePackage(ctx, &PackageWrite{
		RepoID:    obj.RepositoryID,
		Format:    "gitlfs",
		Name:      obj.OID,
		NameKey:   obj.OID,
		Exclusive: true,
		CreatedAt: obj.CreatedAt,
		Files: []FileWrite{{
			Path:     gitlfs.ObjectPath(obj.OID),
			Filename: obj.OID,
			Blob:     BlobWrite{SHA256: obj.OID, Key: obj.BlobKey, MediaType: "application/octet-stream", Size: obj.SizeBytes},
		}},
	})
	if err != nil {
		return fmt.Errorf("failed to put git lfs object: %w", err)
	}

	obj.ID, obj.CreatedAt, obj.UpdatedAt = out.Files[0].ID, out.Files[0].CreatedAt, out.Files[0].UpdatedAt

	return nil
}

// GetGitLFSObjects returns the live objects of the repository among oids, in no
// particular order; an oid that is not stored is absent.
func (p *GitLFSStore) GetGitLFSObjects(ctx context.Context, repoID string, oids []string) ([]*models.GitLFSObject, error) {
	rows, err := p.q.GetGitLFSObjects(ctx, dbsqlc.GetGitLFSObjectsParams{RepositoryID: repoID, Oids: oids})
	if err != nil {
		return nil, fmt.Errorf("failed to get git lfs objects: %w", err)
	}

	out := make([]*models.GitLFSObject, 0, len(rows))

	for _, r := range rows {
		out = append(out, &models.GitLFSObject{
			ID:           r.ID,
			RepositoryID: r.RepositoryID,
			OID:          r.Oid,
			SizeBytes:    r.SizeBytes,
			BlobKey:      r.BlobKey,
			CreatedAt:    r.CreatedAt,
			UpdatedAt:    r.UpdatedAt,
		})
	}

	return out, nil
}
