// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"strconv"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// gateStampBucket is the granularity of gateStamp: a version that ages past the
// threshold shows up in a gated document at most this late.
const gateStampBucket = 5 * time.Minute

// gateStamp is the part of a rendered document's validator that stands for the
// minimum-age gate: the threshold and the bucket of now. What a gated document
// lists changes as versions age, with no change in the cached upstream blob, so
// the validator of the blob alone would revalidate a stale rendering as fresh.
// "" when the gate is off: the validator stays what it was.
func gateStamp(repo *models.Repository, now time.Time) string {
	if minAge(repo) == 0 {
		return ""
	}

	return "h" + strconv.Itoa(repo.MinAgeHours) + "@" + strconv.FormatInt(now.Unix()/int64(gateStampBucket/time.Second), 10)
}
