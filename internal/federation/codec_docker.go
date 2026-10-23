// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Docker / OCI units (docs/design/federation.md, migration 071):
//
//	manifest  m:<image>@<digest>   artifact, one docker_manifests row plus the
//	                               docker_manifest_refs, docker_blobs and
//	                               docker_blob_images rows a push creates
//	tag       t:<image>:<name>     ref, one docker_tags row
//
// A manifest travels with the blob of its body and with every config and layer
// blob the repository holds for it, so a replica can serve a pull as soon as
// the unit is applied. Child manifests of an index and the subject of a
// referrer manifest are dependencies: they are applied first.

const (
	dockerManifestPrefix = "m:"
	dockerTagPrefix      = "t:"

	dockerRefBlob     = "blob"
	dockerRefManifest = "manifest"
	dockerRefIndexed  = "indexed"
)

func init() { //nolint:gochecknoinits // codec registry
	register(dockerCodec{})
}

// dockerRef is one docker_manifest_refs row of a manifest.
type dockerRef struct {
	Kind   string `json:"kind"`
	Digest string `json:"digest"`
}

// dockerBlobData is a blob the repository holds for a manifest: its docker_blobs
// row and the blob content it points at.
type dockerBlobData struct {
	Digest    string `json:"digest"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type,omitempty"`
}

// dockerManifestData is the Data of a manifest unit: the replicated columns of
// the docker_manifests row (blob_id holds the sha256 of the manifest body),
// the references the body holds and the blobs they resolve to.
type dockerManifestData struct {
	Image         string           `json:"image"`
	Digest        string           `json:"digest"`
	MediaType     string           `json:"media_type"`
	Size          int64            `json:"size"`
	SubjectDigest string           `json:"subject_digest,omitempty"`
	BlobID        string           `json:"blob_id"`
	CreatedAt     time.Time        `json:"created_at"`
	Refs          []dockerRef      `json:"refs,omitempty"`
	Blobs         []dockerBlobData `json:"blobs,omitempty"`
}

// dockerTagData is the Data of a tag unit: the replicated columns of the
// docker_tags row.
type dockerTagData struct {
	Image          string    `json:"image"`
	Name           string    `json:"name"`
	ManifestDigest string    `json:"manifest_digest"`
	CreatedAt      time.Time `json:"created_at"`
}

type dockerCodec struct{}

func (dockerCodec) Format() string { return "docker" }

// parseDockerManifestKey splits `m:<image>@<digest>` at its last '@'; the image
// may be empty or contain '/'.
func parseDockerManifestKey(key string) (string, string, bool) {
	rest, found := strings.CutPrefix(key, dockerManifestPrefix)
	if !found {
		return "", "", false
	}

	image, digest, ok := strings.CutLast(rest, "@")

	return image, digest, ok && digest != ""
}

// parseDockerTagKey splits `t:<image>:<name>` at its last ':'.
func parseDockerTagKey(key string) (string, string, bool) {
	rest, found := strings.CutPrefix(key, dockerTagPrefix)
	if !found {
		return "", "", false
	}

	image, name, ok := strings.CutLast(rest, ":")

	return image, name, ok && name != ""
}

func dockerManifestKey(image, digest string) string {
	return dockerManifestPrefix + image + "@" + digest
}

func errDockerKey(key string) error {
	return fmt.Errorf("%w: malformed unit key %q", ErrInvalid, key)
}

func (dockerCodec) Export(ctx context.Context, q Querier, repoID, key string) (*Content, error) {
	if image, digest, ok := parseDockerManifestKey(key); ok {
		return exportDockerManifest(ctx, q, repoID, image, digest)
	}

	if image, name, ok := parseDockerTagKey(key); ok {
		return exportDockerTag(ctx, q, repoID, image, name)
	}

	return nil, errDockerKey(key)
}

