// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/netutil"
	"golang.org/x/net/publicsuffix"
)

const (
	// defaultTokenTTL applies when the token endpoint omits expires_in (the
	// distribution token spec's default).
	defaultTokenTTL = 60 * time.Second
	// maxTokenTTL caps how long one upstream token is reused.
	maxTokenTTL = 5 * time.Minute
	// tokenExpiryMargin retires a token slightly early so it never expires
	// mid-request.
	tokenExpiryMargin = 10 * time.Second
	maxTokenBody      = 1 << 20
	maxCachedTokens   = 1024
)

var (
	errNoRealm     = fmt.Errorf("%w: bearer challenge without realm", errUpstream)
	errNoToken     = errors.New("token response without token")
	errNoChallenge = fmt.Errorf("%w: no usable authentication challenge", errUpstream)
	errNoCreds     = fmt.Errorf("%w: basic authentication required but no credentials are configured", errUpstream)
)

// ociUpstream is where one image of a docker proxy lives: the registry base
// URL (scheme and host only) and the image name on that registry.
type ociUpstream struct {
	base string
	name string
}

// ociTarget maps image (the path below the OpenArtifact repository) to the
// upstream registry. The upstream_url path is ignored: registry names are
// scheme://host. Docker Hub keeps official images under library/, so a
// single-segment image there gains that prefix.
func ociTarget(repo *models.Repository, image string) (ociUpstream, error) {
	u, err := url.Parse(repo.UpstreamURL)
	if err != nil || u.Host == "" {
		return ociUpstream{}, fmt.Errorf("parse upstream url %q: %w", repo.UpstreamURL, err)
	}

	name := image

	switch strings.ToLower(u.Hostname()) {
	case "registry-1.docker.io", "index.docker.io", "registry.hub.docker.com", "docker.io":
		if !strings.Contains(name, "/") {
			name = "library/" + name
		}
	}

	return ociUpstream{base: u.Scheme + "://" + u.Host, name: name}, nil
}

// tokenCache holds the bearer tokens obtained from upstream token endpoints,
// per proxy repository and image.
type tokenCache struct {
	mu sync.Mutex
	m  map[string]cachedToken
}

type cachedToken struct {
	header  string
	expires time.Time
}

func (c *tokenCache) get(key string) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	t, ok := c.m[key]
	if !ok {
		return ""
	}

	if !time.Now().Before(t.expires) {
		delete(c.m, key)
		return ""
	}

	return t.header
}

func (c *tokenCache) put(key, header string, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.m == nil {
		c.m = make(map[string]cachedToken)
	}

	if len(c.m) >= maxCachedTokens {
		now := time.Now()

		for k, t := range c.m {
			if !now.Before(t.expires) {
				delete(c.m, k)
			}
		}

		if len(c.m) >= maxCachedTokens {
			clear(c.m)
		}
	}

	c.m[key] = cachedToken{header: header, expires: time.Now().Add(ttl)}
}

func (c *tokenCache) drop(key string) {
	c.mu.Lock()
	delete(c.m, key)
	c.mu.Unlock()
}

// tokenKey identifies the credentials context of a token: a token is only
// reused for the same repository, upstream, account, password and image. The
// password enters in its stored (encrypted) form, so rotating it retires every
// token fetched with the old one. The key lives only in this process's memory.
func tokenKey(repo *models.Repository, up ociUpstream) string {
	return strings.Join([]string{repo.ID, up.base, repo.UpstreamUsername, repo.UpstreamPasswordEnc, up.name}, "\x00")
}

// origin is the scheme, lower-cased host name and effective port of a URL.
type origin struct{ scheme, host, port string }

func originOf(u *url.URL) origin {
	o := origin{scheme: strings.ToLower(u.Scheme), host: strings.ToLower(u.Hostname()), port: u.Port()}

	if o.port == "" {
		switch o.scheme {
		case "https":
			o.port = "443"
		case "http":
			o.port = "80"
		}
	}

	return o
}

// realmMayReceiveCredentials reports whether the stored upstream credentials
// may be sent to a token endpoint named by the upstream's WWW-Authenticate
// challenge. The realm comes from the upstream's response, so a hostile or
// compromised registry could otherwise name any host and collect them. They go
// only to the upstream's own origin, or to an https endpoint that shares its
// registrable domain (Docker Hub: registry-1.docker.io challenges with
// auth.docker.io). Everything else is asked for a token anonymously.
func realmMayReceiveCredentials(up ociUpstream, realm *url.URL) bool {
	base, err := url.Parse(up.base)
	if err != nil {
		return false
	}

	ro, bo := originOf(realm), originOf(base)

	if ro == bo {
		return true
	}

	if ro.scheme != "https" || ro.host == "" || bo.host == "" || net.ParseIP(ro.host) != nil || net.ParseIP(bo.host) != nil {
		return false
	}

	realmDomain, err := publicsuffix.EffectiveTLDPlusOne(ro.host)
	if err != nil {
		return false
	}

	baseDomain, err := publicsuffix.EffectiveTLDPlusOne(bo.host)
	if err != nil {
		return false
	}

	return realmDomain == baseDomain
}

