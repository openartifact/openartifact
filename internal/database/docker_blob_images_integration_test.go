// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestDockerBlobImageVisibility(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "docker-links", "docker")

	sha := strings.Repeat("a1", 32)
	blob := &models.DockerBlob{
		RepositoryID: s.repo,
		Digest:       "sha256:" + sha,
		BlobSHA256:   sha,
		Size:         7,
		MediaType:    "application/octet-stream",
	}
	imageA, imageB := "team-a/app", "team-b/app"
	blob.Image = &imageA

	if err := pg.CreateDockerBlob(ctx, blob); err != nil {
		t.Fatal(err)
	}

	visible := func(image string) bool {
		t.Helper()

		v, err := pg.DockerBlobVisible(ctx, s.repo, image, blob.Digest)
		if err != nil {
			t.Fatal(err)
		}

		return v
	}

	if !visible(imageA) || visible(imageB) {
		t.Fatalf("after push to A: A=%v B=%v, want true/false", visible(imageA), visible(imageB))
	}

	if err := pg.LinkDockerBlob(ctx, s.repo, imageB, blob.Digest); err != nil {
		t.Fatal(err)
	}

	if !visible(imageB) {
		t.Fatal("blob not visible to B after link")
	}

	// A blob that predates image links (no rows) is visible to every image.
	legacySHA := strings.Repeat("b2", 32)
	legacy := &models.DockerBlob{RepositoryID: s.repo, Digest: "sha256:" + legacySHA, BlobSHA256: legacySHA, Size: 3}

	if err := pg.CreateDockerBlob(ctx, legacy); err != nil {
		t.Fatal(err)
	}

	if v, err := pg.DockerBlobVisible(ctx, s.repo, imageB, legacy.Digest); err != nil || !v {
		t.Fatalf("unlinked blob visible = %v, %v, want true", v, err)
	}

	// The size verified at upload is what the registry serves.
	got, err := pg.GetDockerBlob(ctx, s.repo, blob.Digest)
	if err != nil || got == nil || got.Size != 7 {
		t.Fatalf("GetDockerBlob = %+v, %v, want size 7", got, err)
	}
}

func TestCreateDockerManifestChecksBlobsUnderLock(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "docker-reqblobs", "docker")

	sha := strings.Repeat("c3", 32)
	image := "app"

	if err := pg.CreateDockerBlob(
		ctx,
		&models.DockerBlob{RepositoryID: s.repo, Digest: "sha256:" + sha, BlobSHA256: sha, Size: 1, Image: &image},
	); err != nil {
		t.Fatal(err)
	}

	mk := func(img string, required ...string) error {
		return pg.CreateDockerManifest(ctx, &models.DockerManifest{
			RepositoryID:  s.repo,
			Image:         img,
			Digest:        "sha256:" + strings.Repeat("d4", 32),
			MediaType:     "application/vnd.oci.image.manifest.v1+json",
			BlobID:        s.blob(10),
			Size:          10,
			Refs:          &models.DockerManifestRefs{Blobs: required},
			RequiredBlobs: required,
		})
	}

	if err := mk(image, "sha256:"+sha); err != nil {
		t.Fatalf("manifest with an available blob: %v", err)
	}

	if err := mk(image, "sha256:"+strings.Repeat("e5", 32)); !errors.Is(err, ErrDockerBlobsMissing) {
		t.Fatalf("manifest with a missing blob = %v, want ErrDockerBlobsMissing", err)
	}

	if err := mk("other", "sha256:"+sha); !errors.Is(err, ErrDockerBlobsMissing) {
		t.Fatalf("manifest of an image the blob is not linked to = %v, want ErrDockerBlobsMissing", err)
	}
}

func TestUpsertBlobKeepsFirstMediaTypeAndFixesZeroSize(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()

	sha := strings.Repeat("f6", 32)

	if _, err := pg.upsertBlobWithMediaType(ctx, sha, sha, "application/gzip", 0); err != nil {
		t.Fatal(err)
	}

	if _, err := pg.upsertBlobWithMediaType(ctx, sha, sha, "text/html", 42); err != nil {
		t.Fatal(err)
	}

	if _, err := pg.upsertBlobWithMediaType(ctx, sha, sha, "text/plain", 7); err != nil {
		t.Fatal(err)
	}

	var (
		mt   string
		size int64
	)

	if err := pg.GetDB().
		QueryRowContext(ctx, `SELECT media_type, size_bytes FROM blobs WHERE sha256 = $1`, sha).
		Scan(&mt, &size); err != nil {
		t.Fatal(err)
	}

	if mt != "application/gzip" || size != 42 {
		t.Fatalf("blobs row = (%q, %d), want first media type and the first non-zero size (application/gzip, 42)", mt, size)
	}
}