func (dockerCodec) Usage(ctx context.Context, q Querier, repoID, key string, c *Content) (int64, int, error) {
	if _, _, ok := parseDockerManifestKey(key); !ok {
		if _, _, ok := parseDockerTagKey(key); ok {
			return 0, 0, nil
		}

		return 0, 0, errDockerKey(key)
	}

	d, err := decodeDockerManifest(c)
	if err != nil {
		return 0, 0, err
	}

	size := d.Size

	for _, b := range c.Blobs {
		if b.SHA256 == d.BlobID {
			size = b.Size
		}
	}

	// Layers and config the repository holds already are not new usage; the
	// quota triggers count docker_blobs rows with count 0 (migration 061).
	held, err := dockerHeldDigests(ctx, q, repoID, d.Blobs)
	if err != nil {
		return 0, 0, err
	}

	for _, b := range d.Blobs {
		if !held[b.Digest] {
			size += b.Size
		}
	}

	return size, 1, nil
}

func (dockerCodec) Import(ctx context.Context, tx *sql.Tx, repoID, key string, c *Content, blobIDs map[string]string) (*Change, error) {
	if image, digest, ok := parseDockerManifestKey(key); ok {
		return importDockerManifest(ctx, tx, repoID, image, digest, c, blobIDs)
	}

	if image, name, ok := parseDockerTagKey(key); ok {
		return importDockerTag(ctx, tx, repoID, image, name, c)
	}

	return nil, errDockerKey(key)
}

func (dockerCodec) Delete(ctx context.Context, tx *sql.Tx, repoID, key string, soft bool, deletedBy string) (*Change, error) {
	if image, digest, ok := parseDockerManifestKey(key); ok {
		return deleteDockerManifest(ctx, tx, repoID, image, digest, soft, deletedBy)
	}

	if image, name, ok := parseDockerTagKey(key); ok {
		return deleteDockerTag(ctx, tx, repoID, image, name, soft, deletedBy)
	}

	return nil, errDockerKey(key)
}

