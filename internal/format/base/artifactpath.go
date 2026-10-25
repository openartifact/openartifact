// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"slices"
	"strings"
)

// ArtifactPath is the address of one file in an Ivy or sbt repository.
type ArtifactPath struct {
	// Group is the organisation / group id, dotted ("com.example") whichever way
	// the client wrote it.
	Group    string
	Module   string
	Version  string
	Filename string // may hold a subdirectory in the Ivy layout ("jars/lib.jar")
}

// SplitArtifactPath parses the wildcard part of an Ivy or sbt URL, which is one
// of two layouts:
//
//   - Maven layout, `com/example/lib/1.0/lib-1.0.jar` (what sbt publishes by
//     default and what `m2compatible` Ivy resolvers use): the last segment is the
//     file, the two before it the version and the module, everything earlier the
//     group, joined with dots. Recognised when the file name starts with
//     `<module>-<version>` (a SNAPSHOT version dir also holds timestamped files
//     `<module>-<base version>-<timestamp>-<n>.jar`).
//   - Ivy layout, `<org>/<module>/<revision>/<file>`, where the file name may
//     include subdirectories (`jars/lib-1.0.jar`) and the org is one segment.
//
// The two agree for every four-segment path. It returns the current reading, the
// legacy reading and whether the path is valid. The legacy reading is how
// releases before multi-segment groups understood the path: always the first
// three segments and the rest. Rows published that way live under those
// coordinates, so read and delete fall back to them; it equals the current
// reading when there is nothing to fall back to. The path is invalid when it has
// fewer than four segments or one that ValidArtifactSegments rejects.
func SplitArtifactPath(p string) (ArtifactPath, ArtifactPath, bool) {
	parts := strings.Split(p, "/")
	if len(parts) < 4 || !ValidArtifactSegments(parts) {
		return ArtifactPath{}, ArtifactPath{}, false
	}

	legacy := ArtifactPath{Group: parts[0], Module: parts[1], Version: parts[2], Filename: strings.Join(parts[3:], "/")}
	cur := legacy

	if n := len(parts); n > 4 && isMavenFile(parts[n-3], parts[n-2], parts[n-1]) {
		cur = ArtifactPath{
			Group:    strings.Join(parts[:n-3], "."),
			Module:   parts[n-3],
			Version:  parts[n-2],
			Filename: parts[n-1],
		}
	}

	return cur, legacy, true
}

// ValidArtifactSegments reports whether every path segment is a plain name: not
// empty, not "." or "..", and without a backslash. Such segments would make
// stored coordinates and file names traversal-shaped even though blobs are
// addressed by digest.
func ValidArtifactSegments(parts []string) bool {
	return !slices.ContainsFunc(parts, func(s string) bool {
		return s == "" || s == "." || s == ".." || strings.ContainsRune(s, '\\')
	})
}

// isMavenFile reports whether filename is a file of the Maven version
// directory version of artifact module.
func isMavenFile(module, version, filename string) bool {
	return strings.HasPrefix(filename, module+"-"+strings.TrimSuffix(version, "-SNAPSHOT"))
}
