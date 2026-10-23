// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"

	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	// entraTenantPlaceholder is what Microsoft's multi-tenant discovery
	// documents carry instead of a tenant ID in their "issuer".
	entraTenantPlaceholder = "{tenantid}"

	// defaultGraphEndpoint is Microsoft Graph's global endpoint.
	defaultGraphEndpoint = "https://graph.microsoft.com"

	graphTimeout   = 10 * time.Second
	graphMaxPages  = 10
	graphMaxGroups = 5000
	graphMaxBody   = 4 << 20
)

var (
	errGraphNoToken     = errors.New("graph: no access token")
	errGraphBadEndpoint = errors.New("graph: invalid endpoint")
	errGraphTooMany     = errors.New("graph: too many groups or pages")
	errGraphForeignNext = errors.New("graph: nextLink leaves the graph origin")
	errGraphStatus      = errors.New("graph: unexpected status")
)

var entraGUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsEntraGUID reports whether s is a GUID (an Entra tenant or object ID).
func IsEntraGUID(s string) bool { return entraGUIDRe.MatchString(s) }

// IsEntraMultiTenantAuthority reports whether issuer names one of Microsoft
// identity platform's tenant-less v2.0 authorities
// (https://login.microsoftonline.com/{common|organizations|consumers}/v2.0,
// trailing slash optional). Their discovery document advertises an issuer with
// a literal {tenantid} placeholder, so the issuer check must be done by hand.
func IsEntraMultiTenantAuthority(issuer string) bool {
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "login.microsoftonline.com") {
		return false
	}

	return isTenantlessAuthorityPath(issuer)
}

// isTenantlessAuthorityPath is the host-independent shape check used at
// runtime: discovery of anything shaped like a tenant-less authority tolerates
// a templated issuer, which entraTenancy then enforces strictly.
func isTenantlessAuthorityPath(issuer string) bool {
	u, err := url.Parse(issuer)
	if err != nil {
		return false
	}

	switch strings.TrimSuffix(u.Path, "/") {
	case "/common/v2.0", "/organizations/v2.0", "/consumers/v2.0":
		return true
	}

	return false
}

// entraTenancy enforces the tenant of an ID token issued through a
// multi-tenant authority.
type entraTenancy struct {
	issuerTemplate string // discovery issuer, containing entraTenantPlaceholder
	allowed        []string
}

// verify checks an already signature-verified ID token: tid must be a GUID,
// iss must be exactly the issuer of that tenant, and tid must be allowed.
// Every failure is an ErrSSOLoginDenied.
func (e *entraTenancy) verify(iss string, claims map[string]any) error {
	tid, _ := claims["tid"].(string)
	if !IsEntraGUID(tid) {
		return denied("id token has no valid tid claim")
	}

	if want := strings.Replace(e.issuerTemplate, entraTenantPlaceholder, tid, 1); iss != want {
		return denied("id token issuer does not match its tenant (tid %s)", tid)
	}

	if !slices.ContainsFunc(e.allowed, func(a string) bool { return strings.EqualFold(a, tid) }) {
		return denied("tenant %s is not in allowed_tenants", tid)
	}

	return nil
}