func (dockerCodec) Backfill(ctx context.Context, tx *sql.Tx, repoID string) error {
	_, err := tx.ExecContext(ctx,
		`SELECT federation_record($1::uuid, 'm:' || t.image || '@' || t.digest, $2, $3, t.digest, false, t.updated_at)
		   FROM docker_manifests t WHERE t.repository_id = $1::uuid AND t.deleted_at IS NULL`,
		repoID, KindArtifact, EventArtifactAdded)
	if err != nil {
		return fmt.Errorf("backfill docker_manifests: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`SELECT federation_record($1::uuid, 't:' || t.image || ':' || t.name, $2, $3, t.manifest_digest, false, t.updated_at)
		   FROM docker_tags t WHERE t.repository_id = $1::uuid AND t.deleted_at IS NULL`,
		repoID, KindRef, EventRefMoved)
	if err != nil {
		return fmt.Errorf("backfill docker_tags: %w", err)
	}

	return nil
}

func decodeDockerManifest(c *Content) (*dockerManifestData, error) {
	var d dockerManifestData
	if err := json.Unmarshal(c.Data, &d); err != nil {
		return nil, fmt.Errorf("%w: decode unit data: %v", ErrInvalid, err) //nolint:errorlint // client-safe text only
	}

	return &d, nil
}

// dockerManifestRow is the live docker_manifests row of a unit with the blob
// holding the manifest body.
type dockerManifestRow struct {
	id        string
	mediaType string
	size      int64
	subject   string
	createdAt time.Time
	blob      blobInfo
}

func (r *dockerManifestRow) info(image, digest string) *ArtifactInfo {
	return &ArtifactInfo{Name: image, Version: digest, Path: digest, SHA256: r.blob.sha256, Size: r.size}
}

// loadDockerManifest loads the live row of a manifest unit, locking it inside a
// transaction when lock is set; it returns (nil, nil) when there is none.
func loadDockerManifest(ctx context.Context, q Querier, repoID, image, digest string, lock bool) (*dockerManifestRow, error) {
	query := `SELECT m.id::text, m.media_type, m.size, COALESCE(m.subject_digest, ''), m.created_at,
	                 b.id::text, b.sha256, b.size_bytes, b.media_type
	            FROM docker_manifests m JOIN blobs b ON b.id = m.blob_id
	           WHERE m.repository_id = $1::uuid AND m.image = $2 AND m.digest = $3 AND m.deleted_at IS NULL`
	if lock {
		query += ` FOR UPDATE OF m`
	}

	r := &dockerManifestRow{}

	err := q.QueryRowContext(ctx, query, repoID, image, digest).Scan(
		&r.id, &r.mediaType, &r.size, &r.subject, &r.createdAt,
		&r.blob.id, &r.blob.sha256, &r.blob.size, &r.blob.mediaType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // (nil, nil) is "no live row"
	}

	if err != nil {
		return nil, fmt.Errorf("load docker manifest: %w", err)
	}

	return r, nil
}

// dockerManifestRefs lists the docker_manifest_refs rows of a manifest.
func dockerManifestRefs(ctx context.Context, q Querier, manifestID string) ([]dockerRef, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT kind, digest FROM docker_manifest_refs WHERE manifest_id = $1::uuid ORDER BY kind, digest`, manifestID)
	if err != nil {
		return nil, fmt.Errorf("list docker manifest refs: %w", err)
	}
	defer rows.Close()

	var refs []dockerRef

	for rows.Next() {
		var r dockerRef
		if err := rows.Scan(&r.Kind, &r.Digest); err != nil {
			return nil, fmt.Errorf("scan docker manifest ref: %w", err)
		}

		refs = append(refs, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list docker manifest refs: %w", err)
	}

	return refs, nil
}

// dockerReferencedBlobs resolves the blob references of a manifest that the
// repository holds (foreign layers have no docker_blobs row and are skipped).
func dockerReferencedBlobs(ctx context.Context, q Querier, manifestID string) ([]dockerBlobData, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT r.digest, b.sha256, COALESCE(db.size_bytes, b.size_bytes), b.media_type
		   FROM docker_manifest_refs r
		   JOIN docker_blobs db ON db.repository_id = r.repository_id AND db.digest = r.digest
		   JOIN blobs b ON b.id = db.blob_id
		  WHERE r.manifest_id = $1::uuid AND r.kind = 'blob'
		  ORDER BY r.digest`, manifestID)
	if err != nil {
		return nil, fmt.Errorf("list docker manifest blobs: %w", err)
	}
	defer rows.Close()

	var blobs []dockerBlobData

	for rows.Next() {
		var b dockerBlobData
		if err := rows.Scan(&b.Digest, &b.SHA256, &b.Size, &b.MediaType); err != nil {
			return nil, fmt.Errorf("scan docker manifest blob: %w", err)
		}

		blobs = append(blobs, b)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list docker manifest blobs: %w", err)
	}

	return blobs, nil
}

func dockerRefsIndexed(refs []dockerRef) bool {
	return slices.ContainsFunc(refs, func(r dockerRef) bool { return r.Kind == dockerRefIndexed })
}

// jsonArg encodes a query argument as a JSON document (the queries expand it
// with jsonb_array_elements, which needs no driver support for array types).
func jsonArg(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode argument: %w", err)
	}

	return string(b), nil
}

// dockerLiveManifestKeys returns the unit keys of the live manifests of an image
// among digests.
func dockerLiveManifestKeys(ctx context.Context, q Querier, repoID, image string, digests []string) ([]string, error) {
	if len(digests) == 0 {
		return nil, nil
	}

	arg, err := jsonArg(digests)
	if err != nil {
		return nil, err
	}

	rows, err := q.QueryContext(ctx,
		`SELECT digest FROM docker_manifests
		  WHERE repository_id = $1::uuid AND image = $2 AND deleted_at IS NULL
		    AND digest IN (SELECT jsonb_array_elements_text($3::jsonb))
		  ORDER BY digest`, repoID, image, arg)
	if err != nil {
		return nil, fmt.Errorf("list docker dependencies: %w", err)
	}
	defer rows.Close()

	var keys []string

	for rows.Next() {
		var digest string
		if err := rows.Scan(&digest); err != nil {
			return nil, fmt.Errorf("scan docker dependency: %w", err)
		}

		keys = append(keys, dockerManifestKey(image, digest))
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list docker dependencies: %w", err)
	}

	return keys, nil
}

// dockerManifestDeps are the manifest units that must exist before a manifest:
// the children of an index and the subject of a referrer. Digests the origin
// itself does not hold live are not dependencies (the origin is as dangling).
func dockerManifestDeps(ctx context.Context, q Querier, repoID, image, digest, subject string, refs []dockerRef) ([]string, error) {
	var candidates []string

	for _, r := range refs {
		if r.Kind == dockerRefManifest && r.Digest != digest {
			candidates = append(candidates, r.Digest)
		}
	}

	if subject != "" && subject != digest {
		candidates = append(candidates, subject)
	}

	return dockerLiveManifestKeys(ctx, q, repoID, image, candidates)
}

func exportDockerManifest(ctx context.Context, q Querier, repoID, image, digest string) (*Content, error) {
	m, err := loadDockerManifest(ctx, q, repoID, image, digest, false)
	if err != nil || m == nil {
		return nil, err
	}

	refs, err := dockerManifestRefs(ctx, q, m.id)
	if err != nil {
		return nil, err
	}

	// A manifest restored from a backup is indexed by registry GC after the
	// fact; until then its layers are unknown and the unit cannot be served
	// completely. The peer retries it.
	if !dockerRefsIndexed(refs) {
		return nil, fmt.Errorf("%w: %s@%s", errDockerNotIndexed, image, digest)
	}

	blobs, err := dockerReferencedBlobs(ctx, q, m.id)
	if err != nil {
		return nil, err
	}

	deps, err := dockerManifestDeps(ctx, q, repoID, image, digest, m.subject, refs)
	if err != nil {
		return nil, err
	}

	content := &Content{
		Kind:       KindArtifact,
		ContentRef: digest,
		Deps:       deps,
		Blobs:      []BlobRef{{SHA256: m.blob.sha256, Size: m.blob.size, MediaType: m.blob.mediaType}},
	}

	seen := map[string]bool{m.blob.sha256: true}

	for _, b := range blobs {
		if seen[b.SHA256] {
			continue
		}

		seen[b.SHA256] = true

		content.Blobs = append(content.Blobs, BlobRef{SHA256: b.SHA256, Size: b.Size, MediaType: b.MediaType})
	}

	content.Data, err = json.Marshal(dockerManifestData{
		Image:         image,
		Digest:        digest,
		MediaType:     m.mediaType,
		Size:          m.size,
		SubjectDigest: m.subject,
		BlobID:        m.blob.sha256,
		CreatedAt:     m.createdAt,
		Refs:          refs,
		Blobs:         blobs,
	})
	if err != nil {
		return nil, fmt.Errorf("encode docker manifest unit: %w", err)
	}

	return content, nil
}

// dockerHeldDigests reports which of the blobs the repository has a docker_blobs
// row for.
func dockerHeldDigests(ctx context.Context, q Querier, repoID string, blobs []dockerBlobData) (map[string]bool, error) {
	held := map[string]bool{}

	if len(blobs) == 0 {
		return held, nil
	}

	digests := make([]string, len(blobs))
	for i, b := range blobs {
		digests[i] = b.Digest
	}

	arg, err := jsonArg(digests)
	if err != nil {
		return nil, err
	}

	rows, err := q.QueryContext(ctx,
		`SELECT digest FROM docker_blobs
		  WHERE repository_id = $1::uuid AND digest IN (SELECT jsonb_array_elements_text($2::jsonb))`, repoID, arg)
	if err != nil {
		return nil, fmt.Errorf("list held docker blobs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var digest string
		if err := rows.Scan(&digest); err != nil {
			return nil, fmt.Errorf("scan held docker blob: %w", err)
		}

		held[digest] = true
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list held docker blobs: %w", err)
	}

	return held, nil
}

// dockerRepoLock takes the shared per-repository docker lock a manifest push
// holds (internal/database dockerRepoLockKey): registry GC takes it exclusively
// before it deletes anything, so it sees either none of the unit or all of it.
// Callers take it before any federation_* function locks the repository's
// clock row, the order a push uses.
func dockerRepoLock(ctx context.Context, tx *sql.Tx, repoID string) error {
	if _, err := tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock_shared(hashtextextended($1, 0))`, "docker-repo:"+repoID); err != nil {
		return fmt.Errorf("lock docker repository: %w", err)
	}

	return nil
}

// dockerBlobRow is a docker_blobs row to write, its blob already in the blobs
// table.
type dockerBlobRow struct {
	Digest string `json:"digest"`
	BlobID string `json:"blob_id"`
	Size   int64  `json:"size"`
}

// validateDockerManifest checks a unit against its key and resolves the blobs
// it references to blobs.id.
func validateDockerManifest(d *dockerManifestData, image, digest string, blobIDs map[string]string) (string, []dockerBlobRow, error) {
	if d.Image != image || d.Digest != digest {
		return "", nil, fmt.Errorf("%w: unit data does not match key %s", ErrInvalid, dockerManifestKey(image, digest))
	}

	manifestBlob, ok := blobIDs[d.BlobID]
	if !ok {
		return "", nil, fmt.Errorf("%w: blob %s of unit %s was not provided", ErrInvalid, d.BlobID, dockerManifestKey(image, digest))
	}

	for _, r := range d.Refs {
		if r.Kind != dockerRefBlob && r.Kind != dockerRefManifest && r.Kind != dockerRefIndexed {
			return "", nil, fmt.Errorf("%w: unknown docker reference kind %q", ErrInvalid, r.Kind)
		}
	}

	rows := make([]dockerBlobRow, 0, len(d.Blobs))

	for _, b := range d.Blobs {
		id, ok := blobIDs[b.SHA256]
		if !ok {
			return "", nil, fmt.Errorf("%w: blob %s of unit %s was not provided", ErrInvalid, b.SHA256, dockerManifestKey(image, digest))
		}

		rows = append(rows, dockerBlobRow{Digest: b.Digest, BlobID: id, Size: b.Size})
	}

	return manifestBlob, rows, nil
}

func importDockerManifest(ctx context.Context, tx *sql.Tx, repoID, image, digest string, c *Content, blobIDs map[string]string) (*Change, error) {
	d, err := decodeDockerManifest(c)
	if err != nil {
		return nil, err
	}

	manifestBlob, blobRows, err := validateDockerManifest(d, image, digest, blobIDs)
	if err != nil {
		return nil, err
	}

	if err := dockerRepoLock(ctx, tx, repoID); err != nil {
		return nil, err
	}

	existing, err := loadDockerManifest(ctx, tx, repoID, image, digest, true)
	if err != nil {
		return nil, err
	}

	change := &Change{Artifact: &ArtifactInfo{Name: image, Version: digest, Path: digest, SHA256: d.BlobID, Size: d.Size}}

	manifestID, err := writeDockerManifestRow(ctx, tx, repoID, d, manifestBlob, existing, change)
	if err != nil {
		return nil, err
	}

	if err := writeDockerBlobRows(ctx, tx, repoID, blobRows); err != nil {
		return nil, err
	}

	if err := writeDockerRefs(ctx, tx, repoID, manifestID, d); err != nil {
		return nil, err
	}

	return change, nil
}

// writeDockerManifestRow inserts the manifest row, or updates the live row of
// the unit in place, and records the replaced usage in change.
func writeDockerManifestRow(ctx context.Context, tx *sql.Tx, repoID string, d *dockerManifestData, blobID string, existing *dockerManifestRow, change *Change) (string, error) {
	if existing == nil {
		var id string

		err := tx.QueryRowContext(ctx,
			`INSERT INTO docker_manifests (repository_id, image, digest, media_type, blob_id, size, subject_digest, created_at)
			 VALUES ($1::uuid, $2, $3, $4, $5::uuid, $6, NULLIF($7, ''), $8) RETURNING id::text`,
			repoID, d.Image, d.Digest, d.MediaType, blobID, d.Size, d.SubjectDigest, d.CreatedAt).Scan(&id)
		if err != nil {
			return "", fmt.Errorf("insert docker manifest: %w", err)
		}

		return id, nil
	}

	change.Replaced = true
	change.ReplacedSize = existing.blob.size
	change.ReplacedCount = 1

	if existing.blob.sha256 != d.BlobID {
		change.ReleasedBlobs = append(change.ReleasedBlobs, existing.blob.sha256)
	}

	_, err := tx.ExecContext(ctx,
		`UPDATE docker_manifests SET media_type = $2, blob_id = $3::uuid, size = $4,
		        subject_digest = NULLIF($5, ''), created_at = $6
		  WHERE id = $1::uuid`,
		existing.id, d.MediaType, blobID, d.Size, d.SubjectDigest, d.CreatedAt)
	if err != nil {
		return "", fmt.Errorf("update docker manifest: %w", err)
	}

	return existing.id, nil
}

// writeDockerBlobRows records the repository's docker_blobs rows exactly as
// CreateDockerBlob does: an existing row keeps its size and is marked as just
// used, so registry GC keeps it for its grace period.
func writeDockerBlobRows(ctx context.Context, tx *sql.Tx, repoID string, rows []dockerBlobRow) error {
	if len(rows) == 0 {
		return nil
	}

	arg, err := jsonArg(rows)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO docker_blobs (repository_id, digest, blob_id, size_bytes)
		 SELECT $1::uuid, e->>'digest', (e->>'blob_id')::uuid, (e->>'size')::bigint
		   FROM jsonb_array_elements($2::jsonb) e
		 ON CONFLICT (repository_id, digest) DO UPDATE SET blob_id = EXCLUDED.blob_id, last_used_at = now(),
		     size_bytes = COALESCE(docker_blobs.size_bytes, EXCLUDED.size_bytes)`,
		repoID, arg)
	if err != nil {
		return fmt.Errorf("write docker blobs: %w", err)
	}

	return nil
}

// writeDockerRefs replaces the references of the manifest and links the blobs
// they name to its image, as a push does (insertDockerManifestRefs).
func writeDockerRefs(ctx context.Context, tx *sql.Tx, repoID, manifestID string, d *dockerManifestData) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM docker_manifest_refs WHERE manifest_id = $1::uuid`, manifestID); err != nil {
		return fmt.Errorf("clear docker manifest refs: %w", err)
	}

	if len(d.Refs) == 0 {
		return nil
	}

	arg, err := jsonArg(d.Refs)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO docker_manifest_refs (manifest_id, repository_id, image, kind, digest)
		 SELECT $1::uuid, $2::uuid, $3, r->>'kind', r->>'digest' FROM jsonb_array_elements($4::jsonb) r
		 ON CONFLICT (manifest_id, kind, digest) DO NOTHING`,
		manifestID, repoID, d.Image, arg)
	if err != nil {
		return fmt.Errorf("write docker manifest refs: %w", err)
	}

	var blobDigests []string

	for _, r := range d.Refs {
		if r.Kind == dockerRefBlob {
			blobDigests = append(blobDigests, r.Digest)
		}
	}

	if len(blobDigests) == 0 {
		return nil
	}

	digests, err := jsonArg(blobDigests)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO docker_blob_images (repository_id, image, digest)
		 SELECT b.repository_id, $2, b.digest FROM docker_blobs b
		  WHERE b.repository_id = $1::uuid AND b.digest IN (SELECT jsonb_array_elements_text($3::jsonb))
		 ON CONFLICT DO NOTHING`,
		repoID, d.Image, digests)
	if err != nil {
		return fmt.Errorf("link docker blobs to image: %w", err)
	}

	return nil
}

