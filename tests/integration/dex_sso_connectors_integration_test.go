// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"html"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// The connector side of the Dex test (dex_sso_integration_test.go): one Dex
// client, three connectors (ldap, the password database `local`, and `upstream`,
// an oidc connector to a second Dex), one OpenArtifact provider per connector
// pinned with dex_connector_id, and one provider without a pin.

const (
	// Dex's password database signs users in by email address.
	dexUpstreamLogin = "dana@upstream.example"

	dexLocalSlug    = "dex-local"
	dexUpstreamSlug = "dex-upstream"
	dexOpenSlug     = "dex-open"
)

// dexConnectorLink matches a connector entry of Dex's chooser page.
var dexConnectorLink = regexp.MustCompile(`href="([^"]*/auth/([A-Za-z0-9_-]+)\?[^"]*)"`)

// chooseConnector follows the chooser link of connector id on page.
func (b *browser) chooseConnector(page step, id string) step {
	b.t.Helper()

	for _, m := range dexConnectorLink.FindAllStringSubmatch(page.body, -1) {
		if m[2] != id {
			continue
		}

		u, err := page.pageURL.Parse(html.UnescapeString(m[1]))
		if err != nil {
			b.t.Fatalf("connector link %q: %v", m[1], err)
		}

		return b.walk(u.String())
	}

	b.t.Fatalf("no %q connector on the chooser page (status %d): %.600s", id, page.status, page.body)

	return step{}
}

func (o *dexOA) loginURL(slug string) string {
	return o.baseURL + "/api/v1/auth/sso/oidc/" + slug + "/login"
}

// userByEmail returns the account that owns an address, or nil.
func userByEmail(t *testing.T, oa *dexOA, email string) *models.User {
	t.Helper()

	u, err := oa.pg.GetUserByEmail(t.Context(), email)
	if err != nil {
		t.Fatalf("get user by email %s: %v", email, err)
	}

	return u
}

// connectorHint: the login route redirects to Dex with the connector id of a
// pinned provider (so Dex skips its chooser) and the Dex scopes, and sends no
// connector_id for an unpinned one.
func (f *dexFixture) connectorHint(t *testing.T) {
	tests := []struct {
		slug       string
		hint       string
		wantScopes []string
	}{
		{dexTestSlug, "ldap", []string{"openid", "email", "profile", "groups", "federated:id"}},
		{dexLocalSlug, "local", []string{"openid", "email", "profile", "groups", "federated:id"}},
		{dexUpstreamSlug, "upstream", []string{"openid", "email", "profile", "groups", "federated:id"}},
		{dexOpenSlug, "", []string{"openid", "email", "profile"}},
	}

	for _, tc := range tests {
		b := newBrowser(t, f.oa)
		r, from := b.get(f.oa.loginURL(tc.slug))

		loc := r.location(from)
		if loc == nil || !strings.HasPrefix(loc.String(), f.disc.AuthEndpoint) {
			t.Fatalf("%s: login redirects to %v (status %d), want Dex's authorization endpoint", tc.slug, loc, r.status)
		}

		q := loc.Query()
		if got := q.Get("connector_id"); got != tc.hint {
			t.Errorf("%s: connector_id = %q, want %q", tc.slug, got, tc.hint)
		}

		if got := strings.Fields(q.Get("scope")); !slices.Equal(got, tc.wantScopes) {
			t.Errorf("%s: scope = %v, want %v", tc.slug, got, tc.wantScopes)
		}
	}
}

// connectorClaims: what each connector puts in the ID token, in particular
// federated_claims.connector_id, which the pinned provider enforces.
func (f *dexFixture) connectorClaims(t *testing.T) {
	const scope = "openid email profile groups federated:id"

	tests := []struct {
		connector, user, password string
		wantEmail                 string
	}{
		{"ldap", "alice", "alice-pass", "alice@example.org"},
		{"local", "carol@example.org", "carol-pass", "carol@example.org"},
		{"upstream", dexUpstreamLogin, dexUpstreamPassword, "dana@upstream.example"},
	}

	for _, tc := range tests {
		claims := probeClaims(t, f.oa, f.disc, tc.connector, scope, tc.user, tc.password)
		t.Logf("%s id_token claims: %v", tc.connector, claims)

		fed, _ := claims["federated_claims"].(map[string]any)
		if fed["connector_id"] != tc.connector {
			t.Errorf("%s: federated_claims = %v, want connector_id %s", tc.connector, claims["federated_claims"], tc.connector)
		}

		if claims["email"] != tc.wantEmail {
			t.Errorf("%s: email = %v, want %s", tc.connector, claims["email"], tc.wantEmail)
		}
	}

	// Without the scope Dex releases no federated_claims, which is why a pinned
	// provider with explicit scopes must list it.
	claims := probeClaims(t, f.oa, f.disc, "local", "openid email profile", "carol@example.org", "carol-pass")
	if _, ok := claims["federated_claims"]; ok {
		t.Errorf("federated_claims present without the federated:id scope: %v", claims["federated_claims"])
	}
}

