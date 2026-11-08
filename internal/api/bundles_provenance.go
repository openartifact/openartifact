// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/openartifact/openartifact/internal/attestation"
	"github.com/openartifact/openartifact/internal/database/models"
)

// ProvenanceGate decides whether a docker image may be promoted from srcRepoID
// into dstRepoID: it answers attestation.ErrProvenanceRequired for an image
// without a verified SLSA provenance while either repository requires one.
type ProvenanceGate interface {
	CheckProvenance(ctx context.Context, srcRepoID, dstRepoID, digest string) error
}

// provenanceRequiredCode is the error code of a promotion refused for lack of
// verified provenance.
const provenanceRequiredCode = "PROVENANCE_REQUIRED"

// WithProvenance enables require_verified_provenance of the attestation trust
// configuration on the promotion of docker items.
func (h *BundlesHandler) WithProvenance(g ProvenanceGate) *BundlesHandler {
	h.provenance = g
	return h
}

// provenanceFailure is the 422 answer of a promotion into target while a docker
// item of the bundle has no verified SLSA provenance and a repository involved
// requires one; nil when the promotion may proceed.
func (h *BundlesHandler) provenanceFailure(ctx context.Context, bundle *models.ReleaseBundle, target *models.Repository) *apiFailure {
	if h.provenance == nil || target.Format != dockerRepoFormat {
		return nil
	}

	items, err := h.db.ListBundleItems(ctx, bundle.ID)
	if err != nil {
		h.log.Errorw("promote bundle: db error listing items", "error", err)
		return &apiFailure{http.StatusInternalServerError, "internal_error", "database error"}
	}

	for _, it := range items {
		if it.Format != dockerRepoFormat {
			continue
		}

		err := h.provenance.CheckProvenance(ctx, it.SourceRepoID, target.ID, "sha256:"+it.SHA256)
		if errors.Is(err, attestation.ErrProvenanceRequired) {
			return &apiFailure{http.StatusUnprocessableEntity, provenanceRequiredCode,
				"the docker image sha256:" + it.SHA256 + " has no verified SLSA provenance"}
		}

		if err != nil {
			h.log.Errorw("promote bundle: check provenance", "error", err)
			return &apiFailure{http.StatusInternalServerError, "internal_error", "provenance check failed"}
		}
	}

	return nil
}
