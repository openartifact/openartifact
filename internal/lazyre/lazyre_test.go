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
			first, second := re(), re()
			if first != second {
				t.Error("two calls returned different programs")
			}

			if !re().MatchString("abc") || re().MatchString("abcde") {
				t.Error("the expression does not behave as written")
			}
		})
	}

	wg.Wait()
}

func TestNewPanicsOnAnInvalidExpressionUnderTest(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("New accepted an invalid expression")
		}
	}()

	lazyre.New(`(unclosed`)
}
