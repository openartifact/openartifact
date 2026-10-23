// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package licensepolicy applies a repository's license policies to the licenses
// an artifact declares.
//
// Two paths use it:
//
//   - Upload: a format's upload handler calls Enforce with the licenses read
//     from the package metadata. A policy with action "block" that the licenses
//     violate rejects the upload with 403 and code LICENSE_BLOCKED before
//     anything is stored. Action "warn" logs the violation and lets the upload
//     through; "allow" is informational only.
//   - After upload: license detection that runs once the artifact is stored (the
//     license scan endpoint, a change of the artifact's license properties) calls
//     Record, which stores the scan result and quarantines the artifact when a
//     blocking policy is violated. Downloads of a quarantined artifact answer 403
//     (see package quarantine) until an admin approves it.
//
// An artifact with no detectable license never violates a policy: the policies
// match licenses, they do not require one to be declared.
package licensepolicy

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/license"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// CodeBlocked is the error code of an upload rejected by a blocking policy.
	CodeBlocked = "LICENSE_BLOCKED"

	// CodePolicyUnavailable is the error code returned when the policies cannot
	// be read; the upload is refused rather than admitted unchecked.
	CodePolicyUnavailable = "LICENSE_POLICY_UNAVAILABLE"

	// CodeMetadataUnsafe is the error code returned when the package file's
	// own metadata cannot be read safely (oversized or ambiguous), so its
	// licenses cannot be checked.
	CodeMetadataUnsafe = "LICENSE_METADATA_UNSAFE"

	// CodeMetadataUnreadable is the error code returned when a repository has
	// a blocking policy and the licenses of the upload cannot be derived from
	// the package file (a file type or archive that cannot be read), so
	// nothing but the client's own claims would decide the policy.
	CodeMetadataUnreadable = "LICENSE_METADATA_UNREADABLE"

	// ActionBlock is the policy action that rejects or quarantines.
	ActionBlock = "block"

	// ActionWarn is the policy action that only logs / reports.
	ActionWarn = "warn"
)

// PolicyLister reads the license policies that apply to a repository
// (its own and the global ones). database.Database implements it.
type PolicyLister interface {
	ListLicensePolicies(ctx context.Context, repoID string) ([]*models.LicensePolicy, error)
}

// Store is what Record needs from the database.
type Store interface {
	PolicyLister
	GetLicenseScanResult(ctx context.Context, repoID, artifactID string) (*models.LicenseScanResult, error)
	CreateLicenseScanResult(ctx context.Context, result *models.LicenseScanResult) error
}

// Violation is one policy violated by an artifact's licenses.
type Violation struct {
	PolicyID   string `json:"policy_id"`
	PolicyName string `json:"policy_name"`
	Action     string `json:"action"`
	Violation  string `json:"violation"`
}

// Blocking reports whether the violation comes from a "block" policy.
func (v Violation) Blocking() bool { return v.Action == ActionBlock }

// Evaluate checks the licenses against every enabled policy of the repository.
// Licenses are normalized (aliases, SPDX spelling) before matching. No licenses
// means no violations and no database access.
func Evaluate(ctx context.Context, db PolicyLister, log *logger.Logger, repoID string, licenses []string) ([]Violation, error) {
	licenses = normalize(log, licenses)
	if len(licenses) == 0 {
		return nil, nil
	}

	policies, err := db.ListLicensePolicies(ctx, repoID)
	if err != nil {
		return nil, err //nolint:wrapcheck // callers log it and answer with a fixed message
	}

	detector := license.NewDetector(log)

	var violations []Violation

	for _, policy := range policies {
		if policy == nil {
			continue
		}

		compliant, msgs := detector.EvaluatePolicy(licenses, policy)
		if compliant {
			continue
		}

		for _, msg := range msgs {
			violations = append(violations, Violation{
				PolicyID:   policy.ID,
				PolicyName: policy.Name,
				Action:     policy.Action,
				Violation:  msg,
			})
		}
	}

	return violations, nil
}