const (
	uploadOther = "44444444-4444-4444-8444-444444444444"
	tokenA      = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	tokenB      = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	noRepo      = "00000000-0000-4000-8000-000000000000"
	liveTTL     = time.Hour
	shortTTL    = 50 * time.Millisecond
)

// uploadSession opens an upload session in a seeded docker repository.
func uploadSession(t *testing.T, name string) (*PostgresDB, *models.DockerUpload) {
	t.Helper()

	pg := newQuotaTestDB(t, 8)
	s := newQuotaSeeder(t, pg, name, "docker")

	up := &models.DockerUpload{RepositoryID: s.repo, Image: "app", UploadUUID: "11111111-1111-4111-8111-111111111111"}
	if err := pg.CreateDockerUpload(context.Background(), up); err != nil {
		t.Fatal(err)
	}

	return pg, up
}

// acquire takes the lease and fails the test unless it is granted.
func acquire(t *testing.T, pg *PostgresDB, up *models.DockerUpload, token string, ttl time.Duration) *models.DockerUpload {
	t.Helper()

	got, err := pg.AcquireDockerUploadLease(context.Background(), up.RepositoryID, up.Image, up.UploadUUID, token, ttl)
	if err != nil || got == nil {
		t.Fatalf("acquire %s = %+v, %v, want the session", token, got, err)
	}

	return got
}

// refused fails the test unless the lease is refused (nil session, no error).
func refused(t *testing.T, pg *PostgresDB, repoID, image, uploadUUID, token string) {
	t.Helper()

	got, err := pg.AcquireDockerUploadLease(context.Background(), repoID, image, uploadUUID, token, liveTTL)
	if err != nil || got != nil {
		t.Fatalf("acquire %s = %+v, %v, want nil (refused)", token, got, err)
	}
}

func TestAcquireDockerUploadLeaseIsExclusive(t *testing.T) {
	pg, up := uploadSession(t, "docker-lease")

	first := acquire(t, pg, up, tokenA, shortTTL)
	if first.ID != up.ID || first.Offset != 0 || len(first.HashState) != 0 || first.StageState != "" {
		t.Fatalf("acquired session = %+v, want the fresh session at offset 0", first)
	}

	refused(t, pg, up.RepositoryID, up.Image, up.UploadUUID, tokenB)

	// The holder's own token does not get a second lease either: one request at a time.
	refused(t, pg, up.RepositoryID, up.Image, up.UploadUUID, tokenA)

	time.Sleep(3 * shortTTL)

	// A lease whose holder died is taken over once it has expired.
	acquire(t, pg, up, tokenB, liveTTL)

	// The session of another repository or an unknown session grants nothing.
	refused(t, pg, noRepo, up.Image, up.UploadUUID, tokenA)
	refused(t, pg, up.RepositoryID, up.Image, uploadOther, tokenA)
}

func TestAcquireDockerUploadLeaseReturnsTheCommittedPosition(t *testing.T) {
	pg, up := uploadSession(t, "docker-lease-fresh")
	ctx := context.Background()

	acquire(t, pg, up, tokenA, liveTTL)

	state := []byte{0, 1, 2, 0xff}
	if ok, err := pg.AdvanceDockerUpload(
		ctx,
		up.RepositoryID,
		up.UploadUUID,
		tokenA,
		0,
		10,
		state,
		"stage-1",
		liveTTL,
		false,
	); err != nil ||
		!ok {
		t.Fatalf("advance = %v, %v, want true", ok, err)
	}

	got := acquire(t, pg, up, tokenB, liveTTL)
	if got.Offset != 10 || !bytes.Equal(got.HashState, state) || got.StageState != "stage-1" {
		t.Fatalf("acquired = %+v, want offset 10 with the stored hash and stage state", got)
	}
}

