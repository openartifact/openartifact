// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pyversion

import "testing"

func TestComparePEP440(t *testing.T) {
	order := []string{"1.0.dev1", "1.0a1", "1.0b2", "1.0rc1", "1.0", "1.0.post1", "1.1", "9.0.0", "10.0.0", "1!0.1"}
	for i := 1; i < len(order); i++ {
		if Compare(order[i-1], order[i]) >= 0 {
			t.Errorf("%s should sort below %s", order[i-1], order[i])
		}
	}

	if Compare("1.0", "1.0.0") != 0 {
		t.Error("1.0 and 1.0.0 should be equal")
	}
}
