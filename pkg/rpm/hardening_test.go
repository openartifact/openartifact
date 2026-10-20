// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"errors"
	"testing"
)

func TestHeaderGettersRejectNegativeOffsetsAndCounts(t *testing.T) {
	h := &Header{
		Data: make([]byte, 16),
		Entries: []TagEntry{
			{Tag: 1, Type: TagTypeSTRING, Offset: -4, Count: 1},
			{Tag: 2, Type: TagTypeINT32, Offset: -8, Count: 1},
			{Tag: 3, Type: TagTypeINT32, Offset: 0, Count: -1},
			{Tag: 4, Type: TagTypeINT64, Offset: -1, Count: 1},
			{Tag: 5, Type: TagTypeSTRING_ARRAY, Offset: -1, Count: 1},
			{Tag: 6, Type: TagTypeSTRING_ARRAY, Offset: 0, Count: -5},
			{Tag: 7, Type: TagTypeINT32, Offset: 1 << 30, Count: 1 << 30},
		},
	}

	if _, ok := h.GetString(1); ok {
		t.Error("GetString accepted negative offset")
	}

	for _, tag := range []int32{2, 3, 7} {
		if _, ok := h.GetInt32s(tag); ok {
			t.Errorf("GetInt32s accepted bad entry %d", tag)
		}
	}

	if _, ok := h.GetInt64(4); ok {
		t.Error("GetInt64 accepted negative offset")
	}

	for _, tag := range []int32{5, 6} {
		if _, ok := h.GetStringArray(tag); ok {
			t.Errorf("GetStringArray accepted bad entry %d", tag)
		}
	}
}

func TestTagConstantsMatchRPM(t *testing.T) {
	want := map[string][2]int32{
		"REQUIREVERSION": {RPMTAG_REQUIREVERSION, 1050}, "CONFLICTVERSION": {RPMTAG_CONFLICTVERSION, 1055},
		"OBSOLETEFLAGS": {RPMTAG_OBSOLETEFLAGS, 1114}, "OBSOLETEVERSION": {RPMTAG_OBSOLETEVERSION, 1115},
		"FILEDIGESTS": {RPMTAG_FILEDIGESTS, 1035}, "BASENAMES": {RPMTAG_BASENAMES, 1117},
		"DIRNAMES": {RPMTAG_DIRNAMES, 1118}, "DIRINDEXES": {RPMTAG_DIRINDEXES, 1116},
		"SUGGESTSNAME": {RPMTAG_SUGGESTSNAME, 5049}, "SUPPLEMENTNAME": {RPMTAG_SUPPLEMENTNAME, 5052},
		"ENHANCENAME": {RPMTAG_ENHANCENAME, 5055},
	}
	for n, v := range want {
		if v[0] != v[1] {
			t.Errorf("%s = %d, want %d", n, v[0], v[1])
		}
	}
}

func TestExtractFilesJoinsDirAndBase(t *testing.T) {
	// data: "/usr/bin/\0" at 0 (10 bytes), "foo\0bar\0" at 10, dirindexes int32 x2 at 20.
	data := append([]byte("/usr/bin/\x00foo\x00bar\x00"), []byte{0, 0, 0, 0}...)
	data = append(data, 0, 0, 0, 0, 0, 0, 0, 0)
	h := &Header{Data: data, Entries: []TagEntry{
		{Tag: RPMTAG_DIRNAMES, Type: TagTypeSTRING_ARRAY, Offset: 0, Count: 1},
		{Tag: RPMTAG_BASENAMES, Type: TagTypeSTRING_ARRAY, Offset: 10, Count: 2},
		{Tag: RPMTAG_DIRINDEXES, Type: TagTypeINT32, Offset: 22, Count: 2},
	}}

	files := extractFiles(h)
	if len(files) != 2 || files[0].Path != "/usr/bin/foo" || files[1].Path != "/usr/bin/bar" {
		t.Fatalf("unexpected files: %+v", files)
	}
}

func TestValidateRequired(t *testing.T) {
	ok := &Package{Name: "a", Version: "1", Release: "1", Arch: "x86_64"}
	if err := validateRequired(ok); err != nil {
		t.Fatal(err)
	}

	for _, p := range []*Package{
		{Version: "1", Release: "1", Arch: "x"},
		{Name: "a", Release: "1", Arch: "x"},
		{Name: "a", Version: "1", Arch: "x"},
		{Name: "a", Version: "1", Release: "1"},
		{Name: "../a", Version: "1", Release: "1", Arch: "x"},
	} {
		if err := validateRequired(p); !errors.Is(err, ErrInvalidHeader) {
			t.Errorf("expected ErrInvalidHeader for %+v, got %v", p, err)
		}
	}
}
