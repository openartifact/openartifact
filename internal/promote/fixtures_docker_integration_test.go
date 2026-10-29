// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

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
