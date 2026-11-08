// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/ociref"
)

// dockerSeed writes docker rows straight into the tables of one repository.
type dockerSeed struct {
	*cleanupSeed

	repo *models.Repository
}

func newDockerSeed(t *testing.T, name string) *dockerSeed {
	t.Helper()

	s := newCleanupSeed(t)

	return &dockerSeed{cleanupSeed: s, repo: s.formatRepo(name, "docker")}
}

// digest is a well-formed digest derived from a label.
func digestFor(label string) string {
	sum := sha256.Sum256([]byte(label))

	return "sha256:" + hex.EncodeToString(sum[:])
}

// manifest stores a live manifest of image, last updated 30 days ago, and returns
// the sha256 of its body blob. subject and expires are optional ("" / nil).
func (s *dockerSeed) manifest(image, digest, subject string, expires *time.Duration) string {
	s.t.Helper()

	blobID, sha := s.blob("manifest "+image+digest, time.Hour)

	var expiresAt *time.Time

	if expires != nil {
		at := time.Now().Add(*expires)
		expiresAt = &at
	}

	s.exec(`INSERT INTO docker_manifests (repository_id, image, digest, media_type, blob_id, size, subject_digest, updated_at, expires_at)
	        VALUES ($1, $2, $3, 'application/vnd.oci.image.manifest.v1+json', $4, 10, NULLIF($5, ''), now() - interval '30 days', $6)`,
		s.repo.ID, image, digest, blobID, subject, expiresAt)

	return sha
}

// tag points a tag at a digest, last updated ageDays ago, and returns its id.
func (s *dockerSeed) tag(image, name, digest string, ageDays int) string {
	s.t.Helper()

	var id string
	if err := s.pg.GetDB().QueryRowContext(context.Background(),
		`INSERT INTO docker_tags (repository_id, image, name, manifest_digest, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, now() - make_interval(days => $5), now() - make_interval(days => $5)) RETURNING id`,
		s.repo.ID, image, name, digest, ageDays).Scan(&id); err != nil {
		s.t.Fatalf("insert tag %s: %v", name, err)
	}

	return id
}

// listChild records that the index manifest lists child and is fully indexed
// (registry GC would otherwise re-read the seeded body and replace the rows).
func (s *dockerSeed) listChild(index, child string) {
	s.t.Helper()

	s.exec(`INSERT INTO docker_manifest_refs (manifest_id, repository_id, image, kind, digest)
	        SELECT id, repository_id, image, 'manifest', $2 FROM docker_manifests WHERE repository_id = $1 AND digest = $3`,
		s.repo.ID, child, index)
	s.exec(`INSERT INTO docker_manifest_refs (manifest_id, repository_id, image, kind, digest)
	        SELECT id, repository_id, image, 'indexed', '' FROM docker_manifests WHERE repository_id = $1 AND digest = $2`,
		s.repo.ID, index)
}

func (s *dockerSeed) tagLive(id string) bool {
	return s.count(`SELECT COUNT(*) FROM docker_tags WHERE id = $1 AND deleted_at IS NULL`, id) == 1
}

func (s *dockerSeed) manifestLive(image, digest string) bool {
	return s.count(`SELECT COUNT(*) FROM docker_manifests WHERE repository_id = $1 AND image = $2 AND digest = $3 AND deleted_at IS NULL`,
		s.repo.ID, image, digest) == 1
}

func (s *dockerSeed) pin(sha string) {
	s.t.Helper()

	var bundleID string
	if err := s.pg.GetDB().QueryRowContext(context.Background(),
		`INSERT INTO release_bundles (name, version) VALUES ('b', '1') RETURNING id`).Scan(&bundleID); err != nil {
		s.t.Fatal(err)
	}

	s.exec(
		`INSERT INTO release_bundle_items (bundle_id, source_repo_id, sha256, artifact_path, format) VALUES ($1, $2, $3, 'app', 'docker')`,
		bundleID,
		s.repo.ID,
		sha,
	)
}

