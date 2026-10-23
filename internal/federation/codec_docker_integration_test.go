// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package federation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/ocimanifest"
)

const (
	dockerTestImage = "team/app"
	dockerPeer      = "federation:peer"
)

// dockerRepo pushes images into one repository the way the registry does: blob
// rows through CreateDockerBlob, manifests with their references through
// CreateDockerManifest and tags through UpsertDockerTag.
type dockerRepo struct {
	t    *testing.T
	pg   *database.PostgresDB
	db   *sql.DB
	id   string
	name string
}

func newDockerRepo(t *testing.T, pg *database.PostgresDB, name string) *dockerRepo {
	t.Helper()

	db := pg.GetDB()

	return &dockerRepo{t: t, pg: pg, db: db, id: seedRepo(t, db, name, "docker"), name: name}
}

func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))

	return hex.EncodeToString(sum[:])
}

// blob stores a layer or config blob for an image and returns its digest.
func (r *dockerRepo) blob(image, content string) string {
	r.t.Helper()

	sha := sha256Hex(content)

	err := r.pg.CreateDockerBlob(context.Background(), &models.DockerBlob{
		RepositoryID: r.id,
		Digest:       "sha256:" + sha,
		MediaType:    "application/octet-stream",
		Size:         int64(len(content)),
		BlobSHA256:   sha,
		Image:        &image,
	})
	if err != nil {
		r.t.Fatalf("create docker blob: %v", err)
	}

	return "sha256:" + sha
}

type descriptorJSON struct {
	MediaType string `json:"mediaType,omitempty"` //nolint:tagliatelle // OCI wire format
	Digest    string `json:"digest"`
	Size      int    `json:"size"`
}

// manifest pushes a manifest body, indexed, and returns its digest.
func (r *dockerRepo) manifest(image string, body map[string]any, subject string) string {
	r.t.Helper()

	raw, err := json.Marshal(body)
	if err != nil {
		r.t.Fatalf("encode manifest: %v", err)
	}

	r.storeManifest(image, string(raw), subject, true)

	return "sha256:" + sha256Hex(string(raw))
}

func (r *dockerRepo) storeManifest(image, body, subject string, indexed bool) {
	r.t.Helper()

	ctx := context.Background()
	sha := sha256Hex(body)

	blobID, err := r.pg.EnsureBlob(ctx, sha, "application/vnd.oci.image.manifest.v1+json", int64(len(body)))
	if err != nil {
		r.t.Fatalf("ensure manifest blob: %v", err)
	}

	m := &models.DockerManifest{
		RepositoryID:  r.id,
		Image:         image,
		Digest:        "sha256:" + sha,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		BlobID:        blobID,
		Size:          int64(len(body)),
		SubjectDigest: subject,
	}

	if indexed {
		refs := ocimanifest.ParseRefs([]byte(body))
		m.Refs = &refs
		m.RequiredBlobs = ocimanifest.PushedBlobs([]byte(body))
	}

	if err := r.pg.CreateDockerManifest(ctx, m); err != nil {
		r.t.Fatalf("create docker manifest: %v", err)
	}
}

// image pushes a config and a layer blob and the image manifest referencing
// them, and returns the manifest digest.
func (r *dockerRepo) image(image, seed string) string {
	r.t.Helper()

	config := r.blob(image, "config-"+seed)
	layer := r.blob(image, "layer-"+seed)

	return r.manifest(image, map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config":        descriptorJSON{Digest: config, Size: len("config-" + seed)},
		"layers":        []descriptorJSON{{Digest: layer, Size: len("layer-" + seed)}},
	}, "")
}

func (r *dockerRepo) tag(image, name, digest string) {
	r.t.Helper()

	err := r.pg.UpsertDockerTag(context.Background(), &models.DockerTag{
		RepositoryID: r.id, Image: image, Name: name, ManifestDigest: digest,
	})
	if err != nil {
		r.t.Fatalf("upsert docker tag: %v", err)
	}
}

