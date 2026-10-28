// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package repotypetest holds the exhaustiveness helpers of the format
// registry: tests of every place that branches on a format use them, so adding
// a format to internal/repotype fails each place that does not decide on it
// yet.
package repotypetest

import (
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/repotype"
)

// Classified fails t for every registered format that is in neither list, in
// both, or listed twice, and for every listed name that is not a registered
// format, naming site. handled are the formats the code at site has a branch
// for; ignored are the formats it deliberately leaves to its default. Every
// format must be decided, so a new one cannot be skipped by accident. It
// returns handled for the caller to probe.
func Classified(tb testing.TB, site string, handled, ignored []string) []string {
	tb.Helper()

	for _, name := range repotype.Names() {
		in, out := slices.Contains(handled, name), slices.Contains(ignored, name)

		switch {
		case in && out:
			tb.Errorf("%s: format %q is both handled and ignored", site, name)
		case !in && !out:
			tb.Errorf("%s: format %q is not handled: add a branch for it, or list it as ignored if falling through is intended", site, name)
		}
	}

	for _, name := range slices.Concat(handled, ignored) {
		if !repotype.Valid(name) {
			tb.Errorf("%s: %q is not a registered format: drop it from the lists", site, name)
		}
	}

	return handled
}

// Specs returns every registered Spec in display (alphabetical) order.
func Specs() []repotype.Spec {
	names := repotype.Names()
	specs := make([]repotype.Spec, len(names))

	for i, name := range names {
		specs[i], _ = repotype.Lookup(name)
	}

	return specs
}

// Each runs fn once per registered format as a subtest named after it.
func Each(t *testing.T, fn func(t *testing.T, format string)) {
	t.Helper()

	for _, name := range repotype.Names() {
		t.Run(name, func(t *testing.T) { fn(t, name) })
	}
}
