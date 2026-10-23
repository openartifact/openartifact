// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	githubAuthBase = "https://github.com"
	githubAPIBase  = "https://api.github.com"

	// githubAPIVersion pins the REST API version sent in X-GitHub-Api-Version.
	githubAPIVersion = "2022-11-28"

	// oauth2IdentityTimeout bounds all identity API calls of one login.
	oauth2IdentityTimeout = 20 * time.Second
	oauth2MaxPages        = 10
	oauth2MaxBody         = 4 << 20
	githubPerPage         = "100"
)

var (
	errOAuth2NoSubject    = errors.New("oauth2: userinfo carries no subject")
	errOAuth2ForeignURL   = errors.New("oauth2: pagination link leaves the API origin")
	errOAuth2TooManyPages = errors.New("oauth2: too many result pages")
	errOAuth2Config       = errors.New("oauth2: authorization_url, token_url and userinfo_url are required")
)

// githubDefaultScopes are requested when the provider configures none:
// read:user (profile), user:email (/user/emails) and read:org (memberships).
//
//nolint:gochecknoglobals // constant list
var githubDefaultScopes = []string{"read:user", "user:email", "read:org"}

// newOAuth2Provider builds a plain OAuth 2.0 provider (no discovery, no ID
// token, no nonce). Nothing is fetched at load time.
func newOAuth2Provider(slug string, cfg models.OIDCConfig, redirectURL string) (*OIDCProvider, error) {
	conf := oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  redirectURL,
		Scopes:       cfg.Scopes,
	}

	switch cfg.Type {
	case models.OIDCTypeGitHub:
		authBase := firstNonEmpty(strings.TrimRight(cfg.AuthBaseURL, "/"), githubAuthBase)
		conf.Endpoint = oauth2.Endpoint{
			AuthURL:   authBase + "/login/oauth/authorize",
			TokenURL:  authBase + "/login/oauth/access_token",
			AuthStyle: oauth2.AuthStyleInParams,
		}

		if len(conf.Scopes) == 0 {
			conf.Scopes = githubDefaultScopes
		}
	default:
		if cfg.AuthorizationURL == "" || cfg.TokenURL == "" || cfg.UserinfoURL == "" {
			return nil, fmt.Errorf("%w: %w", ErrOIDCConfigInvalid, errOAuth2Config)
		}

		conf.Endpoint = oauth2.Endpoint{AuthURL: cfg.AuthorizationURL, TokenURL: cfg.TokenURL}
	}

	return &OIDCProvider{Slug: slug, Config: conf, Cfg: cfg}, nil
}

// oauth2Identity resolves the user behind accessToken through the provider's
// API and applies the provider's login policy.
func (p *OIDCProvider) oauth2Identity(ctx context.Context, accessToken string, log *logger.Logger) (SSOClaims, error) {
	ctx, cancel := context.WithTimeout(ctx, oauth2IdentityTimeout)
	defer cancel()

	var (
		claims SSOClaims
		err    error
	)

	if p.Cfg.Type == models.OIDCTypeGitHub {
		claims, err = p.githubIdentity(ctx, accessToken, log)
	} else {
		claims, err = p.genericIdentity(ctx, accessToken)
	}

	if err != nil {
		return SSOClaims{}, err
	}

	if p.Cfg.RequireEmailVerified && !claims.EmailVerified {
		return SSOClaims{}, denied("no verified email address")
	}

	return claims, nil
}

// oauth2API issues bearer-authenticated GETs against one API origin.
type oauth2API struct {
	origin  *url.URL
	token   string
	headers map[string]string
}

type apiStatusError struct{ status int }

func (e *apiStatusError) Error() string {
	return fmt.Sprintf("oauth2 api: unexpected status %d", e.status)
}

// get decodes one JSON response into out and returns the rel="next" link.
func (a *oauth2API) get(ctx context.Context, rawURL string, out any) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("oauth2 api request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")

	for k, v := range a.headers {
		req.Header.Set(k, v)
	}

	resp, err := oidcHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("oauth2 api request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", &apiStatusError{status: resp.StatusCode}
	}

	dec := json.NewDecoder(io.LimitReader(resp.Body, oauth2MaxBody))
	dec.UseNumber()

	if err := dec.Decode(out); err != nil {
		return "", fmt.Errorf("oauth2 api response: %w", err)
	}

	return nextLink(resp.Header.Get("Link")), nil
}

// getAll follows Link rel="next" pages (capped) and appends every element.
func getAll[T any](ctx context.Context, a *oauth2API, first string) ([]T, error) {
	var all []T

	next := first

	for range oauth2MaxPages {
		var page []T

		link, err := a.get(ctx, next, &page)
		if err != nil {
			return nil, err
		}

		all = append(all, page...)
		if link == "" {
			return all, nil
		}

		// The token must never leave the API origin.
		u, err := url.Parse(link)
		if err != nil || u.Scheme != a.origin.Scheme || !strings.EqualFold(u.Host, a.origin.Host) {
			return nil, errOAuth2ForeignURL
		}

		next = link
	}

	return nil, errOAuth2TooManyPages
}

// nextLink extracts the rel="next" target of an RFC 8288 Link header.
func nextLink(header string) string {
	for part := range strings.SplitSeq(header, ",") {
		segs := strings.Split(strings.TrimSpace(part), ";")
		if len(segs) < 2 || !strings.HasPrefix(segs[0], "<") || !strings.HasSuffix(segs[0], ">") {
			continue
		}

		for _, param := range segs[1:] {
			if strings.ReplaceAll(strings.TrimSpace(param), " ", "") == `rel="next"` {
				return strings.TrimSuffix(strings.TrimPrefix(segs[0], "<"), ">")
			}
		}
	}

	return ""
}

