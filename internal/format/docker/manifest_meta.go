// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
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

// referrerArtifactType is the artifactType the Referrers API reports for the
// manifest: its own, or the media type of its config when it declares none.
func (m ociManifestMeta) referrerArtifactType() string {
	if m.ArtifactType == "" && m.Config != nil {
		return m.Config.MediaType
	}

	return m.ArtifactType
}

// ociLicensesKey is the OCI annotation / image label declaring an image's
// SPDX license expression.
const ociLicensesKey = "org.opencontainers.image.licenses"

// maxConfigBlobBytes bounds how much of an image config blob is read when
// looking for its labels.
const maxConfigBlobBytes = 1 << 20

// configLabels returns the labels of the image config blob a manifest
// references, when the blob is already stored in the repository. An unreadable
// or oversized config counts as having no labels.
func (h *HTTPHandler) configLabels(ctx context.Context, repoID string, meta ociManifestMeta) map[string]string {
	if meta.Config == nil || meta.Config.Digest == "" {
		return nil
	}

	blob, err := h.db.GetDockerBlob(ctx, repoID, meta.Config.Digest)
	if err != nil || blob == nil || blob.Size > maxConfigBlobBytes {
		return nil
	}

	rc, err := h.blobStore.Get(ctx, blob.BlobSHA256)
	if err != nil {
		return nil
	}

	defer func() { _ = rc.Close() }()

	var cfg struct {
		Config struct {
			Labels map[string]string `json:"Labels"` //nolint:tagliatelle // OCI image-spec wire format
		} `json:"config"`
	}

	if json.NewDecoder(io.LimitReader(rc, maxConfigBlobBytes)).Decode(&cfg) != nil {
		return nil
	}

	return cfg.Config.Labels
}

// labelReader reads the config labels of one pushed manifest at most once, for
// whichever of the push, the policies and the hooks asks first.
type labelReader struct {
	once   sync.Once
	labels map[string]string
	read   func(ctx context.Context) map[string]string
}

func (l *labelReader) get(ctx context.Context) map[string]string {
	l.once.Do(func() { l.labels = l.read(ctx) })

	return l.labels
}

// declaredLicenses returns the licenses a manifest declares: its
// org.opencontainers.image.licenses annotation and the config's label of the
// same name. Unreadable config blobs count as undeclared.
func declaredLicenses(meta ociManifestMeta, labels map[string]string) []string {
	var licenses []string

	if l := meta.Annotations[ociLicensesKey]; l != "" {
		licenses = append(licenses, l)
	}

	if l := labels[ociLicensesKey]; l != "" {
		licenses = append(licenses, l)
	}

	return licenses
}
