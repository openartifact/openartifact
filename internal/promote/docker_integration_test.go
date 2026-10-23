// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// dockerIndexImage is a multi-arch image seeded into a repository: an index
// with an amd64 and an arm64 child that share one layer, the index tagged 1.0
// and latest and the amd64 child tagged amd64-only.
type dockerIndexImage struct {
	index, amd, arm dockerSeed
	manifestBytes   int64
	blobBytes       int64
	distinctBlobs   int
}

const dockerTestImage = "team/app"

func seedDockerIndexImage(h *harness, repoID string) dockerIndexImage {
	h.t.Helper()

	amdBlob, _ := h.blob("manifest amd64")
	armBlob, _ := h.blob("manifest arm64")
	idxBlob, _ := h.blob("manifest index")

	amd := seedDockerManifest(h, repoID, amdBlob, dockerSpec{image: dockerTestImage, blobs: []string{"cfg amd64", "layer shared", "layer amd64"}, tags: []string{"amd64-only"}})
	arm := seedDockerManifest(h, repoID, armBlob, dockerSpec{image: dockerTestImage, blobs: []string{"cfg arm64", "layer shared", "layer arm64"}})
	idx := seedDockerManifest(h, repoID, idxBlob, dockerSpec{
		image: dockerTestImage, mediaType: dockerIndexMedia, children: []string{amd.digest, arm.digest}, tags: []string{"latest", "1.0"},
	})

	return dockerIndexImage{
		index: idx, amd: amd, arm: arm,
		manifestBytes: idx.size + amd.size + arm.size,
		blobBytes:     dockerBlobsSize("cfg amd64", "cfg arm64", "layer shared", "layer amd64", "layer arm64"),
		distinctBlobs: 5,
	}
}

func dockerTags(h *harness, repoID string) []string {
	h.t.Helper()

	rows, err := h.pg.GetDB().QueryContext(context.Background(), `SELECT image || ':' || name FROM docker_tags WHERE repository_id = $1 AND deleted_at IS NULL ORDER BY 1`, repoID)
	if err != nil {
		h.t.Fatalf("list tags: %v", err)
	}

	defer func() { _ = rows.Close() }()

	var tags []string

	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			h.t.Fatalf("scan tag: %v", err)
		}

		tags = append(tags, s)
	}

	if err := rows.Err(); err != nil {
		h.t.Fatalf("read tags: %v", err)
	}

	return tags
}

func TestDockerIndexPromotion(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "docker")
	src, dst := h.repo("docker", false), h.repo("docker", false)
	img := seedDockerIndexImage(h, src.ID)
	req := Request{Src: src, Dst: dst, SHA256: img.index.sha, Path: dockerTestImage + ":latest"}

	blobsBefore := h.count(`SELECT COUNT(*) FROM blobs`)

	res, err := h.promoteOnce(p, req)
	if err != nil {
		t.Fatalf("promotion: %v", err)
	}

	assertDockerIndexCopied(t, h, res, img, src, dst)

	blobsAfter := h.count(`SELECT COUNT(*) FROM blobs`)
	if blobsAfter != blobsBefore {
		t.Errorf("blobs rows %d -> %d: a promotion must not create blobs", blobsBefore, blobsAfter)
	}

	t.Run("promoting twice is idempotent", func(t *testing.T) {
		again, err := h.promoteOnce(p, req)
		if err != nil {
			t.Fatalf("second promotion: %v", err)
		}

		if len(again.Copied) != 0 || len(again.Existing) != 3 || again.Usage.Artifacts != 0 || again.Usage.Bytes != 0 {
			t.Errorf("second promotion = %+v, want 3 existing and nothing charged", again)
		}

		if n := h.count(`SELECT COUNT(*) FROM docker_manifests WHERE repository_id = $1`, dst.ID); n != 3 {
			t.Errorf("dst has %d manifest rows after a retry, want 3", n)
		}

		if n := h.count(`SELECT COUNT(*) FROM docker_tags WHERE repository_id = $1`, dst.ID); n != 2 {
			t.Errorf("dst has %d tag rows after a retry, want 2", n)
		}
	})

	t.Run("a missing layer of an existing manifest is completed", func(t *testing.T) {
		h.exec(`DELETE FROM docker_blobs WHERE repository_id = $1 AND digest = $2`, dst.ID, "sha256:"+sha256Of(t, h, "layer amd64"))

		again, err := h.promoteOnce(p, req)
		if err != nil {
			t.Fatalf("completing promotion: %v", err)
		}

		if len(again.Copied) != 0 || again.Usage.Artifacts != 0 || again.Usage.Bytes != dockerBlobSize("layer amd64") {
			t.Errorf("completing promotion = %+v, want only the missing layer's bytes", again)
		}

		if n := h.count(`SELECT COUNT(*) FROM docker_blobs WHERE repository_id = $1`, dst.ID); n != img.distinctBlobs {
			t.Errorf("dst has %d docker_blobs rows, want %d", n, img.distinctBlobs)
		}

		if n := h.count(`SELECT COUNT(*) FROM docker_blob_images WHERE repository_id = $1 AND image = $2`, dst.ID, dockerTestImage); n != img.distinctBlobs {
			t.Errorf("dst links %d blobs to the image, want %d", n, img.distinctBlobs)
		}
	})
}

