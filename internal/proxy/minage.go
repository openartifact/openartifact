// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"errors"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// The minimum-age gate.
//
// A proxy repository with MinAgeHours > 0 does not offer a version until the
// upstream has published it for that long: a freshly published malicious
// release is usually found and pulled within hours or days, and the gate keeps
// it away from clients meanwhile. Each format that knows the publish time of a
// version from its upstream metadata applies the gate twice:
//
//   - in the metadata it renders, the versions that are too new are left out
//     (and pointers such as npm's dist-tags move to the newest version left),
//     so resolvers pick an older release on their own;
//   - in the locate hook of a download, a file of a version that is too new
//     answers errTooNew (403), so a client that kept or guessed the file's URL
//     gets no further.
//
// A version whose publish time the upstream does not state passes: the gate
// only holds back what it can date. A file already in the cache is served
// without asking again (it was admitted, or cached before the gate was set).

// errTooNew is returned by a download's locate hook for a file of a version
// younger than the repository's minimum age.
var errTooNew = errors.New("version is younger than the repository's minimum age")

// minAge is repo's minimum age, 0 when the gate is off.
func minAge(repo *models.Repository) time.Duration {
	if repo == nil || repo.MinAgeHours <= 0 {
		return 0
	}

	return time.Duration(repo.MinAgeHours) * time.Hour
}

// tooNew reports whether a version published at published is younger than
// repo's minimum age at now. An unknown (zero) publish time is never too new.
func tooNew(repo *models.Repository, published, now time.Time) bool {
	age := minAge(repo)
	if age == 0 || published.IsZero() {
		return false
	}

	return now.Sub(published) < age
}