// state lists the replicated docker rows of the repository, one line each.
func (r *dockerRepo) state() []string {
	r.t.Helper()

	rows, err := r.db.QueryContext(context.Background(),
		`SELECT line FROM (
		   SELECT 'M|' || m.image || '|' || m.digest || '|' || m.media_type || '|' || m.size || '|' ||
		          COALESCE(m.subject_digest, '') || '|' || b.sha256 || '|' || m.created_at::text AS line
		     FROM docker_manifests m JOIN blobs b ON b.id = m.blob_id
		    WHERE m.repository_id = $1::uuid AND m.deleted_at IS NULL
		   UNION ALL
		   SELECT 'B|' || d.digest || '|' || COALESCE(d.size_bytes, -1) || '|' || b.sha256
		     FROM docker_blobs d JOIN blobs b ON b.id = d.blob_id WHERE d.repository_id = $1::uuid
		   UNION ALL
		   SELECT 'R|' || m.image || '|' || m.digest || '|' || x.kind || '|' || x.digest
		     FROM docker_manifest_refs x JOIN docker_manifests m ON m.id = x.manifest_id
		    WHERE m.repository_id = $1::uuid AND m.deleted_at IS NULL
		   UNION ALL
		   SELECT 'I|' || i.image || '|' || i.digest FROM docker_blob_images i WHERE i.repository_id = $1::uuid
		   UNION ALL
		   SELECT 'T|' || t.image || '|' || t.name || '|' || t.manifest_digest || '|' || t.created_at::text
		     FROM docker_tags t WHERE t.repository_id = $1::uuid AND t.deleted_at IS NULL
		 ) s ORDER BY line`, r.id)
	if err != nil {
		r.t.Fatalf("list docker state: %v", err)
	}
	defer rows.Close()

	var out []string

	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			r.t.Fatalf("scan docker state: %v", err)
		}

		out = append(out, line)
	}

	if err := rows.Err(); err != nil {
		r.t.Fatalf("list docker state: %v", err)
	}

	return out
}

func (r *dockerRepo) count(query string, args ...any) int {
	r.t.Helper()

	var n int
	if err := r.db.QueryRowContext(context.Background(), query, append([]any{r.id}, args...)...).Scan(&n); err != nil {
		r.t.Fatalf("count: %v", err)
	}

	return n
}

func assertSameDockerState(t *testing.T, a, b *dockerRepo) {
	t.Helper()

	sa, sb := a.state(), b.state()
	if !slices.Equal(sa, sb) {
		t.Fatalf("docker state differs\n%s:\n  %s\n%s:\n  %s", a.name, strings.Join(sa, "\n  "), b.name, strings.Join(sb, "\n  "))
	}
}

func dockerCodecForTest(t *testing.T) Codec {
	t.Helper()

	c, err := CodecFor("docker")
	if err != nil {
		t.Fatalf("docker codec: %v", err)
	}

	return c
}

func manifestKey(image, digest string) string { return dockerManifestKey(image, digest) }

func tagKey(image, name string) string { return "t:" + image + ":" + name }

// export reads a unit from the repository; it fails the test when there is none.
func (r *dockerRepo) export(key string) *Content {
	r.t.Helper()

	c, err := dockerCodecForTest(r.t).Export(context.Background(), r.db, r.id, key)
	if err != nil {
		r.t.Fatalf("export %s: %v", key, err)
	}

	if c == nil {
		r.t.Fatalf("export %s: no live unit", key)
	}

	return c
}

