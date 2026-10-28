// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta

import (
	"encoding/json"
	"fmt"
	"strings"
)

// parseComposer reads the "license" field of a composer.json: one identifier
// or a list of them.
func parseComposer(text string) (Result, error) {
	var m struct {
		License json.RawMessage `json:"license"`
	}

	if err := json.Unmarshal([]byte(text), &m); err != nil {
		return Result{}, fmt.Errorf("composer.json: %w", err)
	}

	var out []string

	var one string
	if json.Unmarshal(m.License, &one) == nil {
		out = []string{one}
	} else {
		var many []string
		if json.Unmarshal(m.License, &many) != nil {
			return Result{Source: "composer.json"}, nil //nolint:nilerr // a malformed license field is an unknown license, not a failure
		}

		out = many
	}

	licenses := make([]string, 0, len(out))

	for _, l := range out {
		if l = strings.TrimSpace(l); l != "" {
			licenses = append(licenses, l)
		}
	}

	return Result{Licenses: licenses, Source: "composer.json"}, nil
}