func TestRenewDockerUploadLeaseNeedsTheToken(t *testing.T) {
	pg, up := uploadSession(t, "docker-renew")
	ctx := context.Background()

	acquire(t, pg, up, tokenA, shortTTL)

	if ok, err := pg.RenewDockerUploadLease(ctx, up.RepositoryID, up.UploadUUID, tokenB, liveTTL); err != nil || ok {
		t.Fatalf("renew with a foreign token = %v, %v, want false", ok, err)
	}

	if ok, err := pg.RenewDockerUploadLease(ctx, noRepo, up.UploadUUID, tokenA, liveTTL); err != nil || ok {
		t.Fatalf("renew in a foreign repository = %v, %v, want false", ok, err)
	}

	if ok, err := pg.RenewDockerUploadLease(ctx, up.RepositoryID, up.UploadUUID, tokenA, liveTTL); err != nil || !ok {
		t.Fatalf("renew by the holder = %v, %v, want true", ok, err)
	}

	// The renewal outlives the original ttl.
	time.Sleep(3 * shortTTL)
	refused(t, pg, up.RepositoryID, up.Image, up.UploadUUID, tokenB)

	// A renewal is activity for the idle sweep: a session a request is still
	// streaming into is not stale.
	got, err := pg.GetDockerUpload(ctx, up.RepositoryID, up.Image, up.UploadUUID)
	if err != nil || got == nil || !got.UpdatedAt.After(up.UpdatedAt) {
		t.Fatalf("updated_at after renew = %+v, %v, want after %v", got, err, up.UpdatedAt)
	}
}

func TestRenewDockerUploadLeaseFailsAfterTakeover(t *testing.T) {
	pg, up := uploadSession(t, "docker-renew-lost")
	ctx := context.Background()

	acquire(t, pg, up, tokenA, shortTTL)
	time.Sleep(3 * shortTTL)
	acquire(t, pg, up, tokenB, liveTTL)

	if ok, err := pg.RenewDockerUploadLease(ctx, up.RepositoryID, up.UploadUUID, tokenA, liveTTL); err != nil || ok {
		t.Fatalf("renew after takeover = %v, %v, want false", ok, err)
	}
}

func TestReleaseDockerUploadLeaseNeedsTheToken(t *testing.T) {
	pg, up := uploadSession(t, "docker-release")
	ctx := context.Background()

	acquire(t, pg, up, tokenA, liveTTL)

	// A holder that lost the lease releases nothing and is not told off.
	if err := pg.ReleaseDockerUploadLease(ctx, up.RepositoryID, up.UploadUUID, tokenB); err != nil {
		t.Fatalf("release with a foreign token = %v, want no error", err)
	}

	refused(t, pg, up.RepositoryID, up.Image, up.UploadUUID, tokenB)

	if err := pg.ReleaseDockerUploadLease(ctx, up.RepositoryID, up.UploadUUID, tokenA); err != nil {
		t.Fatalf("release by the holder = %v", err)
	}

	acquire(t, pg, up, tokenB, liveTTL)

	if err := pg.ReleaseDockerUploadLease(ctx, noRepo, uploadOther, tokenA); err != nil {
		t.Fatalf("release of an unknown session = %v, want no error", err)
	}
}

