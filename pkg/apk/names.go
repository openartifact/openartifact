// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package apk

import "strings"

const (
	fileExt       = ".apk"
	maxNameLen    = 128
	maxVersionLen = 128
	maxArchLen    = 32
	maxSegmentLen = 64
)

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isAlnum(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// validName reports whether s is a package name: ^[a-zA-Z0-9][a-zA-Z0-9._+-]{0,127}$.
func validName(s string) bool {
	if s == "" || len(s) > maxNameLen || !isAlnum(s[0]) {
		return false
	}

	for i := 1; i < len(s); i++ {
		c := s[i]
		if !isAlnum(c) && c != '.' && c != '_' && c != '+' && c != '-' {
			return false
		}
	}

	return true
}

// validVersion reports whether s is a package version: ^[0-9][A-Za-z0-9._+~-]{0,127}$
// in which no '-' is followed by a digit. The extra rule is what lets
// ParseFileName find the end of the name again: real apk versions only use '-'
// in front of the "r<release>" suffix.
func validVersion(s string) bool {
	if s == "" || len(s) > maxVersionLen || !isDigit(s[0]) {
		return false
	}

	for i := 1; i < len(s); i++ {
		c := s[i]
		if !isAlnum(c) && c != '.' && c != '_' && c != '+' && c != '~' && c != '-' {
			return false
		}

		if c == '-' && i+1 < len(s) && isDigit(s[i+1]) {
			return false
		}
	}

	return true
}

// validArch reports whether s is an architecture: ^[a-z0-9_]{1,32}$.
func validArch(s string) bool {
	if s == "" || len(s) > maxArchLen {
		return false
	}

	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') && !isDigit(c) && c != '_' {
			return false
		}
	}

	return true
}

// ValidSegment reports whether s may be used as a branch, repository or
// architecture path segment: ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$. Because the
// first byte must be alphanumeric, "." and ".." never pass.
func ValidSegment(s string) bool {
	if s == "" || len(s) > maxSegmentLen || !isAlnum(s[0]) {
		return false
	}

	for i := 1; i < len(s); i++ {
		c := s[i]
		if !isAlnum(c) && c != '.' && c != '_' && c != '-' {
			return false
		}
	}

	return true
}

// FileName returns the name the package is served under: name-version.apk.
func (p *Package) FileName() string {
	return p.Name + "-" + p.Version + fileExt
}

// ParseFileName is the inverse of [Package.FileName]. The version starts at the
// last '-' that is followed by a digit and whose prefix is a valid package
// name; both halves must be valid. It returns the name, the version and
// whether s is a valid package file name. For every valid name and version,
// ParseFileName(name + "-" + version + ".apk") returns them again.
func ParseFileName(s string) (string, string, bool) {
	base, found := strings.CutSuffix(s, fileExt)
	if !found {
		return "", "", false
	}

	for i := len(base) - 2; i >= 1; i-- {
		if base[i] != '-' || !isDigit(base[i+1]) || !validName(base[:i]) {
			continue
		}

		if !validVersion(base[i+1:]) {
			return "", "", false
		}

		return base[:i], base[i+1:], true
	}

	return "", "", false
}
