// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var ErrInvalidSemver = errors.New("invalid semver")

var semverRe = regexp.MustCompile(
	`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
		`(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?` +
		`(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`,
)

func ValidateSemver(v string) error {
	if v == "" {
		return fmt.Errorf("%w: must not be empty", ErrInvalidSemver)
	}

	if !semverRe.MatchString(v) {
		return fmt.Errorf("%w: %q does not match semver 2.0.0", ErrInvalidSemver, v)
	}

	return nil
}

// ComparePrerelease orders two semver prerelease identifiers (semver 2.0.0,
// section 11): numeric identifiers numerically and below alphanumeric ones,
// alphanumeric ones lexically in ASCII order.
func ComparePrerelease(a, b string) int {
	an, aErr := strconv.ParseUint(a, 10, 64)
	bn, bErr := strconv.ParseUint(b, 10, 64)

	switch {
	case aErr == nil && bErr == nil:
		return cmp.Compare(an, bn)
	case aErr == nil:
		return -1
	case bErr == nil:
		return 1
	}

	return strings.Compare(a, b)
}
