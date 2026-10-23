// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"runtime"
	"sync"
	"weak"
)

// The transfer units of one run share three things the core has no place for:
// the destination listing indexed by image and tag (many tags can carry the
// same digest, which the core's by-path and by-digest indexes cannot express),
// which blobs a destination already got during the run, and which images of a
// repository hold a blob, so a layer shared by several images is uploaded once
// and mounted into the others.

// dockerRuns holds the state of the runs in progress, keyed weakly by the
// importer that owns the run: an entry disappears with its importer.
var dockerRuns = struct { //nolint:gochecknoglobals // the format hooks receive the importer only; there is no other place for per-run state
	sync.Mutex

	runs map[weak.Pointer[importer]]*dockerRun
}{runs: map[weak.Pointer[importer]]*dockerRun{}}

func dockerRunOf(im *importer) *dockerRun {
	key := weak.Make(im)

	dockerRuns.Lock()
	defer dockerRuns.Unlock()

	if r, ok := dockerRuns.runs[key]; ok {
		return r
	}

	r := newDockerRun()
	dockerRuns.runs[key] = r

	runtime.AddCleanup(im, func(k weak.Pointer[importer]) {
		dockerRuns.Lock()
		defer dockerRuns.Unlock()

		delete(dockerRuns.runs, k)
	}, key)

	return r
}

type dockerRun struct {
	mu      sync.Mutex
	tags    map[*targetIndex]*dockerListing
	blobs   map[dockerBlobKey]*dockerFlight
	holders map[dockerHolderKey]string
	gates   map[dockerTagKey]*dockerTagGate
	reached map[dockerManifestKey]bool
}

func newDockerRun() *dockerRun {
	return &dockerRun{
		tags:    map[*targetIndex]*dockerListing{},
		blobs:   map[dockerBlobKey]*dockerFlight{},
		holders: map[dockerHolderKey]string{},
		gates:   map[dockerTagKey]*dockerTagGate{},
		reached: map[dockerManifestKey]bool{},
	}
}

// dockerListKey names one tag in the destination's package listing.
type dockerListKey struct{ name, tag string }

// dockerListing is a destination listing indexed by image and tag.
type dockerListing struct {
	once sync.Once
	rows map[dockerListKey]listedPackage
}

// listed finds the destination's row for a tag of an image.
func (r *dockerRun) listed(ix *targetIndex, dst string, id dockerTagID) (listedPackage, bool) {
	if ix == nil {
		return listedPackage{}, false
	}

	r.mu.Lock()

	l := r.tags[ix]
	if l == nil {
		l = &dockerListing{}
		r.tags[ix] = l
	}

	r.mu.Unlock()

	l.once.Do(func() {
		l.rows = make(map[dockerListKey]listedPackage, len(ix.all))
		for _, p := range ix.all {
			l.rows[dockerListKey{name: p.Name, tag: p.Version}] = p
		}
	})

	p, ok := l.rows[dockerListKey{name: dst + "/" + id.Image, tag: id.Tag}]

	return p, ok
}

// dockerBlobKey identifies a blob in one image of a destination repository.
type dockerBlobKey struct{ dst, image, digest string }

// dockerFlight is a blob transfer that one unit performs and the others wait for.
type dockerFlight struct {
	done chan struct{}
	err  error
}

// once runs fn for a blob unless a unit already did or is doing so; the other
// units wait for that outcome, success or failure. first tells the caller that
// ran fn.
func (r *dockerRun) once(ctx context.Context, key dockerBlobKey, fn func() error) (bool, error) {
	r.mu.Lock()

	f, running := r.blobs[key]
	if !running {
		f = &dockerFlight{done: make(chan struct{})}
		r.blobs[key] = f
	}

	r.mu.Unlock()

	if running {
		select {
		case <-f.done:
			return false, f.err
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}

	f.err = fn()
	close(f.done)

	return true, f.err
}

// dockerHolderKey identifies a blob in a destination repository.
type dockerHolderKey struct{ dst, digest string }

// hold records that image of dst has the blob.
func (r *dockerRun) hold(dst, image, digest string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	k := dockerHolderKey{dst: dst, digest: digest}
	if _, ok := r.holders[k]; !ok {
		r.holders[k] = image
	}
}

// holder returns an image of dst other than image that holds the blob, or "".
func (r *dockerRun) holder(dst, image, digest string) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if h := r.holders[dockerHolderKey{dst: dst, digest: digest}]; h != image {
		return h
	}

	return ""
}

// dockerTagKey names a tag of an image in a destination repository.
type dockerTagKey struct{ dst, image, tag string }

// dockerTagGate opens when the unit that copies a tag has finished with it.
type dockerTagGate struct {
	once sync.Once
	done chan struct{}
}

func (r *dockerRun) gate(dst string, id dockerTagID) *dockerTagGate {
	r.mu.Lock()
	defer r.mu.Unlock()

	k := dockerTagKey{dst: dst, image: id.Image, tag: id.Tag}

	g := r.gates[k]
	if g == nil {
		g = &dockerTagGate{done: make(chan struct{})}
		r.gates[k] = g
	}

	return g
}

// finishTag tells the units waiting for the tag that it is done, whether it was
// copied, found present, resumed or failed.
func (r *dockerRun) finishTag(dst string, id dockerTagID) {
	g := r.gate(dst, id)
	g.once.Do(func() { close(g.done) })
}

// waitTag blocks until the unit of the tag has finished.
func (r *dockerRun) waitTag(ctx context.Context, dst string, id dockerTagID) error {
	select {
	case <-r.gate(dst, id).done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// dockerManifestKey identifies a manifest in one image of a destination
// repository.
type dockerManifestKey struct{ dst, image, digest string }

// reach records manifests that the tags of this run stand for: they are stored
// with their tag, not as manifests of their own.
func (r *dockerRun) reach(dst, image string, digests ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, d := range digests {
		r.reached[dockerManifestKey{dst: dst, image: image, digest: d}] = true
	}
}

func (r *dockerRun) isReached(dst, image, digest string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.reached[dockerManifestKey{dst: dst, image: image, digest: digest}]
}
