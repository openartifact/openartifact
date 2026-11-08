// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"

	"github.com/openartifact/openartifact/internal/attestation"
	formatdocker "github.com/openartifact/openartifact/internal/format/docker"
)

// attestationHook returns the manifest push hook that indexes and verifies the
// signatures, provenance and SBOMs pushed next to an image (cosign tags and
// referrers).
func (a *app) attestationHook() formatdocker.ManifestPutHook {
	svc := attestation.NewService(a.log, a.db, a.store.blobs)

	return func(ctx context.Context, ev formatdocker.ManifestEvent) {
		err := svc.Ingest(ctx, attestation.Event{
			RepositoryID: ev.RepoID,
			Image:        ev.Image,
			Tag:          ev.Tag,
			Digest:       ev.Digest,
			Raw:          ev.Manifest,
		})
		if err != nil {
			a.log.Errorw("attestation ingest failed", "repository", ev.RepoName, "digest", ev.Digest, "error", err)
		}
	}
}
