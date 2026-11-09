// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import "testing"

func TestIsConflict(t *testing.T) {
	t.Parallel()

	me := unitStampUUID(1)

	at := func(wall int64, origin byte) Stamp { return Stamp{Wall: wall, Origin: unitStampUUID(origin)} }
	ptr := func(s Stamp) *Stamp { return &s }
	live := func(stamp Stamp, ref string, base *Stamp) Entry {
		return Entry{Stamp: stamp, ContentRef: ref, Base: base}
	}
	dead := func(stamp Stamp, base *Stamp) Entry { return Entry{Stamp: stamp, Tombstone: true, Base: base} }

	tests := []struct {
		name            string
		local, incoming Entry
		want            bool
	}{
		{"replace: peer overwrote what it had seen", live(at(10, 1), "x", nil), live(at(20, 2), "y", ptr(at(10, 1))), false},
		{"replace: peer based on a newer state", live(at(10, 1), "x", nil), live(at(30, 2), "y", ptr(at(20, 2))), false},
		{"replace: peer had not seen our change", live(at(20, 1), "x", nil), live(at(30, 2), "y", ptr(at(10, 2))), true},
		{"replace: peer without base", live(at(10, 1), "x", nil), live(at(20, 2), "y", nil), true},
		{"replace: same content", live(at(10, 1), "x", nil), live(at(20, 2), "x", nil), false},
		{"replace: delete not seen", live(at(20, 1), "x", nil), dead(at(30, 2), ptr(at(10, 2))), true},
		{"replace: delete seen", live(at(10, 1), "x", nil), dead(at(30, 2), ptr(at(10, 1))), false},
		{"replace: local change written elsewhere", live(at(10, 2), "x", nil), live(at(20, 2), "y", nil), false},
		{"replace: local tombstone", dead(at(10, 1), nil), live(at(20, 2), "y", nil), false},
		{"skip: we had not seen the peer's change", live(at(20, 1), "x", nil), live(at(10, 2), "y", nil), true},
		{"skip: we had not seen it, base older", live(at(30, 1), "x", ptr(at(5, 2))), live(at(10, 2), "y", nil), true},
		{"skip: we overwrote it knowingly", live(at(30, 1), "x", ptr(at(10, 2))), live(at(10, 2), "y", nil), false},
		{"skip: we overwrote a newer state", live(at(30, 1), "x", ptr(at(20, 2))), live(at(10, 2), "y", nil), false},
		{"skip: same content", live(at(20, 1), "x", nil), live(at(10, 2), "x", nil), false},
		{"skip: local delete, peer live", dead(at(20, 1), nil), live(at(10, 2), "y", nil), true},
		{"skip: both deleted", dead(at(20, 1), nil), dead(at(10, 2), nil), false},
		{"skip: local state written elsewhere", live(at(20, 2), "x", nil), live(at(10, 2), "y", nil), false},
		{"equal stamps", live(at(10, 1), "x", nil), live(at(10, 1), "y", nil), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := isConflict(tc.local, tc.incoming, me); got != tc.want {
				t.Errorf("isConflict = %v, want %v", got, tc.want)
			}
		})
	}
}
