// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/moby/moby/api/types/container"
	networktypes "github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.yaml.in/yaml/v3"

	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

// Dex in front of OpenLDAP, driven through the real OIDC login of an
// in-process OpenArtifact server over HTTP.
//
// The images, the LDIF seed and the Dex config are the ones of the `dex`
// compose profile (docker/docker-compose.yml, docker/dex/): the test reads them
// from there, so the profile cannot drift from what is verified. It only swaps
// what depends on the run: the issuer (a free host port, so the test process and
// the in-process server reach Dex at the URL Dex advertises), the client secret
// and the redirect URI (the in-process server's port).
//
// Needs TEST_DATABASE_URL (a fresh database is created on that server) and a
// container runtime. Without a runtime the test skips; with CI set it fails.

const (
	dexTestSlug         = "dex"
	dexTestClientID     = "openartifact"
	dexTestClientSecret = "dex-integration-secret"
	dexTestPort         = "5556/tcp"
	dexLDIFPath         = "/container/service/slapd/assets/config/bootstrap/ldif/custom/50-seed.ldif"

	dexAdminGroup = "oa-admins"
	dexDevGroup   = "oa-developers"
)

// composeService is the part of a docker-compose service the test reads.
type composeService struct {
	Image       string            `yaml:"image"`
	Environment map[string]string `yaml:"environment"`
}

func dexComposeServices(t *testing.T) map[string]composeService {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "docker", "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}

	var doc struct {
		Services map[string]composeService `yaml:"services"`
	}

	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse docker-compose.yml: %v", err)
	}

	for _, name := range []string{"dex", "openldap"} {
		svc, ok := doc.Services[name]
		if !ok || !strings.Contains(svc.Image, "@sha256:") {
			t.Fatalf("compose service %q missing or not pinned by digest: %+v", name, svc)
		}
	}

	return doc.Services
}

// dexSkipWithoutRuntime skips (fails under CI) when no container runtime answers.
func dexSkipWithoutRuntime(t *testing.T) {
	t.Helper()

	provider, err := testcontainers.NewDockerProvider()
	if err == nil {
		defer provider.Close()
		err = provider.Health(t.Context())
	}

	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("no container runtime in CI: %v", err)
		}

		t.Skipf("no container runtime: %v", err)
	}
}

// freeHostPort returns a TCP port on 127.0.0.1 that was free a moment ago.
func freeHostPort(t *testing.T) int {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick a free port: %v", err)
	}

	defer func() { _ = l.Close() }()

	return l.Addr().(*net.TCPAddr).Port
}

// dexConfig renders docker/dex/config.yaml for this run.
func dexConfig(t *testing.T, issuer, redirectURI string) []byte {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "docker", "dex", "config.yaml"))
	if err != nil {
		t.Fatalf("read dex config: %v", err)
	}

	var cfg map[string]any
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse dex config: %v", err)
	}

	cfg["issuer"] = issuer

	clients, _ := cfg["staticClients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("dex config: want one static client, have %d", len(clients))
	}

	client, _ := clients[0].(map[string]any)
	if client["id"] != dexTestClientID {
		t.Fatalf("dex config: static client id = %v, want %s", client["id"], dexTestClientID)
	}

	delete(client, "secretEnv")
	client["secret"] = dexTestClientSecret
	client["redirectURIs"] = []string{redirectURI}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("render dex config: %v", err)
	}

	return out
}

// logContainerOnFailure prints a container's log when the test failed.
func logContainerOnFailure(t *testing.T, name string, c testcontainers.Container) {
	t.Helper()

	t.Cleanup(func() {
		if !t.Failed() {
			return
		}

		rc, err := c.Logs(context.Background())
		if err != nil {
			t.Logf("%s logs: %v", name, err)
			return
		}

		defer func() { _ = rc.Close() }()

		b, _ := io.ReadAll(rc)
		t.Logf("--- %s log ---\n%s", name, b)
	})
}

