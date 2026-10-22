// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pypisimple models the PyPI "simple repository API": the index of
// projects and the per-project file lists of PEP 503 (HTML) and PEP 691 (JSON),
// including the PEP 592 yanked flag, PEP 658/714 core metadata hints and PEP 700
// size/upload-time. Proxy and group repositories parse upstream and member
// pages into this model, rewrite the file links, and render the result in
// whatever format the client negotiated.
package pypisimple

import (
	"regexp"
	"strings"
)

// maxNameLen bounds a project name accepted from a client.
const maxNameLen = 256

var nameSeparators = regexp.MustCompile(`[-_.]+`)

// NormalizeName is the PEP 503 normalization: runs of "-", "_" and "." become
// a single "-" and the name is lowercased.
func NormalizeName(name string) string {
	return strings.ToLower(nameSeparators.ReplaceAllString(name, "-"))
}

// ValidName reports whether a normalized project name is safe to use as a path
// segment of an upstream URL: ASCII lowercase letters, digits and inner "-".
func ValidName(norm string) bool {
	if norm == "" || len(norm) > maxNameLen || norm[0] == '-' || norm[len(norm)-1] == '-' {
		return false
	}

	for i := range len(norm) {
		c := norm[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}

	return true
}

// File is one distribution file of a project.
type File struct {
	Filename string
	// URL is the file's location as published: absolute, or relative to the
	// page it came from. The fragment, if any, is dropped (hashes live in Hashes).
	URL string
	// Hashes maps a hashlib algorithm name to its lowercase hex digest.
	Hashes         map[string]string
	RequiresPython string
	// CoreMetadata reports that "<file URL>.metadata" serves the file's core
	// metadata (PEP 658); CoreMetadataHashes are its digests, when published.
	CoreMetadata       bool
	CoreMetadataHashes map[string]string
	// Yanked marks a withdrawn file; YankedReason is the optional explanation.
	Yanked       bool
	YankedReason string
	// Size is the file size in bytes, 0 when unknown.
	Size       int64
	UploadTime string
}

// SHA256 returns the file's published sha256 digest, "" when it has none.
func (f *File) SHA256() string { return f.Hashes["sha256"] }

// MetadataSHA256 returns the published sha256 digest of the file's core
// metadata, "" when none was published.
func (f *File) MetadataSHA256() string { return f.CoreMetadataHashes["sha256"] }

// Merge returns the union of the file lists, keyed by filename; when several
// lists contain a filename the earliest list wins. Order is preserved: the
// files of the first list, then the new files of the second, and so on.
func Merge(lists ...[]File) []File {
	seen := make(map[string]struct{})

	var out []File

	for _, files := range lists {
		for i := range files {
			if _, dup := seen[files[i].Filename]; dup {
				continue
			}

			seen[files[i].Filename] = struct{}{}

			out = append(out, files[i])
		}
	}

	return out
}

// MergeNames returns the union of the project name lists in first-seen order,
// without duplicates.
func MergeNames(lists ...[]string) []string {
	seen := make(map[string]struct{})

	var out []string

	for _, names := range lists {
		for _, n := range names {
			if _, dup := seen[n]; dup {
				continue
			}

			seen[n] = struct{}{}

			out = append(out, n)
		}
	}

	return out
}
