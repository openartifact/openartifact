// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
)

// regenerateRestored refreshes the generated metadata of the repository an
// item was restored into, through the Restored hook of its format. Formats
// without one compute their indexes live from the tables; restoring a whole
// repository changes no index. Failures are logged: the restore itself already
// succeeded.
func (h *TrashHandler) regenerateRestored(ctx context.Context, item *models.TrashItem) {
	if h.workers == nil || item.Kind == models.TrashKindRepository {
		return
	}

	hooks, ok := lifecycle.Lookup(item.Format)
	if !ok || hooks.Restored == nil {
		return
	}

	hooks.Restored(ctx, &lifecycle.Env{
		Log:                h.log,
		Workers:            h.workers,
		MarkRepoStale:      h.db.MarkRepoStale,
		MarkPackumentStale: h.db.MarkPackumentStale,
	}, lifecycle.Restoration{
		RepositoryID:   item.RepositoryID,
		RepositoryName: item.RepositoryName,
		Kind:           item.Kind,
		RegenKeys: func(ctx context.Context) (string, string, error) {
			store, _ := h.db.(database.TrashOps)
			if store == nil {
				return "", "", errNoRegenKeys
			}

			return store.RegenKeys(ctx, item.Kind, item.ID) //nolint:wrapcheck // the caller logs it with context
		},
	})
}

// errNoRegenKeys: the store cannot resolve the keys a regeneration needs.
var errNoRegenKeys = errors.New("store cannot resolve regeneration keys")
