// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package policytest provides the license-policy scenarios every upload path is
// tested with: for one declared license, which policy configurations admit the
// upload and which reject it.
//
// A format's upload test runs the same upload once per Case, with the
// repository's database wrapped by Case.DB, and checks the response with
// Case.AssertResponse:
//
//	for _, c := range policytest.Cases("GPL-3.0") {
//		t.Run(c.Name, func(t *testing.T) {
//			rec := upload(t, c.DB(mockDB)) // publish a package declaring GPL-3.0
//			c.AssertResponse(t, rec.Code, rec.Body.Bytes())
//			// c.Want == policytest.Blocked => also assert nothing was stored.
//		})
//	}
package policytest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/licensepolicy"
)

// Outcome is what an upload must do under a Case.
type Outcome int

const (
	// Admitted: the upload is not stopped by the license policy (it may still
	// fail for unrelated reasons; the caller asserts its own success status).
	Admitted Outcome = iota
	// Blocked: 403 with code LICENSE_BLOCKED, nothing stored.
	Blocked
	// Unavailable: 500 with code LICENSE_POLICY_UNAVAILABLE, nothing stored.
	Unavailable
	// Unreadable: 422 with code LICENSE_METADATA_UNREADABLE, nothing stored.
	Unreadable
)

var errStore = errors.New("policytest: policy store unavailable")

// DB overlays license policies on any database.Database.
type DB struct {
	database.Database

	Policies []*models.LicensePolicy
	Err      error
}

// ListLicensePolicies returns the configured policies (or Err).
func (d *DB) ListLicensePolicies(context.Context, string) ([]*models.LicensePolicy, error) {
	return d.Policies, d.Err
}

// Policy returns an enabled global policy with the given action and lists.
func Policy(action string, allowed, denied []string) *models.LicensePolicy {
	return &models.LicensePolicy{
		ID:              "policy-" + action,
		Name:            action + " policy",
		Action:          action,
		AllowedLicenses: allowed,
		DeniedLicenses:  denied,
		Enabled:         true,
	}
}

// Case is one policy configuration and the outcome it must produce.
type Case struct {
	Name     string
	Policies []*models.LicensePolicy
	Err      error
	Want     Outcome
}

// DB wraps inner so that the repository has this case's policies.
func (c Case) DB(inner database.Database) *DB {
	return &DB{Database: inner, Policies: c.Policies, Err: c.Err}
}

// Cases returns the scenarios for an upload declaring license: each policy
// action (allow, warn, block) against a deny list and an allow list that match
// and that do not, a disabled policy, and an unreadable policy store.
func Cases(license string) []Case {
	const other = "LicenseRef-policytest-other"

	disabled := Policy(licensepolicy.ActionBlock, nil, []string{license})
	disabled.Enabled = false

	return []Case{
		{Name: "no policies", Want: Admitted},
		{Name: "allow action denying the license", Want: Admitted,
			Policies: []*models.LicensePolicy{Policy("allow", nil, []string{license})}},
		{Name: "warn action denying the license", Want: Admitted,
			Policies: []*models.LicensePolicy{Policy(licensepolicy.ActionWarn, nil, []string{license})}},
		{Name: "warn action with an allow list excluding the license", Want: Admitted,
			Policies: []*models.LicensePolicy{Policy(licensepolicy.ActionWarn, []string{other}, nil)}},
		{Name: "block action denying the license", Want: Blocked,
			Policies: []*models.LicensePolicy{Policy(licensepolicy.ActionBlock, nil, []string{license})}},
		{Name: "block action with an allow list excluding the license", Want: Blocked,
			Policies: []*models.LicensePolicy{Policy(licensepolicy.ActionBlock, []string{other}, nil)}},
		{Name: "block action with an allow list including the license", Want: Admitted,
			Policies: []*models.LicensePolicy{Policy(licensepolicy.ActionBlock, []string{license}, nil)}},
		{Name: "block action denying another license", Want: Admitted,
			Policies: []*models.LicensePolicy{Policy(licensepolicy.ActionBlock, nil, []string{other})}},
		{Name: "disabled block policy", Want: Admitted,
			Policies: []*models.LicensePolicy{disabled}},
		{Name: "policy store error", Want: Unavailable, Err: errStore},
	}
}

