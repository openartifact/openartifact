// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package httpcache

import (
	"strings"
	"testing"
)

// FuzzETagMatches: a quoted tag always matches itself, alone and inside a list,
// whatever it contains (an entity-tag cannot contain a quote), and nothing panics.
func FuzzETagMatches(f *testing.F) {
	f.Add("release,1", `"x", W/"y"`)
	f.Add("", "*")
	f.Add("a b", `"unbalanced`)

	f.Fuzz(func(t *testing.T, body, other string) {
		_ = ETagMatches(other, body)

		if strings.ContainsRune(body, '"') {
			return
		}

		tag := `"` + body + `"`
		if !ETagMatches(tag, tag) || !ETagMatches(`"other", W/`+tag+` , "last"`, tag) {
			t.Fatalf("%s does not match itself", tag)
		}
	})
}
