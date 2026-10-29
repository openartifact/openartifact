// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

// isMetadataKey reports whether key names an index or metadata file of format
// (small by nature, capped at DefaultMaxMetadata) rather than a package file.
func isMetadataKey(format, key string) bool {
	h, ok := hooks.Lookup(format)

	return ok && h.IsMetadata != nil && h.IsMetadata(key)
}