// startDex starts OpenLDAP (seeded) and Dex on one network and returns Dex's
// issuer URL, reachable from this process.
func startDex(t *testing.T, redirectURI string) string {
	t.Helper()

	dexSkipWithoutRuntime(t)

	services := dexComposeServices(t)
	ctx := t.Context()

	nw, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}

	testcontainers.CleanupNetwork(t, nw)

	seed, err := filepath.Abs(filepath.Join("..", "..", "docker", "dex", "seed.ldif"))
	if err != nil {
		t.Fatal(err)
	}

	ldapSvc := services["openldap"]

	ldap, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Image: ldapSvc.Image,
		Cmd:   []string{"--copy-service"},
		Env:   ldapSvc.Environment,
		Files: []testcontainers.ContainerFile{{HostFilePath: seed, ContainerFilePath: dexLDIFPath, FileMode: 0o644}},
		// The bootstrap loads the seed before slapd starts; the line below is
		// slapd's own, after the directory is ready to bind.
		WaitingFor:     wait.ForLog("slapd starting").WithStartupTimeout(3 * time.Minute),
		Networks:       []string{nw.Name},
		NetworkAliases: map[string][]string{nw.Name: {"openldap"}},
		Started:        true,
	})
	testcontainers.CleanupContainer(t, ldap)

	if err != nil {
		t.Fatalf("start openldap: %v", err)
	}

	logContainerOnFailure(t, "openldap", ldap)

	hostPort := freeHostPort(t)
	issuer := fmt.Sprintf("http://127.0.0.1:%d/dex", hostPort)

	dex, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Image:        services["dex"].Image,
		Cmd:          []string{"dex", "serve", "/etc/dex/config.yaml"},
		ExposedPorts: []string{dexTestPort},
		// A fixed host port: the issuer URL, which Dex stamps into every
		// endpoint it advertises, has to be known before Dex starts.
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.PortBindings = networktypes.PortMap{
				networktypes.MustParsePort(dexTestPort): {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: strconv.Itoa(hostPort)}},
			}
		},
		Files: []testcontainers.ContainerFile{{
			Reader:            strings.NewReader(string(dexConfig(t, issuer, redirectURI))),
			ContainerFilePath: "/etc/dex/config.yaml",
			FileMode:          0o644,
		}},
		WaitingFor: wait.ForHTTP("/dex/.well-known/openid-configuration").
			WithPort(dexTestPort).WithStartupTimeout(2 * time.Minute),
		Networks: []string{nw.Name},
		Started:  true,
	})
	testcontainers.CleanupContainer(t, dex)

	if err != nil {
		t.Fatalf("start dex: %v", err)
	}

	logContainerOnFailure(t, "dex", dex)

	return issuer
}

// dexOA is the in-process OpenArtifact server under test.
type dexOA struct {
	baseURL string
	host    string
	pg      *database.PostgresDB
	admin   http.Handler
}

// newDexOA builds the server on a database of its own: the public SSO routes
// behind a listener, and the admin SSO routes (called in-process, as an admin)
// to create and update the provider the way an operator does.
func newDexOA(t *testing.T) (*dexOA, *httptest.Server) {
	t.Helper()

	pg := bsrFreshDB(t, "dex")
	log := harness.Log
	eval := auth.NewEvaluator(pg, log, false)

	srv := httptest.NewUnstartedServer(nil)
	host := srv.Listener.Addr().String()
	baseURL := "http://" + host

	public := chi.NewRouter()
	api.NewOIDCHandler(pg, log, eval, []byte("dex-test-hmac-key"), baseURL).RegisterPublic(public)
	api.NewAuthHandler(pg, log, eval, nil).WithBaseURL(baseURL).RegisterPublic(public)
	srv.Config.Handler = public
	srv.Start()
	t.Cleanup(srv.Close)

	admin := chi.NewRouter()
	admin.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := &auth.Principal{Username: "dex-admin", IsLegacy: true, Credential: auth.CredentialLegacy}
			next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), p)))
		})
	})
	api.NewAdminSSOHandler(pg, log, eval, baseURL, nil).RegisterAuthed(admin)

	return &dexOA{baseURL: baseURL, host: host, pg: pg, admin: admin}, srv
}

