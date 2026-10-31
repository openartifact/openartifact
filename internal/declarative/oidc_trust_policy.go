// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

const (
	// oidcPolicyRank orders trust policies after the service accounts they
	// exchange tokens for.
	oidcPolicyRank = 55

	oidcPoliciesPath = "/api/v1/oidc-trust-policies"

	// Limits of the API: lifetime of an exchanged token in seconds, size of a
	// policy and of its fields.
	oidcPolicyMinTTL       = 60
	oidcPolicyMaxTTL       = 3600
	oidcMaxConditions      = 16
	oidcMaxPatterns        = 8
	oidcMaxPatternLength   = 256
	oidcMaxAudienceLength  = 255
	oidcMaxIssuerLength    = 512
	oidcPolicyTTLField     = "token_ttl_seconds"
	oidcPolicyAccountField = "service_account"
)

var (
	oidcPolicyNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
	oidcClaimNameRe  = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

	errOIDCPolicyInvalid = errors.New("invalid OIDCTrustPolicy")
)

// oidcPolicyKind manages CI OIDC trust policies: the rules under which an ID
// token of an external issuer (a CI system) is exchanged for a short-lived
// token of a service account. Policy names are unique on the server and are the
// natural key. conditions are ANDed across entries and ORed within one entry's
// patterns. The server refuses a policy for a CI issuer shared by every tenant
// that does not constrain an identity claim; that rule is not checked offline.
type oidcPolicyKind struct{}

// NewOIDCTrustPolicyKind returns the OIDCTrustPolicy kind.
func NewOIDCTrustPolicyKind() Kind { return oidcPolicyKind{} }

func (oidcPolicyKind) Name() string { return KindOIDCTrustPolicy }

func (oidcPolicyKind) Rank() int { return oidcPolicyRank }

func (oidcPolicyKind) MetaKeys() []string { return nil }

func (oidcPolicyKind) Key(o *Object) string { return o.Name }

func (oidcPolicyKind) Order(*Object) int { return 0 }

func (oidcPolicyKind) Schema() *Field {
	return &Field{Type: TObject, Fields: map[string]*Field{
		"issuer":   {Type: TString, Required: true},
		"audience": {Type: TString, Required: true},
		"conditions": {Type: TSet, Required: true, KeyBy: "claim", Elem: &Field{Type: TObject, Fields: map[string]*Field{
			"claim":    {Type: TString, Required: true},
			"patterns": {Type: TSet, Required: true, Elem: &Field{Type: TString}},
		}}},
		oidcPolicyAccountField: {Type: TString, Required: true},
		oidcPolicyTTLField:     {Type: TInt, HasMin: true, Min: oidcPolicyMinTTL},
		"enabled":              {Type: TBool},
	}}
}

func (oidcPolicyKind) Refs(o *Object) []Ref {
	return []Ref{{Kind: KindServiceAccount, Key: Str(o.Spec, oidcPolicyAccountField), Field: oidcPolicyAccountField}}
}

// Validate mirrors the server rules that need no lookup. The lower bound of
// the token lifetime is part of the schema.
func (oidcPolicyKind) Validate(o *Object) []error {
	var msgs []string

	if !oidcPolicyNameRe.MatchString(o.Name) {
		msgs = append(msgs, "metadata.name must be 1-64 letters, digits, dot, dash or underscore, starting with a letter or digit")
	}

	if msg := oidcCheckIssuer(Str(o.Spec, "issuer")); msg != "" {
		msgs = append(msgs, "issuer "+msg)
	}

	if a := Str(o.Spec, "audience"); a == "" || len(a) > oidcMaxAudienceLength || strings.TrimSpace(a) != a {
		msgs = append(msgs, fmt.Sprintf("audience must be 1-%d characters without surrounding spaces", oidcMaxAudienceLength))
	}

	if ttl, ok := o.Spec[oidcPolicyTTLField].(float64); ok && ttl > oidcPolicyMaxTTL {
		msgs = append(msgs, fmt.Sprintf("%s must be between %d and %d", oidcPolicyTTLField, oidcPolicyMinTTL, oidcPolicyMaxTTL))
	}

	msgs = append(msgs, oidcCheckConditions(specEntries(o.Spec, "conditions"))...)

	errs := make([]error, len(msgs))
	for i, m := range msgs {
		errs[i] = invalidf(errOIDCPolicyInvalid, "%s", m)
	}

	return errs
}

// oidcCheckIssuer requires a URL naming the issuer the way its tokens do: http
// or https, a host, no credentials, query or fragment. It returns "" when the
// issuer is acceptable.
func oidcCheckIssuer(issuer string) string {
	u, err := url.Parse(issuer)

	switch {
	case err != nil || len(issuer) > oidcMaxIssuerLength || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Opaque != "":
		return "must be a URL such as https://token.actions.githubusercontent.com"
	case u.Scheme != "https" && u.Scheme != "http":
		return "must be an https URL"
	}

	return ""
}

