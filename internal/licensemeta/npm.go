// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParsePackageJSON reads the licenses of an npm manifest: a version object of
// a publish request or a package.json. It never fails: a manifest that is not
// valid JSON declares nothing.
func ParsePackageJSON(manifest []byte) []string {
	res, err := parsePackageJSON(manifest)
	if err != nil {
		return nil
	}

	return res.Licenses
}

// parsePackageJSON reads the "license" field (an SPDX string or the legacy
// {"type": ...} object) and, when it is absent, the legacy "licenses" array.
func parsePackageJSON(manifest []byte) (Result, error) {
	var m struct {
		License  json.RawMessage   `json:"license"`
		Licenses []json.RawMessage `json:"licenses"`
	}

	if err := json.Unmarshal(manifest, &m); err != nil {
		return Result{}, fmt.Errorf("package.json: %w", err)
	}

	if out := npmLicenseValues([]json.RawMessage{m.License}); len(out) > 0 {
		return Result{Licenses: out, Source: "package.json"}, nil
	}

	return Result{Licenses: npmLicenseValues(m.Licenses), Source: "package.json"}, nil
}

func npmLicenseValues(raws []json.RawMessage) []string {
	var out []string

	for _, raw := range raws {
		if len(raw) == 0 {
			continue
		}

		var s string
		if json.Unmarshal(raw, &s) == nil {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}

			continue
		}

		var obj struct {
			Type string `json:"type"`
		}

		if json.Unmarshal(raw, &obj) == nil {
			if t := strings.TrimSpace(obj.Type); t != "" {
				out = append(out, t)
			}
		}
	}

	return out
}