// adminJSON calls the admin API in-process and returns the status.
func (o *dexOA) adminJSON(t *testing.T, method, path string, body any) (int, string) {
	t.Helper()

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	o.admin.ServeHTTP(rec, req)

	return rec.Code, rec.Body.String()
}

func (o *dexOA) createProvider(t *testing.T, issuer string) {
	t.Helper()

	code, body := o.adminJSON(t, http.MethodPost, "/api/v1/admin/sso-providers", map[string]any{
		"slug":         dexTestSlug,
		"kind":         "oidc",
		"display_name": "Dex (LDAP)",
		"oidc_config": map[string]any{
			"issuer":        issuer,
			"client_id":     dexTestClientID,
			"client_secret": dexTestClientSecret,
			// groups is opt-in at OpenArtifact; Dex only emits the claim for it.
			"scopes": []string{"openid", "email", "profile", "groups"},
		},
		"group_mapping":        map[string]string{"artifact-admins": dexAdminGroup, "developers": dexDevGroup},
		"auto_provision_users": true,
	})
	if code != http.StatusCreated {
		t.Fatalf("create provider = %d %s", code, body)
	}
}

func (o *dexOA) setLinkByEmail(t *testing.T, on bool) {
	t.Helper()

	code, body := o.adminJSON(t, http.MethodPatch, "/api/v1/admin/sso-providers/"+dexTestSlug, map[string]any{"link_by_email": on})
	if code != http.StatusOK {
		t.Fatalf("patch link_by_email = %d %s", code, body)
	}
}

// browser is an HTTP client with a cookie jar that follows redirects by hand,
// so a login can be stopped where the identity provider hands the browser back
// to OpenArtifact.
type browser struct {
	t      *testing.T
	client *http.Client
	oaHost string
}

func newBrowser(t *testing.T, oaHost string) *browser {
	t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}

	return &browser{
		t:      t,
		oaHost: oaHost,
		client: &http.Client{
			Jar:           jar,
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// step is where a walk ended: a page (the Dex login form, or an error page) or
// the redirect back to OpenArtifact.
type step struct {
	status   int
	body     string
	pageURL  *url.URL
	callback *url.URL // the redirect target on OpenArtifact; not fetched
}

// reply is a fully read HTTP response.
type reply struct {
	status int
	header http.Header
	body   []byte
}

// location resolves the Location header against base; nil when there is none.
func (r reply) location(base *url.URL) *url.URL {
	loc := r.header.Get("Location")
	if loc == "" {
		return nil
	}

	u, err := base.Parse(loc)
	if err != nil {
		return nil
	}

	return u
}

func (b *browser) send(req *http.Request) reply {
	b.t.Helper()

	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)

	return reply{status: resp.StatusCode, header: resp.Header, body: body}
}

func (b *browser) get(rawURL string) (reply, *url.URL) {
	b.t.Helper()

	req, err := http.NewRequestWithContext(b.t.Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		b.t.Fatal(err)
	}

	return b.send(req), req.URL
}

// walk fetches rawURL and follows redirects until a page, or a redirect that
// points at OpenArtifact.
func (b *browser) walk(rawURL string) step {
	b.t.Helper()

	r, from := b.get(rawURL)

	return b.advance(r, from)
}

func (b *browser) advance(r reply, from *url.URL) step {
	b.t.Helper()

	for range 20 {
		if r.status < 300 || r.status >= 400 {
			return step{status: r.status, body: string(r.body), pageURL: from}
		}

		loc := r.location(from)
		if loc == nil {
			b.t.Fatalf("redirect from %s without a usable Location", from)
		}

		if loc.Host == b.oaHost {
			return step{status: r.status, callback: loc, pageURL: from}
		}

		r, from = b.get(loc.String())
	}

	b.t.Fatalf("more than 20 redirects from %s", from)

	return step{}
}

var dexFormAction = regexp.MustCompile(`<form[^>]*\saction="([^"]*)"`)

// submitLogin posts the Dex LDAP login form found on page and follows on.
func (b *browser) submitLogin(page step, user, password string) step {
	b.t.Helper()

	m := dexFormAction.FindStringSubmatch(page.body)
	if m == nil {
		b.t.Fatalf("no login form on %s (status %d): %.400s", page.pageURL, page.status, page.body)
	}

	action, err := page.pageURL.Parse(html.UnescapeString(m[1]))
	if err != nil {
		b.t.Fatalf("form action %q: %v", m[1], err)
	}

	form := url.Values{"login": {user}, "password": {password}}

	req, err := http.NewRequestWithContext(b.t.Context(), http.MethodPost, action.String(), strings.NewReader(form.Encode()))
	if err != nil {
		b.t.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return b.advance(b.send(req), action)
}

// loginViaOpenArtifact runs the whole browser login: OpenArtifact's login
// route, Dex, the LDAP form, back to OpenArtifact's callback. It returns the
// callback's own answer (the session cookies are in the jar); ok is false when
// Dex refused the credentials and never sent the browser back.
func (b *browser) loginViaOpenArtifact(oa *dexOA, user, password string) (reply, bool) {
	b.t.Helper()

	page := b.walk(oa.baseURL + "/api/v1/auth/sso/oidc/" + dexTestSlug + "/login")
	if page.callback != nil {
		b.t.Fatalf("login did not reach Dex: %v", page.callback)
	}

	done := b.submitLogin(page, user, password)
	if done.callback == nil {
		return reply{}, false
	}

	r, _ := b.get(done.callback.String())

	return r, true
}

// sessionCookie returns the OpenArtifact session cookie the jar holds.
func (b *browser) sessionCookie(oa *dexOA) string {
	u, _ := url.Parse(oa.baseURL)

	for _, c := range b.client.Jar.Cookies(u) {
		if c.Name == auth.SessionCookieName {
			return c.Value
		}
	}

	return ""
}

// idTokenClaims decodes the payload of a JWT without verifying it: the test
// only inspects what Dex put in it; OpenArtifact verifies the real login.
func idTokenClaims(t *testing.T, jwt string) map[string]any {
	t.Helper()

	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("id_token is not a JWT: %.60q", jwt)
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("id_token payload: %v", err)
	}

	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("id_token claims: %v", err)
	}

	return claims
}

