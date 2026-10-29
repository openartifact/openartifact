// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pkgdeps

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/ai/manifest"
)

// ComposerHooks reads the dependencies of composer.
var ComposerHooks = Hooks{ //nolint:gochecknoglobals // immutable format hook value
	Parse: func(_ string, parts []manifest.Part) (Result, error) { return parseComposer(parts) },
}

// parseComposer reads the require map of composer.json: package to version
// constraint. Platform requirements (php, ext-*, lib-*) name no package and
// require-dev is not part of what the package ships.
func parseComposer(parts []manifest.Part) (Result, error) {
	var res Result

	body, ok := part(parts, "composer.json")
	if !ok {
		return res, nil
	}

	var m struct {
		Require map[string]string `json:"require"`
	}

	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return Result{}, fmt.Errorf("composer.json: %w", err)
	}

	for name, constraint := range m.Require {
		if !strings.Contains(name, "/") {
			continue
		}

		res.Deps = append(res.Deps, Dep{Name: name, Range: constraint})
	}

	return res, nil
}