// deleteDockerManifest trashes (soft) or removes (hard) the live manifest row.
// Tags of the manifest are units of their own and arrive as their own deletes.
// A hard delete mirrors the registry's permanent delete: the manifest row goes
// (its refs cascade) and its body blob is a release candidate; layer and config
// docker_blobs rows stay for registry GC, which removes them under the
// repository lock after its grace period once no manifest references them.
func deleteDockerManifest(ctx context.Context, tx *sql.Tx, repoID, image, digest string, soft bool, deletedBy string) (*Change, error) {
	m, err := loadDockerManifest(ctx, tx, repoID, image, digest, true)
	if err != nil {
		return nil, err
	}

	if m == nil {
		return &Change{Missing: true}, nil
	}

	change := &Change{RemovedSize: m.blob.size, RemovedCount: 1, Artifact: m.info(image, digest)}

	if soft {
		_, err = tx.ExecContext(ctx,
			`UPDATE docker_manifests SET deleted_at = now(), deleted_by = $2 WHERE id = $1::uuid`, m.id, deletedBy)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM docker_manifests WHERE id = $1::uuid`, m.id)
		change.ReleasedBlobs = []string{m.blob.sha256}
	}

	if err != nil {
		return nil, fmt.Errorf("delete docker manifest: %w", err)
	}

	return change, nil
}

// dockerTagRow is the live docker_tags row of a tag unit.
type dockerTagRow struct {
	id             string
	manifestDigest string
	createdAt      time.Time
}

func loadDockerTag(ctx context.Context, q Querier, repoID, image, name string, lock bool) (*dockerTagRow, error) {
	query := `SELECT id::text, manifest_digest, created_at FROM docker_tags
	           WHERE repository_id = $1::uuid AND image = $2 AND name = $3 AND deleted_at IS NULL`
	if lock {
		query += ` FOR UPDATE`
	}

	r := &dockerTagRow{}

	err := q.QueryRowContext(ctx, query, repoID, image, name).Scan(&r.id, &r.manifestDigest, &r.createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // (nil, nil) is "no live row"
	}

	if err != nil {
		return nil, fmt.Errorf("load docker tag: %w", err)
	}

	return r, nil
}

func exportDockerTag(ctx context.Context, q Querier, repoID, image, name string) (*Content, error) {
	t, err := loadDockerTag(ctx, q, repoID, image, name, false)
	if err != nil || t == nil {
		return nil, err
	}

	deps, err := dockerLiveManifestKeys(ctx, q, repoID, image, []string{t.manifestDigest})
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(dockerTagData{Image: image, Name: name, ManifestDigest: t.manifestDigest, CreatedAt: t.createdAt})
	if err != nil {
		return nil, fmt.Errorf("encode docker tag unit: %w", err)
	}

	return &Content{Kind: KindRef, Data: data, Deps: deps, ContentRef: t.manifestDigest}, nil
}

func importDockerTag(ctx context.Context, tx *sql.Tx, repoID, image, name string, c *Content) (*Change, error) {
	var d dockerTagData
	if err := json.Unmarshal(c.Data, &d); err != nil {
		return nil, fmt.Errorf("%w: decode unit data: %v", ErrInvalid, err) //nolint:errorlint // client-safe text only
	}

	if d.Image != image || d.Name != name || d.ManifestDigest == "" {
		return nil, fmt.Errorf("%w: unit data does not match key t:%s:%s", ErrInvalid, image, name)
	}

	existing, err := loadDockerTag(ctx, tx, repoID, image, name, true)
	if err != nil {
		return nil, err
	}

	if existing == nil {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO docker_tags (repository_id, image, name, manifest_digest, created_at)
			 VALUES ($1::uuid, $2, $3, $4, $5)`,
			repoID, image, name, d.ManifestDigest, d.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("insert docker tag: %w", err)
		}

		return &Change{}, nil
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE docker_tags SET manifest_digest = $2, created_at = $3 WHERE id = $1::uuid`,
		existing.id, d.ManifestDigest, d.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("update docker tag: %w", err)
	}

	return &Change{Replaced: true}, nil
}

func deleteDockerTag(ctx context.Context, tx *sql.Tx, repoID, image, name string, soft bool, deletedBy string) (*Change, error) {
	t, err := loadDockerTag(ctx, tx, repoID, image, name, true)
	if err != nil {
		return nil, err
	}

	if t == nil {
		return &Change{Missing: true}, nil
	}

	if soft {
		_, err = tx.ExecContext(ctx,
			`UPDATE docker_tags SET deleted_at = now(), deleted_by = $2 WHERE id = $1::uuid`, t.id, deletedBy)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM docker_tags WHERE id = $1::uuid`, t.id)
	}

	if err != nil {
		return nil, fmt.Errorf("delete docker tag: %w", err)
	}

	return &Change{}, nil
}
