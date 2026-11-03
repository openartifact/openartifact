// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package lazyre compiles package-level regular expressions on first use.
//
// A bounded repetition such as [A-Za-z0-9._-]{0,127} compiles to a program
// with one instruction group per repetition, so a handful of validators cost
// the server a megabyte and thousands of allocations at start, before any
// request needs them. Declare them with New and call the result where the
// expression is used:
//
//	var tagRE = lazyre.New(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
//
//	tagRE().MatchString(s)
//
// An invalid expression panics at its first use instead of at start; New
// compiles it at once when running under go test, which catches it there.
package lazyre

import (
	"regexp"
	"sync"
	"testing"
)

// New returns a function that compiles expr on its first call and returns the
// same *regexp.Regexp afterwards. It is safe for concurrent use. Under go test
// the expression is compiled at once, so an invalid one fails every test run of
// the package that declares it, not a request.
func New(expr string) func() *regexp.Regexp {
	if testing.Testing() {
		regexp.MustCompile(expr)
	}

	return sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(expr) })
}
