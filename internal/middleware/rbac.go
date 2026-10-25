// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/httpx"
	"github.com/openartifact/openartifact/internal/ocipath"
	"github.com/openartifact/openartifact/internal/pathseg"
	"github.com/openartifact/openartifact/pkg/logger"
)

type principalCtxKey struct{}

func WithPrincipal(ctx context.Context, p *auth.Principal) context.Context {
	return context.WithValue(ctx, principalCtxKey{}, p)
}

func PrincipalFromContext(ctx context.Context) *auth.Principal {
	p, _ := ctx.Value(principalCtxKey{}).(*auth.Principal)
	return p
}

type RBACConfig struct {
	LegacyAPIKey        string
	LegacyAPIKeyEnabled bool
	AllowAnonymousRead  bool
	// MCPPath is the mount path for the MCP HTTP endpoint (e.g. "/mcp").
	// It lives outside /api/, /v2/, and /repos/ but must never fall through
	// the SPA/static bypass below — MCP tools require auth like the REST API.
	MCPPath string
	// SPAHandler serves the web UI. Browser navigations to Vue-router page
	// shapes under /repos/ (which collide with format-handler routes such as
	// the npm packument GET /repos/{name}/{pkg}) are answered by it directly,
	// never by the format handler and never without going through RBAC for
	// any non-navigation request. When nil, those paths get full RBAC
	// evaluation like every other /repos/ path.
	SPAHandler http.Handler
	// BaseURL is the public URL of the server; cargo's WWW-Authenticate
	// challenge points its login_url at it.
	BaseURL string
	// MCPBearerVerifier accepts OIDC access tokens on the MCP endpoint in
	// addition to API tokens; nil accepts API tokens only.
	MCPBearerVerifier MCPBearerVerifier
	// ProjectSettings decides whether non-admin users may create projects
	// (POST /api/v1/projects); nil means only global admins may.
	ProjectSettings *auth.ProjectSettings
}

// routeScope is what the permission evaluator is asked about for a request.
type routeScope struct {
	action       string
	repoName     string
	artifactPath string
	// anyAuthenticated marks list-style endpoints whose handler filters the
	// result per repository itself; RBAC only requires a real (non-anonymous)
	// principal for them.
	anyAuthenticated bool
	// project and projectAction mark a control-plane route of one project
	// (/api/v1/projects/{project}/...): the caller needs a project role that
	// allows the action (a global admin always does). projectsRoot marks
	// GET/POST /api/v1/projects, whose handlers filter or check per caller.
	project       string
	projectAction auth.ProjectAction
	projectsRoot  bool
	// delegateRepo names a repository whose administration is delegated to the
	// maintainers and owners of its project (auth.EvaluateRepoAdmin): the route
	// passes for a caller who satisfies the scope above (global manage, or the
	// repo-scoped one when repoName is set) or administers that repository.
	delegateRepo string
	// bundles marks a release-bundle route (/api/v1/bundles/{name}/{version}...).
	// The repositories involved live in the request body or the stored items,
	// so RBAC only requires a real principal; the handler authorizes against
	// each repository (read on the sources, write on the target) and the
	// bundle's owner.
	bundles bool
}

