// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package lazyre_test

import (
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/lazyre"
)

func TestNewCompilesOnceAndMatches(t *testing.T) {
	t.Parallel()

	re := lazyre.New(`^[a-z]{2,4}$`)

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			if re() != re() {
				t.Error("two calls returned different programs")
			}

			if !re().MatchString("abc") || re().MatchString("abcde") {
				t.Error("the expression does not behave as written")
			}
		})
	}

	wg.Wait()
}

func TestCheckReportsAnInvalidExpression(t *testing.T) {
	// Not parallel: it reads the shared list of declared expressions.
	if err := lazyre.Check(); err != nil {
		t.Fatalf("Check() = %v with only valid expressions declared", err)
	}

	lazyre.New(`(unclosed`)

	if err := lazyre.Check(); err == nil {
		t.Fatal("Check() accepted an invalid expression")
	}
}
