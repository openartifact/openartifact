// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package composer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
)

// Expand is Composer's MetadataMinifier::expand: it turns a minified version
// list, in which every entry after the first carries only what changed, into
// full entries. The input is not modified.
func Expand(versions []Version) []Version {
	out := make([]Version, 0, len(versions))

	var cur Version

	for _, v := range versions {
		if cur == nil {
			cur = maps.Clone(v)
			if cur == nil {
				cur = Version{}
			}

			out = append(out, maps.Clone(cur))

			continue
		}

		for k, val := range v {
			if s, ok := val.(string); ok && s == unsetMarker {
				delete(cur, k)
				continue
			}

			cur[k] = val
		}

		out = append(out, maps.Clone(cur))
	}

	return out
}

// Decode parses a p2 document into full version entries per package name,
// expanding a minified one. Numbers decode as json.Number.
func Decode(data []byte) (map[string][]Version, error) {
	var doc document

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode composer metadata: %w", err)
	}

	if doc.Minified == MinifiedMarker {
		for name, versions := range doc.Packages {
			doc.Packages[name] = Expand(versions)
		}
	}

	return doc.Packages, nil
}