// inApply runs fn in a transaction with the federation apply guard on, as the
// engine's apply does.
func (r *dockerRepo) inApply(fn func(tx *sql.Tx) error) {
	r.t.Helper()

	ctx := context.Background()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		r.t.Fatalf("begin: %v", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SET LOCAL oa.federation_apply = 'on'`); err != nil {
		r.t.Fatalf("set apply guard: %v", err)
	}

	if err := fn(tx); err != nil {
		r.t.Fatalf("apply: %v", err)
	}

	if err := tx.Commit(); err != nil {
		r.t.Fatalf("commit: %v", err)
	}
}

// blobIDs maps the blobs of a unit to the blobs.id the engine would have stored.
func (r *dockerRepo) blobIDs(c *Content) map[string]string {
	r.t.Helper()

	ids := map[string]string{}

	for _, b := range c.Blobs {
		var id string

		err := r.db.QueryRowContext(context.Background(), `SELECT id::text FROM blobs WHERE sha256 = $1`, b.SHA256).Scan(&id)
		if err != nil {
			r.t.Fatalf("blob %s: %v", b.SHA256, err)
		}

		ids[b.SHA256] = id
	}

	return ids
}

func (r *dockerRepo) importUnit(key string, c *Content) *Change {
	r.t.Helper()

	var change *Change

	r.inApply(func(tx *sql.Tx) error {
		var err error

		change, err = dockerCodecForTest(r.t).Import(context.Background(), tx, r.id, key, c, r.blobIDs(c))

		return err
	})

	return change
}

func (r *dockerRepo) deleteUnit(key string, soft bool) *Change {
	r.t.Helper()

	var change *Change

	r.inApply(func(tx *sql.Tx) error {
		var err error

		change, err = dockerCodecForTest(r.t).Delete(context.Background(), tx, r.id, key, soft, dockerPeer)

		return err
	})

	return change
}

// replicate exports every key from src and imports it into dst in order.
func replicate(src, dst *dockerRepo, keys ...string) {
	src.t.Helper()

	for _, key := range keys {
		dst.importUnit(key, src.export(key))
	}
}

func TestDockerCodecReplicatesImage(t *testing.T) {
	pg := freshDB(t)
	a := newDockerRepo(t, pg, "dock-a")
	b := newDockerRepo(t, pg, "dock-b")
	enableLog(t, a.db, a.id)
	enableLog(t, b.db, b.id)

	digest := a.image(dockerTestImage, "one")
	a.tag(dockerTestImage, "latest", digest)

	want := map[string]string{
		manifestKey(dockerTestImage, digest): "artifact_added/false",
		tagKey(dockerTestImage, "latest"):    "ref_moved/false",
	}

	if got := loggedUnits(t, a.db, a.id); !mapsEqual(got, want) {
		t.Fatalf("trigger log = %v, want %v", got, want)
	}

	manifest := a.export(manifestKey(dockerTestImage, digest))

	if manifest.Kind != KindArtifact || manifest.ContentRef != digest {
		t.Fatalf("manifest unit kind/content_ref = %s/%s", manifest.Kind, manifest.ContentRef)
	}

	if len(manifest.Blobs) != 3 || manifest.Blobs[0].SHA256 != strings.TrimPrefix(digest, "sha256:") {
		t.Fatalf("manifest blobs = %+v, want the manifest body, the config and the layer", manifest.Blobs)
	}

	if len(manifest.Deps) != 0 {
		t.Fatalf("image manifest deps = %v, want none", manifest.Deps)
	}

	tag := a.export(tagKey(dockerTestImage, "latest"))
	if tag.Kind != KindRef || tag.ContentRef != digest || len(tag.Blobs) != 0 ||
		!slices.Equal(tag.Deps, []string{manifestKey(dockerTestImage, digest)}) {
		t.Fatalf("tag unit = %+v", tag)
	}

	assertDockerUsage(t, b, manifestKey(dockerTestImage, digest), manifest, sumBlobSizes(manifest.Blobs))

	replicate(a, b, manifestKey(dockerTestImage, digest), tagKey(dockerTestImage, "latest"))

	assertSameDockerState(t, a, b)

	if got := loggedUnits(t, b.db, b.id); len(got) != 0 {
		t.Fatalf("applying logged %v, want nothing", got)
	}

	// The layers are held now, only the manifest body is new usage.
	assertDockerUsage(t, b, manifestKey(dockerTestImage, digest), manifest, manifest.Blobs[0].Size)

	gc, err := pg.ListDockerGCBlobs(context.Background(), b.id, 0, nil, 100)
	if err != nil {
		t.Fatalf("list gc blobs: %v", err)
	}

	if len(gc) != 0 {
		t.Fatalf("registry GC would collect %d blobs of the replica, want 0", len(gc))
	}
}

func sumBlobSizes(blobs []BlobRef) int64 {
	var n int64
	for _, b := range blobs {
		n += b.Size
	}

	return n
}

func assertDockerUsage(t *testing.T, r *dockerRepo, key string, c *Content, wantSize int64) {
	t.Helper()

	size, count, err := dockerCodecForTest(t).Usage(context.Background(), r.db, r.id, key, c)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if size != wantSize || count != 1 {
		t.Fatalf("usage = %d bytes, %d artifacts; want %d bytes, 1", size, count, wantSize)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		if b[k] != v {
			return false
		}
	}

	return true
}

func TestDockerCodecImportIsIdempotent(t *testing.T) {
	pg := freshDB(t)
	a := newDockerRepo(t, pg, "dock-a")
	b := newDockerRepo(t, pg, "dock-b")

	digest := a.image("", "default")
	a.tag("", "v1", digest)

	replicate(a, b, manifestKey("", digest), tagKey("", "v1"))

	first := b.state()

	change := b.importUnit(manifestKey("", digest), a.export(manifestKey("", digest)))
	if !change.Replaced || change.ReplacedCount != 1 || change.ReplacedSize <= 0 || len(change.ReleasedBlobs) != 0 {
		t.Fatalf("second import change = %+v, want an in-place update releasing nothing", change)
	}

	if change.Artifact == nil || change.Artifact.Name != "" || change.Artifact.Version != digest || change.Artifact.Path != digest ||
		change.Artifact.SHA256 != strings.TrimPrefix(digest, "sha256:") {
		t.Fatalf("change artifact = %+v", change.Artifact)
	}

	if got := b.state(); !slices.Equal(first, got) {
		t.Fatalf("state changed by re-import:\n%v\n%v", first, got)
	}

	if n := b.count(`SELECT count(*) FROM docker_manifests WHERE repository_id = $1::uuid`); n != 1 {
		t.Fatalf("manifest rows = %d, want 1", n)
	}
}

func TestDockerCodecMovedTagUpdatesInPlace(t *testing.T) {
	pg := freshDB(t)
	a := newDockerRepo(t, pg, "dock-a")
	b := newDockerRepo(t, pg, "dock-b")
	enableLog(t, b.db, b.id)

	d1 := a.image(dockerTestImage, "one")
	a.tag(dockerTestImage, "latest", d1)
	replicate(a, b, manifestKey(dockerTestImage, d1), tagKey(dockerTestImage, "latest"))

	var tagID string
	if err := b.db.QueryRowContext(context.Background(),
		`SELECT id::text FROM docker_tags WHERE repository_id = $1::uuid AND name = 'latest'`, b.id).Scan(&tagID); err != nil {
		t.Fatalf("tag id: %v", err)
	}

	d2 := a.image(dockerTestImage, "two")
	a.tag(dockerTestImage, "latest", d2)

	moved := a.export(tagKey(dockerTestImage, "latest"))
	if moved.ContentRef != d2 || !slices.Equal(moved.Deps, []string{manifestKey(dockerTestImage, d2)}) {
		t.Fatalf("moved tag unit = %+v", moved)
	}

	b.importUnit(manifestKey(dockerTestImage, d2), a.export(manifestKey(dockerTestImage, d2)))

	change := b.importUnit(tagKey(dockerTestImage, "latest"), moved)
	if !change.Replaced || change.Artifact != nil {
		t.Fatalf("moved tag change = %+v, want an in-place update without webhook", change)
	}

	var gotID, gotDigest string
	if err := b.db.QueryRowContext(context.Background(),
		`SELECT id::text, manifest_digest FROM docker_tags WHERE repository_id = $1::uuid AND name = 'latest' AND deleted_at IS NULL`,
		b.id).Scan(&gotID, &gotDigest); err != nil {
		t.Fatalf("moved tag row: %v", err)
	}

	if gotID != tagID || gotDigest != d2 {
		t.Fatalf("tag row = %s -> %s, want %s -> %s", gotID, gotDigest, tagID, d2)
	}

	if n := b.count(`SELECT count(*) FROM docker_tags WHERE repository_id = $1::uuid`); n != 1 {
		t.Fatalf("tag rows = %d, want 1", n)
	}

	if got := loggedUnits(t, b.db, b.id); len(got) != 0 {
		t.Fatalf("applying logged %v, want nothing", got)
	}

	assertSameDockerState(t, a, b)
}

func TestDockerCodecDeleteTag(t *testing.T) {
	pg := freshDB(t)
	a := newDockerRepo(t, pg, "dock-a")
	b := newDockerRepo(t, pg, "dock-b")
	enableLog(t, b.db, b.id)

	digest := a.image(dockerTestImage, "one")
	a.tag(dockerTestImage, "latest", digest)
	replicate(a, b, manifestKey(dockerTestImage, digest), tagKey(dockerTestImage, "latest"))

	key := tagKey(dockerTestImage, "latest")

	b.deleteUnit(key, true)

	var by string
	if err := b.db.QueryRowContext(context.Background(),
		`SELECT deleted_by FROM docker_tags WHERE repository_id = $1::uuid AND name = 'latest' AND deleted_at IS NOT NULL`,
		b.id).Scan(&by); err != nil || by != dockerPeer {
		t.Fatalf("soft deleted tag: deleted_by = %q, err = %v", by, err)
	}

	if c, err := dockerCodecForTest(t).Export(context.Background(), b.db, b.id, key); err != nil || c != nil {
		t.Fatalf("export of a trashed tag = %v, %v; want nothing", c, err)
	}

	if change := b.deleteUnit(key, true); !change.Missing {
		t.Fatalf("second delete = %+v, want Missing", change)
	}

	// A re-created tag lives next to the trashed one; a hard delete removes
	// the live row only.
	b.importUnit(key, a.export(key))
	b.deleteUnit(key, false)

	if n := b.count(`SELECT count(*) FROM docker_tags WHERE repository_id = $1::uuid AND deleted_at IS NULL`); n != 0 {
		t.Fatalf("live tags = %d, want 0", n)
	}

	if n := b.count(`SELECT count(*) FROM docker_tags WHERE repository_id = $1::uuid`); n != 1 {
		t.Fatalf("tag rows = %d, want the trashed one only", n)
	}

	if got := loggedUnits(t, b.db, b.id); len(got) != 0 {
		t.Fatalf("applying deletes logged %v, want nothing", got)
	}
}

func TestDockerCodecDeleteManifest(t *testing.T) {
	pg := freshDB(t)
	a := newDockerRepo(t, pg, "dock-a")
	b := newDockerRepo(t, pg, "dock-b")
	enableLog(t, b.db, b.id)

	digest := a.image(dockerTestImage, "one")
	key := manifestKey(dockerTestImage, digest)
	replicate(a, b, key)

	blobs := b.count(`SELECT count(*) FROM docker_blobs WHERE repository_id = $1::uuid`)
	body := strings.TrimPrefix(digest, "sha256:")

	soft := b.deleteUnit(key, true)
	if soft.Missing || soft.RemovedCount != 1 || soft.RemovedSize <= 0 || len(soft.ReleasedBlobs) != 0 {
		t.Fatalf("soft delete = %+v, want usage kept and nothing released", soft)
	}

	if n := b.count(`SELECT count(*) FROM docker_manifests WHERE repository_id = $1::uuid AND deleted_by = $2 AND deleted_at IS NOT NULL`, dockerPeer); n != 1 {
		t.Fatalf("trashed manifests = %d, want 1", n)
	}

	if n := b.count(`SELECT count(*) FROM docker_blobs WHERE repository_id = $1::uuid`); n != blobs {
		t.Fatalf("docker_blobs after soft delete = %d, want %d (the trash keeps layers)", n, blobs)
	}

	if change := b.deleteUnit(key, true); !change.Missing {
		t.Fatalf("second soft delete = %+v, want Missing", change)
	}

	// Re-applied after the trash: a new live row next to the trashed one.
	b.importUnit(key, a.export(key))

	hard := b.deleteUnit(key, false)
	if hard.RemovedCount != 1 || !slices.Equal(hard.ReleasedBlobs, []string{body}) {
		t.Fatalf("hard delete = %+v, want the manifest body released", hard)
	}

	if n := b.count(`SELECT count(*) FROM docker_manifests WHERE repository_id = $1::uuid AND deleted_at IS NULL`); n != 0 {
		t.Fatalf("live manifests = %d, want 0", n)
	}

	if n := b.count(`SELECT count(*) FROM docker_manifest_refs WHERE repository_id = $1::uuid`); n != 3 {
		// the trashed row keeps its three references (config, layer, indexed marker); the live row's cascaded
		t.Fatalf("manifest refs = %d, want 3 (the trashed manifest's)", n)
	}

	if n := b.count(`SELECT count(*) FROM docker_blobs WHERE repository_id = $1::uuid`); n != blobs {
		t.Fatalf("docker_blobs after hard delete = %d, want %d (registry GC reclaims layers)", n, blobs)
	}

	if got := loggedUnits(t, b.db, b.id); len(got) != 0 {
		t.Fatalf("applying deletes logged %v, want nothing", got)
	}
}

func TestDockerCodecIndexAndReferrerDeps(t *testing.T) {
	pg := freshDB(t)
	a := newDockerRepo(t, pg, "dock-a")
	b := newDockerRepo(t, pg, "dock-b")
	enableLog(t, b.db, b.id)

	c1 := a.image(dockerTestImage, "amd64")
	c2 := a.image(dockerTestImage, "arm64")
	absent := "sha256:" + strings.Repeat("0", 64)

	index := a.manifest(dockerTestImage, map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []descriptorJSON{
			{Digest: c1, Size: 1}, {Digest: c2, Size: 1}, {Digest: absent, Size: 1},
		},
	}, "")
	a.tag(dockerTestImage, "multi", index)

	config := a.blob(dockerTestImage, "sbom-config")
	referrer := a.manifest(dockerTestImage, map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config":        descriptorJSON{Digest: config, Size: len("sbom-config")},
		"layers":        []descriptorJSON{},
		"subject":       descriptorJSON{Digest: index, Size: 1},
	}, index)

	indexUnit := a.export(manifestKey(dockerTestImage, index))

	wantDeps := []string{manifestKey(dockerTestImage, c1), manifestKey(dockerTestImage, c2)}
	slices.Sort(wantDeps)

	if !slices.Equal(indexUnit.Deps, wantDeps) {
		t.Fatalf("index deps = %v, want %v (the child that is not pushed is no dependency)", indexUnit.Deps, wantDeps)
	}

	if len(indexUnit.Blobs) != 1 {
		t.Fatalf("index blobs = %+v, want the index body only", indexUnit.Blobs)
	}

	referrerUnit := a.export(manifestKey(dockerTestImage, referrer))
	if !slices.Equal(referrerUnit.Deps, []string{manifestKey(dockerTestImage, index)}) {
		t.Fatalf("referrer deps = %v, want the subject", referrerUnit.Deps)
	}

	replicate(a, b,
		manifestKey(dockerTestImage, c1), manifestKey(dockerTestImage, c2),
		manifestKey(dockerTestImage, index), manifestKey(dockerTestImage, referrer),
		tagKey(dockerTestImage, "multi"))

	assertSameDockerState(t, a, b)

	subjects, err := pg.ListDockerManifestsBySubject(context.Background(), b.id, dockerTestImage, index)
	if err != nil || len(subjects) != 1 || subjects[0].Digest != referrer {
		t.Fatalf("referrers of the index on the replica = %v, %v", subjects, err)
	}

	if got := loggedUnits(t, b.db, b.id); len(got) != 0 {
		t.Fatalf("applying logged %v, want nothing", got)
	}
}

func TestDockerCodecUnindexedManifestIsNotServed(t *testing.T) {
	pg := freshDB(t)
	a := newDockerRepo(t, pg, "dock-a")

	a.blob(dockerTestImage, "late")
	a.storeManifest(dockerTestImage, `{"schemaVersion":2,"unindexed":true}`, "", false)

	key := manifestKey(dockerTestImage, "sha256:"+sha256Hex(`{"schemaVersion":2,"unindexed":true}`))

	_, err := dockerCodecForTest(t).Export(context.Background(), a.db, a.id, key)
	if err == nil || errors.Is(err, ErrInvalid) {
		t.Fatalf("export of an unindexed manifest = %v, want a retryable error", err)
	}
}

func TestDockerCodecRejectsMismatchedUnit(t *testing.T) {
	pg := freshDB(t)
	a := newDockerRepo(t, pg, "dock-a")
	b := newDockerRepo(t, pg, "dock-b")

	d1 := a.image(dockerTestImage, "one")
	d2 := a.image(dockerTestImage, "two")
	unit := a.export(manifestKey(dockerTestImage, d1))

	b.inApply(func(tx *sql.Tx) error {
		_, err := dockerCodecForTest(t).Import(context.Background(), tx, b.id, manifestKey(dockerTestImage, d2), unit, b.blobIDs(unit))
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("import under the wrong key = %v, want ErrInvalid", err)
		}

		_, err = dockerCodecForTest(t).Import(context.Background(), tx, b.id, manifestKey(dockerTestImage, d1), unit, map[string]string{})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("import without its blobs = %v, want ErrInvalid", err)
		}

		return nil
	})
}

func TestDockerCodecBackfillMatchesTriggerKeys(t *testing.T) {
	pg := freshDB(t)
	logged := newDockerRepo(t, pg, "dock-logged")
	backfilled := newDockerRepo(t, pg, "dock-backfilled")
	enableLog(t, logged.db, logged.id)

	for _, r := range []*dockerRepo{logged, backfilled} {
		d1 := r.image(dockerTestImage, "one")
		d2 := r.image("", "two")
		r.tag(dockerTestImage, "latest", d1)
		r.tag("", "stable", d2)
	}

	// A trashed manifest is not live state.
	trashed := backfilled.image(dockerTestImage, "trashed")
	if _, err := backfilled.db.ExecContext(context.Background(),
		`UPDATE docker_manifests SET deleted_at = now() WHERE repository_id = $1::uuid AND digest = $2`, backfilled.id, trashed); err != nil {
		t.Fatalf("trash manifest: %v", err)
	}

	enableLog(t, backfilled.db, backfilled.id)
	backfilled.inApply(func(tx *sql.Tx) error {
		return dockerCodecForTest(t).Backfill(context.Background(), tx, backfilled.id)
	})

	want := loggedUnits(t, logged.db, logged.id)
	got := loggedUnits(t, backfilled.db, backfilled.id)

	if len(want) != 4 || !mapsEqual(got, want) {
		t.Fatalf("backfilled log = %v, trigger log = %v", got, want)
	}

	if a, b := contentRefs(t, logged), contentRefs(t, backfilled); !mapsEqual(a, b) {
		t.Fatalf("backfilled content_ref = %v, trigger content_ref = %v", b, a)
	}
}

func contentRefs(t *testing.T, r *dockerRepo) map[string]string {
	t.Helper()

	rows, err := r.db.QueryContext(context.Background(),
		`SELECT unit_key, content_ref FROM federation_units WHERE repo_id = $1::uuid`, r.id)
	if err != nil {
		t.Fatalf("list content refs: %v", err)
	}
	defer rows.Close()

	out := map[string]string{}

	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatalf("scan content ref: %v", err)
		}

		out[k] = v
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("list content refs: %v", err)
	}

	return out
}

// TestDockerTableColumnsAreDecided pins the columns of the tables the docker
// codec copies, so that a new column forces a decision about replicating it.
func TestDockerTableColumnsAreDecided(t *testing.T) {
	pg := freshDB(t)

	want := map[string][]string{
		// replicated: image, digest, media_type, size, subject_digest, blob_id, created_at.
		// local: id, repository_id, updated_at, deleted_at, deleted_by.
		"docker_manifests": {"blob_id", "created_at", "deleted_at", "deleted_by", "digest", "id", "image", "media_type", "repository_id", "size", "subject_digest", "updated_at"},
		// replicated: image, name, manifest_digest, created_at. local: the rest.
		"docker_tags": {"created_at", "deleted_at", "deleted_by", "id", "image", "manifest_digest", "name", "repository_id", "updated_at"},
		// replicated as refs.
		"docker_manifest_refs": {"digest", "image", "kind", "manifest_id", "repository_id"},
		// replicated as the blobs of a manifest: digest, blob content, size_bytes.
		// local: id, created_at, last_used_at (GC grace period of this instance).
		"docker_blobs": {"blob_id", "created_at", "digest", "id", "last_used_at", "repository_id", "size_bytes"},
		// recreated from the manifest's blob references: image, digest.
		"docker_blob_images": {"created_at", "digest", "image", "repository_id"},
	}

	for table, cols := range want {
		got, err := tableColumns(context.Background(), pg.GetDB(), table)
		if err != nil {
			t.Fatalf("columns of %s: %v", table, err)
		}

		slices.Sort(got)

		if !slices.Equal(got, cols) {
			t.Errorf("%s columns = %v, want %v: decide how the docker codec treats the change", table, got, cols)
		}
	}
}
