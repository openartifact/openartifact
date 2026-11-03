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
// An invalid expression panics at its first use instead of at start, so every
// package that declares one runs Check in a test.
package lazyre

import (
	"regexp"
	"sync"
)

//nolint:gochecknoglobals // the expressions declared by the packages linked into the binary; written during package initialisation only
var declared []string

// New returns a function that compiles expr on its first call and returns the
// same *regexp.Regexp afterwards. It is safe for concurrent use.
func New(expr string) func() *regexp.Regexp {
	declared = append(declared, expr)

	return sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(expr) })
}

// Check compiles every expression declared so far and returns the first error.
// It is for tests: the compiled result is thrown away.
func Check() error {
	for _, expr := range declared {
		if _, err := regexp.Compile(expr); err != nil {
			return err
		}
	}

	return nil
}
