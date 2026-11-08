// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/netutil"
)

// Limits of a CI OIDC trust policy.
const (
	// OIDCPolicyMinTTL and OIDCPolicyMaxTTL bound the lifetime of an exchanged
	// token in seconds; OIDCPolicyDefaultTTL applies when a policy names none.
	OIDCPolicyMinTTL     = 60
	OIDCPolicyMaxTTL     = 3600
	OIDCPolicyDefaultTTL = 900

	maxPolicyConditions = 16
	maxConditionValues  = 8
	maxPatternLength    = 256
	maxAudienceLength   = 255
	maxIssuerLength     = 512
)

var (
	policyNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
	claimNameRe  = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
)

// ValidClaimPath reports whether s names a claim: up to 64 letters, digits or
// _ . : - where a dot joins the names of nested claims (kubernetes.io.namespace),
// so it neither starts nor ends with a dot and has no empty segment.
func ValidClaimPath(s string) bool {
	return claimNameRe.MatchString(s) && !strings.HasPrefix(s, ".") && !strings.HasSuffix(s, ".") && !strings.Contains(s, "..")
}

// scopingClaims lists, per shared issuer host, the claims that tie a token to
// one owner or repository. Every installation of GitHub Actions and every
// project on gitlab.com mints tokens under the same issuer, so a policy for
// such an issuer that does not constrain one of these claims would trust every
// customer of the CI system.
//
//nolint:gochecknoglobals // immutable registry
var scopingClaims = map[string][]string{
	"token.actions.githubusercontent.com": {"sub", "repository", "repository_id", "repository_owner", "repository_owner_id"},
	"gitlab.com":                          {"sub", "project_path", "project_id", "namespace_path", "namespace_id"},
}

// policyError is a validation failure of a trust policy; its message is written
// for the administrator who submitted it.
type policyError string

func (e policyError) Error() string { return string(e) }

func policyErrorf(format string, args ...any) error {
	return policyError(fmt.Sprintf(format, args...))
}

// ValidateOIDCPolicy checks the fields of a trust policy an administrator
// writes (everything but the service account, which the caller resolves) and
// normalizes it: an unset TTL becomes OIDCPolicyDefaultTTL. The messages are
// meant for the administrator.
func ValidateOIDCPolicy(p *models.OIDCTrustPolicy) error {
	if p.TokenTTLSeconds == 0 {
		p.TokenTTLSeconds = OIDCPolicyDefaultTTL
	}

	if !policyNameRe.MatchString(p.Name) {
		return policyErrorf("name must be 1-64 letters, digits, dot, dash or underscore, starting with a letter or digit")
	}

	if err := validateIssuer(p.Issuer); err != nil {
		return err
	}

	if p.Audience == "" || len(p.Audience) > maxAudienceLength || strings.TrimSpace(p.Audience) != p.Audience {
		return policyErrorf("audience is required (at most 255 characters, no surrounding spaces)")
	}

	if p.TokenTTLSeconds < OIDCPolicyMinTTL || p.TokenTTLSeconds > OIDCPolicyMaxTTL {
		return policyErrorf("token_ttl_seconds must be between %d and %d", OIDCPolicyMinTTL, OIDCPolicyMaxTTL)
	}

	if err := validateDiscovery(p); err != nil {
		return err
	}

	return validateConditions(p.Issuer, p.Conditions)
}

