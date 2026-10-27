// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// npmDeprecation is a change of the `deprecated` field of an existing version.
// message is nil when the field is to be removed (`npm deprecate pkg ""`).
type npmDeprecation struct {
	version  *models.NPMVersion
	manifest map[string]json.RawMessage
	message  json.RawMessage
}

// extractNPMDeprecations pulls the versions that already exist and arrive
// without a tarball attachment out of the publish body. `npm deprecate` PUTs
// the whole packument back with an edited `deprecated` field on some versions
// and no attachments at all; those versions are metadata updates, not
// publishes, so they must neither demand an attachment nor conflict with the
// stored version. The versions that remain in pub.Versions are the real
// publishes. Only versions whose `deprecated` value changed are returned.
func (h *NPMUploadHandler) extractNPMDeprecations(
	ctx context.Context,
	pkg *models.NPMPackage,
	pub *npmPublishBody,
) ([]npmDeprecation, error) {
	if pkg == nil {
		return nil, nil
	}

	var out []npmDeprecation

	for version, raw := range pub.Versions {
		if _, hasAtt := pub.attachment(version); hasAtt {
			continue
		}

		existing, err := h.db.GetNPMVersion(ctx, pkg.ID, version)
		if err != nil {
			return nil, fmt.Errorf("get npm version %s: %w", version, err)
		}

		if existing == nil {
			continue // a new version without attachment: left for validation to reject
		}

		delete(pub.Versions, version)

		var incoming, stored map[string]json.RawMessage
		if json.Unmarshal(raw, &incoming) != nil || json.Unmarshal(existing.ManifestJSON, &stored) != nil {
			continue
		}

		if !bytes.Equal(normalizeDeprecated(incoming["deprecated"]), normalizeDeprecated(stored["deprecated"])) {
			out = append(out, npmDeprecation{version: existing, manifest: stored, message: incoming["deprecated"]})
		}
	}

	return out, nil
}

// normalizeDeprecated maps absent, null and "" to nil: all three mean "not
// deprecated" to the npm client.
func normalizeDeprecated(raw json.RawMessage) []byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`)) {
		return nil
	}

	return trimmed
}

// applyNPMDeprecations stores the new `deprecated` values in the version
// manifests. Nothing else of a stored manifest is taken from the request.
func (h *NPMUploadHandler) applyNPMDeprecations(ctx context.Context, pkg *models.NPMPackage, deps []npmDeprecation) error {
	for _, d := range deps {
		if msg := normalizeDeprecated(d.message); msg != nil {
			d.manifest["deprecated"] = msg
		} else {
			delete(d.manifest, "deprecated")
		}

		body, err := json.Marshal(d.manifest)
		if err != nil {
			return fmt.Errorf("encode manifest %s: %w", d.version.Version, err)
		}

		if err := h.db.UpdateNPMVersionManifest(ctx, pkg.ID, d.version.Version, body); err != nil {
			return fmt.Errorf("update manifest %s: %w", d.version.Version, err)
		}
	}

	return nil
}
