// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"encoding/json"
	"io"
	"strings"
)

// ociManifestMeta captures the subset of an OCI image manifest / index
// relevant to the Referrers API (OCI distribution-spec 1.1): the optional
// `subject` a manifest points at, and the metadata used to describe the
// manifest itself as a referrer entry (artifactType, annotations, and the
// config descriptor's mediaType, used as an artifactType fallback).
type ociManifestMeta struct {
	Subject *struct {
		Digest string `json:"digest"`
	} `json:"subject"`
	ArtifactType string            `json:"artifactType"` //nolint:tagliatelle // OCI image-spec wire format is camelCase
	Annotations  map[string]string `json:"annotations"`
	Config       *struct {
		MediaType string `json:"mediaType"` //nolint:tagliatelle // OCI image-spec wire format is camelCase
		Digest    string `json:"digest"`
	} `json:"config"`
}

// parseOCIManifestMeta best-effort parses a manifest body for `subject` and
// referrer metadata. These fields are OPTIONAL and specific to the OCI
// image-spec; malformed or legacy (Docker v2 schema1) content simply yields
// a zero value rather than failing the caller.
func parseOCIManifestMeta(body []byte) ociManifestMeta {
	var m ociManifestMeta

	_ = json.Unmarshal(body, &m)

	return m
}

// manifestBlobKey returns the blob-store identifier for a manifest's
// content: the hex portion of its "sha256:<hex>" digest, matching the
// "sha256" metadata key manifests (and blobs) are Put under.
func manifestBlobKey(digest string) string {
	return strings.TrimPrefix(digest, "sha256:")
}

// ociLicensesKey is the OCI annotation / image label declaring an image's
// SPDX license expression.
const ociLicensesKey = "org.opencontainers.image.licenses"

// maxConfigBlobBytes bounds how much of an image config blob is read when
// looking for the license label.
const maxConfigBlobBytes = 1 << 20

// declaredLicenses returns the licenses a manifest declares: its
// org.opencontainers.image.licenses annotation and, when the config blob is
// already stored in the repository, the config's label of the same name.
// Unreadable config blobs count as undeclared.
func (h *HTTPHandler) declaredLicenses(ctx context.Context, repoID string, meta ociManifestMeta) []string {
	var licenses []string

	if l := meta.Annotations[ociLicensesKey]; l != "" {
		licenses = append(licenses, l)
	}

	if meta.Config == nil || meta.Config.Digest == "" {
		return licenses
	}

	blob, err := h.db.GetDockerBlob(ctx, repoID, meta.Config.Digest)
	if err != nil || blob == nil || blob.Size > maxConfigBlobBytes {
		return licenses
	}

	rc, err := h.blobStore.Get(ctx, blob.BlobSHA256)
	if err != nil {
		return licenses
	}

	defer func() { _ = rc.Close() }()

	var cfg struct {
		Config struct {
			Labels map[string]string `json:"Labels"` //nolint:tagliatelle // OCI image-spec wire format
		} `json:"config"`
	}

	if json.NewDecoder(io.LimitReader(rc, maxConfigBlobBytes)).Decode(&cfg) != nil {
		return licenses
	}

	if l := cfg.Config.Labels[ociLicensesKey]; l != "" {
		licenses = append(licenses, l)
	}

	return licenses
}