func oidcCheckConditions(conditions []map[string]any) []string {
	var msgs []string

	if len(conditions) == 0 || len(conditions) > oidcMaxConditions {
		msgs = append(msgs, fmt.Sprintf(
			"conditions: between 1 and %d are required, a policy must say which tokens it trusts", oidcMaxConditions))
	}

	seen := make(map[string]bool, len(conditions))

	for i, c := range conditions {
		claim := Str(c, "claim")

		if !oidcClaimNameRe.MatchString(claim) {
			msgs = append(msgs, fmt.Sprintf("conditions[%d].claim must be a claim name of up to 64 letters, digits or _ . : -", i))
		}

		if seen[claim] {
			msgs = append(msgs, fmt.Sprintf("conditions[%d]: claim %q is listed twice, list its patterns in one condition", i, claim))
		}

		seen[claim] = true

		msgs = append(msgs, oidcCheckPatterns(i, StrList(c, "patterns"))...)
	}

	return msgs
}

func oidcCheckPatterns(index int, patterns []string) []string {
	var msgs []string

	if len(patterns) == 0 || len(patterns) > oidcMaxPatterns {
		msgs = append(msgs, fmt.Sprintf("conditions[%d].patterns: between 1 and %d are required", index, oidcMaxPatterns))
	}

	for _, p := range patterns {
		switch {
		case len(p) > oidcMaxPatternLength:
			msgs = append(msgs, fmt.Sprintf("conditions[%d].patterns: a pattern is at most %d characters", index, oidcMaxPatternLength))
		case strings.Trim(p, "*?") == "":
			msgs = append(msgs, fmt.Sprintf(
				"conditions[%d].patterns: a pattern must contain at least one literal character, not only wildcards", index))
		}
	}

	return msgs
}

// oidcConditionResponse is one claim condition of a policy.
type oidcConditionResponse struct {
	Claim    string   `json:"claim"`
	Patterns []string `json:"patterns"`
}

// oidcPolicyResponse is one policy of GET /api/v1/oidc-trust-policies.
type oidcPolicyResponse struct {
	ID              string                  `json:"id"`
	Name            string                  `json:"name"`
	Issuer          string                  `json:"issuer"`
	Audience        string                  `json:"audience"`
	Conditions      []oidcConditionResponse `json:"conditions"`
	ServiceAccount  string                  `json:"service_account"`
	TokenTTLSeconds int                     `json:"token_ttl_seconds"`
	Enabled         bool                    `json:"enabled"`
}

func (p *oidcPolicyResponse) live() *Live {
	conditions := append([]oidcConditionResponse(nil), p.Conditions...)
	sort.Slice(conditions, func(i, j int) bool { return conditions[i].Claim < conditions[j].Claim })

	list := make([]any, len(conditions))
	for i, c := range conditions {
		list[i] = map[string]any{"claim": c.Claim, "patterns": Canon(SortedStrings(c.Patterns))}
	}

	return &Live{
		Kind: KindOIDCTrustPolicy,
		Key:  p.Name,
		ID:   p.ID,
		Spec: map[string]any{
			"issuer":               p.Issuer,
			"audience":             p.Audience,
			"conditions":           list,
			oidcPolicyAccountField: p.ServiceAccount,
			oidcPolicyTTLField:     float64(p.TokenTTLSeconds),
			"enabled":              p.Enabled,
		},
	}
}

func (oidcPolicyKind) List(ctx context.Context, env *Env) ([]*Live, error) {
	var resp struct {
		Data []oidcPolicyResponse `json:"data"`
	}

	if err := env.Client.Get(ctx, oidcPoliciesPath, &resp); err != nil {
		return nil, fmt.Errorf("list OIDC trust policies: %w", err)
	}

	out := make([]*Live, len(resp.Data))
	for i := range resp.Data {
		out[i] = resp.Data[i].live()
	}

	return out, nil
}

func (oidcPolicyKind) Create(ctx context.Context, env *Env, o *Object) (string, error) {
	accountID, err := env.ServiceAccountID(ctx, Str(o.Spec, oidcPolicyAccountField))
	if err != nil {
		return "", fmt.Errorf("create OIDC trust policy %q: %w", o.Name, err)
	}

	body := Pick(o.Spec, "issuer", "audience", "conditions", oidcPolicyTTLField, "enabled")
	body["name"] = o.Name
	body["service_account_id"] = accountID

	return env.Client.create(ctx, oidcPoliciesPath, body, "OIDC trust policy", o.Name)
}

// Update sends the changed fields; a changed condition resends all of them, as
// the server replaces the list as a whole, and a changed service account is
// sent as its id.
func (oidcPolicyKind) Update(ctx context.Context, env *Env, live *Live, o *Object, changes []Change) (string, error) {
	body := map[string]any{}

	for field := range ChangedTop(changes) {
		if field != oidcPolicyAccountField {
			body[field] = o.Spec[field]
			continue
		}

		id, err := env.ServiceAccountID(ctx, Str(o.Spec, field))
		if err != nil {
			return live.ID, fmt.Errorf("update OIDC trust policy %q: %w", live.Key, err)
		}

		body["service_account_id"] = id
	}

	if len(body) == 0 {
		return live.ID, nil
	}

	return env.Client.update(ctx, http.MethodPatch, oidcPoliciesPath+"/"+PathEscape(live.ID), body, live, "OIDC trust policy")
}

func (oidcPolicyKind) Delete(ctx context.Context, env *Env, live *Live) error {
	return env.Client.remove(ctx, oidcPoliciesPath+"/"+PathEscape(live.ID), "OIDC trust policy", live.Key)
}
