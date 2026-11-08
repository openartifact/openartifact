// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/ociref"
)

// Docker dependent tags (cosign .sig/.att/.sbom/.src, the referrers tag schema)
// are not artifacts of their own: they are never ranked by keep_last_n, matched
// by name_pattern or aged by keep_since_days, so a cleanup policy cannot keep
// the signatures of ten images and drop the images. They live and die with their
// subject manifest: a run that removes the tags holding an image also removes
// its dependent tags, and one whose subject is gone removes them too.

// dependentTag is the SQL condition (on the docker_tags alias col) that holds for
// a dependent tag; its pattern is the one internal/ociref parses.
func dependentTag(col string) string { return col + " ~ '" + ociref.TagPattern + "'" }

// dockerHead is the manifest a removed docker tag pointed at.
type dockerHead struct {
	Image  string `json:"image"`
	Digest string `json:"digest"`
}

// dockerTagHeads returns the manifests the tags (by id, live or trashed) point at.
func (w *CleanupWorker) dockerTagHeads(ctx context.Context, repoID string, ids []string) (map[string]dockerHead, error) {
	rows, err := w.db.GetDB().QueryContext(ctx,
		`SELECT id::text, image, manifest_digest FROM docker_tags WHERE repository_id = $1 AND id = ANY($2::uuid[])`,
		repoID, uuidArrayLiteral(ids))
	if err != nil {
		return nil, fmt.Errorf("query docker tag heads: %w", err)
	}

	defer func() { _ = rows.Close() }()

	heads := make(map[string]dockerHead, len(ids))

	for rows.Next() {
		var (
			id string
			h  dockerHead
		)

		if err := rows.Scan(&id, &h.Image, &h.Digest); err != nil {
			return nil, fmt.Errorf("scan docker tag head: %w", err)
		}

		heads[id] = h
	}

	return heads, rows.Err()
}

// subjectHeld renders the condition that head (a manifest digest expression)
// is the subject of a dependent tag of alias t, or an index listing it as a child.
func subjectHeld(subject, head string) string {
	return fmt.Sprintf(`(%[1]s = %[2]s OR EXISTS (
		SELECT 1 FROM docker_manifest_refs rr
		JOIN docker_manifests pm ON pm.id = rr.manifest_id AND pm.deleted_at IS NULL
		WHERE rr.repository_id = t.repository_id AND rr.kind = 'manifest' AND rr.image = t.image
		  AND rr.digest = %[1]s AND pm.digest = %[2]s))`, subject, head)
}

// orphanDependentsSQL selects the live dependent tags that have lost their
// subject: the subject manifest no longer exists, or a removed tag (the head
// arrays) held the subject, directly or through an index listing it, and no
// other live, non-dependent tag (not among the excluded ids, for a preview where
// the removal has not happened) still does. A subject that never had a tag (an
// image signed by digest) is left to registry GC. $1 is the repository, $2 the
// excluded tag ids, $3 the JSON list of the removed tags' manifests ({image, digest}).
func orphanDependentsSQL() string {
	subject := ociref.SubjectSQL("t.name")

	return fmt.Sprintf(`
		SELECT t.id::text, CASE WHEN t.image = '' THEN r.name ELSE r.name || '/' || t.image END,
		       t.name, ''::text, %[1]s, 0
		FROM docker_tags t JOIN repositories r ON r.id = t.repository_id
		WHERE t.repository_id = $1 AND t.deleted_at IS NULL AND %[2]s
		  AND (
		      NOT EXISTS (
		          SELECT 1 FROM docker_manifests x
		          WHERE x.repository_id = t.repository_id AND x.image = t.image AND x.digest = %[3]s AND x.deleted_at IS NULL)
		      OR (
		          EXISTS (SELECT 1 FROM jsonb_to_recordset($3::jsonb) AS h(image text, digest text)
		                  WHERE h.image = t.image AND %[4]s)
		          AND NOT EXISTS (
		              SELECT 1 FROM docker_tags a
		              WHERE a.repository_id = t.repository_id AND a.image = t.image AND a.deleted_at IS NULL
		                AND NOT (%[5]s) AND a.id <> ALL($2::uuid[]) AND %[6]s)))
		ORDER BY t.id`,
		dockerManifestBlobSQL, dependentTag("t.name"), subject,
		subjectHeld(subject, "h.digest"), dependentTag("a.name"), subjectHeld(subject, "a.manifest_digest"))
}

// selectOrphanDependents returns the dependent tags left without a subject once
// the tags of removed disappear (excluding the tags in excludeIDs from the
// ones that keep a subject), restricted by the policy's name_pattern.
func (w *CleanupWorker) selectOrphanDependents(
	ctx context.Context,
	policy *models.CleanupPolicy,
	removed []dockerHead,
	excludeIDs []string,
) ([]cleanupCandidate, error) {
	var nameRegex *regexp.Regexp

	if policy.NamePattern != "" {
		var err error

		if nameRegex, err = regexp.Compile(policy.NamePattern); err != nil {
			return nil, fmt.Errorf("invalid name pattern regex: %w", err)
		}
	}

	if removed == nil {
		removed = []dockerHead{} // an empty JSON list, not null
	}

	heads, err := json.Marshal(removed)
	if err != nil {
		return nil, fmt.Errorf("encode removed docker tags: %w", err)
	}

	rows, err := w.db.GetDB().QueryContext(ctx, orphanDependentsSQL(), policy.RepositoryID, uuidArrayLiteral(excludeIDs), string(heads))
	if err != nil {
		return nil, fmt.Errorf("failed to query orphan dependent tags: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []cleanupCandidate

	for rows.Next() {
		c, err := scanCandidate(rows)
		if err != nil {
			return nil, err
		}

		if nameRegex == nil || nameRegex.MatchString(c.name) {
			out = append(out, c)
		}
	}

	return out, rows.Err()
}

// recordDockerHeads notes, before the tags of eligible are removed, the
// manifests they point at: afterwards (a hard delete leaves no row) they say
// which dependent tags lost their subject. A lookup failure only leaves those to
// registry GC.
func (w *CleanupWorker) recordDockerHeads(
	ctx context.Context,
	repoID string,
	t cleanupTable,
	eligible []cleanupCandidate,
	run *cleanupRun,
) {
	if !t.docker {
		return
	}

	ids := make([]string, len(eligible))
	for i, c := range eligible {
		ids[i] = c.id
	}

	heads, err := w.dockerTagHeads(ctx, repoID, ids)
	if err != nil {
		w.log.Warnw("cleanup: docker tag manifests unknown; their dependent tags are left to registry gc", "error", err)

		return
	}

	if run.heads == nil {
		run.heads = make(map[string]dockerHead, len(heads))
	}

	maps.Copy(run.heads, heads)
}

// previewOrphanDependents selects the dependent tags a run that removes the
// docker tags in eligible would remove with them.
func (w *CleanupWorker) previewOrphanDependents(
	ctx context.Context,
	policy *models.CleanupPolicy,
	eligible []cleanupCandidate,
) ([]cleanupCandidate, error) {
	ids := make([]string, len(eligible))
	for i, c := range eligible {
		ids[i] = c.id
	}

	heads, err := w.dockerTagHeads(ctx, policy.RepositoryID, ids)
	if err != nil {
		return nil, err
	}

	return w.selectOrphanDependents(ctx, policy, slices.Collect(maps.Values(heads)), ids)
}