// validateDiscovery checks the discovery mode and its settings and normalizes
// them: an unset mode becomes anonymous, a static key set is stored compact. A
// key set belongs to the static mode alone, which fetches nothing and so has no
// use for private-address access.
func validateDiscovery(p *models.OIDCTrustPolicy) error {
	p.Discovery.Mode = p.Discovery.EffectiveMode()

	switch p.Discovery.Mode {
	case models.OIDCDiscoveryAnonymous, models.OIDCDiscoveryServiceAccount:
		if len(p.Discovery.JWKS) > 0 {
			return policyErrorf("discovery.jwks is only for the static discovery mode")
		}
	case models.OIDCDiscoveryStatic:
		if _, err := parseStaticJWKS(p.Discovery.JWKS); err != nil {
			return err
		}

		var compact bytes.Buffer
		if err := json.Compact(&compact, p.Discovery.JWKS); err != nil {
			return policyErrorf("discovery.jwks must be valid JSON")
		}

		p.Discovery.JWKS = compact.Bytes()

		if p.AllowPrivateIssuer {
			return policyErrorf("allow_private_issuer has no effect with static discovery, which contacts nobody")
		}
	default:
		return policyErrorf("discovery.mode must be %s, %s or %s",
			models.OIDCDiscoveryAnonymous, models.OIDCDiscoveryStatic, models.OIDCDiscoveryServiceAccount)
	}

	return nil
}

// validateIssuer requires a plain https URL (http only for a loopback host, as
// for SSO providers) naming the issuer exactly as its tokens do: no query, no
// fragment, no credentials. The SSRF check of the host is the caller's.
func validateIssuer(issuer string) error {
	u, err := url.Parse(issuer)
	if err != nil || len(issuer) > maxIssuerLength || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		u.Opaque != "" {
		return policyErrorf("issuer must be a URL such as https://token.actions.githubusercontent.com")
	}

	if !netutil.IsHTTPSOrLoopbackURL(issuer) {
		return policyErrorf("issuer must be an https URL (http is only allowed for a loopback host)")
	}

	return nil
}

func validateConditions(issuer string, conditions []models.OIDCCondition) error {
	if len(conditions) == 0 || len(conditions) > maxPolicyConditions {
		return policyErrorf("conditions: between 1 and %d are required, a policy must say which tokens it trusts", maxPolicyConditions)
	}

	seen := make(map[string]bool, len(conditions))

	for i, c := range conditions {
		if !ValidClaimPath(c.Claim) {
			return policyErrorf(
				"conditions[%d].claim must be a claim name or a dotted path to a nested claim, of up to 64 letters, digits or _ . : -", i)
		}

		if seen[c.Claim] {
			return policyErrorf("conditions[%d]: claim %q is listed twice, list its patterns in one condition", i, c.Claim)
		}

		seen[c.Claim] = true

		if len(c.Patterns) == 0 || len(c.Patterns) > maxConditionValues {
			return policyErrorf("conditions[%d].patterns: between 1 and %d are required", i, maxConditionValues)
		}

		for _, pat := range c.Patterns {
			if err := validatePattern(pat); err != nil {
				return fmt.Errorf("conditions[%d].patterns: %w", i, err)
			}
		}
	}

	return requireScopingCondition(issuer, conditions)
}

// validatePattern rejects patterns that match everything or nothing useful.
func validatePattern(pat string) error {
	if len(pat) > maxPatternLength {
		return policyErrorf("a pattern is at most 256 characters")
	}

	if strings.Trim(pat, "*?") == "" {
		return policyErrorf("a pattern must contain at least one literal character, not only wildcards")
	}

	return nil
}

// requireScopingCondition enforces, for the shared CI issuers, a condition on
// a claim that names the owner or repository, whose patterns do not begin with
// a wildcard.
func requireScopingCondition(issuer string, conditions []models.OIDCCondition) error {
	u, err := url.Parse(issuer)
	if err != nil {
		return nil //nolint:nilerr // validateIssuer reported it
	}

	claims, shared := scopingClaims[strings.ToLower(u.Hostname())]
	if !shared {
		return nil
	}

	for _, c := range conditions {
		if !slices.Contains(claims, c.Claim) {
			continue
		}

		if allLiteralPrefix(c.Patterns) {
			return nil
		}
	}

	return policyErrorf(
		"issuer %s is shared by every customer of the CI system: add a condition on one of %s "+
			"whose patterns start with a literal character, such as an organization or project path",
		u.Hostname(), strings.Join(claims, ", "))
}

func allLiteralPrefix(patterns []string) bool {
	return !slices.ContainsFunc(patterns, func(p string) bool {
		return strings.HasPrefix(p, "*") || strings.HasPrefix(p, "?")
	})
}