// UnderivableCases are the policy scenarios for an upload whose licenses cannot
// be derived from the package file: only a blocking policy refuses it (422
// LICENSE_METADATA_UNREADABLE, whatever the policy lists), because the
// client's own claims must not decide that policy. Any other scenario admits it
// on the client's fields.
func UnderivableCases() []Case {
	disabled := Policy(licensepolicy.ActionBlock, nil, []string{"GPL-3.0-only"})
	disabled.Enabled = false

	return []Case{
		{Name: "no policies", Want: Admitted},
		{Name: "allow action", Want: Admitted,
			Policies: []*models.LicensePolicy{Policy("allow", nil, []string{"GPL-3.0-only"})}},
		{Name: "warn action", Want: Admitted,
			Policies: []*models.LicensePolicy{Policy(licensepolicy.ActionWarn, nil, []string{"GPL-3.0-only"})}},
		{Name: "disabled block policy", Want: Admitted,
			Policies: []*models.LicensePolicy{disabled}},
		{Name: "block action denying a license", Want: Unreadable,
			Policies: []*models.LicensePolicy{Policy(licensepolicy.ActionBlock, nil, []string{"GPL-3.0-only"})}},
		{Name: "block action with an allow list", Want: Unreadable,
			Policies: []*models.LicensePolicy{Policy(licensepolicy.ActionBlock, []string{"MIT"}, nil)}},
		{Name: "warn and block policies", Want: Unreadable,
			Policies: []*models.LicensePolicy{
				Policy(licensepolicy.ActionWarn, nil, []string{"MIT"}),
				Policy(licensepolicy.ActionBlock, nil, []string{"GPL-3.0-only"}),
			}},
		{Name: "policy store error", Want: Unavailable, Err: errStore},
	}
}

// AssertResponse checks an upload response against the case's outcome. For
// Admitted it only asserts that the policy did not stop the upload; the caller
// asserts the success status of its own format.
func (c Case) AssertResponse(tb testing.TB, status int, body []byte) {
	tb.Helper()

	var parsed struct {
		Code     string `json:"code"`
		Licenses []any  `json:"licenses"`
	}

	_ = json.Unmarshal(body, &parsed) // non-JSON bodies leave Code empty

	switch c.Want {
	case Blocked:
		if status != http.StatusForbidden || parsed.Code != licensepolicy.CodeBlocked {
			tb.Fatalf("want 403 %s, got %d: %s", licensepolicy.CodeBlocked, status, body)
		}

		if len(parsed.Licenses) == 0 {
			tb.Errorf("blocked response must list the licenses: %s", body)
		}
	case Unavailable:
		if status != http.StatusInternalServerError || parsed.Code != licensepolicy.CodePolicyUnavailable {
			tb.Fatalf("want 500 %s, got %d: %s", licensepolicy.CodePolicyUnavailable, status, body)
		}
	case Unreadable:
		if status != http.StatusUnprocessableEntity || parsed.Code != licensepolicy.CodeMetadataUnreadable {
			tb.Fatalf("want 422 %s, got %d: %s", licensepolicy.CodeMetadataUnreadable, status, body)
		}
	case Admitted:
		if strings.HasPrefix(parsed.Code, "LICENSE_") {
			tb.Fatalf("license policy must not stop this upload, got %d: %s", status, body)
		}

		if status >= http.StatusBadRequest {
			tb.Fatalf("upload must succeed, got %d: %s", status, body)
		}
	}
}

// Stopped reports whether the case's outcome rejects the upload, i.e. nothing
// may be stored.
func (c Case) Stopped() bool { return c.Want != Admitted }
