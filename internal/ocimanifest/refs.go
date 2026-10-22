// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ocimanifest extracts what an OCI image manifest, OCI image index or
// Docker v2 manifest/manifest list references.
package ocimanifest

import (
	"encoding/json"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
)

type descriptor struct {
	Digest    string   `json:"digest"`
	MediaType string   `json:"mediaType"` //nolint:tagliatelle // OCI/Docker wire format is camelCase
	URLs      []string `json:"urls"`
}

// foreign reports whether the layer's bytes live elsewhere (Windows base
// layers and other non-distributable content): they are never pushed to the
// registry, so the registry cannot be expected to store them.
func (d descriptor) foreign() bool {
	return len(d.URLs) > 0 ||
		d.MediaType == "application/vnd.docker.image.rootfs.foreign.diff.tar.gzip" ||
		strings.Contains(d.MediaType, ".nondistributable.")
}

// PushedBlobs returns the digests of the config and layer blobs a manifest
// references whose bytes a client must push, i.e. excluding foreign and
// non-distributable layers. Unparseable bodies yield none.
func PushedBlobs(body []byte) []string {
	var m manifestBody

	if err := json.Unmarshal(body, &m); err != nil {
		return nil
	}

	out := newSet()

	if m.Config != nil {
		out.add(m.Config.Digest)
	}

	for _, group := range [][]descriptor{m.Layers, m.Blobs} {
		for _, d := range group {
			if !d.foreign() {
				out.add(d.Digest)
			}
		}
	}

	for _, l := range m.FSLayers {
		out.add(l.BlobSum)
	}

	return out.list
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
