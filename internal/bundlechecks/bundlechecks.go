// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package bundlechecks decides whether the external checks reported against a
// release bundle satisfy the checks a promotion stage requires.
package bundlechecks

import (
	"strconv"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Verdict is the outcome of Evaluate: the required checks that do not let the
// promotion through, by reason. Every list is in the order of the required
// names.
type Verdict struct {
	// Missing were never reported.
	Missing []string
	// Failed last reported failure or error.
	Failed []string
	// Pending last reported pending.
	Pending []string
	// Expired last reported success, but that success has run out.
	Expired []string
}

// Satisfied reports whether every required check is a live success.
func (v Verdict) Satisfied() bool {
	return len(v.Missing) == 0 && len(v.Failed) == 0 && len(v.Pending) == 0 && len(v.Expired) == 0
}

// String is the client message of an unsatisfied verdict, for example
// "missing: conforma; failed: scan".
func (v Verdict) String() string {
	var parts []string

	for _, g := range []struct {
		label string
		names []string
	}{{"missing", v.Missing}, {"failed", v.Failed}, {"pending", v.Pending}, {"expired", v.Expired}} {
		if len(g.names) > 0 {
			parts = append(parts, g.label+": "+strings.Join(g.names, ", "))
		}
	}

	return strings.Join(parts, "; ")
}

// Evaluate judges the reported checks against the required names at now. A
// success counts only while its expiry (when it has one) lies in the future;
// duplicate required names are judged once.
func Evaluate(required []string, reported []*models.BundleCheck, now time.Time) Verdict {
	byName := make(map[string]*models.BundleCheck, len(reported))
	for _, c := range reported {
		byName[c.Name] = c
	}

	var v Verdict

	seen := make(map[string]struct{}, len(required))

	for _, name := range required {
		if _, dup := seen[name]; dup {
			continue
		}

		seen[name] = struct{}{}

		c := byName[name]

		switch {
		case c == nil:
			v.Missing = append(v.Missing, name)
		case c.Status == models.BundleCheckPending:
			v.Pending = append(v.Pending, name)
		case c.Status != models.BundleCheckSuccess:
			v.Failed = append(v.Failed, name)
		case c.ExpiresAt != nil && !c.ExpiresAt.After(now):
			v.Expired = append(v.Expired, name)
		}
	}

	return v
}

const (
	// MaxNameLen bounds a check name (bundle_checks.name is varchar(64)).
	MaxNameLen = 64
	// MaxPerStage bounds the checks one pipeline stage may require.
	MaxPerStage = 20
	// MaxSummaryLen bounds the summary of a report.
	MaxSummaryLen = 2000
	// MaxURLLen bounds the details URL of a report.
	MaxURLLen = 2048
)

// ValidateName returns a client-facing message when name cannot name a check:
// it must be non-empty, trimmed, at most MaxNameLen bytes and free of control
// characters. The empty string means valid.
func ValidateName(name string) string {
	switch {
	case name == "":
		return "check name is required"
	case len(name) > MaxNameLen:
		return "check name must be at most " + strconv.Itoa(MaxNameLen) + " characters"
	case name != strings.TrimSpace(name):
		return "check name must not start or end with whitespace"
	}

	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "check name must not contain control characters"
		}
	}

	return ""
}

// ValidStatus reports whether s is a status a check can report.
func ValidStatus(s models.BundleCheckStatus) bool {
	switch s {
	case models.BundleCheckPending, models.BundleCheckSuccess, models.BundleCheckFailure, models.BundleCheckError:
		return true
	}

	return false
}

// ValidateRequired returns a client-facing message when the required check
// names of one stage are invalid: too many, a bad name or a duplicate. The
// empty string means valid.
func ValidateRequired(names []string) string {
	if len(names) > MaxPerStage {
		return "too many required checks (max " + strconv.Itoa(MaxPerStage) + ")"
	}

	seen := make(map[string]struct{}, len(names))

	for _, n := range names {
		if msg := ValidateName(n); msg != "" {
			return msg
		}

		if _, dup := seen[n]; dup {
			return "duplicate required check: " + n
		}

		seen[n] = struct{}{}
	}

	return ""
}
