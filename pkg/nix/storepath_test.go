// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nix

import (
	"errors"
	"strings"
	"testing"
)

// testHash is a valid store hash: every character of the alphabet once.
const testHash = base32Alphabet

func TestValidStoreHash(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{testHash, true},
		{strings.Repeat("0", 32), true},
		{strings.Repeat("0", 31), false},
		{strings.Repeat("0", 33), false},
		{"", false},
		{strings.Repeat("e", 32), false},
		{strings.Repeat("o", 32), false},
		{strings.Repeat("u", 32), false},
		{strings.Repeat("t", 32), false},
		{strings.Repeat("A", 32), false},
		{strings.Repeat("0", 31) + "-", false},
		{strings.Repeat("0", 31) + "/", false},
	}

	for _, tc := range tests {
		if got := ValidStoreHash(tc.in); got != tc.want {
			t.Errorf("ValidStoreHash(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestValidName(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"hello-2.12.1", true},
		{"a", true},
		{"gcc-13.2.0-lib", true},
		{"x+y_z=1?", true},
		{"UPPER.case", true},
		{strings.Repeat("a", maxNameLen), true},
		{strings.Repeat("a", maxNameLen+1), false},
		{"", false},
		{".hidden", false},
		{"..", false},
		{"a/b", false},
		{"a b", false},
		{"a\nb", false},
		{"a\x00b", false},
		{"caf\u00e9", false},
	}

	for _, tc := range tests {
		if got := ValidName(tc.in); got != tc.want {
			t.Errorf("ValidName(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSplitStorePath(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantName string
		wantErr  bool
	}{
		{"valid", StoreDir + "/" + testHash + "-hello-2.12.1", "hello-2.12.1", false},
		{"name with dashes", StoreDir + "/" + testHash + "-a-b-c", "a-b-c", false},
		{"no prefix", testHash + "-hello", "", true},
		{"other store dir", "/gnu/store/" + testHash + "-hello", "", true},
		{"prefix without slash", "/nix/store" + testHash + "-hello", "", true},
		{"empty", "", "", true},
		{"only prefix", StoreDir + "/", "", true},
		{"no name", StoreDir + "/" + testHash, "", true},
		{"empty name", StoreDir + "/" + testHash + "-", "", true},
		{"short hash", StoreDir + "/abc-hello", "", true},
		{"bad hash char", StoreDir + "/" + strings.Repeat("e", 32) + "-hello", "", true},
		{"subpath", StoreDir + "/" + testHash + "-hello/bin/hello", "", true},
		{"traversal in name", StoreDir + "/" + testHash + "-../etc", "", true},
		{"traversal as base", StoreDir + "/../etc/passwd", "", true},
		{"dot name", StoreDir + "/" + testHash + "-.x", "", true},
		{"trailing newline", StoreDir + "/" + testHash + "-hello\n", "", true},
		{"nul", StoreDir + "/" + testHash + "-he\x00llo", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hash, name, err := SplitStorePath(tc.in)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidStorePath) {
					t.Fatalf("SplitStorePath(%q) error = %v, want ErrInvalidStorePath", tc.in, err)
				}

				return
			}

			if err != nil || hash != testHash || name != tc.wantName {
				t.Fatalf("SplitStorePath(%q) = %q, %q, %v; want %q, %q", tc.in, hash, name, err, testHash, tc.wantName)
			}
		})
	}
}

func TestSplitBaseName(t *testing.T) {
	hash, name, err := SplitBaseName(testHash + "-foo-1.0")
	if err != nil || hash != testHash || name != "foo-1.0" {
		t.Fatalf("SplitBaseName = %q, %q, %v", hash, name, err)
	}

	for _, bad := range []string{"", "foo", testHash, StoreDir + "/" + testHash + "-foo", "x-" + testHash} {
		if _, _, err := SplitBaseName(bad); !errors.Is(err, ErrInvalidStorePath) {
			t.Errorf("SplitBaseName(%q) error = %v, want ErrInvalidStorePath", bad, err)
		}
	}
}
