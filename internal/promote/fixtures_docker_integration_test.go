// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import "strings"

const (
	dockerImageMedia = "application/vnd.oci.image.manifest.v1+json"
	dockerIndexMedia = "application/vnd.oci.image.index.v1+json"
	// dockerBlobSkew makes the verified docker_blobs.size_bytes differ from
	// blobs.size_bytes, so a promotion that charges or copies the wrong one is seen.
	dockerBlobSkew = 7
)

// dockerSpec describes one manifest seeded the way a registry push leaves it.
type dockerSpec struct {
	image, mediaType, subject string
	// blobs are the contents of the config and layer blobs the manifest references.
	blobs []string
	// foreign are digests of referenced layers the registry never stores.
	foreign []string
	// children are the digests of the child manifests of an index.
	children []string
	tags     []string
	// unindexed leaves the manifest without docker_manifest_refs, like one restored from a backup.
	unindexed bool
}

// dockerSeed is a seeded manifest.
type dockerSeed struct {
	id, digest, sha string
	size            int64
}

// dockerBlobSize is the verified size seeded for a blob of the content.
func dockerBlobSize(content string) int64 { return int64(len(content)) + dockerBlobSkew }

// dockerBlobsSize is the verified size of all contents.
func dockerBlobsSize(contents ...string) int64 {
	var total int64
	for _, c := range contents {
		total += dockerBlobSize(c)
	}

	return total
}

// seedDockerManifest inserts the manifest stored as blobID into repoID with its
// config/layer docker_blobs rows, image links, references (the indexed marker
// included) and tags. Blobs the repository already holds are left alone.
func seedDockerManifest(h *harness, repoID, blobID string, spec dockerSpec) dockerSeed {
	h.t.Helper()

	sha := h.str(`SELECT sha256 FROM blobs WHERE id = $1`, blobID)
	size := h.count(`SELECT size_bytes FROM blobs WHERE id = $1`, blobID)
	digest := "sha256:" + sha

	media := spec.mediaType
	if media == "" {
		media = dockerImageMedia
	}

	var subject any
	if spec.subject != "" {
		subject = spec.subject
	}

	manifestID := h.row("docker_manifests", repoID, "blob_id", blobID, map[string]any{
		"image": spec.image, "digest": digest, "media_type": media, "size": size, "subject_digest": subject,
	})

	type ref struct{ kind, digest string }

	var refs []ref

	for _, content := range spec.blobs {
		id, blobSHA := h.blob(content)
		d := "sha256:" + blobSHA

		h.exec(`INSERT INTO docker_blobs (repository_id, digest, blob_id, size_bytes, last_used_at)
		        VALUES ($1, $2, $3, $4, now() - interval '1 day') ON CONFLICT (repository_id, digest) DO NOTHING`,
			repoID, d, id, dockerBlobSize(content))
		h.exec(`INSERT INTO docker_blob_images (repository_id, image, digest) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			repoID, spec.image, d)

		refs = append(refs, ref{"blob", d})
	}

	for _, d := range spec.foreign {
		refs = append(refs, ref{"blob", d})
	}

	for _, d := range spec.children {
		refs = append(refs, ref{"manifest", d})
	}

	if !spec.unindexed {
		refs = append(refs, ref{"indexed", ""})

		for _, r := range refs {
			h.exec(`INSERT INTO docker_manifest_refs (manifest_id, repository_id, image, kind, digest) VALUES ($1, $2, $3, $4, $5)`,
				manifestID, repoID, spec.image, r.kind, r.digest)
		}
	}

	for _, tag := range spec.tags {
		h.row("docker_tags", repoID, "", "", map[string]any{"image": spec.image, "name": tag, "manifest_digest": digest})
	}

	return dockerSeed{id: manifestID, digest: digest, sha: sha, size: int64(size)}
}

// dockerFixture covers a multi-layer image: manifest, config and two layers,
// one foreign layer reference, a subject, and a tag, in a non-default image.
func dockerFixture() fixture {
	return fixture{
		format: "docker", version: "1.0", path: "team/app:1.0",
		tables:  []string{"docker_manifests", "docker_tags"},
		compare: []string{"docker_manifests", "docker_tags", "docker_blobs", "docker_blob_images", "docker_manifest_refs"},
		// last_used_at is refreshed by the copy; manifest_id is the re-pointed parent id.
		ignore: []string{"last_used_at", "manifest_id"},
		seed: func(h *harness, repoID, blobID string) {
			seedDockerManifest(h, repoID, blobID, dockerSpec{
				image:   "team/app",
				subject: "sha256:" + strings.Repeat("a", 64),
				blobs:   []string{"docker config", "docker layer one", "docker layer two"},
				foreign: []string{"sha256:" + strings.Repeat("f", 64)},
				tags:    []string{"1.0"},
			})
		},
		verify: func(h *harness, src, dst string) {
			t := h.t

			if n := h.count(`SELECT COUNT(*) FROM docker_blobs s JOIN docker_blobs d ON d.digest = s.digest AND d.blob_id = s.blob_id
			                  WHERE s.repository_id = $1 AND d.repository_id = $2`, src, dst); n != 3 {
				t.Errorf("dst holds %d of the 3 source docker_blobs rows with the same blobs row", n)
			}

			if n := h.count(`SELECT COUNT(*) FROM docker_blobs WHERE repository_id = $1 AND last_used_at > now() - interval '1 hour'`, dst); n != 3 {
				t.Errorf("%d of 3 dst blobs were used recently, want all", n)
			}

			if n := h.count(`SELECT COUNT(*) FROM docker_blob_images WHERE repository_id = $1 AND image = 'team/app'`, dst); n != 3 {
				t.Errorf("dst links %d blobs to the image, want 3", n)
			}

			if n := h.count(`SELECT COUNT(*) FROM docker_manifest_refs WHERE repository_id = $1 AND kind = 'blob'`, dst); n != 4 {
				t.Errorf("dst has %d blob refs, want 4 (3 stored + 1 foreign)", n)
			}

			if n := h.count(`SELECT COUNT(*) FROM docker_manifest_refs WHERE repository_id = $1 AND kind = 'indexed'`, dst); n != 1 {
				t.Errorf("dst has %d indexed markers, want 1", n)
			}

			want := h.str(`SELECT digest FROM docker_manifests WHERE repository_id = $1`, src)
			if got := h.str(`SELECT manifest_digest FROM docker_tags WHERE repository_id = $1 AND image = 'team/app' AND name = '1.0' AND deleted_at IS NULL`, dst); got != want {
				t.Errorf("dst tag 1.0 points at %q, want %q", got, want)
			}

			if got := h.str(`SELECT subject_digest FROM docker_manifests WHERE repository_id = $1`, dst); got != "sha256:"+strings.Repeat("a", 64) {
				t.Errorf("dst subject_digest = %q", got)
			}
		},
	}
}