// Blocked returns the violations that come from "block" policies.
func Blocked(violations []Violation) []Violation {
	var blocked []Violation

	for _, v := range violations {
		if v.Blocking() {
			blocked = append(blocked, v)
		}
	}

	return blocked
}

// normalize trims the licenses, resolves aliases and drops empties and
// duplicates.
func normalize(log *logger.Logger, licenses []string) []string {
	detector := license.NewDetector(log)
	out := make([]string, 0, len(licenses))

	for _, l := range licenses {
		n := detector.NormalizeLicense(strings.TrimSpace(l))
		if n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}

	return out
}

type errorBody struct {
	Error    string      `json:"error"`
	Code     string      `json:"code"`
	Licenses []string    `json:"licenses,omitempty"`
	Policies []Violation `json:"policies,omitempty"`
}

func writeBody(w http.ResponseWriter, log *logger.Logger, status int, body errorBody) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Debugw("license policy: writing the error response failed", "error", err)
	}
}

// RejectUnsafe answers 422 LICENSE_METADATA_UNSAFE for an upload whose package
// file is a decompression bomb or holds ambiguous metadata (licensemeta.Unsafe).
// The caller MUST return after calling it.
func RejectUnsafe(w http.ResponseWriter, log *logger.Logger) {
	writeBody(w, log, http.StatusUnprocessableEntity, errorBody{
		Error: "upload rejected: the package metadata is too large or ambiguous to be read safely",
		Code:  CodeMetadataUnsafe,
	})
}

// HasBlocking reports whether the repository (or a global policy) has an
// enabled policy with action "block".
func HasBlocking(ctx context.Context, db PolicyLister, repoID string) (bool, error) {
	policies, err := db.ListLicensePolicies(ctx, repoID)
	if err != nil {
		return false, err //nolint:wrapcheck // callers log it and answer with a fixed message
	}

	for _, policy := range policies {
		if policy != nil && policy.Enabled && policy.Action == ActionBlock {
			return true, nil
		}
	}

	return false, nil
}

// RefuseUnderivable is called by an upload handler when the licenses of the
// package could not be derived from the package file (licensemeta reported it
// unsupported, without metadata or unreadable). Without a blocking policy it
// returns false and the handler falls back to what the client declared. With
// one, trusting the client's fields would let a file that tools read
// differently, or that this server cannot read, bypass the policy, so the
// upload is refused (true, response written):
//
//   - 422 LICENSE_METADATA_UNREADABLE;
//   - 500 LICENSE_POLICY_UNAVAILABLE when the policies cannot be read.
func RefuseUnderivable(w http.ResponseWriter, r *http.Request, db PolicyLister, log *logger.Logger, repoID string) bool {
	blocking, err := HasBlocking(r.Context(), db, repoID)
	if err != nil {
		log.Errorw("license policy: cannot read policies", "repository_id", repoID, "error", err)
		writeBody(w, log, http.StatusInternalServerError, errorBody{
			Error: "unable to verify the license policy; try again later",
			Code:  CodePolicyUnavailable,
		})

		return true
	}

	if !blocking {
		return false
	}

	log.Warnw("license policy: upload rejected, licenses cannot be derived from the package file",
		"repository_id", repoID)

	writeBody(w, log, http.StatusUnprocessableEntity, errorBody{
		Error: "upload rejected: the repository has a blocking license policy and the package's license cannot be read from the file",
		Code:  CodeMetadataUnreadable,
	})

	return true
}