func RBACMiddleware(cfg RBACConfig, db database.Database, evaluator *auth.Evaluator, log *logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			// The repository RBAC authorizes must be the repository the router
			// and handler will serve: refuse dot-dot segments and encoded
			// separators in the segments that select it.
			if hasAmbiguousPath(r.URL) {
				httpx.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_path"})

				return
			}

			if isMCPPath(r.URL.Path, cfg) {
				serveMCP(w, r, next, cfg, db, log)

				return
			}

			principal, credential := resolvePrincipal(ctx, r, cfg, db, log)
			r = r.WithContext(WithPrincipal(ctx, principal))

			if serveStaticOrSPA(w, r, next, cfg) {
				return
			}

			if passwordChangeBlocked(r, principal) {
				httpx.WriteJSON(w, http.StatusForbidden, map[string]string{
					"error":      "password_change_required",
					"change_url": "/api/v1/auth/change-password",
				})

				return
			}

			// Self-service endpoints operate solely on the caller's own account
			// and don't require any permission target.
			if isSelfServiceRequest(r, principal) {
				next.ServeHTTP(w, r)
				return
			}

			scope := classifyRoute(r)

			if scope.isProjectRoute() {
				if authorizeProjectRoute(w, r, cfg, evaluator, log, principal, credential, scope, db) {
					next.ServeHTTP(w, r)
				}

				return
			}

			// A project token's service account is a repository credential
			// only: it gets no /api/v1 list endpoint, not even a filtered one.
			if (((scope.anyAuthenticated || scope.bundles) && principal.ServiceProjectID == "") || isDockerRootless(r)) && !principal.IsAnonymous {
				next.ServeHTTP(w, r)
				return
			}

			allowed, err := evaluateRepoScope(ctx, evaluator, principal, scope)
			if err == nil && !allowed && scope.delegateRepo != "" && writeDelegatedHidden(w, r, evaluator, principal, scope.delegateRepo) {
				return
			}

			if err != nil {
				log.Errorw("auth evaluation error",
					"error", err,
					"path", r.URL.Path,
					"method", r.Method,
				)
				httpx.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "auth_evaluation_failed"})

				return
			}

			if !allowed {
				writeAccessDenied(w, r, cfg, db, log, principal, credential, scope)

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// serveStaticOrSPA serves static assets and SPA page loads without
// authorization. Only GET/HEAD can be an SPA page load: every other method on
// any path gets the full evaluation, so an SPA-shaped URL can never be used to
// reach a mutating format handler unauthenticated. It reports whether it served.
func serveStaticOrSPA(w http.ResponseWriter, r *http.Request, next http.Handler, cfg RBACConfig) bool {
	if !isSafeMethod(r.Method) {
		return false
	}

	if isStaticPath(r.URL.Path, cfg) {
		next.ServeHTTP(w, r)
		return true
	}

	if cfg.SPAHandler != nil && isSPANavigation(r) {
		cfg.SPAHandler.ServeHTTP(w, r)
		return true
	}

	return false
}

// evaluateRepoScope runs the regular check of scope, then, for a delegated
// repository route, the project-delegated repo.admin check.
func evaluateRepoScope(ctx context.Context, evaluator *auth.Evaluator, principal *auth.Principal, scope routeScope) (bool, error) {
	allowed, err := evaluator.Evaluate(ctx, *principal, scope.repoName, scope.artifactPath, scope.action)
	if err != nil || allowed || scope.delegateRepo == "" {
		return allowed, err
	}

	return evaluator.EvaluateRepoAdmin(ctx, *principal, scope.delegateRepo)
}

// writeDelegatedHidden answers a denied delegated repository route with 404 when
// the caller cannot even read the repository (it looks like an unknown one, so
// a maintainer of another project learns nothing); a caller who can read it
// gets the regular 403 from the caller. It reports whether it wrote.
func writeDelegatedHidden(w http.ResponseWriter, r *http.Request, evaluator *auth.Evaluator, principal *auth.Principal, repoName string) bool {
	if principal.IsAnonymous {
		return false
	}

	canRead, err := evaluator.Evaluate(r.Context(), *principal, repoName, "", "read")
	if err != nil || canRead {
		return false
	}

	httpx.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not_found", "code": "not_found"})

	return true
}

// isDockerPing reports whether r is the distribution API version check
// (GET|HEAD /v2/). It names no repository, so it authorizes as "any
// authenticated principal"; the handler answers a static 200.
func isDockerPing(r *http.Request) bool {
	return isSafeMethod(r.Method) && r.URL.Path == "/v2/"
}

// isDockerRootless reports whether r is a registry request that names no
// repository: the ping or the catalog (GET|HEAD /v2/_catalog). Any
// authenticated principal passes; the catalog handler lists only the images the
// principal may read.
func isDockerRootless(r *http.Request) bool {
	return isDockerPing(r) || (isSafeMethod(r.Method) && r.URL.Path == "/v2/_catalog")
}

func isSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// isStaticPath reports whether urlPath is plain SPA/static content that needs
// no RBAC: neither API, OCI, MCP, nor anything under /repos/. Callers must
// additionally restrict this to GET/HEAD.
func isStaticPath(urlPath string, cfg RBACConfig) bool {
	return !strings.HasPrefix(urlPath, "/api/") && !strings.HasPrefix(urlPath, "/v2/") && !isMCPPath(urlPath, cfg) &&
		!strings.HasPrefix(urlPath, "/repos/")
}

// isSPANavigation reports whether r is a browser page navigation to one of the
// exact Vue-router page shapes under /repos/. It requires the browser's own
// navigation signals so a package-manager request for a package that happens
// to be called "edit"/"browse"/"graph" is never treated as a page load.
func isSPANavigation(r *http.Request) bool {
	// Segment on the escaped path: package names containing "/" (scoped npm,
	// docker, go modules) arrive as %2F and must stay one segment.
	escaped := r.URL.EscapedPath()
	if !strings.HasPrefix(escaped, "/repos/") ||
		!isSPARepoPath(strings.Split(strings.Trim(escaped, "/"), "/")) {
		return false
	}

	if r.Header.Get("Authorization") != "" || !strings.Contains(r.Header.Get("Accept"), "text/html") {
		return false
	}

	mode := r.Header.Get("Sec-Fetch-Mode")

	return mode == "" || mode == "navigate"
}

// hasAmbiguousPath reports whether the URL could make the RBAC decision and the
// router/handler disagree about which repository is addressed: any ".." path
// segment (decoded or not), and an encoded "/" or "\" inside the segments that
// name the repository of a /repos/ or /v2/ request.
func hasAmbiguousPath(u *url.URL) bool {
	escaped := u.EscapedPath()

	if pathseg.HasParentRaw(escaped) {
		return true
	}

	if strings.HasPrefix(u.Path, "/v2/") {
		return hasEncodedSeparator(escaped)
	}

	if after, ok := strings.CutPrefix(escaped, "/repos/"); ok {
		segs := strings.SplitN(after, "/", 3)

		if hasEncodedSeparator(segs[0]) {
			return true
		}

		if len(segs) > 1 && slices.Contains(isolatedRepoNamespaces(), segs[0]) && hasEncodedSeparator(segs[1]) {
			return true
		}
	}

	return false
}

func hasEncodedSeparator(s string) bool {
	s = strings.ToLower(s)

	return strings.Contains(s, "%2f") || strings.Contains(s, "%5c")
}

// passwordChangeBlocked reports whether the principal must change its password
// first and the request is not one of the two endpoints allowed in that state.
func passwordChangeBlocked(r *http.Request, principal *auth.Principal) bool {
	if !principal.MustChangePassword || principal.IsAnonymous || principal.IsLegacy {
		return false
	}

	return r.Method != http.MethodPost ||
		(r.URL.Path != "/api/v1/auth/change-password" && r.URL.Path != "/api/v1/auth/logout")
}

func writeAccessDenied(w http.ResponseWriter, r *http.Request, cfg RBACConfig, db database.Database, log *logger.Logger, principal *auth.Principal, credential string, scope routeScope) {
	principalName := "anonymous"
	if principal.Username != "" {
		principalName = principal.Username
	}

	log.Warnw("access denied",
		"event", "auth_deny",
		"principal", principalName,
		"credential", credential,
		"repo", scope.repoName,
		"path", scope.artifactPath,
		"action", scope.action,
	)

	if principal.IsAnonymous {
		// The OCI registry has no token service: a Bearer challenge there makes
		// docker/podman fetch a token from the realm, which is not a URL, and
		// fail. Offer only Basic on /v2/ so clients send their credentials.
		if !strings.HasPrefix(r.URL.Path, "/v2/") && r.URL.Path != "/v2" {
			w.Header().Add("WWW-Authenticate", `Bearer realm="openartifact"`)
		}

		// Package managers (maven, pip, helm, docker) only send credentials
		// after a Basic challenge. Never challenge /api/ with Basic: browsers
		// would pop a native login dialog over the SPA on every expired session.
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Add("WWW-Authenticate", `Basic realm="openartifact"`)
		}

		// Cargo only sends its token after a challenge naming its login page
		// (RFC 3139 auth-required flow).
		if scope.repoName != "" && cfg.BaseURL != "" {
			if repo, err := db.GetRepositoryByName(r.Context(), scope.repoName); err == nil && repo != nil && repo.Format == "cargo" {
				w.Header().Add("WWW-Authenticate", `Cargo login_url="`+strings.TrimRight(cfg.BaseURL, "/")+`/login"`)
			}
		}

		httpx.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})

		return
	}

	httpx.WriteJSON(w, http.StatusForbidden, map[string]any{
		"error":  "forbidden",
		"action": scope.action,
		"path":   scope.artifactPath,
	})
}

