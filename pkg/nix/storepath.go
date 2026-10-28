// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nix

import (
	"errors"
	"strings"
)

// StoreDir is the store directory of the binary caches the server hosts. A
// store with another prefix has other store paths, so its paths cannot be
// substituted by a client using /nix/store.
const StoreDir = "/nix/store"

// hashLen is the length of the nixbase32 hash that starts a store path name (a
// 160 bit hash).
const hashLen = 32

// maxNameLen is the longest name part of a store path Nix accepts.
const maxNameLen = 211

// ErrInvalidStorePath is returned for text that is not a store path or the
// base name of one.
var ErrInvalidStorePath = errors.New("invalid store path")

// ValidStoreHash reports whether s is the hash part of a store path.
func ValidStoreHash(s string) bool {
	if len(s) != hashLen {
		return false
	}

	for i := range len(s) {
		if strings.IndexByte(base32Alphabet, s[i]) < 0 {
			return false
		}
	}

	return true
}

// validNameChar reports whether c may occur in the name part of a store path:
// letters, digits and + - . _ ? =.
func validNameChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}

	return strings.IndexByte("+-._?=", c) >= 0
}

// ValidName reports whether s is acceptable as the name part of a store path.
func ValidName(s string) bool {
	if s == "" || len(s) > maxNameLen || s[0] == '.' {
		return false
	}

	for i := range len(s) {
		if !validNameChar(s[i]) {
			return false
		}
	}

	return true
}

// SplitBaseName splits "<hash>-<name>" into its parts.
func SplitBaseName(base string) (hash, name string, err error) {
	hash, name, ok := strings.Cut(base, "-")
	if !ok || !ValidStoreHash(hash) || !ValidName(name) {
		return "", "", ErrInvalidStorePath
	}

	return hash, name, nil
}

// SplitStorePath splits "/nix/store/<hash>-<name>" into its parts.
func SplitStorePath(path string) (hash, name string, err error) {
	base, ok := strings.CutPrefix(path, StoreDir+"/")
	if !ok {
		return "", "", ErrInvalidStorePath
	}

	return SplitBaseName(base)
}
