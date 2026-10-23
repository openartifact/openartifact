// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
)

// Manifests that are stored by digest and that no tag leads to are copied after
// the tags, one unit per image. They are the old images a tag moved away from,
// and the artifacts attached to an image through the OCI 1.1 subject field:
// signatures, SBOMs and attestations. The destination keeps the subject of a
// manifest it receives and lists it as a referrer of that subject, so pushing
// the manifest body unchanged is all the linkage there is; the subject goes
// first when it is among the manifests to copy, as the distribution spec asks.
//
// What counts as "no tag leads to it" is decided against the destination, once
// every tag of the image is done: a manifest the destination holds is not
// copied again, whether a tag brought it, or a previous run did. The manifests
// a tag reaches in this run are known without asking the destination, which a
// dry run needs, as it stores nothing.
//
// The referrers tag of a registry without the referrers API (sha256-<hex>) is a
// tag like any other: it is copied with the tags, and its children are
// referrers pushed by digest. A file the source lists as a manifest but does
// not serve as one is skipped.

// dockerUntagged is the transfer of the digest manifests of one image.
type dockerUntagged struct {
	im    *importer
	j     importJob
	run   *dockerRun
	api   ociSource // nil when the source has no registry API
	dst   ociRepo
	image string

	// order lists the digests in the source's order; assets holds the file
	// each one is listed as.
	order  []string
	assets map[string]sourceAsset
	// tags are the tags of the image whose units this one waits for.
	tags []dockerTagID
	done map[string]bool
}

func dockerUntaggedUnit(ctx context.Context, im *importer, j importJob) {
	u := newDockerUntagged(im, j)

	for _, id := range u.tags {
		if u.run.waitTag(ctx, j.dst, id) != nil {
			return
		}
	}

	for _, digest := range u.order {
		u.visit(ctx, digest)
	}
}

func newDockerUntagged(im *importer, j importJob) *dockerUntagged {
	u := &dockerUntagged{
		im: im, j: j, run: dockerRunOf(im),
		assets: map[string]sourceAsset{}, done: map[string]bool{},
	}

	u.api, _ = im.src.(ociSource)

	for _, a := range j.assets {
		if d, ok := dockerParseDigestPath(a.Path); ok {
			u.image = d.Image

			if _, dup := u.assets[d.Digest]; dup {
				im.rep.add(func(r *importReport) { r.Ignored++ })

				continue
			}

			u.assets[d.Digest] = a
			u.order = append(u.order, d.Digest)

			continue
		}

		if id, ok := dockerIdentity(a); ok {
			u.tags = append(u.tags, id)
		}
	}

	u.dst = newOCIRepo(im.tgt, j.dst, u.image)

	return u
}

// visit copies the manifest with the digest, after its subject when that is
// another manifest of the unit.
func (u *dockerUntagged) visit(ctx context.Context, digest string) {
	if _, listed := u.assets[digest]; !listed || u.done[digest] || ctx.Err() != nil {
		return
	}

	u.done[digest] = true

	if err := u.copyOne(ctx, digest); err != nil {
		u.im.rep.fail(u.j.repo.Name, dockerDigestID{Image: u.image, Digest: digest}.String(), err)
	}
}

func (u *dockerUntagged) copyOne(ctx context.Context, digest string) error {
	a := u.assets[digest]
	path := dockerDigestID{Image: u.image, Digest: digest}.String()
	key := stateKey(u.j.repo.Name, u.j.dst, path)

	if marker := resumeMarker(a); marker != "" && u.im.state.isDone(key, marker) {
		u.im.rep.add(func(r *importReport) { r.Resumed++ })

		return nil
	}

	covered, err := u.covered(ctx, digest)
	if err != nil {
		return err
	}

	if covered {
		u.im.rep.add(func(r *importReport) { r.Ignored++ })

		return nil
	}

	if u.api == nil {
		return errOCINoAPI
	}

	src := u.api.registry(u.j.repo.Name)

	m, err := src.manifest(ctx, u.image, digest)
	if errors.Is(err, errNotFound) {
		u.im.rep.add(func(r *importReport) { r.Ignored++ })
		u.im.rep.logf("skipped  %s/%s: the source registry does not serve it as a manifest", u.j.repo.Name, path)

		return nil
	}

	if err != nil {
		return err
	}

	if s := m.doc.Subject; s != nil {
		u.visit(ctx, s.Digest)
	}

	img, err := src.imageOf(ctx, u.image, digest, m)
	if err != nil {
		return err
	}

	c, err := newDockerCopyOf(u.im, u.j, dockerTagID{Image: u.image}, a, path)
	if err != nil {
		return err
	}

	if u.im.cfg.DryRun {
		return c.plan(ctx, img)
	}

	return c.copy(ctx, img)
}

// covered reports whether a tag brought the manifest to the destination, or
// the destination already holds it.
func (u *dockerUntagged) covered(ctx context.Context, digest string) (bool, error) {
	if u.run.isReached(u.j.dst, u.image, digest) {
		return true, nil
	}

	var held bool

	err := u.im.retry.do(ctx, fmt.Sprintf("check manifest %s@%s", u.image, shortDigest(digest)), func(int) error {
		var err error

		held, err = u.dst.hasManifest(ctx, digest)

		return err
	})

	return held, err
}
