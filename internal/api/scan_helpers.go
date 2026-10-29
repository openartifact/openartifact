// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	// maxArtifactIDLen bounds artifact identifiers (artifact_refs.artifact_path is HTTP path-like).
	maxArtifactIDLen = 1024

	// maxPropertyCount, maxPropertyKeyLen and maxPropertyValueLen cap artifact properties.
	maxPropertyCount    = 100
	maxPropertyKeyLen   = 255 // artifact_properties.key is varchar(255)
	maxPropertyValueLen = 4096

	maxNameLen = 255
)

// validationError is a client-visible validation message carried as an error.
type validationError string

func (e validationError) Error() string { return string(e) }

// isValidLicensePolicyAction mirrors the chk_license_policy_action DB constraint.
func isValidLicensePolicyAction(action string) bool {
	switch action {
	case "warn", "block", "allow":
		return true
	}

	return false
}

// isUUIDString reports whether s parses as a UUID. Lookups by ID against uuid
// columns would otherwise surface as database errors (500) instead of 404.
func isUUIDString(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

// validateArtifactID returns a validation message for an unusable artifact
// identifier, or "" when it is acceptable.
func validateArtifactID(id string) string {
	switch {
	case id == "":
		return "artifact_id is required"
	case len(id) > maxArtifactIDLen:
		return "artifact_id is too long"
	}

	return plainTextProblem("artifact_id", id)
}

// plainTextProblem returns a message when s, named label in it, is not valid
// UTF-8 or holds control characters, or "" when it is plain text.
func plainTextProblem(label, s string) string {
	if !utf8.ValidString(s) {
		return label + " must be valid UTF-8"
	}

	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return label + " must not contain control characters"
		}
	}

	return ""
}

// storedArtifactMetadataStore is the part of the database storedArtifactMetadata needs.
type storedArtifactMetadataStore interface {
	ListArtifactProperties(ctx context.Context, repoID string, artifactID string) (map[string]string, error)
}

// storedArtifactMetadata builds the metadata map used by license detection and
// SBOM generation from the artifact's stored properties rather than from
// caller-supplied request data. Recognised properties:
//
//	license, licenses (comma separated), spdx_id, spdxId  -> license detection
//	dependencies (JSON array of {"name","version"})       -> SBOM components
func storedArtifactMetadata(ctx context.Context, db storedArtifactMetadataStore, repoID, artifactID string) (map[string]any, error) {
	props, err := db.ListArtifactProperties(ctx, repoID, artifactID)
	if err != nil {
		return nil, err //nolint:wrapcheck // caller logs and maps to 500
	}

	metadata := make(map[string]any, len(props))

	for key, value := range props {
		switch key {
		case "license", "License", "spdx_id", "spdxId":
			metadata[key] = value
		case "licenses", "Licenses":
			var list []any

			for part := range strings.SplitSeq(value, ",") {
				if part = strings.TrimSpace(part); part != "" {
					list = append(list, part)
				}
			}

			metadata[key] = list
		case "dependencies":
			var deps []any
			if json.Unmarshal([]byte(value), &deps) == nil {
				metadata[key] = deps
			}
		}
	}

	return metadata, nil
}
