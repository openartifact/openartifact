// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"encoding/binary"
	"testing"
)

func TestSenseFlagsString(t *testing.T) {
	tests := []struct {
		flags uint32
		want  string
	}{
		{SENSE_EQUAL, "EQ"},
		{SENSE_LESS, "LT"},
		{SENSE_GREATER, "GT"},
		{SENSE_LESSEQUAL, "LE"},
		{SENSE_GREATEREQUAL, "GE"},
		{SENSE_ANY, ""},
		{0, ""},
		{SENSE_LESS | SENSE_GREATER, ""},
	}

	for _, tt := range tests {
		got := SenseFlagsString(tt.flags)
		if got != tt.want {
			t.Errorf("SenseFlagsString(0x%02x) = %q, want %q", tt.flags, got, tt.want)
		}
	}
}

func TestParseDependencies(t *testing.T) {
	nameData := []byte("libc\x00libssl\x00")
	flagsData := make([]byte, 8)
	binary.BigEndian.PutUint32(flagsData[0:4], SENSE_GREATEREQUAL)
	binary.BigEndian.PutUint32(flagsData[4:8], SENSE_EQUAL)

	versionData := []byte("2.31\x001.1.1\x00")

	entries := []TagEntry{
		{Tag: RPMTAG_REQUIRENAME, Type: TagTypeSTRING_ARRAY, Offset: 0, Count: 2},
		{Tag: RPMTAG_REQUIREFLAGS, Type: TagTypeINT32, Offset: int32(len(nameData)), Count: 2},
		{Tag: RPMTAG_REQUIREVERSION, Type: TagTypeSTRING_ARRAY, Offset: int32(len(nameData) + len(flagsData)), Count: 2},
	}

	data := make([]byte, len(nameData)+len(flagsData)+len(versionData))
	copy(data, nameData)
	copy(data[len(nameData):], flagsData)
	copy(data[len(nameData)+len(flagsData):], versionData)

	h := &Header{
		NIndex:  int32(len(entries)),
		HSize:   int32(len(data)),
		Entries: entries,
		Data:    data,
	}

	deps, err := ParseDependencies(h, RPMTAG_REQUIRENAME, RPMTAG_REQUIREFLAGS, RPMTAG_REQUIREVERSION)
	if err != nil {
		t.Fatalf("ParseDependencies() error: %v", err)
	}

	if len(deps) != 2 {
		t.Fatalf("len(deps) = %d, want 2", len(deps))
	}

	if deps[0].Name != "libc" {
		t.Errorf("deps[0].Name = %q, want %q", deps[0].Name, "libc")
	}

	if deps[0].Flags != SENSE_GREATEREQUAL {
		t.Errorf("deps[0].Flags = 0x%x, want 0x%x", deps[0].Flags, SENSE_GREATEREQUAL)
	}

	if deps[0].Version != "2.31" {
		t.Errorf("deps[0].Version = %q, want %q", deps[0].Version, "2.31")
	}

	if deps[1].Name != "libssl" {
		t.Errorf("deps[1].Name = %q, want %q", deps[1].Name, "libssl")
	}

	if deps[1].Flags != SENSE_EQUAL {
		t.Errorf("deps[1].Flags = 0x%x, want 0x%x", deps[1].Flags, SENSE_EQUAL)
	}
}

func TestParseDependencies_NoDeps(t *testing.T) {
	h := &Header{NIndex: 0, HSize: 0, Entries: nil, Data: nil}

	deps, err := ParseDependencies(h, RPMTAG_REQUIRENAME, RPMTAG_REQUIREFLAGS, RPMTAG_REQUIREVERSION)
	if err != nil {
		t.Fatalf("ParseDependencies() error: %v", err)
	}

	if deps != nil {
		t.Errorf("expected nil deps, got %v", deps)
	}
}

func TestExtractVersionEpoch(t *testing.T) {
	tests := []struct {
		input       string
		wantEpoch   uint32
		wantVersion string
	}{
		{"1:1.2.3", 1, "1.2.3"},
		{"2:3.0.0", 2, "3.0.0"},
		{"1.0.0", 0, "1.0.0"},
		{"0:1.0", 0, "1.0"},
	}

	for _, tt := range tests {
		epoch, version := extractVersionEpoch(tt.input)
		if epoch != tt.wantEpoch || version != tt.wantVersion {
			t.Errorf("extractVersionEpoch(%q) = (%d, %q), want (%d, %q)", tt.input, epoch, version, tt.wantEpoch, tt.wantVersion)
		}
	}
}