// Enforce applies the repository's license policies to an upload whose
// metadata declares licenses. It returns true when the upload must not
// continue, after writing the response:
//
//   - 403 LICENSE_BLOCKED when a "block" policy is violated;
//   - 500 LICENSE_POLICY_UNAVAILABLE when the policies cannot be read (fail
//     closed, like the quarantine check).
//
// Violations of "warn" policies are logged and do not stop the upload. Call it
// after the metadata is parsed and validated and before anything is stored; the
// caller MUST return without writing when it returns true.
func Enforce(w http.ResponseWriter, r *http.Request, db PolicyLister, log *logger.Logger, repoID string, licenses ...string) bool {
	licenses = normalize(log, licenses)
	if len(licenses) == 0 {
		return false
	}

	violations, err := Evaluate(r.Context(), db, log, repoID, licenses)
	if err != nil {
		log.Errorw("license policy: cannot read policies", "repository_id", repoID, "error", err)
		writeBody(w, log, http.StatusInternalServerError, errorBody{
			Error: "unable to verify the license policy; try again later",
			Code:  CodePolicyUnavailable,
		})

		return true
	}

	for _, v := range violations {
		if v.Action == ActionWarn {
			log.Warnw("license policy warning: upload violates a warn policy",
				"repository_id", repoID, "policy_id", v.PolicyID, "policy", v.PolicyName,
				"violation", v.Violation, "licenses", licenses)
		}
	}

	blocked := Blocked(violations)
	if len(blocked) == 0 {
		return false
	}

	log.Warnw("license policy: upload rejected",
		"repository_id", repoID, "licenses", licenses, "violations", blocked)

	writeBody(w, log, http.StatusForbidden, errorBody{
		Error:    "upload rejected: the package license is blocked by a license policy (" + blocked[0].Violation + ")",
		Code:     CodeBlocked,
		Licenses: licenses,
		Policies: blocked,
	})

	return true
}

// Record stores the license scan result of an artifact detected after upload and
// applies the quarantine rule:
//
//   - a violated "block" policy quarantines the artifact, unless an admin
//     approved it for exactly these licenses;
//   - a quarantined artifact stays quarantined until an admin approves it, even
//     when a re-scan finds other licenses or the policy changed meanwhile (the
//     license properties that feed the scan are editable, so a re-scan must not
//     be a way out of quarantine);
//   - an approval holds only for the licenses it was given for.
//
// source is models.LicenseSourceArtifact when the licenses include ones read
// from the package file and models.LicenseSourceClientDeclared when they only
// come from what a client declared; it is stored with the result.
//
// It returns the stored result and all violations (blocking or not).
func Record(ctx context.Context, db Store, log *logger.Logger, repoID, artifactID string, licenses []string, source string) (*models.LicenseScanResult, []Violation, error) {
	licenses = normalize(log, licenses)

	violations, err := Evaluate(ctx, db, log, repoID, licenses)
	if err != nil {
		return nil, nil, err
	}

	existing, err := db.GetLicenseScanResult(ctx, repoID, artifactID)
	if err != nil {
		return nil, nil, err //nolint:wrapcheck // callers log it and answer with a fixed message
	}

	same := existing != nil && sameLicenses(existing.Licenses, licenses)
	approved := same && existing.QuarantineApproved
	prior := existing != nil && existing.Quarantined
	quarantined := !approved && (prior || len(Blocked(violations)) > 0)

	now := time.Now()
	result := &models.LicenseScanResult{
		RepositoryID:       repoID,
		ArtifactID:         artifactID,
		Licenses:           licenses,
		UnresolvedLicenses: license.NewDetector(log).Unresolved(licenses),
		LicenseSource:      source,
		SPDXExpressions:    []string{},
		ScanStatus:         "completed",
		Quarantined:        quarantined,
		QuarantineApproved: approved,
		ScannedAt:          &now,
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	if err := db.CreateLicenseScanResult(ctx, result); err != nil {
		return nil, nil, err //nolint:wrapcheck // callers log it and answer with a fixed message
	}

	if quarantined {
		log.Warnw("license policy: artifact quarantined",
			"repository_id", repoID, "artifact_id", artifactID, "licenses", licenses)
	}

	return result, violations, nil
}

// sameLicenses reports whether two license lists hold the same licenses,
// ignoring order and case.
func sameLicenses(a, b []string) bool {
	set := func(list []string) []string {
		out := make([]string, 0, len(list))
		for _, l := range list {
			out = append(out, strings.ToLower(strings.TrimSpace(l)))
		}

		slices.Sort(out)

		return slices.Compact(out)
	}

	return slices.Equal(set(a), set(b))
}