func (s *dockerSeed) gc() *DockerGCWorker {
	return NewDockerGCWorker(s.pg, s.blobs, testLogger(s.t), time.Hour, 0)
}

// reload re-reads the repository after its settings changed.
func (s *dockerSeed) reload() *models.Repository {
	s.t.Helper()

	repo, err := s.pg.GetRepositoryByID(context.Background(), s.repo.ID)
	if err != nil || repo == nil {
		s.t.Fatalf("GetRepositoryByID: %v", err)
	}

	return repo
}

// The Go and SQL sides of "what is a dependent tag" must agree.
func TestDependentTagPatternAgreesWithOciref(t *testing.T) {
	s := newCleanupSeed(t)
	hexA := digestFor("a")[len("sha256:"):]

	for _, tag := range []string{
		"sha256-" + hexA, "sha256-" + hexA + ".sig", "sha256-" + hexA + ".att", "sha256-" + hexA + ".sbom", "sha256-" + hexA + ".src",
		"latest", "sha256-abcd.sig", "sha256-" + hexA + ".txt", "sha256-" + hexA + "00", "v1.0",
	} {
		var sql bool
		row := s.pg.GetDB().QueryRowContext(context.Background(), `SELECT $1::text ~ $2::text`, tag, ociref.TagPattern)
		if err := row.Scan(&sql); err != nil {
			t.Fatal(err)
		}

		if _, _, want := ociref.ParseCosignTag(tag); sql != want {
			t.Errorf("SQL pattern on %q = %v, ociref = %v", tag, sql, want)
		}
	}
}

// Dependent tags are never ranked: keep_last_n counts the images, the
// signatures of the images it removes go with them and the ones of the images it
// keeps stay. The preview names exactly what the run removes.
func TestCleanupPolicyTreatsDependentTagsWithTheirImage(t *testing.T) {
	s := newDockerSeed(t, "dep-clean")

	old, fresh := digestFor("old"), digestFor("fresh")
	s.manifest("app", old, "", nil)
	s.manifest("app", fresh, "", nil)
	s.manifest("app", digestFor("old-sig"), "", nil)
	s.manifest("app", digestFor("fresh-sig"), "", nil)

	oldTag, freshTag := s.tag("app", "v1", old, 30), s.tag("app", "v2", fresh, 1)
	oldSig := s.tag("app", ociref.CosignTag(old, ociref.KindSignature), digestFor("old-sig"), 29)
	oldAtt := s.tag("app", ociref.CosignTag(old, ociref.KindAttestation), digestFor("old-sig"), 29)
	freshSig := s.tag("app", ociref.CosignTag(fresh, ociref.KindSignature), digestFor("fresh-sig"), 40)

	p := s.policy(s.repo.ID, new(1), nil, "")

	want := []string{oldTag, oldSig, oldAtt}
	slices.Sort(want)

	if got := previewIDs(s.preview(p)); !slices.Equal(got, want) {
		t.Fatalf("preview = %v, want the old image and its two dependent tags %v", got, want)
	}

	if deleted, err := s.w.runPolicy(context.Background(), p); err != nil || deleted != 3 {
		t.Fatalf("runPolicy = %d, %v; want 3", deleted, err)
	}

	if s.tagLive(oldTag) || s.tagLive(oldSig) || s.tagLive(oldAtt) {
		t.Error("the old image or one of its dependent tags survived")
	}

	if !s.tagLive(freshTag) || !s.tagLive(freshSig) {
		t.Error("the kept image or its signature was removed (a 40 day old signature must not be ranked away)")
	}
}