// principalForUser builds the request principal of an authenticated user.
func principalForUser(ctx context.Context, db database.Database, log *logger.Logger, user principalUser) *auth.Principal {
	groups, err := db.ListGroupsByUser(ctx, user.id)
	if err != nil {
		// Fewer groups can only mean fewer rights: fail closed.
		log.Warnw("list groups for principal failed", "error", err, "user_id", user.id)
	}

	groupIDs := make([]string, 0, len(groups))
	for _, g := range groups {
		groupIDs = append(groupIDs, g.ID)
	}

	userID := user.id

	return &auth.Principal{
		UserID:             &userID,
		Username:           user.username,
		GroupIDs:           groupIDs,
		MustChangePassword: user.mustChangePassword,
		ServiceProjectID:   user.serviceProject,
	}
}

type principalUser struct {
	id                 string
	username           string
	mustChangePassword bool
	// serviceProject is the project of a service account ("" for humans).
	serviceProject string
}

// resolvePrincipal authenticates the request. Credential precedence: session
// cookie, then token (Authorization: Bearer or Basic password), then the
// deprecated legacy API key. It returns the principal and the credential kind
// (also stored in principal.Credential so CSRF can tell ambient cookie auth
// from explicit header auth).
func resolvePrincipal(ctx context.Context, r *http.Request, cfg RBACConfig, db database.Database, log *logger.Logger) (*auth.Principal, string) {
	if p := sessionPrincipal(ctx, r, db, log); p != nil {
		return p, auth.CredentialSession
	}

	bearer, basicSecret := authorizationSecrets(r)

	for _, c := range []struct {
		value string
		kind  string
	}{
		{bearer, auth.CredentialBearerToken},
		{basicSecret, auth.CredentialBasicToken},
	} {
		if p := tokenPrincipal(ctx, c.value, c.kind, db, log); p != nil {
			return p, c.kind
		}
	}

	if cfg.LegacyAPIKeyEnabled && cfg.LegacyAPIKey != "" {
		candidate := extractLegacyKey(r, bearer, basicSecret)
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(cfg.LegacyAPIKey)) == 1 {
			log.Warnw("request authenticated via deprecated API_KEY; migrate to user tokens",
				"event", "legacy_api_key_used",
				"path", r.URL.Path,
				"method", r.Method,
				"remote_addr", r.RemoteAddr,
			)

			return &auth.Principal{
				Username:   "system/legacy-api-key",
				IsLegacy:   true,
				Credential: auth.CredentialLegacy,
			}, auth.CredentialLegacy
		}
	}

	return &auth.Principal{IsAnonymous: true, Credential: auth.CredentialNone}, auth.CredentialNone
}

