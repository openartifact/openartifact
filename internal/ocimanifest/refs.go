// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ocimanifest extracts what an OCI image manifest, OCI image index or
// Docker v2 manifest/manifest list references.
package ocimanifest

import (
	"encoding/json"

	"github.com/openartifact/openartifact/internal/database/models"
)

type descriptor struct {
	Digest string `json:"digest"`
}

// manifestBody is the union of the manifest shapes this registry stores:
// image manifest (config + layers), OCI artifact manifest (config + blobs),
// index / manifest list (manifests) and Docker schema 1 (fsLayers).
type manifestBody struct {
	Config    *descriptor  `json:"config"`
	Layers    []descriptor `json:"layers"`
	Blobs     []descriptor `json:"blobs"`
	Manifests []descriptor `json:"manifests"`
	FSLayers  []struct {
		BlobSum string `json:"blobSum"` //nolint:tagliatelle // Docker schema 1 wire format is camelCase
	} `json:"fsLayers"` //nolint:tagliatelle // Docker schema 1 wire format is camelCase
}

// ParseRefs returns the blob digests (config, layers) and child manifest
// digests (index entries) a manifest body references, each listed once.
// Content that is not a JSON manifest yields no references, like a manifest
// that references nothing: such a body cannot keep anything alive.
func ParseRefs(body []byte) models.DockerManifestRefs {
	var m manifestBody

	if err := json.Unmarshal(body, &m); err != nil {
		return models.DockerManifestRefs{}
	}

	blobs := newSet()

	if m.Config != nil {
		blobs.add(m.Config.Digest)
	}

	for _, d := range m.Layers {
		blobs.add(d.Digest)
	}

	for _, d := range m.Blobs {
		blobs.add(d.Digest)
	}

	for _, l := range m.FSLayers {
		blobs.add(l.BlobSum)
	}

	manifests := newSet()

	for _, d := range m.Manifests {
		manifests.add(d.Digest)
	}

	return models.DockerManifestRefs{Blobs: blobs.list, Manifests: manifests.list}
}

// set is an insertion-ordered string set that ignores empty values.
type set struct {
	seen map[string]struct{}
	list []string
}

func newSet() *set { return &set{seen: map[string]struct{}{}} }

func (s *set) add(v string) {
	if v == "" {
		return
	}

	if _, ok := s.seen[v]; ok {
		return
	}

	s.seen[v] = struct{}{}
	s.list = append(s.list, v)
}
