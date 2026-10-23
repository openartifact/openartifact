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
// What is imported is every tag, and then every manifest stored by digest that
// no imported tag leads to: an old image whose tag moved on, and the artifacts
// attached to an image through the OCI 1.1 subject field (signatures, SBOMs,
// attestations), which the destination lists as referrers of their subject. A
// blob, and the manifests a tag reaches, are copied with the tag that uses them,
// so their own assets are not transfers; foreign layers are never copied.
// Because the core's per-asset hooks do not fit that shape, the format
// registers transferUnit and leaves match, present and upload unset.

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

// dockerDigestID identifies a manifest stored by its digest in a source
// repository.
type dockerDigestID struct{ Image, Digest string }

func (id dockerDigestID) String() string { return id.Image + "@" + id.Digest }

// dockerParseDigestPath recognises the files that stand for a manifest stored
// by digest: v2/<image>/manifests/sha256:<hex> in Nexus, and the manifest.json
// (or list.manifest.json) in the folder <image>/sha256__<hex>/ of Artifactory.
func dockerParseDigestPath(p string) (dockerDigestID, bool) {
	if rest, ok := strings.CutPrefix(p, "v2/"); ok {
		if i := strings.LastIndex(rest, "/manifests/"); i > 0 {
			if d := rest[i+len("/manifests/"):]; ociValidDigest(d) {
				return dockerDigestID{Image: rest[:i], Digest: d}, true
			}
		}
	}

	dir, file := path.Split(p)
	if file != "manifest.json" && file != "list.manifest.json" {
		return dockerDigestID{}, false
	}

	image, folder, ok := strings.CutLast(strings.TrimSuffix(dir, "/"), "/")
	if !ok || image == "" {
		return dockerDigestID{}, false
	}

	hexPart, ok := strings.CutPrefix(folder, "sha256__")
	if d := ociDigestPrefix + hexPart; ok && ociValidDigest(d) {
		return dockerDigestID{Image: image, Digest: d}, true
	}

	return dockerDigestID{}, false
}

// dockerImportable accepts the files of tags and of manifests stored by digest.
func dockerImportable(p string) bool {
	if _, ok := dockerParsePath(p); ok {
		return true
	}

	_, ok := dockerParseDigestPath(p)

	return ok
}

// dockerImageOf names the image a tag or digest manifest file belongs to.
func dockerImageOf(a sourceAsset) (string, bool) {
	if id, ok := dockerIdentity(a); ok {
		return id.Image, true
	}

	d, ok := dockerParseDigestPath(a.Path)

	return d.Image, ok
}

// dockerUnits groups the tag files by image and tag, in order of appearance.
// A tag has two files only when Artifactory keeps both a manifest.json and a
// list.manifest.json; the list leads, as it is what the tag resolves to.
//
// The manifests stored by digest follow, one unit per image, as they can only
// be told apart from the manifests of the tags once those are copied. Such a
// unit lists the digest files first and then one file of each tag of the image:
// the tags it has to wait for.
func dockerUnits(assets []sourceAsset) [][]sourceAsset {
	var (
		units  [][]sourceAsset
		slot   = map[dockerTagID]int{}
		digest = map[string]int{}
		loose  [][]sourceAsset
	)

	for _, a := range assets {
		if d, ok := dockerParseDigestPath(a.Path); ok {
			i, seen := digest[d.Image]
			if !seen {
				i = len(loose)
				digest[d.Image] = i
				loose = append(loose, nil)
			}

			loose[i] = append(loose[i], a)

			continue
		}

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

	for _, u := range units {
		id, _ := dockerIdentity(u[0])
		if i, ok := digest[id.Image]; ok {
			loose[i] = append(loose[i], u[0])
		}
	}

	return append(units, loose...)
}

// dockerRemove deletes the manifest a verification failure left behind; res.ID
// is the digest the registry reported for it.
func dockerRemove(ctx context.Context, t *importTarget, dst string, a sourceAsset, res uploadResult) error {
	image, ok := dockerImageOf(a)
	if !ok || res.ID == "" {
		return fmt.Errorf("%w: cannot determine what to remove", errImportUsage)
	}

	return newOCIRepo(t, dst, image).removeManifest(ctx, res.ID)
}

// dockerTransferUnit copies one tag, or the manifests of an image that no tag
// leads to; a failure fails that tag or manifest only.
func dockerTransferUnit(ctx context.Context, im *importer, j importJob) {
	if _, ok := dockerParseDigestPath(j.assets[0].Path); ok {
		dockerUntaggedUnit(ctx, im, j)

		return
	}

	if id, ok := dockerIdentity(j.assets[0]); ok {
		run := dockerRunOf(im)
		defer run.finishTag(j.dst, id)

		// The tag file's checksum names the manifest the tag leads to, which
		// keeps it from being taken for an untagged one should the tag fail.
		if d := ociDigestPrefix + j.assets[0].SHA256; ociValidDigest(d) {
			run.reach(j.dst, id.Image, d)
		}
	}

	c, err := newDockerCopy(im, j)
	if err != nil {
		im.rep.fail(j.repo.Name, j.assets[0].Path, err)

		return
	}

	if err := c.runTag(ctx); err != nil {
		im.rep.fail(j.repo.Name, c.path, err)
	}
}
