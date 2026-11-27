// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cocoapods

// ParseShard reads a shard into its lines.
func ParseShard(body []byte) ([]PodVersions, error) {
	var out []PodVersions

	err := lines(body, func(name []byte, versions [][]byte) bool {
		pv := PodVersions{Name: string(name), Versions: make([]string, len(versions))}
		for i, v := range versions {
			pv.Versions[i] = string(v)
		}

		out = append(out, pv)

		return true
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}
