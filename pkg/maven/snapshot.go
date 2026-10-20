// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var errNoSnapshotTimestamp = errors.New("filename does not contain a snapshot timestamp")

var snapshotTimestampRe = regexp.MustCompile(`^(.+?)-(\d{8}\.\d{6})-(\d+)([-.].+)$`)

func IsSnapshot(version string) bool {
	return strings.HasSuffix(version, "-SNAPSHOT")
}

func ParseSnapshotTimestamp(filename string) (string, int, string, error) {
	m := snapshotTimestampRe.FindStringSubmatch(filename)
	if m == nil {
		return "", 0, "", fmt.Errorf("%w: %q", errNoSnapshotTimestamp, filename)
	}

	buildNumber, err := strconv.Atoi(m[3])
	if err != nil {
		return "", 0, "", fmt.Errorf("invalid build number: %w", err)
	}

	return m[2], buildNumber, m[1] + m[4], nil
}