func sessionPrincipal(ctx context.Context, r *http.Request, db database.Database, log *logger.Logger) *auth.Principal {
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}

	session, err := auth.ValidateSession(ctx, db, cookie.Value)
	if err != nil || session == nil {
		return nil
	}

	user, err := db.GetUserByID(ctx, session.UserID)
	// Service accounts have no session; never accept one.
	if err != nil || user == nil || user.Disabled || user.IsService() {
		return nil
	}

	p := principalForUser(ctx, db, log, principalUser{id: user.ID, username: user.Username, mustChangePassword: user.MustChangePassword})
	p.Credential = auth.CredentialSession

	// Sliding expiry: one bounded write per session per minute, never beyond
	// the absolute session lifetime. No per-request goroutine.
	if extErr := auth.TouchSession(ctx, db, session); extErr != nil {
		log.Debugw("touch session error", "error", extErr, "session_id", session.ID)
	}

	return p
}

func tokenPrincipal(ctx context.Context, tokenValue, kind string, db database.Database, log *logger.Logger) *auth.Principal {
	if !strings.HasPrefix(tokenValue, auth.TokenPrefixAccess) && !strings.HasPrefix(tokenValue, auth.TokenPrefixDeploy) {
		return nil
	}

	token, user, err := auth.ValidateToken(ctx, db, tokenValue)
	if err != nil {
		log.Warnw("token validation error", "error", err)
		return nil
	}

	// Failed validations are intentionally not audited: an unauthenticated
	// caller must not be able to write one audit row per request.
	if token == nil || user == nil {
		return nil
	}

	pu := principalUser{id: user.ID, username: user.Username, mustChangePassword: user.MustChangePassword}

	if user.IsService() {
		// A project token carries its project; anything else is inconsistent
		// and gets no access.
		if user.ProjectID == nil || token.ProjectID == nil || *user.ProjectID != *token.ProjectID || token.Kind != auth.TokenKindDeploy {
			return nil
		}

		pu.serviceProject = *user.ProjectID
	}

	p := principalForUser(ctx, db, log, pu)
	p.Credential = kind
	p.TokenKind = token.Kind
	p.TokenScopes = token.Scopes

	auth.TouchToken(ctx, db, token)

	return p
}