//nolint:gocyclo,cyclop // one case per rule of the compare-and-set
func TestAdvanceDockerUploadIsCompareAndSet(t *testing.T) {
	pg, up := uploadSession(t, "docker-advance")
	ctx := context.Background()

	acquire(t, pg, up, tokenA, liveTTL)

	state := []byte("state-after-10")

	// Nothing may change when the lease token or the old offset does not match.
	for name, c := range map[string]struct {
		token string
		old   int64
	}{
		"foreign token": {tokenB, 0},
		"stale offset":  {tokenA, 5},
	} {
		ok, err := pg.AdvanceDockerUpload(ctx, up.RepositoryID, up.UploadUUID, c.token, c.old, 99, []byte("other"), "other", liveTTL, true)
		if err != nil || ok {
			t.Fatalf("%s: advance = %v, %v, want false", name, ok, err)
		}

		got, err := pg.GetDockerUpload(ctx, up.RepositoryID, up.Image, up.UploadUUID)
		if err != nil || got == nil || got.Offset != 0 || len(got.HashState) != 0 || got.StageState != "" {
			t.Fatalf("%s: session = %+v, %v, want untouched", name, got, err)
		}
	}

	if ok, err := pg.AdvanceDockerUpload(ctx, noRepo, up.UploadUUID, tokenA, 0, 10, state, "s", liveTTL, true); err != nil || ok {
		t.Fatalf("advance in a foreign repository = %v, %v, want false", ok, err)
	}

	// Keeping the lease moves offset, hash state and stage state together and
	// leaves the session held.
	if ok, err := pg.AdvanceDockerUpload(
		ctx,
		up.RepositoryID,
		up.UploadUUID,
		tokenA,
		0,
		10,
		state,
		"stage-1",
		liveTTL,
		true,
	); err != nil ||
		!ok {
		t.Fatalf("advance keeping the lease = %v, %v, want true", ok, err)
	}

	got, err := pg.GetDockerUpload(ctx, up.RepositoryID, up.Image, up.UploadUUID)
	if err != nil || got == nil || got.Offset != 10 || !bytes.Equal(got.HashState, state) || got.StageState != "stage-1" {
		t.Fatalf("session = %+v, %v, want offset 10, the new hash state and stage-1", got, err)
	}

	if !got.UpdatedAt.After(up.UpdatedAt) {
		t.Fatalf("updated_at = %v, want after %v: the idle sweep counts advances as activity", got.UpdatedAt, up.UpdatedAt)
	}

	refused(t, pg, up.RepositoryID, up.Image, up.UploadUUID, tokenB)

	// Replaying the same move fails: the offset is no longer the old one.
	if ok, err := pg.AdvanceDockerUpload(
		ctx,
		up.RepositoryID,
		up.UploadUUID,
		tokenA,
		0,
		10,
		state,
		"stage-1",
		liveTTL,
		true,
	); err != nil ||
		ok {
		t.Fatalf("replayed advance = %v, %v, want false", ok, err)
	}

	// Releasing the lease with the move frees the session for the next request,
	// which starts from the new position.
	if ok, err := pg.AdvanceDockerUpload(
		ctx,
		up.RepositoryID,
		up.UploadUUID,
		tokenA,
		10,
		25,
		[]byte("state-25"),
		"stage-2",
		liveTTL,
		false,
	); err != nil ||
		!ok {
		t.Fatalf("advance releasing the lease = %v, %v, want true", ok, err)
	}

	next := acquire(t, pg, up, tokenB, liveTTL)
	if next.Offset != 25 || string(next.HashState) != "state-25" || next.StageState != "stage-2" {
		t.Fatalf("next request starts from %+v, want offset 25, state-25, stage-2", next)
	}

	// The previous holder no longer holds anything.
	if ok, err := pg.AdvanceDockerUpload(
		ctx,
		up.RepositoryID,
		up.UploadUUID,
		tokenA,
		25,
		30,
		[]byte("x"),
		"x",
		liveTTL,
		false,
	); err != nil ||
		ok {
		t.Fatalf("advance without the lease = %v, %v, want false", ok, err)
	}
}

func TestAdvanceDockerUploadOfDeletedSession(t *testing.T) {
	pg, up := uploadSession(t, "docker-advance-gone")
	ctx := context.Background()

	acquire(t, pg, up, tokenA, liveTTL)

	if err := pg.DeleteDockerUpload(ctx, up.RepositoryID, up.UploadUUID); err != nil {
		t.Fatal(err)
	}

	if ok, err := pg.AdvanceDockerUpload(
		ctx,
		up.RepositoryID,
		up.UploadUUID,
		tokenA,
		0,
		10,
		[]byte("s"),
		"s",
		liveTTL,
		true,
	); err != nil ||
		ok {
		t.Fatalf("advance of a deleted session = %v, %v, want false", ok, err)
	}

	if ok, err := pg.RenewDockerUploadLease(ctx, up.RepositoryID, up.UploadUUID, tokenA, liveTTL); err != nil || ok {
		t.Fatalf("renew of a deleted session = %v, %v, want false", ok, err)
	}

	if got, err := pg.GetDockerUpload(ctx, up.RepositoryID, up.Image, up.UploadUUID); err != nil || got != nil {
		t.Fatalf("get of a deleted session = %+v, %v, want nil", got, err)
	}
}