// A dependent tag survives while any other live tag still holds its subject,
// directly or through the index that lists it, and one whose subject manifest no
// longer exists is removed.
func TestCleanupPolicyOrphanDependentTags(t *testing.T) {
	s := newDockerSeed(t, "dep-orphan")

	shared, child, gone := digestFor("shared"), digestFor("child"), digestFor("gone")
	index := digestFor("index")

	s.manifest("app", shared, "", nil)
	s.manifest("app", child, "", nil)
	s.manifest("app", index, "", nil)
	s.manifest("app", digestFor("sig-shared"), "", nil)
	s.manifest("app", digestFor("sig-child"), "", nil)
	s.manifest("app", digestFor("sig-gone"), "", nil)

	s.listChild(index, child)

	oldShared := s.tag("app", "v1", shared, 30)
	s.tag("app", "alias", shared, 1) // keeps `shared` held
	oldIndex := s.tag("app", "v0", index, 40)
	s.tag("app", "v3", digestFor("another"), 2)

	sigShared := s.tag("app", ociref.CosignTag(shared, ociref.KindSignature), digestFor("sig-shared"), 5)
	sigChild := s.tag("app", ociref.CosignTag(child, ociref.KindSignature), digestFor("sig-child"), 5)
	sigGone := s.tag("app", ociref.CosignTag(gone, ociref.KindSignature), digestFor("sig-gone"), 5)

	p := s.policy(s.repo.ID, new(2), nil, "")
	if _, err := s.w.runPolicy(context.Background(), p); err != nil {
		t.Fatalf("runPolicy: %v", err)
	}

	if s.tagLive(oldShared) || s.tagLive(oldIndex) {
		t.Error("the old tags were kept")
	}

	if !s.tagLive(sigShared) {
		t.Error("signature of an image another tag still holds was removed")
	}

	if s.tagLive(sigChild) {
		t.Error("signature of a child of the removed index survived")
	}

	if s.tagLive(sigGone) {
		t.Error("signature whose subject manifest does not exist survived")
	}
}

// Registry GC keeps what a dependent tag holds while its subject is kept, and
// collects subject, signature and its tag together once the subject is gone.
func TestDockerGCFollowsDependentTags(t *testing.T) {
	s := newDockerSeed(t, "dep-gc")
	ctx := context.Background()

	live, dead := digestFor("live"), digestFor("dead")
	pinned := digestFor("pinned")

	s.manifest("app", live, "", nil)
	s.manifest("app", digestFor("sig-live"), "", nil)
	s.manifest("app", dead, "", nil)
	s.manifest("app", digestFor("sig-dead"), "", nil)
	s.manifest("app", digestFor("sig-orphan"), "", nil)
	pinnedSHA := s.manifest("app", pinned, "", nil)
	s.manifest("app", digestFor("sig-pinned"), "", nil)
	s.pin(pinnedSHA)

	s.tag("app", "v1", live, 1)
	sigLive := s.tag("app", ociref.CosignTag(live, ociref.KindSignature), digestFor("sig-live"), 1)
	sigDead := s.tag("app", ociref.CosignTag(dead, ociref.KindSignature), digestFor("sig-dead"), 1)
	// Its subject was never pushed (or is long gone).
	sigOrphan := s.tag("app", ociref.CosignTag(digestFor("never"), ociref.KindSignature), digestFor("sig-orphan"), 1)
	sigPinned := s.tag("app", ociref.CosignTag(pinned, ociref.KindSignature), digestFor("sig-pinned"), 1)

	report, err := s.gc().RunRepo(ctx, s.reload(), DockerGCOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}

	if report.ManifestsDeleted != 3 {
		t.Fatalf("dry run reports %d manifests, want the dead one, its signature and the orphan signature", report.ManifestsDeleted)
	}

	if _, err := s.gc().RunRepo(ctx, s.reload(), DockerGCOptions{}); err != nil {
		t.Fatalf("RunRepo: %v", err)
	}

	for _, tt := range []struct {
		name  string
		image string
		dig   string
		tag   string
		want  bool
	}{
		{"signature of a tagged image", "app", digestFor("sig-live"), sigLive, true},
		{"signature of a bundle-pinned subject", "app", digestFor("sig-pinned"), sigPinned, true},
		{"signature of a collected subject", "app", digestFor("sig-dead"), sigDead, false},
		{"signature without a subject", "app", digestFor("sig-orphan"), sigOrphan, false},
	} {
		if got := s.manifestLive(tt.image, tt.dig); got != tt.want {
			t.Errorf("%s: manifest live = %v, want %v", tt.name, got, tt.want)
		}

		if got := s.tagLive(tt.tag); got != tt.want {
			t.Errorf("%s: tag live = %v, want %v", tt.name, got, tt.want)
		}
	}

	if s.manifestLive("app", dead) || !s.manifestLive("app", pinned) || !s.manifestLive("app", live) {
		t.Error("GC removed a held manifest or kept the dead one")
	}
}

