// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/openartifact/openartifact/internal/ai"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

// stackDB is the persistence of the full-stack tests: the mcp fixtures plus
// what the real RBAC middleware and permission evaluator read (tokens, users,
// sessions, permission targets, SSO providers).
type stackDB struct {
	mockDB

	users     map[string]*models.User
	tokens    map[string]*models.Token // by lookup digest
	sessions  map[string]*models.Session
	targets   map[string][]*models.PermissionTarget // by user id
	providers []*models.SSOProvider
	linked    map[string]*models.User // "slug/sub" -> user
}

func (d *stackDB) GetUserByID(_ context.Context, id string) (*models.User, error) {
	return d.users[id], nil
}

func (d *stackDB) GetTokenByHash(_ context.Context, hash string) (*models.Token, error) {
	return d.tokens[hash], nil
}

func (d *stackDB) CreateToken(_ context.Context, t *models.Token) (*models.Token, error) {
	t.ID = "tok-" + t.Name
	d.tokens[t.TokenHash] = t

	return t, nil
}

func (d *stackDB) GetSessionByTokenHash(_ context.Context, hash string) (*models.Session, error) {
	return d.sessions[hash], nil
}

func (d *stackDB) CreateSession(_ context.Context, s *models.Session) (*models.Session, error) {
	s.ID = "sess-1"
	d.sessions[s.TokenHash] = s

	return s, nil
}

func (d *stackDB) ListGroupsByUser(context.Context, string) ([]*models.Group, error) {
	return nil, nil
}

func (d *stackDB) GetPermissionTargetsForPrincipal(_ context.Context, userID string, _ []string) ([]*models.PermissionTarget, error) {
	return d.targets[userID], nil
}

func (d *stackDB) GetPermissionTargetByName(context.Context, string) (*models.PermissionTarget, error) {
	return nil, nil
}

func (d *stackDB) ListEnabledSSOProviders(context.Context) ([]*models.SSOProvider, error) {
	return d.providers, nil
}

func (d *stackDB) GetUserByExternalID(_ context.Context, sub, slug string) (*models.User, error) {
	return d.linked[slug+"/"+sub], nil
}

func (d *stackDB) GetRepoComponentBlob(_ context.Context, repoID, id string) (*models.ComponentBlob, error) {
	for _, c := range d.components {
		if c.RepositoryID == repoID && c.ID == id {
			return &models.ComponentBlob{ID: c.ID, Format: c.Format, Filename: c.Filename, Name: c.Name, Version: c.Version}, nil
		}
	}

	return nil, nil
}

const (
	// Permission targets of the stack: a read-only one on two repositories and
	// an administrator one on everything.
	targetReadOnly = "t-ro"
	targetAdmin    = "t-admin"
)

// stack is the real request path of the server: Origin guard and public
// metadata routes on the bare router, then the RBAC middleware, CSRF guard and
// the MCP handler mounted on the authenticated sub-router, as in cmd/server.
type stack struct {
	t       *testing.T
	db      *stackDB
	mcp     *Server
	handler http.Handler
	secrets map[string]string // user name -> API token
}

type stackOptions struct {
	server       Options
	origins      []string
	baseURL      string
	bearerVerify middleware.MCPBearerVerifier
	aiEnabled    bool
}