// hintStripped is the attack the connector check stops: the connector_id the
// login route adds is only a hint, so the user removes it, picks another
// connector of the same Dex and completes that login at the pinned provider.
func (f *dexFixture) hintStripped(t *testing.T) {
	tests := []struct{ slug, via, user, password string }{
		{dexTestSlug, "local", "carol@example.org", "carol-pass"},
		{dexLocalSlug, "ldap", "alice", "alice-pass"},
		{dexUpstreamSlug, "ldap", "bob", "bob-pass"},
	}

	for _, tc := range tests {
		sessions := countSessions(t, f.oa.pg)
		b := newBrowser(t, f.oa)

		r, from := b.get(f.oa.loginURL(tc.slug))

		authURL := r.location(from)
		if authURL == nil || authURL.Query().Get("connector_id") == "" {
			t.Fatalf("%s: login redirect %v carries no connector_id to strip", tc.slug, authURL)
		}

		q := authURL.Query()
		q.Del("connector_id")
		authURL.RawQuery = q.Encode()

		page := b.chooseConnector(b.walk(authURL.String()), tc.via)

		done := b.submitLogin(page, tc.user, tc.password)
		if done.callback == nil {
			t.Fatalf("%s via %s: Dex did not send the browser back (status %d)", tc.slug, tc.via, done.status)
		}

		cb, _ := b.get(done.callback.String())
		requireLoginDenied(t, b, f.oa, cb, "sso_login_denied")

		if got := countSessions(t, f.oa.pg); got != sessions {
			t.Errorf("%s via %s: sessions = %d, want %d", tc.slug, tc.via, got, sessions)
		}
	}

	if userByEmail(t, f.oa, "carol@example.org") != nil {
		t.Error("a refused login provisioned carol")
	}
}

// pinnedLogin signs in through a provider pinned to one connector (the browser
// never sees Dex's chooser) and returns the provisioned account.
func (f *dexFixture) pinnedLogin(t *testing.T, slug, user, password, email string) *models.User {
	t.Helper()

	b := newBrowser(t, f.oa)

	r, ok := b.loginViaOpenArtifact(f.oa, slug, user, password)
	if !ok {
		t.Fatal("Dex refused the right password")
	}

	requireLoggedIn(t, f.oa, r)

	u := userByEmail(t, f.oa, email)
	if u == nil {
		t.Fatalf("no account for %s after the login", email)
	}

	t.Logf("%s: provisioned username %q", slug, u.Username)

	if u.ProviderSlug == nil || *u.ProviderSlug != slug || u.ExternalID == nil || *u.ExternalID == "" {
		t.Errorf("%s: account is not bound to the provider's identity: %+v", slug, u)
	}

	requireSession(t, f.oa, b, u.ID)

	return u
}

func (f *dexFixture) localLogin(t *testing.T) {
	u := f.pinnedLogin(t, dexLocalSlug, "carol@example.org", "carol-pass", "carol@example.org")

	// Dex's password database has no groups.
	if got := userGroups(t, f.oa.pg, u.ID); len(got) != 0 {
		t.Errorf("carol groups = %v, want none", got)
	}
}

func (f *dexFixture) upstreamLogin(t *testing.T) {
	u := f.pinnedLogin(t, dexUpstreamSlug, dexUpstreamLogin, dexUpstreamPassword, "dana@upstream.example")

	if got := userGroups(t, f.oa.pg, u.ID); len(got) != 0 {
		t.Errorf("dana groups = %v, want none", got)
	}

	if carol := userByEmail(t, f.oa, "carol@example.org"); carol == nil || carol.ID == u.ID {
		t.Errorf("connectors share an account: %+v %+v", carol, u)
	}
}

// chooser: without a pin Dex asks which connector to use, and one OpenArtifact
// provider accepts a login through any of them.
func (f *dexFixture) chooser(t *testing.T) {
	b := newBrowser(t, f.oa)

	page := b.walk(f.oa.loginURL(dexOpenSlug))
	if page.callback != nil || page.status != http.StatusOK {
		t.Fatalf("expected Dex's chooser page, got status %d callback %v", page.status, page.callback)
	}

	for _, id := range []string{"ldap", "local", "upstream"} {
		if !dexConnectorLink.MatchString(page.body) || !strings.Contains(page.body, "/auth/"+id+"?") {
			t.Errorf("the chooser does not offer the %s connector", id)
		}
	}

	done := b.submitLogin(b.chooseConnector(page, "local"), "carol@example.org", "carol-pass")
	if done.callback == nil || !strings.HasSuffix(done.callback.Path, "/"+dexOpenSlug+"/callback") {
		t.Fatalf("login through the chooser did not return to the %s callback: %+v", dexOpenSlug, done)
	}

	if done.callback.Query().Get("code") == "" {
		t.Errorf("callback carries no code: %s", done.callback)
	}
}