// ociDo sends method to target on the upstream registry, answering the
// registry's authentication challenge. A cached bearer token is sent up front;
// on a 401 the Bearer (token endpoint, anonymous or with the stored
// credentials) or Basic challenge is answered and the request repeated once.
// The caller owns the returned response body. errUpstreamDown is returned for
// transport failures, errUpstream when authentication cannot be completed.
func (s *Service) ociDo(ctx context.Context, repo *models.Repository, up ociUpstream, method, target, accept string) (*http.Response, error) {
	key := tokenKey(repo, up)
	authz := s.tokens.get(key)

	resp, err := s.ociRequest(ctx, repo, method, target, accept, authz)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}

	challenges := resp.Header.Values("WWW-Authenticate")

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	base.CloseLog(resp.Body, s.log)

	s.tokens.drop(key)

	authz, err = s.answerChallenge(ctx, repo, up, challenges)
	if err != nil {
		return nil, err
	}

	return s.ociRequest(ctx, repo, method, target, accept, authz)
}

// ociRequest performs one upstream request with the given Authorization value.
func (s *Service) ociRequest(ctx context.Context, repo *models.Repository, method, target, accept, authz string) (*http.Response, error) {
	if err := netutil.ValidateOutboundURLContext(ctx, target); err != nil {
		s.log.Warnw("docker proxy upstream rejected by outbound URL policy", "repo", repo.Name, "error", err)
		return nil, outboundError(err)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		s.log.Errorw("failed to build docker proxy upstream request", "repo", repo.Name, "error", err)
		return nil, errInternal
	}

	req.Header.Set("User-Agent", "OpenArtifact-Proxy")
	req.Header.Set("Accept-Encoding", "identity")

	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	if authz != "" {
		req.Header.Set("Authorization", authz)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Warnw("docker proxy upstream request failed", "repo", repo.Name, "url", target, "error", err)
		return nil, errUpstreamDown
	}

	return resp, nil
}

// answerChallenge turns the upstream's WWW-Authenticate challenges into an
// Authorization header value, caching a bearer token for later requests.
func (s *Service) answerChallenge(ctx context.Context, repo *models.Repository, up ociUpstream, challenges []string) (string, error) {
	var basic bool

	for _, c := range challenges {
		scheme, params := parseChallenge(c)

		switch scheme {
		case "bearer":
			return s.bearerToken(ctx, repo, up, params)
		case "basic":
			basic = true
		}
	}

	if !basic {
		s.log.Warnw("docker proxy upstream sent no usable authentication challenge", "repo", repo.Name)
		return "", errNoChallenge
	}

	if repo.UpstreamUsername == "" {
		s.log.Warnw("docker proxy upstream wants basic credentials", "repo", repo.Name)
		return "", errNoCreds
	}

	user, pass, err := upstreamCredentials(repo)
	if err != nil {
		s.log.Errorw("failed to decrypt proxy upstream password", "repo", repo.Name, "error", err)
		return "", errInternal
	}

	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)), nil
}

// bearerToken fetches a token from the realm named by a Bearer challenge:
// anonymously, or with the repository's stored credentials as HTTP Basic.
func (s *Service) bearerToken(ctx context.Context, repo *models.Repository, up ociUpstream, params map[string]string) (string, error) {
	realm := params["realm"]
	if realm == "" {
		return "", errNoRealm
	}

	ru, err := url.Parse(realm)
	if err != nil {
		s.log.Warnw("docker proxy upstream sent an invalid token realm", "repo", repo.Name, "error", err)
		return "", errUpstream
	}

	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + up.name + ":pull"
	}

	q := ru.Query()
	if svc := params["service"]; svc != "" {
		q.Set("service", svc)
	}

	q.Set("scope", scope)
	ru.RawQuery = q.Encode()

	withCreds := realmMayReceiveCredentials(up, ru)
	if !withCreds && repo.UpstreamUsername != "" {
		s.log.Warnw("docker proxy token realm is not the upstream's own site; requesting the token anonymously",
			"repo", repo.Name, "realm_host", ru.Hostname())
	}

	tok, err := s.fetchToken(ctx, repo, ru.String(), withCreds)
	if err != nil {
		return "", err
	}

	s.tokens.put(tokenKey(repo, up), tok.header, time.Until(tok.expires))

	return tok.header, nil
}