// assertDockerIndexCopied checks what promoting seedDockerIndexImage's index wrote.
func assertDockerIndexCopied(t *testing.T, h *harness, res *Result, img dockerIndexImage, src, dst *models.Repository) {
	t.Helper()

	if len(res.Copied) != 3 || len(res.Existing) != 0 {
		t.Fatalf("copied %d existing %d, want 3 and 0: %+v", len(res.Copied), len(res.Existing), res)
	}

	if first := res.Copied[0]; first.Name != dst.Name+"/"+dockerTestImage || first.Version != "1.0" || first.SHA256 != img.index.sha {
		t.Errorf("first artifact = %+v, want the index of %s/%s tagged 1.0", first, dst.Name, dockerTestImage)
	}

	// Manifests are charged their size and one artifact each; layers and
	// configs are bytes only, the shared layer once.
	if res.Usage.Artifacts != 3 || res.Usage.Bytes != img.manifestBytes+img.blobBytes {
		t.Errorf("usage = %+v, want 3 artifacts and %d bytes", res.Usage, img.manifestBytes+img.blobBytes)
	}

	if n := h.count(`SELECT COUNT(*) FROM docker_manifests WHERE repository_id = $1 AND deleted_at IS NULL`, dst.ID); n != 3 {
		t.Errorf("dst has %d manifests, want index + 2 children", n)
	}

	// Only the tags of the promoted index come along, not the child's.
	if got := dockerTags(h, dst.ID); !slices.Equal(got, []string{dockerTestImage + ":1.0", dockerTestImage + ":latest"}) {
		t.Errorf("dst tags = %v, want 1.0 and latest of the index", got)
	}

	if n := h.count(`SELECT COUNT(*) FROM docker_manifest_refs WHERE repository_id = $1 AND kind = 'manifest'`, dst.ID); n != 2 {
		t.Errorf("dst index refs %d children, want 2", n)
	}

	if n := h.count(`SELECT COUNT(*) FROM docker_manifest_refs WHERE repository_id = $1 AND kind = 'indexed'`, dst.ID); n != 3 {
		t.Errorf("dst has %d indexed markers, want one per manifest", n)
	}

	if n := h.count(`SELECT COUNT(*) FROM docker_blobs WHERE repository_id = $1`, dst.ID); n != img.distinctBlobs {
		t.Errorf("dst has %d docker_blobs rows, want %d", n, img.distinctBlobs)
	}

	if n := h.count(`SELECT COUNT(*) FROM docker_blob_images WHERE repository_id = $1 AND image = $2`, dst.ID, dockerTestImage); n != img.distinctBlobs {
		t.Errorf("dst links %d blobs to the image, want %d", n, img.distinctBlobs)
	}

	for _, key := range []string{img.index.id, img.index.digest, img.amd.digest, img.arm.id, "latest", src.Name + "/" + dockerTestImage + "/blobs/sha256:" + sha256Of(t, h, "layer shared")} {
		if !slices.Contains(res.QuarantineKeys, key) {
			t.Errorf("quarantine keys lack %q: %v", key, res.QuarantineKeys)
		}
	}
}

func sha256Of(t *testing.T, h *harness, content string) string {
	t.Helper()

	_, sha := h.blob(content)

	return sha
}