// authorizationSecrets extracts the credential strings carried by the
// Authorization header: the Bearer value, and the password part of Basic
// credentials (package managers send tokens as `user:<token>`).
func authorizationSecrets(r *http.Request) (string, string) {
	header := r.Header.Get("Authorization")

	if after, ok := strings.CutPrefix(header, "Bearer "); ok {
		return strings.TrimSpace(after), ""
	}

	if strings.HasPrefix(header, "Basic ") {
		if _, password, ok := r.BasicAuth(); ok {
			return "", password
		}
	}

	return "", ""
}

func extractLegacyKey(r *http.Request, bearer, basicSecret string) string {
	if v := r.Header.Get("X-Api-Key"); v != "" {
		return v
	}

	if bearer != "" {
		return bearer
	}

	return basicSecret
}

// isSelfServiceRequest reports whether the request targets one of the endpoints
// that operate solely on the caller's own account and therefore bypass the
// permission evaluator. Everything except reading one's own identity requires
// an interactive session: API tokens must not be able to mint further tokens
// or change passwords, and the legacy key has no account at all.
func isSelfServiceRequest(r *http.Request, principal *auth.Principal) bool {
	// A service account has no profile; it falls through to the evaluator and
	// is refused on every /api/v1 route.
	if principal.IsAnonymous || principal.ServiceProjectID != "" {
		return false
	}

	method, path := r.Method, r.URL.Path

	if path == "/api/v1/auth/me" {
		return method == http.MethodGet
	}

	if principal.Credential != auth.CredentialSession || principal.UserID == nil {
		return false
	}

	switch path {
	case "/api/v1/auth/change-password", "/api/v1/auth/logout":
		return method == http.MethodPost
	case "/api/v1/auth/tokens":
		return method == http.MethodGet || method == http.MethodPost
	}

	if id, ok := strings.CutPrefix(path, "/api/v1/auth/tokens/"); ok {
		return method == http.MethodDelete && isSingleSegment(id)
	}

	rest, ok := strings.CutPrefix(path, "/api/v1/users/")
	if !ok {
		return false
	}

	segs := strings.Split(rest, "/")
	if segs[0] != *principal.UserID {
		return false
	}

	switch len(segs) {
	case 1:
		return method == http.MethodPatch
	case 2:
		return segs[1] == "tokens" && (method == http.MethodGet || method == http.MethodPost)
	case 3:
		return segs[1] == "tokens" && segs[2] != "" && method == http.MethodDelete
	}

	return false
}

func isSingleSegment(s string) bool {
	return s != "" && !strings.Contains(s, "/")
}

// methodToAction maps an HTTP method to a permission action. Every /api/v1/
// path is a management endpoint ("manage"); classifyRoute narrows the few
// repository-scoped API routes.
func methodToAction(method, path string) string {
	if strings.HasPrefix(path, "/api/v1/") {
		return "manage"
	}

	return actionForMethod(method)
}

func actionForMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return "read"
	case http.MethodDelete:
		return "delete"
	default:
		return "write"
	}
}

// classifyRoute decides which (action, repository, path) the evaluator checks.
func classifyRoute(r *http.Request) routeScope {
	if strings.HasPrefix(r.URL.Path, "/api/v1/") {
		return classifyAPIRoute(r.Method, r.URL.EscapedPath())
	}

	repoName, artifactPath := extractRepoAndPath(r.URL.Path)

	action := methodToAction(r.Method, r.URL.Path)
	if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/-rev/") {
		// `npm unpublish pkg@ver` PUTs the reduced packument to /pkg/-rev/<rev>;
		// versions missing from the body are removed, so it needs "delete".
		action = "delete"
	}

	if isConanV1Delete(r.Method, artifactPath) {
		action = "delete"
	}

	return routeScope{action: action, repoName: repoName, artifactPath: artifactPath}
}

// isConanV1Delete reports whether the request is one of the Conan 1 POSTs that
// delete (`packages/delete`, `remove_files`): a POST would otherwise need only
// "write".
func isConanV1Delete(method, artifactPath string) bool {
	if method != http.MethodPost || !strings.HasPrefix(artifactPath, "v1/conans/") {
		return false
	}

	return strings.HasSuffix(artifactPath, "/packages/delete") || strings.HasSuffix(artifactPath, "/remove_files")
}

// repoDataSegments are the sub-resources of /api/v1/repositories/{name}/ that
// carry package data (upload/list/download/delete). Their handlers verify the
// package belongs to {name}, so they are authorized against that repository
// with a data action instead of the global "manage" permission.
func repoDataSegments() []string {
	return []string{"debs", "packages", "comps", "uploads"}
}

func classifyAPIRoute(method, escapedPath string) routeScope {
	manage := routeScope{action: "manage"}

	segs := strings.Split(strings.Trim(strings.TrimPrefix(escapedPath, "/api/v1/"), "/"), "/")

	switch {
	case isSafeMethod(method) && len(segs) == 1 && (segs[0] == "repositories" || segs[0] == "search"):
		// Handlers filter per repository (RepoReadFilter / per-hit Evaluate).
		// The action stays "manage" so that an anonymous principal, which the
		// pass-through does not cover, can never satisfy it (the handlers'
		// anonymous branch is unfiltered).
		return routeScope{action: "manage", anyAuthenticated: true}
	case isSafeMethod(method) && len(segs) == 2 && segs[0] == "repository" && isPlainName(segs[1]):
		return routeScope{action: "read", repoName: segs[1]}
	case len(segs) >= 3 && segs[0] == "repositories" && isPlainName(segs[1]) && slices.Contains(repoDataSegments(), segs[2]):
		switch method {
		case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete:
			return routeScope{action: actionForMethod(method), repoName: segs[1]}
		}
	case isCIRoute(method, segs):
		return classifyCIRoute(method, segs)
	case isRepoTrashEmptyRoute(method, segs):
		// Emptying a repository's trash destroys data for good: repo `manage`,
		// or the repository's project maintainers and owners.
		return routeScope{action: "manage", repoName: segs[1], delegateRepo: segs[1]}
	case delegatedRepoName(method, segs) != "":
		return routeScope{action: "manage", delegateRepo: delegatedRepoName(method, segs)}
	case isRepoTrashRoute(method, segs):
		// Viewing and restoring a repository's trash is part of managing its
		// deletions: the repo-scoped `delete` permission, whatever the method.
		// (Purging one item and the global /api/v1/trash routes stay administrative;
		// emptying the whole trash is the separate manage route above.)
		return routeScope{action: "delete", repoName: segs[1]}
	case segs[0] == "projects":
		if scope, ok := classifyProjectRoute(method, segs); ok {
			return scope
		}
	}

	return manage
}