// fetchToken calls the token endpoint. The endpoint is outside the configured
// upstream URL, so it passes the same outbound URL policy.
func (s *Service) fetchToken(ctx context.Context, repo *models.Repository, target string, withCreds bool) (cachedToken, error) {
	resp, err := s.ociRequestBasic(ctx, repo, target, withCreds)
	if err != nil {
		return cachedToken{}, err
	}
	defer func() { base.CloseLog(resp.Body, s.log) }()

	switch {
	case resp.StatusCode >= http.StatusInternalServerError || resp.StatusCode == http.StatusTooManyRequests:
		s.log.Warnw("docker proxy token endpoint unavailable", "repo", repo.Name, "status", resp.StatusCode)
		return cachedToken{}, errUpstreamDown
	case resp.StatusCode != http.StatusOK:
		s.log.Warnw("docker proxy token endpoint refused the request", "repo", repo.Name, "status", resp.StatusCode)
		return cachedToken{}, errUpstream
	}

	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, maxTokenBody)).Decode(&body); err != nil {
		s.log.Warnw("docker proxy token response unreadable", "repo", repo.Name, "error", err)
		return cachedToken{}, errUpstream
	}

	token := body.Token
	if token == "" {
		token = body.AccessToken
	}

	if token == "" {
		s.log.Warnw("docker proxy token response empty", "repo", repo.Name, "error", errNoToken)
		return cachedToken{}, errUpstream
	}

	ttl := defaultTokenTTL
	if body.ExpiresIn > 0 {
		ttl = time.Duration(body.ExpiresIn) * time.Second
	}

	ttl = min(ttl, maxTokenTTL) - tokenExpiryMargin

	return cachedToken{header: "Bearer " + token, expires: time.Now().Add(max(ttl, 0))}, nil
}

// ociRequestBasic GETs a token endpoint, with the stored credentials as HTTP
// Basic when the repository has any and withCreds allows sending them.
func (s *Service) ociRequestBasic(ctx context.Context, repo *models.Repository, target string, withCreds bool) (*http.Response, error) {
	if err := netutil.ValidateOutboundURLContext(ctx, target); err != nil {
		s.log.Warnw("docker proxy token endpoint rejected by outbound URL policy", "repo", repo.Name, "error", err)
		return nil, outboundError(err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, errInternal
	}

	req.Header.Set("User-Agent", "OpenArtifact-Proxy")

	if withCreds && repo.UpstreamUsername != "" {
		user, pass, derr := upstreamCredentials(repo)
		if derr != nil {
			s.log.Errorw("failed to decrypt proxy upstream password", "repo", repo.Name, "error", derr)
			return nil, errInternal
		}

		req.SetBasicAuth(user, pass)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Warnw("docker proxy token request failed", "repo", repo.Name, "error", err)
		return nil, errUpstreamDown
	}

	return resp, nil
}

// upstreamCredentials returns the repository's upstream username and the
// decrypted password.
func upstreamCredentials(repo *models.Repository) (string, string, error) {
	if repo.UpstreamPasswordEnc == "" {
		return repo.UpstreamUsername, "", nil
	}

	pass, err := crypto.Decrypt(repo.UpstreamPasswordEnc)
	if err != nil {
		return "", "", fmt.Errorf("decrypt upstream password: %w", err)
	}

	return repo.UpstreamUsername, pass, nil
}

// parseChallenge splits one WWW-Authenticate value into its lower-cased scheme
// and its auth-params (RFC 7235): `Bearer realm="…",service="…",scope="…"`.
// Quoted values may contain commas and escaped quotes.
func parseChallenge(h string) (string, map[string]string) {
	h = strings.TrimSpace(h)
	scheme, rest, _ := strings.Cut(h, " ")
	params := map[string]string{}

	rest = strings.TrimSpace(rest)
	for rest != "" {
		key, after, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}

		key = strings.ToLower(strings.TrimSpace(key))
		after = strings.TrimLeft(after, " ")

		var val string

		if strings.HasPrefix(after, `"`) {
			val, rest = readQuoted(after[1:])
		} else {
			val, rest, _ = strings.Cut(after, ",")
			val = strings.TrimSpace(val)
		}

		params[key] = val
		rest = strings.TrimLeft(strings.TrimSpace(rest), ", ")
	}

	return strings.ToLower(scheme), params
}

// readQuoted reads a quoted-string body (the opening quote already consumed)
// and returns it with the text after the closing quote.
func readQuoted(s string) (string, string) {
	var b strings.Builder

	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		case '"':
			return b.String(), s[i+1:]
		default:
			b.WriteByte(s[i])
		}
	}

	return b.String(), ""
}