// quay.expires-after: only a repository that opts in expires its manifests, with
// their tags, referrers and signatures, ignoring the grace period, and never a
// bundle-pinned manifest nor a child of an index that stays.
func TestDockerGCExpiresManifests(t *testing.T) {
	s := newDockerSeed(t, "dep-expiry")
	ctx := context.Background()

	past, future := -time.Hour, 24*time.Hour
	expired, later, pinned := digestFor("expired"), digestFor("later"), digestFor("pinned")
	index, child := digestFor("index"), digestFor("child")

	s.manifest("app", expired, "", &past)
	s.manifest("app", digestFor("referrer"), expired, nil)
	s.manifest("app", digestFor("sig"), "", nil)
	s.manifest("app", later, "", &future)
	pinSHA := s.manifest("app", pinned, "", &past)
	s.manifest("app", index, "", &future)
	s.manifest("app", child, "", &past)
	s.listChild(index, child)
	s.pin(pinSHA)

	expiredTag := s.tag("app", "pr-1", expired, 1)
	sigTag := s.tag("app", ociref.CosignTag(expired, ociref.KindSignature), digestFor("sig"), 1)
	laterTag := s.tag("app", "pr-2", later, 1)
	pinnedTag := s.tag("app", "pr-3", pinned, 1)
	indexTag := s.tag("app", "pr-4", index, 1)

	// Not opted in: nothing expires, whatever the manifests declare.
	if report, err := s.gc().RunRepo(ctx, s.reload(), DockerGCOptions{Grace: 24 * time.Hour}); err != nil || report.ManifestsExpired != 0 {
		t.Fatalf("repository that does not honour expiry: %+v, %v", report, err)
	}

	if !s.manifestLive("app", expired) {
		t.Fatal("manifest expired in a repository that does not honour quay.expires-after")
	}

	s.exec(`UPDATE repositories SET honour_quay_expiry = true WHERE id = $1`, s.repo.ID)

	dry, err := s.gc().RunRepo(ctx, s.reload(), DockerGCOptions{DryRun: true, Grace: 24 * time.Hour})
	if err != nil || dry.ManifestsExpired != 3 || !s.manifestLive("app", expired) {
		t.Fatalf("dry run = %+v, %v; want 3 expired (manifest, referrer, signature) and nothing deleted", dry, err)
	}

	report, err := s.gc().RunRepo(ctx, s.reload(), DockerGCOptions{Grace: 24 * time.Hour})
	if err != nil {
		t.Fatalf("RunRepo: %v", err)
	}

	if report.ManifestsExpired != 3 {
		t.Errorf("ManifestsExpired = %d, want 3", report.ManifestsExpired)
	}

	for _, tt := range []struct {
		name string
		live bool
		want bool
	}{
		{"expired manifest", s.manifestLive("app", expired), false},
		{"its referrer", s.manifestLive("app", digestFor("referrer")), false},
		{"its signature", s.manifestLive("app", digestFor("sig")), false},
		{"its tag", s.tagLive(expiredTag), false},
		{"its signature tag", s.tagLive(sigTag), false},
		{"manifest with a later deadline", s.manifestLive("app", later), true},
		{"its tag", s.tagLive(laterTag), true},
		{"bundle-pinned expired manifest", s.manifestLive("app", pinned), true},
		{"its tag", s.tagLive(pinnedTag), true},
		{"expired child of an index that stays", s.manifestLive("app", child), true},
		{"the index", s.manifestLive("app", index), true},
		{"the index tag", s.tagLive(indexTag), true},
	} {
		if tt.live != tt.want {
			t.Errorf("%s: live = %v, want %v", tt.name, tt.live, tt.want)
		}
	}
}