// delegatedRepoName returns the repository of a route whose administration is
// delegated to project maintainers and owners (global manage still passes):
// PATCH/DELETE repository/{name}, the cleanup-policy routes and the quota
// routes of the repository, or "" for any other route. The one exception in the
// quota family, POST .../quota/reconcile, stays administrative.
func delegatedRepoName(method string, segs []string) string {
	switch {
	case len(segs) == 2 && segs[0] == "repository" && (method == http.MethodPatch || method == http.MethodDelete):
	case len(segs) >= 3 && len(segs) <= 5 && segs[0] == "repositories" && segs[2] == "cleanup-policies":
	case len(segs) == 3 && segs[0] == "repositories" && segs[2] == "quota" && method != http.MethodPost:
	default:
		return ""
	}

	if !isPlainName(segs[1]) {
		return ""
	}

	if slices.Contains(segs[2:], "") {
		return ""
	}

	return segs[1]
}

// isCIRoute reports whether segs (of an /api/v1/ path) is one of the routes
// the CI commands of oa use with a repository-scoped token: artifact
// properties and the per-bundle routes.
func isCIRoute(method string, segs []string) bool {
	return isRepoPropertiesRoute(method, segs) || isBundleRoute(method, segs)
}

// classifyCIRoute scopes an isCIRoute route. Artifact properties are metadata of
// the repository's own artifacts: reading needs `read` on the repository,
// changing them `write`. A bundle route is decided by its handler, because the
// repositories involved are in the request body or the stored items; the
// middleware only lets an authenticated principal through.
func classifyCIRoute(method string, segs []string) routeScope {
	if isBundleRoute(method, segs) {
		return routeScope{action: "manage", bundles: true}
	}

	return routeScope{action: actionForMethod(propertiesMethod(method)), repoName: segs[1]}
}

// isRepoPropertiesRoute reports whether segs (of an /api/v1/ path) is one of the
// artifact-property routes of a repository: GET/PUT/PATCH
// repositories/{name}/artifacts/{id}/properties, DELETE
// repositories/{name}/artifacts/{id}/properties/{key} and GET
// repositories/{name}/artifacts/search.
func isRepoPropertiesRoute(method string, segs []string) bool {
	if len(segs) < 4 || segs[0] != "repositories" || !isPlainName(segs[1]) || segs[2] != "artifacts" {
		return false
	}

	switch {
	case len(segs) == 4 && segs[3] == "search":
		return isSafeMethod(method)
	case len(segs) == 5 && isSingleSegment(segs[3]) && segs[4] == "properties":
		return isSafeMethod(method) || method == http.MethodPut || method == http.MethodPatch
	case len(segs) == 6 && isSingleSegment(segs[3]) && segs[4] == "properties" && isSingleSegment(segs[5]):
		return method == http.MethodDelete
	}

	return false
}

// propertiesMethod maps a property route's method to the one that decides its
// action: removing a property edits metadata (`write`), it is not a package delete.
func propertiesMethod(method string) string {
	if method == http.MethodDelete {
		return http.MethodPut
	}

	return method
}

// isBundleRoute reports whether segs (of an /api/v1/ path) is a per-bundle
// route: everything under bundles/{name}/{version}. The bundle list
// (GET bundles), pipelines and any other shape stay administrative; POST
// bundles (create) is part of the delegated set.
func isBundleRoute(method string, segs []string) bool {
	if len(segs) < 1 || segs[0] != "bundles" || slices.Contains(segs, "") {
		return false
	}

	switch len(segs) {
	case 1:
		return method == http.MethodPost
	case 3:
		return method == http.MethodGet || method == http.MethodDelete
	case 4:
		switch segs[3] {
		case "items", "promotions", "manifest", "manifest.asc":
			return isSafeMethod(method) || (segs[3] == "items" && method == http.MethodPost)
		case "seal", "promote", "archive":
			return method == http.MethodPost
		}
	case 5:
		return segs[3] == "items" && method == http.MethodDelete
	}

	return false
}

// isRepoTrashEmptyRoute reports whether segs is DELETE repositories/{name}/trash
// (empty a repository's trash).
func isRepoTrashEmptyRoute(method string, segs []string) bool {
	return method == http.MethodDelete && len(segs) == 3 && segs[0] == "repositories" && isPlainName(segs[1]) && segs[2] == "trash"
}

