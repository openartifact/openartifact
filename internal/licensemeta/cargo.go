// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta

import (
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// parseCargo reads package.license of a Cargo.toml. `cargo package` writes the
// normalised manifest, so a workspace-inherited license is already a string; a
// crate that only has license-file declares nothing a policy could match.
func parseCargo(text string) (Result, error) {
	var m struct {
		Package struct {
			License any `toml:"license"`
		} `toml:"package"`
	}

	if err := toml.Unmarshal([]byte(text), &m); err != nil {
		return Result{}, fmt.Errorf("Cargo.toml: %w", err) //nolint:staticcheck // file name
	}

	expr, _ := m.Package.License.(string)
	if expr = strings.TrimSpace(expr); expr == "" {
		return Result{}, nil
	}

	return Result{Licenses: []string{cargoExpression(expr)}, Source: "Cargo.toml"}, nil
}

// cargoExpression rewrites the legacy "MIT/Apache-2.0" spelling (a "/" between
// licenses means OR) as an SPDX expression; anything else is returned as is.
func cargoExpression(expr string) string {
	if strings.Contains(expr, "/") && !strings.ContainsAny(expr, " ()") {
		return strings.ReplaceAll(expr, "/", " OR ")
	}

	return expr
}
