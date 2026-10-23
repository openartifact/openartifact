// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
)

// A docker repository is not a set of files: a tag names a manifest (or an index
// of per-platform manifests) that points at a config blob and layer blobs, and
// only the registry API shows them in a form any registry accepts. The importer
// therefore copies one tag at a time: it reads the tag's manifests and blobs
// from the source's registry API and pushes them to the destination's, blobs
// first, children before indexes, the tag last. The digests are preserved.
//
// What is imported is every tag. Manifests that no tag points at, referrers
// (artifacts attached through the OCI 1.1 subject field) and foreign layers are
// not: a registry's blob and digest assets are reached through the tags that
// use them. Because the core's per-asset hooks do not fit that shape, the
// format registers transferUnit and leaves match, present and upload unset.

func init() { //nolint:gochecknoinits // formats register themselves (see registerFormat)
	registerFormat(formatDef{
		format:       formatDocker,
		nexus:        []string{"docker"},
		artifactory:  []string{"docker"},
		importable:   dockerImportable,
		components:   true,
		units:        dockerUnits,
		transferUnit: dockerTransferUnit,
		remove:       dockerRemove,
	})
}

// dockerTagID identifies a tagged image in a source repository.
type dockerTagID struct{ Image, Tag string }

func (id dockerTagID) String() string { return id.Image + ":" + id.Tag }

// dockerParsePath recognises the files that stand for a tag: the manifest
// Nexus serves at v2/<image>/manifests/<tag>, and the manifest.json or
// list.manifest.json Artifactory keeps in the tag's folder <image>/<tag>/. The
// manifests addressed by digest and the blobs are not tags.
func dockerParsePath(p string) (dockerTagID, bool) {
	if rest, ok := strings.CutPrefix(p, "v2/"); ok {
		if i := strings.LastIndex(rest, "/manifests/"); i > 0 {
			return dockerTagOf(rest[:i], rest[i+len("/manifests/"):])
		}
	}

	dir, file := path.Split(p)
	if file != "manifest.json" && file != "list.manifest.json" {
		return dockerTagID{}, false
	}

	image, tag, ok := strings.CutLast(strings.TrimSuffix(dir, "/"), "/")
	if !ok {
		return dockerTagID{}, false
	}

	return dockerTagOf(image, tag)
}

// dockerTagOf validates a candidate tag: digests ("sha256:…", and Artifactory's
// "sha256__…" folders) name content, not tags.
func dockerTagOf(image, tag string) (dockerTagID, bool) {
	if image == "" || tag == "" || strings.ContainsAny(tag, "/:") ||
		strings.HasPrefix(tag, "sha256__") || strings.HasPrefix(tag, "sha512__") {
		return dockerTagID{}, false
	}

	return dockerTagID{Image: image, Tag: tag}, true
}

// dockerIdentity names the tag an asset stands for. A Nexus component carries
// the image and the tag directly; otherwise they come from the path.
func dockerIdentity(a sourceAsset) (dockerTagID, bool) {
	id, ok := dockerParsePath(a.Path)
	if !ok {
		return dockerTagID{}, false
	}

	if c, ok := dockerTagOf(a.Component.Name, a.Component.Version); ok {
		return c, true
	}

	return id, true
}

func dockerImportable(p string) bool {
	_, ok := dockerParsePath(p)

	return ok
}

// dockerUnits groups the tag files by image and tag, in order of appearance.
// A tag has two files only when Artifactory keeps both a manifest.json and a
// list.manifest.json; the list leads, as it is what the tag resolves to.
func dockerUnits(assets []sourceAsset) [][]sourceAsset {
	var (
		units [][]sourceAsset
		slot  = map[dockerTagID]int{}
	)

	for _, a := range assets {
		id, ok := dockerIdentity(a)
		if !ok {
			continue
		}

		i, seen := slot[id]
		if !seen {
			i = len(units)
			slot[id] = i
			units = append(units, nil)
		}

		units[i] = append(units[i], a)
	}

	for _, u := range units {
		sort.SliceStable(u, func(i, j int) bool {
			return strings.HasSuffix(u[i].Path, "list.manifest.json") && !strings.HasSuffix(u[j].Path, "list.manifest.json")
		})
	}

	return units
}

// dockerRemove deletes the manifest a verification failure left behind; res.ID
// is the digest the registry reported for it.
func dockerRemove(ctx context.Context, t *importTarget, dst string, a sourceAsset, res uploadResult) error {
	id, ok := dockerIdentity(a)
	if !ok || res.ID == "" {
		return fmt.Errorf("%w: cannot determine what to remove", errImportUsage)
	}

	return newOCIRepo(t, dst, id.Image).removeManifest(ctx, res.ID)
}

// dockerTransferUnit copies one tag; a failure fails that tag only.
func dockerTransferUnit(ctx context.Context, im *importer, j importJob) {
	c, err := newDockerCopy(im, j)
	if err != nil {
		im.rep.fail(j.repo.Name, j.assets[0].Path, err)

		return
	}

	if err := c.runTag(ctx); err != nil {
		im.rep.fail(j.repo.Name, c.path, err)
	}
}
