// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// dockerCopy is the transfer of one tag from the source to the destination.
type dockerCopy struct {
	im  *importer
	j   importJob
	run *dockerRun
	id  dockerTagID
	// asset is the source file the tag is listed as: it carries the creation
	// time, the properties and the resume marker.
	asset sourceAsset
	// path is how the tag is reported and recorded in the state file.
	path string
	key  string
	src  ociRegistry
	dst  ociRepo
}

func newDockerCopy(im *importer, j importJob) (*dockerCopy, error) {
	a := j.assets[0]

	id, ok := dockerIdentity(a)
	if !ok {
		return nil, fmt.Errorf("%w: %q is not a tagged image", errImportUsage, a.Path)
	}

	return newDockerCopyOf(im, j, id, a, id.String())
}

// newDockerCopyOf prepares the transfer of the manifest tree that asset a
// stands for, reported as path. An id without a tag is a manifest that is only
// stored by its digest.
func newDockerCopyOf(im *importer, j importJob, id dockerTagID, a sourceAsset, path string) (*dockerCopy, error) {
	src, ok := im.src.(ociSource)
	if !ok {
		return nil, errOCINoAPI
	}

	c := &dockerCopy{im: im, j: j, run: dockerRunOf(im), id: id, asset: a, path: path}
	c.key = stateKey(j.repo.Name, j.dst, c.path)
	c.src = src.registry(j.repo.Name)
	c.dst = newOCIRepo(im.tgt, j.dst, id.Image)

	return c, nil
}

// runTag performs the transfer, or finds that nothing is left to do.
func (c *dockerCopy) runTag(ctx context.Context) error {
	if marker := resumeMarker(c.asset); marker != "" && c.im.state.isDone(c.key, marker) {
		c.im.rep.add(func(r *importReport) { r.Resumed++ })

		return nil
	}

	// The source's checksum of the tag file is only a hint of the manifest
	// digest, enough to skip fetching anything for an unchanged tag.
	if e, ok := c.listedAs(c.asset.SHA256); ok {
		return c.finishPresent(ctx, e)
	}

	img, err := c.src.image(ctx, c.id.Image, c.id.Tag)
	if err != nil {
		return err
	}

	c.run.reach(c.j.dst, c.id.Image, manifestDigests(img.manifests)...)

	if e, ok := c.listedAs(strings.TrimPrefix(img.root.Digest, ociDigestPrefix)); ok {
		return c.finishPresent(ctx, e)
	}

	if c.im.cfg.DryRun {
		return c.plan(ctx, img)
	}

	return c.copy(ctx, img)
}

// listedAs finds the tag in the destination's listing when it points at the
// manifest with the given sha256.
func (c *dockerCopy) listedAs(sha string) (indexEntry, bool) {
	if sha == "" {
		return indexEntry{}, false
	}

	p, ok := c.run.listed(c.j.index, c.j.dst, c.id)
	if !ok || (p.Filename != ociDigestPrefix+sha && !strings.EqualFold(p.SHA256, sha)) {
		return indexEntry{}, false
	}

	return indexEntry{ID: p.ID, SHA256: p.SHA256}, true
}

// finishPresent accounts for a tag the destination already has: its properties
// are still brought up to date and the tag is marked done.
func (c *dockerCopy) finishPresent(ctx context.Context, e indexEntry) error {
	if !c.im.cfg.DryRun {
		a := c.asset
		if err := c.im.src.Details(ctx, &a); err != nil {
			return err
		}

		if err := c.im.applyProperties(ctx, c.j, a, e.ID); err != nil {
			return err
		}

		c.im.complete(c.key, resumeMarker(a))
	}

	c.im.rep.add(func(r *importReport) { r.Present++ })
	c.im.rep.logf("present  %s/%s -> %s", c.j.repo.Name, c.path, c.j.dst)

	return nil
}

// plan reports what a dry run would copy; a blob that several tags share counts
// once.
func (c *dockerCopy) plan(ctx context.Context, img ociImage) error {
	var planned int64

	for _, d := range img.blobs {
		first, err := c.run.once(ctx, c.blobKey(d), func() error { return nil })
		if err != nil {
			return err
		}

		if first {
			planned += d.Size
		}
	}

	c.im.rep.add(func(r *importReport) {
		r.Uploaded++
		r.Bytes += planned
	})
	c.im.rep.logf("would upload %s/%s -> %s, %d blob(s), %s", c.j.repo.Name, c.path, c.j.dst, len(img.blobs), humanBytes(planned))

	return nil
}

func (c *dockerCopy) blobKey(d ociDescriptor) dockerBlobKey {
	return dockerBlobKey{dst: c.j.dst, image: c.id.Image, digest: d.Digest}
}

