// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package quarantine enforces artifact quarantine on the download path.
//
// An artifact is quarantined when a vulnerability or secret scan result
// anchored on it (artifact_refs.artifact_path within a repository) carries the
// quarantined flag. Scans are keyed by a caller-chosen artifact identifier —
// the package id listed by GET /repositories/{name}/packages, or the HTTP path
// the artifact is fetched from — so format handlers pass every identifier they
// know for the artifact being served (see PathKeys) and Enforce refuses the
// download when any of them is quarantined.
package quarantine

import (
	"context"
	"net/http"
	"strings"
)

// checker is implemented by databases that can answer quarantine lookups.
type checker interface {
	IsArtifactQuarantined(ctx context.Context, repoID string, keys []string) (bool, error)
}

// IsQuarantined reports whether any of keys identifies a quarantined artifact
// of the repository. Databases without quarantine support report false.
func IsQuarantined(ctx context.Context, db any, repoID string, keys ...string) (bool, error) {
	if repoID == "" || len(keys) == 0 {
		return false, nil
	}

	c, ok := db.(checker)
	if !ok {
		return false, nil
	}

	return c.IsArtifactQuarantined(ctx, repoID, keys)
}

// Enforce refuses the request when the artifact is quarantined. It writes a 403
// (or a 500 when the quarantine state cannot be determined: the check fails
// closed) and returns true, in which case the caller MUST return without
// writing anything else.
func Enforce(w http.ResponseWriter, r *http.Request, db any, repoID string, keys ...string) bool {
	blocked, err := IsQuarantined(r.Context(), db, repoID, keys...)
	if err != nil {
		http.Error(w, "unable to verify artifact quarantine state", http.StatusInternalServerError)
		return true
	}

	if !blocked {
		return false
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"artifact is quarantined","code":"ARTIFACT_QUARANTINED"}` + "\n"))

	return true
}

// PathKeys expands artifact paths into the spellings a scan may have been
// recorded under: as given, without a leading slash, and with a leading slash.
// Empty paths are skipped and the result has no duplicates.
func PathKeys(paths ...string) []string {
	keys := make([]string, 0, len(paths)*3)
	seen := make(map[string]struct{}, len(paths)*3)

	add := func(k string) {
		if k == "" || k == "/" {
			return
		}

		if _, dup := seen[k]; dup {
			return
		}

		seen[k] = struct{}{}

		keys = append(keys, k)
	}

	for _, p := range paths {
		trimmed := strings.TrimPrefix(p, "/")

		add(p)
		add(trimmed)
		add("/" + trimmed)
	}

	return keys
}