func TestGetDockerUploadRoundTripsPositionState(t *testing.T) {
	pg, up := uploadSession(t, "docker-get-state")
	ctx := context.Background()

	fresh, err := pg.GetDockerUpload(ctx, up.RepositoryID, up.Image, up.UploadUUID)
	if err != nil || fresh == nil || fresh.Offset != 0 || len(fresh.HashState) != 0 || fresh.StageState != "" {
		t.Fatalf("fresh session = %+v, %v, want offset 0 and empty states", fresh, err)
	}

	// Binary state, including NUL bytes, survives unchanged.
	state := make([]byte, 108)
	for i := range state {
		state[i] = byte(i * 7)
	}

	stage := `{"parts":[{"n":1,"etag":"\"abc\""}]}`

	acquire(t, pg, up, tokenA, liveTTL)

	if ok, err := pg.AdvanceDockerUpload(
		ctx,
		up.RepositoryID,
		up.UploadUUID,
		tokenA,
		0,
		1<<33,
		state,
		stage,
		liveTTL,
		false,
	); err != nil ||
		!ok {
		t.Fatalf("advance = %v, %v, want true", ok, err)
	}

	got, err := pg.GetDockerUpload(ctx, up.RepositoryID, up.Image, up.UploadUUID)
	if err != nil || got == nil || got.Offset != 1<<33 || !bytes.Equal(got.HashState, state) || got.StageState != stage {
		t.Fatalf("session = %+v, %v, want offset %d with the stored hash and stage state", got, err, int64(1<<33))
	}

	if got, err := pg.GetDockerUpload(ctx, noRepo, up.Image, up.UploadUUID); err != nil || got != nil {
		t.Fatalf("foreign get = %+v, %v, want nil", got, err)
	}
}

// The sweeper asks which upload ids still have a session; deleting the session
// drops it from the answer.
func TestExistingDockerUploadIDsFollowTheSession(t *testing.T) {
	pg, up := uploadSession(t, "docker-existing")
	ctx := context.Background()

	ids, err := pg.ExistingDockerUploadIDs(ctx, []string{up.UploadUUID, uploadOther, "not-a-uuid", ""})
	if err != nil || len(ids) != 1 || ids[0] != up.UploadUUID {
		t.Fatalf("existing ids = %v, %v, want only the live session", ids, err)
	}

	if ids, err := pg.ExistingDockerUploadIDs(ctx, nil); err != nil || len(ids) != 0 {
		t.Fatalf("existing ids of none = %v, %v, want none", ids, err)
	}

	if err := pg.DeleteDockerUpload(ctx, up.RepositoryID, up.UploadUUID); err != nil {
		t.Fatal(err)
	}

	if ids, err := pg.ExistingDockerUploadIDs(ctx, []string{up.UploadUUID}); err != nil || len(ids) != 0 {
		t.Fatalf("existing ids after delete = %v, %v, want none", ids, err)
	}
}

// A session belongs to the image it was started for: asked for through another
// image it does not exist (no lease, no status), and a session that predates
// the image column (NULL) is reachable through no image at all.
func TestDockerUploadIsBoundToItsImage(t *testing.T) {
	pg, up := uploadSession(t, "docker-upload-image")
	ctx := context.Background()

	if got, err := pg.GetDockerUpload(ctx, up.RepositoryID, up.Image, up.UploadUUID); err != nil || got == nil || got.Image != up.Image {
		t.Fatalf("owner get = %+v, %v, want the session of image %q", got, err, up.Image)
	}

	for _, other := range []string{"other", ""} {
		if got, err := pg.GetDockerUpload(ctx, up.RepositoryID, other, up.UploadUUID); err != nil || got != nil {
			t.Fatalf("get through image %q = %+v, %v, want nil", other, got, err)
		}

		refused(t, pg, up.RepositoryID, other, up.UploadUUID, tokenA)
	}

	// The refused attempts took no lease: the owner still gets it.
	acquire(t, pg, up, tokenB, liveTTL)

	if _, err := pg.GetDB().ExecContext(ctx, `UPDATE docker_uploads SET image = NULL, lease_token = NULL, lease_until = NULL`); err != nil {
		t.Fatal(err)
	}

	for _, image := range []string{up.Image, ""} {
		if got, err := pg.GetDockerUpload(ctx, up.RepositoryID, image, up.UploadUUID); err != nil || got != nil {
			t.Fatalf("legacy session get through image %q = %+v, %v, want nil", image, got, err)
		}

		refused(t, pg, up.RepositoryID, image, up.UploadUUID, tokenA)
	}
}