// copy pushes the blobs, then the manifests, and records the tag as done.
func (c *dockerCopy) copy(ctx context.Context, img ociImage) error {
	a := c.asset
	if err := c.im.src.Details(ctx, &a); err != nil {
		return err
	}

	meta, err := c.im.uploadMeta(c.j, a)
	if err != nil {
		return err
	}

	var sent int64

	for _, d := range img.blobs {
		n, err := c.pushBlob(ctx, d)
		if err != nil {
			return fmt.Errorf("blob %s: %w", shortDigest(d.Digest), err)
		}

		sent += n
	}

	if err := c.pushManifests(ctx, a, img, meta); err != nil {
		return err
	}

	if err := c.setProperties(ctx, a); err != nil {
		return err
	}

	c.im.complete(c.key, resumeMarker(a))
	c.im.rep.add(func(r *importReport) {
		r.Uploaded++
		r.Bytes += sent
	})
	c.im.rep.logf("uploaded %s/%s -> %s, %d blob(s), %s", c.j.repo.Name, c.path, c.j.dst, len(img.blobs), humanBytes(sent))

	return nil
}

func shortDigest(d string) string {
	const shortLen = len(ociDigestPrefix) + 12

	if len(d) > shortLen {
		return d[:shortLen]
	}

	return d
}

// pushBlob makes the blob available to the image and returns how many bytes it
// sent. Tags that need the same blob wait for the first one to finish with it.
func (c *dockerCopy) pushBlob(ctx context.Context, d ociDescriptor) (int64, error) {
	var sent int64

	_, err := c.run.once(ctx, c.blobKey(d), func() error {
		var err error

		sent, err = c.transferBlob(ctx, d)

		return err
	})

	return sent, err
}

func (c *dockerCopy) transferBlob(ctx context.Context, d ociDescriptor) (int64, error) {
	what := c.id.Image + "@" + shortDigest(d.Digest)

	var held bool

	err := c.im.retry.do(ctx, "check blob "+what, func(int) error {
		var err error

		held, err = c.dst.hasBlob(ctx, d.Digest)

		return err
	})
	if err != nil {
		return 0, err
	}

	if held {
		c.run.hold(c.j.dst, c.id.Image, d.Digest)

		return 0, nil
	}

	// Another image of the repository that got the blob in this run lets the
	// registry link it instead of receiving it again.
	var (
		from = c.run.holder(c.j.dst, c.id.Image, d.Digest)
		sent int64
	)

	if from != "" {
		from = c.j.dst + "/" + from
	}

	err = c.im.retry.do(ctx, "upload blob "+what, func(int) error {
		var err error

		sent, err = c.dst.pushBlob(ctx, d, from, func(ctx context.Context) (io.ReadCloser, error) {
			return c.src.openBlob(ctx, c.id.Image, d.Digest)
		})

		return err
	})
	if err != nil {
		return 0, err
	}

	c.run.hold(c.j.dst, c.id.Image, d.Digest)

	return sent, nil
}

// pushManifests puts the child manifests by digest and the tagged manifest
// last (by digest too when there is no tag); each digest the registry reports
// must be the source's.
func (c *dockerCopy) pushManifests(ctx context.Context, a sourceAsset, img ociImage, meta uploadMeta) error {
	for i, m := range img.manifests {
		ref := m.Digest
		if i == len(img.manifests)-1 && c.id.Tag != "" {
			ref = c.id.Tag
		}

		var got string

		err := c.im.retry.do(ctx, "push manifest "+c.id.Image+":"+ref, func(int) error {
			var err error

			got, err = c.dst.putManifest(ctx, ref, m, meta)

			return err
		})
		if err != nil {
			return fmt.Errorf("manifest %s: %w", shortDigest(ref), err)
		}

		if got != m.Digest {
			return c.discard(ctx, a, got, fmt.Errorf("%w: manifest %s was stored as %s", errImportVerify, m.Digest, got))
		}
	}

	return nil
}

// discard removes the manifest the registry stored under digest, so a copy that
// failed verification is not left behind, and returns cause.
func (c *dockerCopy) discard(ctx context.Context, a sourceAsset, digest string, cause error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ociCancelTimeout)
	defer cancel()

	if err := c.im.tgt.remove(ctx, formatDocker, c.j.dst, a, uploadResult{ID: digest}); err != nil {
		return fmt.Errorf("%w (the bad copy could not be removed: %w)", cause, err)
	}

	return fmt.Errorf("%w (the bad copy was removed)", cause)
}

// setProperties attaches the source's properties to the tag, which needs the
// id the destination listed it under.
func (c *dockerCopy) setProperties(ctx context.Context, a sourceAsset) error {
	// A manifest without a tag is not listed, so there is nothing to attach
	// properties to.
	if c.id.Tag == "" || len(selectProperties(a.Props, c.im.cfg.Properties)) == 0 {
		return nil
	}

	p, ok, err := c.im.tgt.findStored(ctx, c.j.dst, formatDocker, c.dst.name, func(p listedPackage) bool {
		return p.Name == c.dst.name && p.Version == c.id.Tag
	})
	if err != nil {
		return err
	}

	if !ok {
		return fmt.Errorf("%w: %s was stored but is not listed in %q", errImportVerify, c.path, c.j.dst)
	}

	return c.im.applyProperties(ctx, c.j, a, p.ID)
}

func manifestDigests(ms []ociManifest) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Digest)
	}

	return out
}
