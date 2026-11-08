// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"time"

	"github.com/openartifact/openartifact/internal/ociref"
)

// quayExpiryKey is the Quay annotation (manifest) and label (image config)
// holding how long after the push the image may be removed ("90m", "2w").
const quayExpiryKey = "quay.expires-after"

// quayExpiresAt returns the deadline a pushed manifest declared: now plus the
// duration of its quay.expires-after annotation or, for an image manifest, of
// the config label of the same name; the earlier one when both are valid. It is
// nil when neither is set or valid (an unparsable value never expires an image).
// labels is only called for an image manifest (one with a config).
func quayExpiresAt(now time.Time, meta ociManifestMeta, labels func() map[string]string) *time.Time {
	var best time.Duration

	if d, ok := ociref.ParseQuayExpiry(meta.Annotations[quayExpiryKey]); ok {
		best = d
	}

	if meta.Config != nil {
		if d, ok := ociref.ParseQuayExpiry(labels()[quayExpiryKey]); ok && (best == 0 || d < best) {
			best = d
		}
	}

	if best == 0 {
		return nil
	}

	at := now.Add(best)

	return &at
}