// isRepoTrashRoute reports whether segs (of an /api/v1/ path) is one of the
// repository-scoped trash routes: GET repositories/{name}/trash and POST
// repositories/{name}/trash/{kind}/{id}/restore. Anything else under
// .../trash (a purge, odd shapes) falls back to the administrative default.
func isRepoTrashRoute(method string, segs []string) bool {
	if len(segs) < 3 || segs[0] != "repositories" || !isPlainName(segs[1]) || segs[2] != "trash" {
		return false
	}

	switch len(segs) {
	case 3:
		return isSafeMethod(method)
	case 6:
		return method == http.MethodPost && isSingleSegment(segs[3]) && isSingleSegment(segs[4]) && segs[5] == "restore"
	}

	return false
}

// isPlainName reports whether an escaped path segment is a repository name
// that decodes to itself (no percent-escapes), so the name RBAC authorizes is
// the name the handler reads.
func isPlainName(seg string) bool {
	return seg != "" && !strings.Contains(seg, "%")
}

// isolatedRepoNamespaces are literal path markers registered as their own
// /repos/<marker>/{name}/… subtree (main.go isolates them to avoid shadowing
// the generic /repos/{name}/… format-handler block). A repository can never
// present a Vue-router page under these — they're always API traffic.
func isolatedRepoNamespaces() []string {
	return []string{"raw", "maven", "sbt", "ivy"}
}

// isSPARepoPath reports whether segments (from a "/repos/…" path, split on
// "/") is one of the exact Vue-router page shapes rather than a
// format-handler API call. Everything not explicitly listed here requires
// full RBAC evaluation — deny-by-default, no length-based guessing.
func isSPARepoPath(segments []string) bool {
	if len(segments) < 2 || slices.Contains(isolatedRepoNamespaces(), segments[1]) {
		return false
	}

	switch len(segments) {
	case 2: // /repos/{name}, /repos/new
		return true
	case 3: // /repos/{name}/edit|settings|browse|graph|trash
		switch segments[2] {
		case "edit", "settings", "browse", "graph", "trash":
			return true
		}

		return false
	case 5: // /repos/{name}/packages/{format}/{pkg}
		return segments[2] == "packages"
	default:
		return false
	}
}

// splitDockerPath maps the portion of a /v2/… URL after the "/v2/" prefix to
// the (repository, artifact path) pair RBAC scopes against. The distribution
// name's first segment is the OpenArtifact repository; the remaining segments
// are the image inside it and lead the artifact path
// ("org/team/app/manifests/1"). The parse is shared with the registry handler
// (ocipath), so RBAC authorizes the repository the handler serves.
func splitDockerPath(rest string) (string, string) {
	// "" is the version check and "_catalog" the catalog: neither names a
	// repository (a bare repository name is never a registry endpoint).
	if rest == "" || rest == "_catalog" {
		return "", ""
	}

	req := ocipath.Parse(rest)
	repo, _ := ocipath.SplitName(req.Name)

	return repo, ocipath.Resource(rest, req.Name)
}

// RepoAndPath maps a request path to the (repository, artifact path) pair the
// permission evaluator scopes against, so code serving a repository on behalf
// of the router sees exactly what RBAC authorized.
func RepoAndPath(urlPath string) (string, string) {
	return extractRepoAndPath(urlPath)
}

// extractRepoAndPath maps a request path to the (repository, artifact path)
// pair the permission evaluator scopes against. It understands the three
// URL shapes the server actually registers: OCI /v2/… routes, the isolated
// /repos/<marker>/{name}/… namespaces, and the generic /repos/{name}/….
func extractRepoAndPath(urlPath string) (string, string) {
	if rest, ok := strings.CutPrefix(urlPath, "/v2/"); ok {
		return splitDockerPath(rest)
	}

	const repoPrefix = "/repos/"

	after, ok := strings.CutPrefix(urlPath, repoPrefix)
	if !ok {
		return "", urlPath
	}

	for _, marker := range isolatedRepoNamespaces() {
		rest, ok := strings.CutPrefix(after, marker+"/")
		if !ok {
			continue
		}

		before, sub, ok := strings.Cut(rest, "/")
		if !ok {
			return before, ""
		}

		return before, sub
	}

	before, sub, ok := strings.Cut(after, "/")
	if !ok {
		return after, ""
	}

	return before, sub
}
