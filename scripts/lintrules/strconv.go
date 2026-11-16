// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build ruleguard

// Package lintrules holds the ruleguard rules gocritic runs (see .golangci.yaml).
package lintrules

import "github.com/quasilyte/go-ruleguard/dsl"

// discardedStrconvError flags a strconv parse whose error is dropped: the
// result of a failed parse is 0 (or the saturated maximum on overflow), which
// silently turns invalid input into a valid-looking value.
func discardedStrconvError(m dsl.Matcher) {
	m.Match(
		`$x, _ = strconv.$f($*_)`,
		`$x, _ := strconv.$f($*_)`,
		`_, _ = strconv.$f($*_)`,
		`_, _ := strconv.$f($*_)`,
	).
		Where(m["f"].Text.Matches(`^(Atoi|Parse.*)$`)).
		Report("the error of strconv.$f is discarded; handle it or justify with //nolint:gocritic")
}