// discoverProvider runs OIDC discovery for cfg.Issuer. For a tenant-less
// Entra authority the discovery issuer is a template; go-oidc's issuer check
// is disabled for the discovery call only after confirming the document either
// matches the configured issuer exactly or carries the {tenantid} placeholder.
func discoverProvider(ctx context.Context, cfg models.OIDCConfig) (*gooidc.Provider, *entraTenancy, error) {
	dctx := oidcContext(ctx)
	tenantless := isTenantlessAuthorityPath(cfg.Issuer)

	if tenantless {
		dctx = gooidc.InsecureIssuerURLContext(dctx, cfg.Issuer)
	}

	provider, err := gooidc.NewProvider(dctx, cfg.Issuer)
	if err != nil {
		return nil, nil, fmt.Errorf("oidc_discovery_failed: %w", ErrOIDCDiscoveryFailed)
	}

	if !tenantless {
		return provider, nil, nil
	}

	var doc struct {
		Issuer string `json:"issuer"`
	}

	_ = provider.Claims(&doc)

	switch {
	case strings.Count(doc.Issuer, entraTenantPlaceholder) == 1:
		if len(cfg.AllowedTenants) == 0 {
			return nil, nil, fmt.Errorf("%w: allowed_tenants is required for a multi-tenant authority", ErrOIDCConfigInvalid)
		}

		return provider, &entraTenancy{issuerTemplate: doc.Issuer, allowed: cfg.AllowedTenants}, nil
	case doc.Issuer == cfg.Issuer:
		return provider, nil, nil
	default:
		// Neither a template nor the configured issuer: the strict check that
		// was bypassed for discovery would have failed.
		return nil, nil, fmt.Errorf("oidc_discovery_failed: %w", ErrOIDCDiscoveryFailed)
	}
}

// groupsOverage reports whether Entra left the groups out of the token
// because the user has too many: either a groups entry in _claim_names
// (distributed claims) or hasgroups=true.
func groupsOverage(claims map[string]any) bool {
	if names, ok := claims["_claim_names"].(map[string]any); ok {
		if _, has := names["groups"]; has {
			return true
		}
	}

	return boolClaimTrue(claims, "hasgroups")
}

// graphMemberObjects returns the object IDs of the groups the signed-in user
// belongs to (transitively), using accessToken as the delegated credential.
//
// It calls POST {graph}/v1.0/me/getMemberObjects with securityEnabledOnly=true.
// Per Microsoft Graph's directoryObject: getMemberObjects reference, the least
// privileged delegated (work or school account) permission for the signed-in
// user's own memberships is User.Read; the access token therefore has to be
// issued for Graph, i.e. scopes must contain a Graph scope such as
// https://graph.microsoft.com/User.Read. Personal Microsoft accounts are not
// supported by this API. The result lists group object IDs, not names, so
// group_mapping keys must be those IDs.
func graphMemberObjects(ctx context.Context, graphBase, accessToken string) ([]string, error) {
	if accessToken == "" {
		return nil, errGraphNoToken
	}

	base, err := url.Parse(strings.TrimRight(graphBase, "/"))
	if err != nil || base.Host == "" {
		return nil, errGraphBadEndpoint
	}

	ctx, cancel := context.WithTimeout(ctx, graphTimeout)
	defer cancel()

	body := []byte(`{"securityEnabledOnly":true}`)
	next := base.String() + "/v1.0/me/getMemberObjects"

	var ids []string

	for range graphMaxPages {
		var page struct {
			Value []string `json:"value"`
			Next  string   `json:"@odata.nextLink"` //nolint:tagliatelle // Microsoft Graph wire name
		}

		if err := graphPost(ctx, next, accessToken, body, &page); err != nil {
			return nil, err
		}

		ids = append(ids, page.Value...)
		if len(ids) > graphMaxGroups {
			return nil, errGraphTooMany
		}

		if page.Next == "" {
			return ids, nil
		}

		// The bearer token must never leave the configured Graph origin.
		nu, err := url.Parse(page.Next)
		if err != nil || nu.Scheme != base.Scheme || !strings.EqualFold(nu.Host, base.Host) {
			return nil, errGraphForeignNext
		}

		next = page.Next
	}

	return nil, errGraphTooMany
}

func graphPost(ctx context.Context, rawURL, accessToken string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("graph request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := oidcHTTPClient.Do(req)
	if err != nil {
		// The *url.Error carries the URL only, never the Authorization header.
		return fmt.Errorf("graph request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: status %d", errGraphStatus, resp.StatusCode)
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, graphMaxBody)).Decode(out); err != nil {
		return fmt.Errorf("graph response: %w", err)
	}

	return nil
}