// dexDiscovery is the part of Dex's discovery document the test reads.
type dexDiscovery struct {
	Issuer           string   `json:"issuer"`
	AuthEndpoint     string   `json:"authorization_endpoint"`
	TokenEndpoint    string   `json:"token_endpoint"`
	EndSession       string   `json:"end_session_endpoint"`
	ScopesSupported  []string `json:"scopes_supported"`
	ClaimsSupported  []string `json:"claims_supported"`
	ResponseTypes    []string `json:"response_types_supported"`
	GrantTypes       []string `json:"grant_types_supported"`
	CodeChallengeAlg []string `json:"code_challenge_methods_supported"`
}

func fetchDiscovery(t *testing.T, issuer string) dexDiscovery {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	var d dexDiscovery
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatalf("decode discovery: %v", err)
	}

	return d
}

// probeClaims logs in at Dex directly (same client, redirect to the OpenArtifact
// callback that is never fetched), redeems the code and returns the ID token
// claims. It is how the test sees exactly what Dex asserts for an LDAP user.
func probeClaims(t *testing.T, oa *dexOA, disc dexDiscovery, scope, user, password string) map[string]any {
	t.Helper()

	b := newBrowser(t, oa.host)
	redirectURI := oa.baseURL + "/api/v1/auth/sso/oidc/" + dexTestSlug + "/callback"

	q := url.Values{
		"client_id":     {dexTestClientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {scope},
		"state":         {"probe-state"},
		"nonce":         {"probe-nonce"},
	}

	page := b.walk(disc.AuthEndpoint + "?" + q.Encode())

	done := b.submitLogin(page, user, password)
	if done.callback == nil {
		t.Fatalf("probe login for %s did not redirect back (status %d): %.300s", user, done.status, done.body)
	}

	code := done.callback.Query().Get("code")
	if code == "" || done.callback.Query().Get("state") != "probe-state" {
		t.Fatalf("probe callback has no code or the wrong state: %s", done.callback)
	}

	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, disc.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(dexTestClientID, dexTestClientSecret)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	var tok struct {
		IDToken string `json:"id_token"`
	}

	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, &tok); err != nil || tok.IDToken == "" {
		t.Fatalf("token response (%d) has no id_token: %s", resp.StatusCode, raw)
	}

	return idTokenClaims(t, tok.IDToken)
}

func claimStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))

	for _, e := range list {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}

	slices.Sort(out)

	return out
}

// userGroups returns the sorted names of the groups userID belongs to.
func userGroups(t *testing.T, pg *database.PostgresDB, userID string) []string {
	t.Helper()

	groups, err := pg.ListGroupsByUserUncached(t.Context(), userID)
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}

	names := make([]string, 0, len(groups))
	for _, g := range groups {
		names = append(names, g.Name)
	}

	slices.Sort(names)

	return names
}

func mustUser(t *testing.T, pg *database.PostgresDB, username string) *models.User {
	t.Helper()

	u, err := pg.GetUserByUsername(t.Context(), username)
	if err != nil {
		t.Fatalf("get user %s: %v", username, err)
	}

	return u
}

func countSessions(t *testing.T, pg *database.PostgresDB) int {
	t.Helper()

	var n int
	if err := pg.GetDB().QueryRowContext(t.Context(), `SELECT count(*) FROM auth_sessions`).Scan(&n); err != nil {
		t.Fatalf("count sessions: %v", err)
	}

	return n
}

func requireSession(t *testing.T, oa *dexOA, b *browser, userID string) {
	t.Helper()

	cookie := b.sessionCookie(oa)
	if cookie == "" {
		t.Fatal("no OpenArtifact session cookie after login")
	}

	sess, err := auth.ValidateSession(t.Context(), oa.pg, cookie)
	if err != nil || sess == nil {
		t.Fatalf("session cookie is not a valid session: %v", err)
	}

	if sess.UserID != userID {
		t.Fatalf("session belongs to %s, want %s", sess.UserID, userID)
	}
}

// requireLoginDenied checks a callback answered with a 303 to the sign-in page
// carrying the given sso_error code and issued no session cookie.
func requireLoginDenied(t *testing.T, b *browser, oa *dexOA, r reply, code string) {
	t.Helper()

	base, _ := url.Parse(oa.baseURL)
	loc := r.location(base)

	if r.status != http.StatusSeeOther || loc == nil || loc.Path != "/login" || loc.Query().Get("sso_error") != code {
		t.Fatalf("callback = %d %v, want 303 to /login?sso_error=%s", r.status, loc, code)
	}

	if b.sessionCookie(oa) != "" {
		t.Error("a denied login must not set a session cookie")
	}
}

// requireLoggedIn checks a callback answered with a 302 to the app root.
func requireLoggedIn(t *testing.T, oa *dexOA, r reply) {
	t.Helper()

	base, _ := url.Parse(oa.baseURL)

	if loc := r.location(base); r.status != http.StatusFound || loc == nil || loc.Path != "/" {
		t.Fatalf("callback = %d %v, want 302 to /", r.status, loc)
	}
}

// dexFixture is the running stack and what the steps learn along the way.
type dexFixture struct {
	oa      *dexOA
	issuer  string
	disc    dexDiscovery
	aliceID string
	local   *models.User
}

func TestDexLDAPLogin(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true") // Dex is on loopback

	oa, _ := newDexOA(t)
	issuer := startDex(t, oa.baseURL+"/api/v1/auth/sso/oidc/"+dexTestSlug+"/callback")
	oa.createProvider(t, issuer)

	f := &dexFixture{oa: oa, issuer: issuer, disc: fetchDiscovery(t, issuer)}
	if f.disc.Issuer != issuer {
		t.Fatalf("Dex advertises issuer %q, want %q", f.disc.Issuer, issuer)
	}

	t.Logf("dex discovery: scopes=%v claims=%v end_session_endpoint=%q",
		f.disc.ScopesSupported, f.disc.ClaimsSupported, f.disc.EndSession)

	// The steps share state (alice's account, the local account bob links to) and run in order.
	t.Run("what Dex asserts for an LDAP user", f.claims)
	t.Run("without the groups scope Dex sends no groups claim", f.noGroupsScope)
	t.Run("login provisions the user and syncs groups", f.provisioning)
	t.Run("a wrong password does not log in", f.wrongPassword)
	t.Run("link_by_email off: the address is already taken, login is denied", f.linkOff)
	t.Run("link_by_email on: the login binds the existing local user", f.linkOn)
	t.Run("logout", f.logout)
}