func newStack(t *testing.T, mutate func(*stackOptions)) *stack {
	t.Helper()

	opts := stackOptions{baseURL: baseURL, server: Options{Version: "1.2.3"}}
	if mutate != nil {
		mutate(&opts)
	}

	log := &logger.Logger{SugaredLogger: zap.NewNop().Sugar()}

	db := &stackDB{
		mockDB:   *fixtureDB(),
		users:    map[string]*models.User{},
		tokens:   map[string]*models.Token{},
		sessions: map[string]*models.Session{},
		targets:  map[string][]*models.PermissionTarget{},
		linked:   map[string]*models.User{},
	}

	opts.server.BaseURL = opts.baseURL
	if opts.baseURL != "" {
		opts.server.ResourceMetadataURL = middleware.ProtectedResourceMetadataURL(opts.baseURL, "/mcp")
	}

	var aiWorker *worker.AIAnalysisWorker
	if opts.aiEnabled {
		aiWorker = worker.NewAIAnalysisWorker(db, ai.NewClient(ai.Config{Provider: "custom", BaseURL: "http://127.0.0.1:1"}), log).WithDebounce(time.Hour)
		t.Cleanup(aiWorker.Stop)
	}

	s := NewServer(db, aiWorker, log, opts.server)
	evaluator := auth.NewEvaluator(db, log, false)

	s.SetAuthorizer(func(ctx context.Context, repoName, action string) bool {
		p := middleware.PrincipalFromContext(ctx)
		if p == nil {
			return false
		}

		ok, err := evaluator.Evaluate(ctx, *p, repoName, "", action)

		return err == nil && ok
	})
	s.SetReadFilter(func(ctx context.Context) (func(*models.Repository) (bool, error), error) {
		return evaluator.RepoReadFilter(ctx, *middleware.PrincipalFromContext(ctx))
	})
	s.SetSearcher(fakeSearcher{})

	st := &stack{t: t, db: db, mcp: s, secrets: map[string]string{}}
	st.addUser("reader", targetReadOnly, []string{targetReadOnly})
	st.addUser("admin", targetAdmin, nil)

	rbacCfg := middleware.RBACConfig{MCPPath: "/mcp", BaseURL: opts.baseURL, MCPBearerVerifier: opts.bearerVerify}

	router := chi.NewRouter()
	router.Use(OriginMiddleware("/mcp", opts.baseURL, opts.origins))

	meta := NewMetadataHandler(db, log, opts.baseURL, "/mcp")
	router.Handle("/.well-known/oauth-protected-resource", meta)
	router.Handle("/.well-known/oauth-protected-resource/mcp", meta)

	authed := chi.NewRouter()
	authed.Use(middleware.RBACMiddleware(rbacCfg, db, evaluator, log))
	authed.Use(middleware.CSRFMiddleware(opts.baseURL))
	authed.Mount("/mcp", s.HTTPHandler())
	router.Mount("/", authed)

	st.handler = router

	return st
}

// addUser creates a user holding one permission target and an API token; scopes
// restrict the token to those targets (nil = unrestricted).
func (st *stack) addUser(name, target string, scopes []string) {
	st.t.Helper()

	st.db.users[name] = &models.User{ID: name, Username: name}

	pt := &models.PermissionTarget{ID: target, RepositoryFilter: ".*", PathPattern: ".*", Actions: []string{"manage", "read", "write", "delete"}}
	if target == targetReadOnly {
		pt = &models.PermissionTarget{ID: target, RepositoryFilter: "^(web-npm|images)$", PathPattern: ".*", Actions: []string{"read"}}
	}

	st.db.targets[name] = []*models.PermissionTarget{pt}

	secret, _, err := auth.CreateToken(st.t.Context(), st.db, auth.TokenKindAccess, name, name, scopes, nil)
	if err != nil {
		st.t.Fatal(err)
	}

	st.secrets[name] = secret
}

// do sends one request; headers override the defaults of an MCP client
// authenticating as user ("" = no credential).
func (st *stack) do(method, user, body string, headers map[string]string) *httptest.ResponseRecorder {
	st.t.Helper()

	req := httptest.NewRequestWithContext(st.t.Context(), method, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-06-18")

	if user != "" {
		req.Header.Set("Authorization", "Bearer "+st.secrets[user])
	}

	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}

	rec := httptest.NewRecorder()
	st.handler.ServeHTTP(rec, req)

	return rec
}

// call runs tools/call as user and decodes the response.
func (st *stack) call(user, tool string, args map[string]any) toolResponse {
	st.t.Helper()

	rec := st.do(http.MethodPost, user, rpcBody(st.t, 1, "tools/call", map[string]any{"name": tool, "arguments": args}), nil)

	return decodeToolResponse(st.t, rec)
}

func basic(user, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
}
