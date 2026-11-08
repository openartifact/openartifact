// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta

import (
	"errors"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/pelletier/go-toml/v2"
)

// CargoHooks reads the licenses of cargo.
var CargoHooks = Hooks{ //nolint:gochecknoglobals // immutable format hook value
	Expected: expectAlways,
	Parse:    func(_ string, parts []manifest.Part) (Result, error) { return parseCargo(parts[0].Body) },
}

// parseCargo reads package.license of a Cargo.toml. `cargo package` writes the
// normalised manifest, so a workspace-inherited license is already a string; a
// crate that only has license-file declares nothing a policy could match.
func parseCargo(text string) (Result, error) {
	type table struct {
		License any `toml:"license"`
	}

	var m struct {
		Package table `toml:"package"`
		// Project is the deprecated spelling of [package] that cargo still reads.
		Project table `toml:"project"`
	}

	if err := toml.Unmarshal([]byte(text), &m); err != nil {
		return Result{}, fmt.Errorf("Cargo.toml: %w", err) //nolint:staticcheck // file name
	}

	declared := m.Package.License
	if declared == nil {
		declared = m.Project.License
	}

	if declared == nil {
		return Result{}, nil
	}

	expr, isString := declared.(string)
	if !isString {
		// An inherited ({ workspace = true }) or mistyped license in a crate
		// `cargo package` did not write: it declares nothing a policy could
		// match, which must not read as a clean crate.
		return Result{}, errors.New("Cargo.toml: package.license is not a string") //nolint:staticcheck,err113 // file name; one-off message
	}

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