func (f *dexFixture) claims(t *testing.T) {
	claims := probeClaims(t, f.oa, f.disc, "openid email profile groups", "alice", "alice-pass")
	t.Logf("alice id_token claims: %v", claims)

	if claims["iss"] != f.issuer || claims["email"] != "alice@example.org" ||
		claims["preferred_username"] != "alice" || claims["name"] != "Alice Admin" {
		t.Errorf("unexpected identity claims: %v", claims)
	}

	if got := claimStrings(claims["groups"]); !slices.Equal(got, []string{"artifact-admins", "developers"}) {
		t.Errorf("groups = %v, want [artifact-admins developers]", got)
	}

	// Recorded in docs/security/sso.md: Dex's LDAP connector vouches for the
	// address it read from the directory.
	if claims["email_verified"] != true {
		t.Errorf("email_verified = %v, the docs say Dex sets true for LDAP users", claims["email_verified"])
	}
}

// OpenArtifact's default scopes are "openid email profile": a provider that
// does not add groups gets no group sync (the claim is absent, not empty).
func (f *dexFixture) noGroupsScope(t *testing.T) {
	claims := probeClaims(t, f.oa, f.disc, "openid email profile", "alice", "alice-pass")
	if _, ok := claims["groups"]; ok {
		t.Errorf("groups claim present without the groups scope: %v", claims["groups"])
	}
}

func (f *dexFixture) provisioning(t *testing.T) {
	b := newBrowser(t, f.oa.host)

	r, ok := b.loginViaOpenArtifact(f.oa, "alice", "alice-pass")
	if !ok {
		t.Fatal("Dex refused the right password")
	}

	requireLoggedIn(t, f.oa, r)

	u := mustUser(t, f.oa.pg, "alice")
	if u == nil {
		t.Fatal("user alice was not provisioned")
	}

	f.aliceID = u.ID

	if u.Email == nil || *u.Email != "alice@example.org" || u.FullName == nil || *u.FullName != "Alice Admin" {
		t.Errorf("alice email/name = %v/%v", u.Email, u.FullName)
	}

	if u.ProviderSlug == nil || *u.ProviderSlug != dexTestSlug || u.ExternalID == nil || *u.ExternalID == "" {
		t.Errorf("alice is not bound to the Dex identity: %+v", u)
	}

	if got := userGroups(t, f.oa.pg, u.ID); !slices.Equal(got, []string{dexAdminGroup, dexDevGroup}) {
		t.Errorf("alice groups = %v, want [%s %s]", got, dexAdminGroup, dexDevGroup)
	}

	requireSession(t, f.oa, b, u.ID)
}

func (f *dexFixture) wrongPassword(t *testing.T) {
	sessions := countSessions(t, f.oa.pg)
	b := newBrowser(t, f.oa.host)

	if _, ok := b.loginViaOpenArtifact(f.oa, "bob", "not-bobs-password"); ok {
		t.Fatal("Dex accepted a wrong password")
	}

	if mustUser(t, f.oa.pg, "bob") != nil {
		t.Error("a failed login provisioned bob")
	}

	if got := countSessions(t, f.oa.pg); got != sessions {
		t.Errorf("sessions = %d, want %d: a failed login must not create one", got, sessions)
	}

	if b.sessionCookie(f.oa) != "" {
		t.Error("a failed login set a session cookie")
	}
}

