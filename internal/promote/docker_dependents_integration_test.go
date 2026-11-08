// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import (
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/ociref"
)

// seedDependent seeds a manifest with its own content and a tag, as cosign leaves it.
func seedDependent(h *harness, repoID, content, tag string, spec dockerSpec) dockerSeed {
	h.t.Helper()

	blobID, _ := h.blob(content)
	spec.image = dockerTestImage

	if tag != "" {
		spec.tags = append(spec.tags, tag)
	}

	return seedDockerManifest(h, repoID, blobID, spec)
}

// dockerDigestSet is the sorted manifest digests of the repository.
func dockerDigestSet(h *harness, repoID string) []string {
	h.t.Helper()

	var out []string

	for i := range h.count(`SELECT COUNT(*) FROM docker_manifests WHERE repository_id = $1 AND deleted_at IS NULL`, repoID) {
		out = append(out, h.str(
			`SELECT digest FROM docker_manifests WHERE repository_id = $1 AND deleted_at IS NULL ORDER BY digest OFFSET $2 LIMIT 1`,
			repoID,
			i,
		))
	}

	return out
}

func TestDockerPromotionCarriesDependents(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "docker")
	src, dst := h.repo("docker", false), h.repo("docker", false)
	img := seedDockerIndexImage(h, src.ID)

	sig := seedDependent(
		h,
		src.ID,
		"sig of index",
		ociref.CosignTag(img.index.digest, ociref.KindSignature),
		dockerSpec{blobs: []string{"sig layer"}},
	)
	att := seedDependent(h, src.ID, "att of index", ociref.CosignTag(img.index.digest, ociref.KindAttestation), dockerSpec{})
	childSig := seedDependent(h, src.ID, "sig of amd", ociref.CosignTag(img.amd.digest, ociref.KindSignature), dockerSpec{})
	ref := seedDependent(h, src.ID, "referrer of index", "", dockerSpec{subject: img.index.digest, blobs: []string{"referrer layer"}})
	childRef := seedDependent(h, src.ID, "referrer of arm", "", dockerSpec{subject: img.arm.digest})

	// An unrelated image's dependents and a dependent of a dependent stay behind.
	other := seedDependent(h, src.ID, "other image", "other", dockerSpec{})
	otherSig := seedDependent(h, src.ID, "sig of other", ociref.CosignTag(other.digest, ociref.KindSignature), dockerSpec{})
	otherRef := seedDependent(h, src.ID, "referrer of other", "", dockerSpec{subject: other.digest})
	deep := seedDependent(h, src.ID, "referrer of sig", "", dockerSpec{subject: sig.digest})

	req := Request{Src: src, Dst: dst, SHA256: img.index.sha, Path: dockerTestImage + ":latest"}

	res, err := h.promoteOnce(p, req)
	if err != nil {
		t.Fatalf("promotion: %v", err)
	}

	want := []string{img.index.digest, img.amd.digest, img.arm.digest, sig.digest, att.digest, childSig.digest, ref.digest, childRef.digest}
	slices.Sort(want)

	if got := dockerDigestSet(h, dst.ID); !slices.Equal(got, want) {
		t.Errorf("dst manifests = %v, want %v (not %v, %v, %v)", got, want, otherSig.digest, otherRef.digest, deep.digest)
	}

	wantTags := []string{
		dockerTestImage + ":" + ociref.CosignTag(img.amd.digest, ociref.KindSignature),
		dockerTestImage + ":" + ociref.CosignTag(img.index.digest, ociref.KindAttestation),
		dockerTestImage + ":" + ociref.CosignTag(img.index.digest, ociref.KindSignature),
		dockerTestImage + ":1.0",
		dockerTestImage + ":latest",
	}
	slices.Sort(wantTags)

	if got := dockerTags(h, dst.ID); !slices.Equal(got, wantTags) {
		t.Errorf("dst tags = %v, want %v", got, wantTags)
	}

	if len(res.Copied) != 8 || res.Usage.Artifacts != 8 {
		t.Errorf("copied %d, artifacts %d, want 8 and 8 (dependents are counted)", len(res.Copied), res.Usage.Artifacts)
	}

	if n := h.count(`SELECT COUNT(*) FROM docker_blobs WHERE repository_id = $1`, dst.ID); n != img.distinctBlobs+2 {
		t.Errorf("dst has %d docker_blobs rows, want the image's %d plus the sig and referrer layers", n, img.distinctBlobs)
	}

	t.Run("promoting twice is idempotent", func(t *testing.T) {
		again, err := h.promoteOnce(p, req)
		if err != nil {
			t.Fatalf("second promotion: %v", err)
		}

		if len(again.Copied) != 0 || len(again.Existing) != 8 || again.Usage.Artifacts != 0 || again.Usage.Bytes != 0 {
			t.Errorf("second promotion = %+v, want 8 existing and nothing charged", again)
		}

		if got := dockerDigestSet(h, dst.ID); !slices.Equal(got, want) {
			t.Errorf("dst manifests after a retry = %v, want %v", got, want)
		}
	})
}

func TestDockerPromotionPartialDestinationIsCompleted(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "docker")
	src, dst := h.repo("docker", false), h.repo("docker", false)
	img := seedDockerIndexImage(h, src.ID)
	sig := seedDependent(h, src.ID, "sig of index", ociref.CosignTag(img.index.digest, ociref.KindSignature), dockerSpec{})
	req := Request{Src: src, Dst: dst, SHA256: img.index.sha, Path: dockerTestImage + ":latest"}

	// The destination already holds the signature tag and manifest.
	seedDependent(h, dst.ID, "sig of index", ociref.CosignTag(img.index.digest, ociref.KindSignature), dockerSpec{})

	if _, err := h.promoteOnce(p, req); err != nil {
		t.Fatalf("promotion: %v", err)
	}

	if n := h.count(`SELECT COUNT(*) FROM docker_manifests WHERE repository_id = $1 AND digest = $2`, dst.ID, sig.digest); n != 1 {
		t.Errorf("dst has %d rows of the signature manifest, want 1", n)
	}
}

func TestDockerPromotionExpiryOnlyIntoOptingInDestination(t *testing.T) {
	tests := []struct {
		name    string
		honour  bool
		wantSet int
	}{
		{"destination honours quay expiry", true, 2},
		{"destination does not honour quay expiry", false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			p := promoterFor(t, "docker")
			src, dst := h.repo("docker", false), h.repo("docker", false)

			if tt.honour {
				h.exec(`UPDATE repositories SET honour_quay_expiry = true WHERE id = $1`, dst.ID)
			}

			blobID, _ := h.blob("expiring image")
			item := seedDockerManifest(h, src.ID, blobID, dockerSpec{image: dockerTestImage, tags: []string{"pr-1"}, expires: true})
			seedDependent(h, src.ID, "expiring sig", ociref.CosignTag(item.digest, ociref.KindSignature), dockerSpec{expires: true})

			if _, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: item.sha, Path: dockerTestImage + ":pr-1"}); err != nil {
				t.Fatalf("promotion: %v", err)
			}

			if n := h.count(
				`SELECT COUNT(*) FROM docker_manifests WHERE repository_id = $1 AND expires_at IS NOT NULL`,
				dst.ID,
			); n != tt.wantSet {
				t.Errorf("dst has %d manifests with expires_at, want %d", n, tt.wantSet)
			}

			if n := h.count(`SELECT COUNT(*) FROM docker_manifests WHERE repository_id = $1`, dst.ID); n != 2 {
				t.Errorf("dst has %d manifests, want 2", n)
			}
		})
	}
}