func (p *OIDCProvider) githubIdentity(ctx context.Context, accessToken string, log *logger.Logger) (SSOClaims, error) {
	apiBase := firstNonEmpty(strings.TrimRight(p.Cfg.APIBaseURL, "/"), githubAPIBase)

	origin, err := url.Parse(apiBase)
	if err != nil || origin.Host == "" {
		return SSOClaims{}, fmt.Errorf("%w: invalid api_base_url", ErrOIDCConfigInvalid)
	}

	api := &oauth2API{origin: origin, token: accessToken, headers: map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": githubAPIVersion,
	}}

	var user struct {
		ID    json.Number `json:"id"`
		Login string      `json:"login"`
		Name  string      `json:"name"`
	}

	if _, err := api.get(ctx, apiBase+"/user", &user); err != nil {
		return SSOClaims{}, fmt.Errorf("github user: %w", err)
	}

	if user.ID.String() == "" {
		return SSOClaims{}, errOAuth2NoSubject
	}

	email := p.githubEmail(ctx, api, apiBase, log)

	groups, err := p.githubGroups(ctx, api, apiBase)
	if err != nil {
		return SSOClaims{}, err
	}

	return SSOClaims{
		ExternalID:    user.ID.String(),
		Username:      user.Login,
		Email:         email,
		EmailVerified: email != "",
		FullName:      user.Name,
		Groups:        groups,
		GroupsPresent: true,
	}, nil
}

// githubEmail returns the primary email address when GitHub marks it
// verified, else "". An unverified primary, or a token without the user:email
// scope, yields no email rather than a login failure.
func (p *OIDCProvider) githubEmail(ctx context.Context, api *oauth2API, apiBase string, log *logger.Logger) string {
	emails, err := getAll[struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}](ctx, api, apiBase+"/user/emails?per_page="+githubPerPage)
	if err != nil {
		warnf(log, "github login: could not read email addresses; continuing without an email", "slug", p.Slug, "error", err)
		return ""
	}

	for _, e := range emails {
		if e.Primary && e.Verified {
			return e.Email
		}
	}

	return ""
}

// githubGroups returns "org" and "org/team-slug" entries restricted to
// allowed_orgs, lower-cased because GitHub logins are case-insensitive (so
// group_mapping keys are lower-case). A user in none of the allowed organizations is denied.
func (p *OIDCProvider) githubGroups(ctx context.Context, api *oauth2API, apiBase string) ([]string, error) {
	allowed := make(map[string]bool, len(p.Cfg.AllowedOrgs))
	for _, o := range p.Cfg.AllowedOrgs {
		allowed[strings.ToLower(strings.TrimSpace(o))] = true
	}

	memberships, err := getAll[struct {
		State        string `json:"state"`
		Organization struct {
			Login string `json:"login"`
		} `json:"organization"`
	}](ctx, api, apiBase+"/user/memberships/orgs?state=active&per_page="+githubPerPage)
	if err != nil {
		return nil, fmt.Errorf("github organizations: %w", err)
	}

	var groups []string

	for _, m := range memberships {
		if m.State == "active" && allowed[strings.ToLower(m.Organization.Login)] {
			groups = append(groups, strings.ToLower(m.Organization.Login))
		}
	}

	if len(groups) == 0 {
		return nil, denied("user belongs to none of the allowed organizations")
	}

	teams, err := getAll[struct {
		Slug         string `json:"slug"`
		Organization struct {
			Login string `json:"login"`
		} `json:"organization"`
	}](ctx, api, apiBase+"/user/teams?per_page="+githubPerPage)
	if err != nil {
		return nil, fmt.Errorf("github teams: %w", err)
	}

	for _, t := range teams {
		if allowed[strings.ToLower(t.Organization.Login)] {
			groups = append(groups, strings.ToLower(t.Organization.Login+"/"+t.Slug))
		}
	}

	return groups, nil
}

// genericIdentity maps the userinfo JSON of a generic OAuth 2.0 provider with
// the configured claim names.
func (p *OIDCProvider) genericIdentity(ctx context.Context, accessToken string) (SSOClaims, error) {
	origin, err := url.Parse(p.Cfg.UserinfoURL)
	if err != nil || origin.Host == "" {
		return SSOClaims{}, fmt.Errorf("%w: invalid userinfo_url", ErrOIDCConfigInvalid)
	}

	api := &oauth2API{origin: origin, token: accessToken}

	var info map[string]any
	if _, err := api.get(ctx, p.Cfg.UserinfoURL, &info); err != nil {
		return SSOClaims{}, fmt.Errorf("userinfo: %w", err)
	}

	subject := stringifyClaim(info["sub"])
	if subject == "" {
		subject = stringifyClaim(info["id"])
	}

	if subject == "" {
		return SSOClaims{}, errOAuth2NoSubject
	}

	out := SSOClaims{
		ExternalID: subject,
		Username:   stringClaim(info, firstNonEmpty(p.Cfg.UsernameClaim, "preferred_username")),
		Email:      stringClaim(info, firstNonEmpty(p.Cfg.EmailClaim, "email")),
		FullName:   stringClaim(info, firstNonEmpty(p.Cfg.NameClaim, "name")),
		// As for OIDC: only the standard email claim has a verification assertion.
		EmailVerified: (p.Cfg.EmailClaim == "" || p.Cfg.EmailClaim == "email") && boolClaimTrue(info, "email_verified"),
	}
	out.Groups, out.GroupsPresent = groupsFromClaims(info, firstNonEmpty(p.Cfg.GroupsClaim, "groups"))

	return out, nil
}

// stringifyClaim renders a string or numeric claim as a string.
func stringifyClaim(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	}

	return ""
}