// linkOff: a local account owns bob's address before bob's first SSO login.
func (f *dexFixture) linkOff(t *testing.T) {
	f.local = &models.User{Username: "bob-local", Email: new("bob@example.org")}
	if _, err := f.oa.pg.CreateUser(t.Context(), f.local); err != nil {
		t.Fatalf("create local user: %v", err)
	}

	b := newBrowser(t, f.oa.host)

	r, ok := b.loginViaOpenArtifact(f.oa, "bob", "bob-pass")
	if !ok {
		t.Fatal("Dex refused the right password")
	}

	requireLoginDenied(t, b, f.oa, r, "sso_login_denied")

	if mustUser(t, f.oa.pg, "bob") != nil {
		t.Error("a denied login provisioned a second account for the address")
	}
}

func (f *dexFixture) linkOn(t *testing.T) {
	f.oa.setLinkByEmail(t, true)

	b := newBrowser(t, f.oa.host)

	r, ok := b.loginViaOpenArtifact(f.oa, "bob", "bob-pass")
	if !ok {
		t.Fatal("Dex refused the right password")
	}

	requireLoggedIn(t, f.oa, r)

	if mustUser(t, f.oa.pg, "bob") != nil {
		t.Error("link_by_email created a new account instead of linking")
	}

	linked := mustUser(t, f.oa.pg, "bob-local")
	if linked == nil || linked.ID != f.local.ID {
		t.Fatalf("local user lost: %+v", linked)
	}

	if linked.ProviderSlug == nil || *linked.ProviderSlug != dexTestSlug || linked.ExternalID == nil {
		t.Errorf("local user not bound to the Dex identity: %+v", linked)
	}

	// Only developers: the second user is not in artifact-admins.
	if got := userGroups(t, f.oa.pg, linked.ID); !slices.Equal(got, []string{dexDevGroup}) {
		t.Errorf("bob groups = %v, want [%s]", got, dexDevGroup)
	}

	if linked.ID == f.aliceID {
		t.Error("bob and alice share an account")
	}

	requireSession(t, f.oa, b, linked.ID)
}

func (f *dexFixture) logout(t *testing.T) {
	oa := f.oa
	b := newBrowser(t, oa.host)

	if _, ok := b.loginViaOpenArtifact(oa, "alice", "alice-pass"); !ok {
		t.Fatal("Dex refused the right password")
	}

	requireSession(t, oa, b, f.aliceID)

	cookie := b.sessionCookie(oa)
	before := countSessionsOf(t, oa.pg, f.aliceID)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		oa.baseURL+"/api/v1/auth/sso/oidc/"+dexTestSlug+"/logout", nil)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Origin", oa.baseURL)

	out := b.send(req)

	if f.disc.EndSession == "" {
		// Dex does not advertise RP-initiated logout: OpenArtifact ends its
		// own session only (204) and the Dex session is not touched.
		if out.status != http.StatusNoContent {
			t.Fatalf("logout = %d %s, want 204 (Dex has no end_session_endpoint)", out.status, out.body)
		}
	} else {
		var res struct {
			EndSessionURL string `json:"end_session_url"`
		}

		if out.status != http.StatusOK || json.Unmarshal(out.body, &res) != nil ||
			!strings.HasPrefix(res.EndSessionURL, f.disc.EndSession) {
			t.Fatalf("logout = %d %s, want 200 with an end_session_url under %s", out.status, out.body, f.disc.EndSession)
		}

		t.Logf("end_session_url: %s", res.EndSessionURL)
	}

	if sess, err := auth.ValidateSession(t.Context(), oa.pg, cookie); err != nil || sess != nil {
		t.Errorf("the session survived logout: %v %v", sess, err)
	}

	if got := countSessionsOf(t, oa.pg, f.aliceID); got != before-1 {
		t.Errorf("alice has %d session(s) after logout, want %d", got, before-1)
	}

	if b.sessionCookie(oa) != "" {
		t.Error("the browser still holds a session cookie after logout")
	}
}

// countSessionsOf counts the sessions of one user.
func countSessionsOf(t *testing.T, pg *database.PostgresDB, userID string) int {
	t.Helper()

	var n int
	if err := pg.GetDB().
		QueryRowContext(t.Context(), `SELECT count(*) FROM auth_sessions WHERE user_id = $1`, userID).
		Scan(&n); err != nil {
		t.Fatalf("count sessions of %s: %v", userID, err)
	}

	return n
}
