// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package quayapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/webhook"
)

const (
	defaultTagLimit = 50
	maxTagLimit     = 100
)

type tagJSON struct {
	Name           string `json:"name"`
	ManifestDigest string `json:"manifest_digest"`
	StartTS        int64  `json:"start_ts"`
}

// tagMethods routes the sub-resources of /repository/{org}/{repo}/tag: the
// listing and tag/{name}. It returns nil for any other shape.
func tagMethods(rest []string) func(*repoCall) methods {
	switch len(rest) {
	case 0:
		return func(rc *repoCall) methods { return methods{http.MethodGet: rc.listTags} }
	case 1:
		return func(rc *repoCall) methods { return methods{http.MethodDelete: func() { rc.deleteTag(rest[0]) }} }
	}

	return nil
}

// positiveInt reads a query parameter as an integer of at least 1, def when absent or invalid.
func positiveInt(raw string, def int) int {
	if n, err := strconv.Atoi(raw); err == nil && n >= 1 {
		return n
	}

	return def
}

func (rc *repoCall) listTags() {
	query := rc.r.URL.Query()
	page := positiveInt(query.Get("page"), 1)
	limit := min(positiveInt(query.Get("limit"), defaultTagLimit), maxTagLimit)

	tags, more, err := rc.h.db.ListQuayTags(rc.r.Context(), rc.org.ID, rc.q.Name, page, limit)
	if err != nil {
		rc.internalError("list tags", err)
		return
	}

	out := make([]tagJSON, 0, len(tags))
	for _, t := range tags {
		out = append(out, tagJSON{Name: t.Name, ManifestDigest: t.ManifestDigest, StartTS: t.UpdatedAt.Unix()})
	}

	writeJSON(rc.w, http.StatusOK, map[string]any{"tags": out, "page": page, "has_additional": more})
}

// deleteTag deletes one tag the way the registry does: through the trash,
// refused with 409 for a release of an immutable repository.
func (rc *repoCall) deleteTag(tagName string) {
	ctx := rc.r.Context()

	tag, err := rc.h.db.GetDockerTag(ctx, rc.org.ID, rc.q.Name, tagName)
	if err != nil {
		rc.internalError("look up tag", err)
		return
	}

	if tag == nil {
		writeError(rc.w, http.StatusNotFound, "Tag not found")
		return
	}

	if err := trash.GuardRelease(rc.r, rc.org, "docker", tagName); err != nil {
		rc.deleteTagError(err)
		return
	}

	name := rc.org.Name + "/" + rc.q.Name

	err = trash.Delete(ctx, rc.h.db, trash.Request{
		RepoID:   rc.org.ID,
		Kind:     models.TrashKindDockerTag,
		IDs:      []string{tag.ID},
		Resource: name + ":" + tagName,
	}, func() error {
		return rc.h.db.DeleteDockerTag(ctx, rc.org.ID, rc.q.Name, tagName)
	})
	if err != nil {
		rc.deleteTagError(err)
		return
	}

	webhook.Emit(ctx, rc.h.publisher, rc.org.ID, webhook.EventArtifactDeleted,
		webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
			Repository: rc.org.Name,
			Format:     "docker",
			Name:       name,
			Version:    tagName,
			Path:       name + "/manifests/" + tagName,
			SHA256:     strings.TrimPrefix(tag.ManifestDigest, "sha256:"),
		}))

	noContent(rc.w)
}

func (rc *repoCall) deleteTagError(err error) {
	switch {
	case errors.Is(err, database.ErrTrashNotFound):
		writeError(rc.w, http.StatusNotFound, "Tag not found")
	case errors.Is(err, trash.ErrImmutableRelease):
		writeError(rc.w, http.StatusConflict, "Released versions are immutable in this repository")
	default:
		rc.internalError("delete tag", err)
	}
}
