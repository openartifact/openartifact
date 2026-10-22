// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"errors"
	"strings"
)

var errInvalidPath = errors.New("invalid maven repository path: need at least 3 segments")

func RepositoryPath(c Coordinate) string {
	groupPath := strings.ReplaceAll(c.GroupID, ".", "/")

	maybeClassifier := ""
	if c.Classifier != "" {
		maybeClassifier = "-" + c.Classifier
	}

	return groupPath + "/" + c.ArtifactID + "/" + c.Version + "/" +
		c.ArtifactID + "-" + c.Version + maybeClassifier + "." + c.Extension
}

// CleanSegments reports whether every segment of a repository path is a real
// name: no empty, "." or ".." segment (which would alias coordinates, e.g.
// "a//b" and "a/b" deriving the same group id).
func CleanSegments(path string) bool {
	for seg := range strings.SplitSeq(path, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}

	return true
}

func ParseRepositoryPath(path string) (string, string, string, string, error) {
	parts := strings.Split(path, "/")
	if len(parts) < 3 {
		return "", "", "", "", errInvalidPath
	}

	filename := parts[len(parts)-1]
	version := parts[len(parts)-2]
	artifactID := parts[len(parts)-3]
	groupPath := strings.Join(parts[:len(parts)-3], "/")

	return groupPath, artifactID, version, filename, nil
}
