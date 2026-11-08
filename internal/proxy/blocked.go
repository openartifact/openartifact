// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"net/http"

	"github.com/openartifact/openartifact/internal/blockreason"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/quarantine"
)

// Every refusal of a download by a repository policy (the gate's curation and
// minimum age, a scan quarantine, a license policy) is written through
// internal/blockreason: 403, the X-OA-Block-Reason header and the
// BLOCKED_BY_POLICY body, or for the registry API the DENIED error with the same
// facts in its detail.

// quarantineBlock is the refusal of reason for the file stored under key
// (which may name no package) in repo.
func quarantineBlock(repo *models.Repository, key string, reason quarantine.Reason) blockreason.Block {
	pkg, version := keyCoordinates(repo.Format, key)

	return newQuarantineBlock(reason, pkg, version)
}

func newQuarantineBlock(reason quarantine.Reason, pkg, version string) blockreason.Block {
	if reason == quarantine.ReasonLicense {
		return blockreason.Block{
			Policy: blockreason.License, Package: pkg, Version: version,
			Message: "artifact is blocked by the repository's license policy",
		}
	}

	return blockreason.Block{Policy: blockreason.Quarantine, Package: pkg, Version: version, Message: "artifact is quarantined"}
}

// keyCoordinates recovers the package and version a cache key names; both are
// empty when the format's keys do not name them.
func keyCoordinates(format, key string) (string, string) {
	if pkg, version, ok := PackageCoordinates(format, key); ok {
		return pkg, version
	}

	if h, found := hooks.Lookup(format); found && h.Coordinates != nil {
		if pkg, version, ok := h.Coordinates(key); ok {
			return pkg, version
		}
	}

	return "", ""
}

// enforceQuarantine refuses the download of the file stored under key when the
// artifact identified by keys is quarantined or blocked by a license policy. It
// reports whether it answered.
func (s *Service) enforceQuarantine(w http.ResponseWriter, r *http.Request, repo *models.Repository, key string, keys []string) bool {
	return quarantine.EnforceWith(w, r, s.db, repo.ID, func(reason quarantine.Reason) {
		quarantineBlock(repo, key, reason).Write(w)
	}, keys...)
}
