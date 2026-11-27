// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	formatgitlfs "github.com/openartifact/openartifact/internal/format/gitlfs"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/format/storage"
)

// gitlfsFormat is the registration of the gitlfs format: hosted Git LFS
// repositories only (an LFS server is the origin of its objects; there is
// nothing to mirror or merge).
func gitlfsFormat() plugin.Format {
	return plugin.Definition{
		ID:        "gitlfs",
		NewHosted: baseURLHosted(formatgitlfs.NewHTTPHandler),
		Supply:    plugin.NoAnalysis,
		Snippets:  clientsetup.GitLFSBuilder,
		Mirror:    plugin.NoProxy,
		Merge:     plugin.NoGroup,
		Meta:      lifecycle.None,
		Store:     gitlfsStorage(),
	}
}

// gitlfsStorage describes how the generic engines treat Git LFS rows: every
// object is a package of its own named by its oid, so retention ranks the
// objects of the whole repository newest first (keep_last_n keeps the newest N
// objects, age-based cleanup removes the old ones).
func gitlfsStorage() storage.Storage {
	return storage.Storage{
		Trash: storage.Trash{Kind: string(models.TrashKindGitLFS)},
		Cleanup: storage.Cleanup{
			Partition:   []string{"f.repository_id"},
			VersionExpr: "f.path",
		},
	}
}
