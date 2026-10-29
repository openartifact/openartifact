// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import (
	"errors"
	"testing"
)

// TestFilePromoterCopiesIdempotentlyAndForces drives the descriptor-driven
// promoter end to end with the maven descriptor (shared versions, a side
// table): the copy, its retry, the conflict on other content, a sibling file
// in the destination version that is no obstacle, and the forced replacement.
func TestFilePromoterCopiesIdempotentlyAndForces(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	p := promoterFor(t, "maven")
	src, dst := h.repo("maven", false), h.repo("maven", false)
	blobA, shaA := h.blob("jar A")
	blobB, _ := h.blob("jar B")
	blobPom, _ := h.blob("pom")

	seed := fileSeed{pkg: "com.acme:shop", version: "1.0.0", path: "com/acme/shop/1.0.0/shop-1.0.0.jar", unitKey: "com.acme:shop:1.0.0"}
	srcFile := h.file(src.ID, blobA, seed)
	h.exec(`INSERT INTO maven_files (id, classifier, extension, md5) VALUES ($1, 'sources', 'jar', 'm5')`, srcFile)

	// A sibling in the destination version does not stand in the way.
	h.file(dst.ID, blobPom, fileSeed{pkg: seed.pkg, version: seed.version, path: "com/acme/shop/1.0.0/shop-1.0.0.pom", role: "metadata"})

	req := Request{Src: src, Dst: dst, SHA256: shaA, Path: seed.path}

	res, err := h.promoteOnce(p, req)
	if err != nil || len(res.Copied) != 1 || res.Usage.Artifacts != 1 || res.Usage.Bytes != int64(len("jar A")) {
		t.Fatalf("promote = %+v, %v", res, err)
	}

	if n := h.count(`SELECT COUNT(*) FROM package_files f JOIN maven_files m ON m.id = f.id
		WHERE f.repository_id = $1 AND m.classifier = 'sources' AND m.md5 = 'm5' AND f.unit_key = $2`, dst.ID, seed.unitKey); n != 1 {
		t.Errorf("destination has %d copied file+side rows, want 1", n)
	}

	again, err := h.promoteOnce(p, req)
	if err != nil || len(again.Copied) != 0 || len(again.Existing) != 1 {
		t.Fatalf("retry = %+v, %v", again, err)
	}

	// Other content at the same path is a conflict, unless forced.
	other := h.repo("maven", false)
	h.file(other.ID, blobB, seed)

	if _, err := h.promoteOnce(p, Request{Src: src, Dst: other, SHA256: shaA, Path: seed.path}); !errors.As(err, new(*ConflictError)) {
		t.Fatalf("err = %v, want a ConflictError", err)
	}

	forced, err := h.promoteOnce(p, Request{Src: src, Dst: other, SHA256: shaA, Path: seed.path, Force: true, Actor: "admin"})
	if err != nil || len(forced.Replaced) != 1 || len(forced.Copied) != 1 {
		t.Fatalf("forced = %+v, %v", forced, err)
	}

	if n := h.count(`SELECT COUNT(*) FROM package_files
		WHERE repository_id = $1 AND blob_id = $2 AND deleted_at IS NOT NULL`, other.ID, blobB); n != 1 {
		t.Errorf("%d replaced files in the trash, want 1", n)
	}
}