func TestDockerBlobsAlreadyInTargetAreNotCharged(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "docker")
	src, dst := h.repo("docker", false), h.repo("docker", false)
	img := seedDockerIndexImage(h, src.ID)

	// The target already stores the shared layer for another image.
	otherBlob, _ := h.blob("manifest of another image")
	seedDockerManifest(h, dst.ID, otherBlob, dockerSpec{image: "team/other", blobs: []string{"layer shared"}})

	sharedDigest := "sha256:" + sha256Of(t, h, "layer shared")
	rowBefore := h.str(`SELECT id::text FROM docker_blobs WHERE repository_id = $1 AND digest = $2`, dst.ID, sharedDigest)

	res, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: img.index.sha})
	if err != nil {
		t.Fatalf("promotion: %v", err)
	}

	wantBytes := img.manifestBytes + img.blobBytes - dockerBlobSize("layer shared")
	if res.Usage.Artifacts != 3 || res.Usage.Bytes != wantBytes {
		t.Errorf("usage = %+v, want 3 artifacts and %d bytes (the layer already in the target is not charged)", res.Usage, wantBytes)
	}

	if got := h.str(`SELECT id::text FROM docker_blobs WHERE repository_id = $1 AND digest = $2`, dst.ID, sharedDigest); got != rowBefore {
		t.Errorf("the target's docker_blobs row of the shared layer was replaced: %s -> %s", rowBefore, got)
	}

	if n := h.count(`SELECT COUNT(*) FROM docker_blobs WHERE repository_id = $1`, dst.ID); n != img.distinctBlobs {
		t.Errorf("dst has %d docker_blobs rows, want %d", n, img.distinctBlobs)
	}

	// The image of the promoted manifest may now serve the layer, and the
	// layer counts as just used so registry GC keeps it.
	if n := h.count(`SELECT COUNT(*) FROM docker_blob_images WHERE repository_id = $1 AND image = $2 AND digest = $3`, dst.ID, dockerTestImage, sharedDigest); n != 1 {
		t.Errorf("the shared layer is not linked to the promoted image")
	}

	if n := h.count(`SELECT COUNT(*) FROM docker_blobs WHERE repository_id = $1 AND digest = $2 AND last_used_at > now() - interval '1 hour'`, dst.ID, sharedDigest); n != 1 {
		t.Errorf("the shared layer's last_used_at was not refreshed")
	}
}

func TestDockerTagConflict(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "docker")
	src := h.repo("docker", false)
	img := seedDockerIndexImage(h, src.ID)

	for _, immutable := range []bool{true, false} {
		dst := h.repo("docker", immutable)

		// The target's tag 1.0 points at another manifest.
		otherBlob, _ := h.blob("a different manifest")
		other := seedDockerManifest(h, dst.ID, otherBlob, dockerSpec{image: dockerTestImage, blobs: []string{"other layer"}, tags: []string{"1.0"}})

		manifestsBefore := h.count(`SELECT COUNT(*) FROM docker_manifests WHERE repository_id = $1`, dst.ID)
		blobsBefore := h.count(`SELECT COUNT(*) FROM docker_blobs WHERE repository_id = $1`, dst.ID)

		_, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: img.index.sha})

		var conflict *ConflictError
		if !errors.As(err, &conflict) || !errors.Is(err, ErrConflict) {
			t.Fatalf("immutable=%v: err = %v, want a ConflictError", immutable, err)
		}

		if conflict.Format != "docker" || conflict.Version != "1.0" || !strings.HasSuffix(conflict.Name, dockerTestImage+":1.0") || conflict.Immutable != immutable {
			t.Errorf("immutable=%v: conflict = %+v", immutable, conflict)
		}

		// The failed promotion left nothing behind.
		if got := h.count(`SELECT COUNT(*) FROM docker_manifests WHERE repository_id = $1`, dst.ID); got != manifestsBefore {
			t.Errorf("dst manifests %d -> %d after a refused promotion", manifestsBefore, got)
		}

		if got := h.count(`SELECT COUNT(*) FROM docker_blobs WHERE repository_id = $1`, dst.ID); got != blobsBefore {
			t.Errorf("dst blobs %d -> %d after a refused promotion", blobsBefore, got)
		}

		if got := h.str(`SELECT manifest_digest FROM docker_tags WHERE repository_id = $1 AND name = '1.0'`, dst.ID); got != other.digest {
			t.Errorf("the target's tag was moved to %s", got)
		}
	}

	// The same tag on the same digest is not a conflict.
	same := h.repo("docker", true)
	if _, err := h.promoteOnce(p, Request{Src: src, Dst: same, SHA256: img.index.sha}); err != nil {
		t.Fatalf("first promotion: %v", err)
	}

	res, err := h.promoteOnce(p, Request{Src: src, Dst: same, SHA256: img.index.sha})
	if err != nil || len(res.Existing) != 3 {
		t.Errorf("same tag on the same digest: res = %+v, err = %v", res, err)
	}
}

