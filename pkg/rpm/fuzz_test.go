// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"bytes"
	"os"
	"testing"
)

// FuzzParseRPM feeds arbitrary bytes to the full RPM parser; it must return an
// error or a package, never panic or allocate without bound.
func FuzzParseRPM(f *testing.F) {
	full := buildFullRPM()

	f.Add(full)
	f.Add(full[:LeadSize])
	f.Add(full[:len(full)/2])
	f.Add(buildValidLead())
	f.Add([]byte{})

	for _, fixture := range []string{"testdata/weakdeps.rpm", "testdata/sizes-v4.rpm", "testdata/sizes-v6.rpm"} {
		if data, err := os.ReadFile(fixture); err == nil {
			f.Add(data)
		}
	}

	// Amplification shapes: many declared array elements, a shared huge
	// dirname, an over-long dependency list and an index-entry count over the cap.
	var bomb craft

	bomb.identity()
	bomb.strArray(RPMTAG_DIRNAMES, 1, []byte("/\x00"))
	bomb.strArray(RPMTAG_BASENAMES, 1<<16, emptyStrings(1<<16))
	f.Add(bomb.rpmBytes())

	var deps craft

	deps.identity()
	deps.strArray(RPMTAG_REQUIRENAME, MaxArrayEntries+1, emptyStrings(MaxArrayEntries+1))
	f.Add(deps.rpmBytes())

	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = ParseRPM(bytes.NewReader(data))
	})
}

// FuzzParseHeader exercises the header index/data parser and its tag getters.
func FuzzParseHeader(f *testing.F) {
	f.Add(buildHeaderBytes(buildHeader("demo", "1.0", 0).Entries, buildHeader("demo", "1.0", 0).Data))
	f.Add([]byte{0x8E, 0xAD, 0xE8, 0x01, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 4})
	f.Add([]byte{})

	f.Fuzz(func(_ *testing.T, data []byte) {
		h, err := ParseHeader(bytes.NewReader(data), 1<<16)
		if err != nil {
			return
		}

		for _, e := range h.Entries {
			_, _ = h.GetString(e.Tag)
			_, _ = h.GetStringArray(e.Tag)
			_, _ = h.GetInt32s(e.Tag)
			_, _ = h.GetUint32(e.Tag)
		}
	})
}
