// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
)

// deleteLiveFilesByID hard-deletes the live files among ids (versions and packages
// they leave empty go with them). A trashed file is left to the trash
// subsystem: the delete endpoints of the version-grain formats only ever acted
// on live rows.
func (p *core) deleteLiveFilesByID(ctx context.Context, repoID string, ids []string) error {
	live, err := p.q.FilterLivePackageFileIDs(ctx, dbsqlc.FilterLivePackageFileIDsParams{RepositoryID: repoID, Ids: ids})
	if err != nil {
		return fmt.Errorf("find live package files: %w", err)
	}

	if _, err := p.deleteFiles(ctx, repoID, live); err != nil {
		return err
	}

	return nil
}