func TestDockerNewTagOnExistingManifestIsACopy(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "docker")
	src, dst := h.repo("docker", false), h.repo("docker", false)
	img := seedDockerIndexImage(h, src.ID)

	if _, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: img.index.sha}); err != nil {
		t.Fatalf("first promotion: %v", err)
	}

	// A tag added to the source afterwards is carried by the next promotion of the same digest.
	h.row("docker_tags", src.ID, "", "", map[string]any{"image": dockerTestImage, "name": "2.0", "manifest_digest": img.index.digest})

	res, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: img.index.sha})
	if err != nil {
		t.Fatalf("second promotion: %v", err)
	}

	if len(res.Copied) != 1 || len(res.Existing) != 2 || res.Usage.Artifacts != 0 || res.Usage.Bytes != 0 {
		t.Errorf("res = %+v, want the index reported copied for its new tag and nothing charged", res)
	}

	if got := dockerTags(h, dst.ID); len(got) != 3 {
		t.Errorf("dst tags = %v, want 3", got)
	}
}

func TestDockerUnindexedSourceIsRefused(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "docker")
	src, dst := h.repo("docker", false), h.repo("docker", false)

	blob, sha := h.blob("restored manifest")
	seedDockerManifest(h, src.ID, blob, dockerSpec{image: dockerTestImage, blobs: []string{"restored layer"}, tags: []string{"1.0"}, unindexed: true})

	_, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: sha})
	if !errors.Is(err, ErrDockerNotIndexed) {
		t.Fatalf("err = %v, want ErrDockerNotIndexed", err)
	}

	if n := h.count(`SELECT COUNT(*) FROM docker_manifests WHERE repository_id = $1`, dst.ID); n != 0 {
		t.Errorf("dst has %d manifests after a refused promotion", n)
	}
}

func TestDockerManifestInSeveralImages(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "docker")
	src := h.repo("docker", false)

	blob, sha := h.blob("manifest pushed to two images")

	for _, image := range []string{"team/app", "team/tools"} {
		seedDockerManifest(h, src.ID, blob, dockerSpec{image: image, blobs: []string{"shared layer of two images"}, tags: []string{"v1"}})
	}

	check := func(t *testing.T, req Request, wantCopied int, wantTags []string) {
		t.Helper()

		res, err := h.promoteOnce(p, req)
		if err != nil {
			t.Fatalf("promotion: %v", err)
		}

		if len(res.Copied) != wantCopied {
			t.Errorf("copied %d, want %d: %+v", len(res.Copied), wantCopied, res.Copied)
		}

		if got := dockerTags(h, req.Dst.ID); !slices.Equal(got, wantTags) {
			t.Errorf("dst tags = %v, want %v", got, wantTags)
		}
	}

	t.Run("no path copies every image", func(t *testing.T) {
		check(t, Request{Src: src, Dst: h.repo("docker", false), SHA256: sha}, 2, []string{"team/app:v1", "team/tools:v1"})
	})

	t.Run("an image path narrows it", func(t *testing.T) {
		check(t, Request{Src: src, Dst: h.repo("docker", false), SHA256: sha, Path: "team/tools"}, 1, []string{"team/tools:v1"})
	})

	t.Run("a tag path narrows it", func(t *testing.T) {
		check(t, Request{Src: src, Dst: h.repo("docker", false), SHA256: sha, Path: "team/app:v1"}, 1, []string{"team/app:v1"})
	})
}

func TestDockerSkipsMissingIndexChild(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "docker")
	src, dst := h.repo("docker", false), h.repo("docker", false)

	blob, sha := h.blob("index with a dangling child")
	seedDockerManifest(h, src.ID, blob, dockerSpec{
		image: dockerTestImage, mediaType: dockerIndexMedia, children: []string{"sha256:" + strings.Repeat("c", 64)}, tags: []string{"1.0"},
	})

	res, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: sha})
	if err != nil {
		t.Fatalf("promotion: %v", err)
	}

	if len(res.Copied) != 1 {
		t.Errorf("copied %d, want only the index", len(res.Copied))
	}
}
