// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/search"
	"github.com/openartifact/openartifact/pkg/logger"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

const (
	secretUser = "upstream-svc-account"
	secretPass = "enc:v1:TOP-SECRET-CIPHERTEXT"
	baseURL    = "https://artifacts.example.com"
)

var errDB = errors.New("pq: password authentication failed for user \"oa_internal\" at 10.1.2.3:5432")

type mockDB struct {
	database.BaseMockDB

	repos      []*models.Repository
	components []*models.Component
	scans      []*models.VulnScanResult
	analysis   *models.AIAnalysis
	err        error

	lastPage, lastLimit int
}

func (m *mockDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if m.err != nil {
		return nil, m.err
	}

	for _, r := range m.repos {
		if r.Name == name {
			return r, nil
		}
	}

	return nil, nil
}

func (m *mockDB) GetRepositoryByID(_ context.Context, id string) (*models.Repository, error) {
	if m.err != nil {
		return nil, m.err
	}

	for _, r := range m.repos {
		if r.ID == id {
			return r, nil
		}
	}

	return nil, nil
}

func (m *mockDB) ListRepositories(_ context.Context, page, limit int, format string) ([]*models.Repository, error) {
	if m.err != nil {
		return nil, m.err
	}

	var matching []*models.Repository

	for _, r := range m.repos {
		if format == "" || r.Format == format {
			matching = append(matching, r)
		}
	}

	start := (page - 1) * limit
	if start >= len(matching) {
		return nil, nil
	}

	return matching[start:min(start+limit, len(matching))], nil
}

func (m *mockDB) ListComponentsByRepo(_ context.Context, _, _ string, page, limit int) ([]*models.Component, int, error) {
	m.lastPage, m.lastLimit = page, limit

	if m.err != nil {
		return nil, 0, m.err
	}

	start := (page - 1) * limit
	if start >= len(m.components) {
		return nil, len(m.components), nil
	}

	return m.components[start:min(start+limit, len(m.components))], len(m.components), nil
}

func (m *mockDB) ListVulnScanResults(context.Context, string, bool) ([]*models.VulnScanResult, error) {
	return m.scans, m.err
}

func (m *mockDB) GetAIAnalysis(context.Context, string, string) (*models.AIAnalysis, error) {
	return m.analysis, m.err
}

func (m *mockDB) ListAIAnalyses(context.Context, string, string, bool, int, int) ([]*models.AIAnalysis, int, error) {
	if m.analysis == nil {
		return nil, 0, m.err
	}

	return []*models.AIAnalysis{m.analysis}, 1, m.err
}

type harness struct {
	s       *Server
	db      *mockDB
	logs    *observer.ObservedLogs
	handler http.Handler
}

// testPrincipal is the authenticated caller injected in front of the MCP
// handler by the closure-authorized harness.
func testPrincipal() *auth.Principal {
	id := "user-1"

	return &auth.Principal{UserID: &id, Username: "jane", Credential: auth.CredentialBearerToken}
}

// withPrincipal stands in for the RBAC middleware: it authenticates every
// request as testPrincipal. The real chain is exercised by the stack tests.
func withPrincipal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), testPrincipal())))
	})
}

// newHarness builds a server whose authorizer allows every repository except
// the names in denied, reached through the real MCP HTTP handler.
type fakeSearcher struct{}

func (fakeSearcher) Search(context.Context, search.SearchFilter, int, int, map[uuid.UUID]struct{}) ([]search.SearchResult, error) {
	return []search.SearchResult{{Repository: "web-npm", Format: "npm", Name: "left-pad", Version: "1.0.0", Path: "left-pad-1.0.0.tgz", SHA256: "aa", Size: 10}}, nil
}

func newHarness(t *testing.T, db *mockDB, denied ...string) *harness {
	t.Helper()

	core, logs := observer.New(zapcore.ErrorLevel)
	log := &logger.Logger{SugaredLogger: zap.New(core).Sugar()}

	s := NewServer(db, nil, log, Options{Version: "1.2.3", BaseURL: baseURL + "/"})
	s.SetAuthorizer(func(_ context.Context, repoName, _ string) bool {
		return !slices.Contains(denied, repoName)
	})
	s.SetReadFilter(func(context.Context) (func(*models.Repository) (bool, error), error) {
		return func(r *models.Repository) (bool, error) { return !slices.Contains(denied, r.Name), nil }, nil
	})
	s.SetSearcher(fakeSearcher{})

	return &harness{s: s, db: db, logs: logs, handler: withPrincipal(s.HTTPHandler())}
}

// rpc posts one JSON-RPC request (protocol 2025-06-18, which the stateless
// server serves without an initialize round trip) and returns the response.
func (h *harness) rpc(t *testing.T, method string, params any) *httptest.ResponseRecorder {
	t.Helper()

	return post(t, h.handler, rpcBody(t, 1, method, params), nil)
}

func rpcBody(t *testing.T, id int, method string, params any) string {
	t.Helper()

	msg, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}

	return string(msg)
}

// post sends body to handler as an MCP client with a bearer credential would;
// extra headers override the defaults.
func post(t *testing.T, handler http.Handler, body string, extra map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	req.Header.Set("Authorization", "Bearer test")

	for k, v := range extra {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

type toolResponse struct {
	Result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent map[string]any `json:"structuredContent"` //nolint:tagliatelle // MCP wire format is camelCase
		IsError           bool           `json:"isError"`           //nolint:tagliatelle // MCP wire format is camelCase
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeToolResponse(t *testing.T, rec *httptest.ResponseRecorder) toolResponse {
	t.Helper()

	var resp toolResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("status %d, undecodable body %q: %v", rec.Code, rec.Body.String(), err)
	}

	return resp
}

// call invokes a tool through the HTTP handler and returns its first text block
// and error flag.
func (h *harness) call(t *testing.T, tool string, args map[string]any) (string, bool) {
	t.Helper()

	rec := h.rpc(t, "tools/call", map[string]any{"name": tool, "arguments": args})
	resp := decodeToolResponse(t, rec)

	if resp.Error != nil || len(resp.Result.Content) == 0 {
		t.Fatalf("tool %s: protocol error (status %d): %s", tool, rec.Code, rec.Body.String())
	}

	return resp.Result.Content[0].Text, resp.Result.IsError
}

func (h *harness) callJSON(t *testing.T, tool string, args map[string]any) map[string]any {
	t.Helper()

	text, isErr := h.call(t, tool, args)
	if isErr {
		t.Fatalf("tool %s returned error: %s", tool, text)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("tool %s: invalid JSON %q: %v", tool, text, err)
	}

	return out
}

func fixtureDB() *mockDB {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	return &mockDB{
		repos: []*models.Repository{
			{
				ID: "r-npm", Name: "web-npm", Format: "npm", RepoType: "proxy",
				UpstreamURL: "https://registry.npmjs.org", UpstreamUsername: secretUser, UpstreamPasswordEnc: secretPass,
				CreatedAt: created, UpdatedAt: created,
			},
			{ID: "r-docker", Name: "images", Format: "docker", RepoType: "hosted", CreatedAt: created, UpdatedAt: created},
			{ID: "r-secret", Name: "hidden", Format: "npm", RepoType: "hosted", CreatedAt: created, UpdatedAt: created},
		},
		components: []*models.Component{
			{ID: "c1", RepositoryID: "r-npm", Name: "left-pad", Version: "1.0.0", Format: "npm", SHA256: "aa", PackageSize: 10, Filename: "left-pad-1.0.0.tgz", CreatedAt: created},
			{ID: "c2", RepositoryID: "r-npm", Name: "left-pad", Version: "1.1.0", Format: "npm", SHA256: "bb", PackageSize: 11, Filename: "left-pad-1.1.0.tgz", CreatedAt: created},
			{ID: "c3", RepositoryID: "r-npm", Name: "right-pad", Version: "2.0.0", Format: "npm", SHA256: "cc", PackageSize: 12, Filename: "right-pad-2.0.0.tgz", CreatedAt: created},
		},
		scans: []*models.VulnScanResult{{
			ID: "s1", RepositoryID: "r-npm", PackageName: "left-pad", PackageVersion: "1.0.0", Scanner: "osv",
			ScanStatus: "completed", ScanError: "scanner crashed at /opt/scanner/secret-path",
			Vulnerabilities: []models.Vulnerability{
				{ID: "CVE-1", Severity: "LOW", CVSS: 2},
				{ID: "CVE-2", Severity: "CRITICAL", CVSS: 9.8, Summary: "rce", FixedIn: "1.0.1"},
				{ID: "CVE-3", Severity: "high", CVSS: 7.5},
			},
		}},
		analysis: &models.AIAnalysis{
			ID: "a1", RepositoryID: "r-npm", ArtifactID: "c1", Status: "failed",
			ErrorMessage: "provider said: invalid api key sk-LEAK", Tags: []string{"x"}, PromptTokens: 5,
		},
	}
}

var snakeKey = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func assertSnakeKeys(t *testing.T, path string, v any) {
	t.Helper()

	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if !snakeKey.MatchString(k) {
				t.Errorf("%s: key %q is not snake_case", path, k)
			}

			assertSnakeKeys(t, path+"."+k, child)
		}
	case []any:
		for i, child := range x {
			assertSnakeKeys(t, fmt.Sprintf("%s[%d]", path, i), child)
		}
	}
}

func TestToolsEmitSnakeCaseKeys(t *testing.T) {
	h := newHarness(t, fixtureDB())

	tests := []struct {
		tool string
		args map[string]any
	}{
		{"list_repositories", nil},
		{"get_repository", map[string]any{"name": "web-npm"}},
		{"list_packages", map[string]any{"repository": "web-npm"}},
		{"get_package", map[string]any{"repository": "web-npm", "name": "left-pad"}},
		{"get_vulnerabilities", map[string]any{"repository": "web-npm"}},
		{"client_setup", map[string]any{"repository": "web-npm"}},
		{"search_artifacts", map[string]any{"query": "pad"}},
		{"get_artifact_analysis", map[string]any{"repository_id": "r-npm", "artifact_id": "c1"}},
		{"list_ai_analyses", map[string]any{"repository_id": "r-npm"}},
	}

	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			assertSnakeKeys(t, tt.tool, h.callJSON(t, tt.tool, tt.args))
		})
	}
}

func TestNoSecretsInOutput(t *testing.T) {
	h := newHarness(t, fixtureDB())

	tests := []struct {
		tool string
		args map[string]any
	}{
		{"list_repositories", nil},
		{"get_repository", map[string]any{"name": "web-npm"}},
		{"list_packages", map[string]any{"repository": "web-npm"}},
		{"get_vulnerabilities", map[string]any{"repository": "web-npm"}},
		{"get_artifact_analysis", map[string]any{"repository_id": "r-npm", "artifact_id": "c1"}},
		{"list_ai_analyses", map[string]any{"repository_id": "r-npm"}},
	}

	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			text, isErr := h.call(t, tt.tool, tt.args)
			if isErr {
				t.Fatalf("unexpected error: %s", text)
			}

			for _, leak := range []string{secretUser, secretPass, "TOP-SECRET", "sk-LEAK", "secret-path", "registry.npmjs.org"} {
				if strings.Contains(text, leak) {
					t.Errorf("output leaks %q: %s", leak, text)
				}
			}
		})
	}
}

func TestRepositoryOutputOmitsCredentialKeys(t *testing.T) {
	h := newHarness(t, fixtureDB())

	list := h.callJSON(t, "list_repositories", nil)
	repos, ok := list["repositories"].([]any)

	if !ok || len(repos) == 0 {
		t.Fatalf("no repositories in %v", list)
	}

	single := h.callJSON(t, "get_repository", map[string]any{"name": "web-npm"})

	candidates := []map[string]any{single}
	for _, r := range repos {
		candidates = append(candidates, r.(map[string]any))
	}

	for _, repo := range candidates {
		for _, key := range []string{"UpstreamPasswordEnc", "UpstreamUsername", "upstream_password_enc", "upstream_username", "upstream_password", "upstream_url"} {
			if _, present := repo[key]; present {
				t.Errorf("repository output has forbidden key %q", key)
			}
		}

		for _, key := range []string{"id", "name", "format", "repo_type", "public", "created_at"} {
			if _, present := repo[key]; !present {
				t.Errorf("repository output missing key %q", key)
			}
		}
	}
}

func TestUnauthorizedRepositoryIsIndistinguishableFromMissing(t *testing.T) {
	h := newHarness(t, fixtureDB(), "hidden")

	tests := []struct {
		tool string
		args func(repo string) map[string]any
	}{
		{"get_repository", func(r string) map[string]any { return map[string]any{"name": r} }},
		{"list_packages", func(r string) map[string]any { return map[string]any{"repository": r} }},
		{"get_package", func(r string) map[string]any { return map[string]any{"repository": r, "name": "left-pad"} }},
		{"get_vulnerabilities", func(r string) map[string]any { return map[string]any{"repository": r} }},
		{"client_setup", func(r string) map[string]any { return map[string]any{"repository": r} }},
		{"search_artifacts", func(r string) map[string]any { return map[string]any{"query": "pad", "repository": r} }},
	}

	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			denied, deniedErr := h.call(t, tt.tool, tt.args("hidden"))
			missing, missingErr := h.call(t, tt.tool, tt.args("does-not-exist"))

			if !deniedErr || !missingErr {
				t.Fatalf("expected tool errors, got denied=%v missing=%v", deniedErr, missingErr)
			}

			if denied != msgNotFound || missing != msgNotFound {
				t.Errorf("responses differ or are not generic: denied=%q missing=%q", denied, missing)
			}
		})
	}
}

func TestRepositoryScopedToolsDenyByID(t *testing.T) {
	h := newHarness(t, fixtureDB(), "hidden")

	tests := map[string]map[string]any{
		"list_ai_analyses":      {"repository_id": "r-secret"},
		"get_artifact_analysis": {"repository_id": "r-secret", "artifact_id": "x"},
	}

	for tool, args := range tests {
		text, isErr := h.call(t, tool, args)
		if !isErr || text != msgNotFound {
			t.Errorf("%s: got (%q, %v), want denial", tool, text, isErr)
		}
	}
}

func TestWithoutAuthorizerEverythingIsDenied(t *testing.T) {
	log := &logger.Logger{SugaredLogger: zap.NewNop().Sugar()}
	s := NewServer(fixtureDB(), nil, log, Options{})
	h := &harness{s: s, handler: withPrincipal(s.HTTPHandler())}

	text, isErr := h.call(t, "get_repository", map[string]any{"name": "web-npm"})
	if !isErr || text != msgNotFound {
		t.Fatalf("got (%q, %v), want denial", text, isErr)
	}

	list := h.callJSON(t, "list_repositories", nil)
	if list["total"] != float64(0) {
		t.Fatalf("list_repositories without authorizer must be empty: %v", list)
	}
}

func TestListRepositoriesFiltersBeforePaginating(t *testing.T) {
	h := newHarness(t, fixtureDB(), "images")

	out := h.callJSON(t, "list_repositories", map[string]any{"limit": 1, "page": 2})

	if out["total"] != float64(2) {
		t.Fatalf("total = %v, want 2 readable repositories", out["total"])
	}

	repos, _ := out["repositories"].([]any)
	if len(repos) != 1 {
		t.Fatalf("page 2 limit 1 returned %d repositories", len(repos))
	}

	if name := repos[0].(map[string]any)["name"]; name != "hidden" {
		t.Fatalf("page 2 = %v, want hidden (images is denied, web-npm is page 1)", name)
	}
}

func TestListPackagesPaginationCaps(t *testing.T) {
	tests := []struct {
		name                string
		args                map[string]any
		wantPage, wantLimit int
	}{
		{"limit above cap", map[string]any{"limit": 100000}, 1, maxPageSize},
		{"zero means default", map[string]any{"page": 0, "limit": 0}, 1, defaultPageSize},
		{"negative", map[string]any{"page": -5, "limit": -5}, 1, 1},
		{"defaults", nil, 1, defaultPageSize},
		{"explicit", map[string]any{"page": 3, "limit": 7}, 3, 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := fixtureDB()
			h := newHarness(t, db)

			args := map[string]any{"repository": "web-npm"}
			maps.Copy(args, tt.args)

			out := h.callJSON(t, "list_packages", args)

			if db.lastPage != tt.wantPage || db.lastLimit != tt.wantLimit {
				t.Errorf("DB queried with page=%d limit=%d, want page=%d limit=%d", db.lastPage, db.lastLimit, tt.wantPage, tt.wantLimit)
			}

			if out["limit"] != float64(tt.wantLimit) || out["page"] != float64(tt.wantPage) {
				t.Errorf("response page/limit = %v/%v", out["page"], out["limit"])
			}
		})
	}
}

func TestListPackagesShape(t *testing.T) {
	h := newHarness(t, fixtureDB())

	out := h.callJSON(t, "list_packages", map[string]any{"repository": "web-npm", "limit": 2})

	if out["total"] != float64(3) {
		t.Errorf("total = %v, want 3", out["total"])
	}

	pkgs, _ := out["packages"].([]any)
	if len(pkgs) != 2 {
		t.Fatalf("got %d packages, want 2", len(pkgs))
	}

	first := pkgs[0].(map[string]any)
	for _, key := range []string{"id", "name", "version", "format", "arch", "size", "sha256", "filename", "created_at"} {
		if _, ok := first[key]; !ok {
			t.Errorf("package missing key %q", key)
		}
	}
}

func TestGetPackageVersionsAndDownloadURL(t *testing.T) {
	h := newHarness(t, fixtureDB())

	out := h.callJSON(t, "get_package", map[string]any{"repository": "web-npm", "name": "left-pad"})

	versions, _ := out["versions"].([]any)
	if len(versions) != 2 {
		t.Fatalf("got %d versions, want 2 (right-pad must be excluded): %v", len(versions), out)
	}

	v := versions[1].(map[string]any)

	want := baseURL + "/api/v1/repositories/web-npm/packages/c2/download"
	if v["download_url"] != want {
		t.Errorf("download_url = %v, want %s", v["download_url"], want)
	}

	if v["version"] != "1.1.0" || v["sha256"] != "bb" || v["filename"] != "left-pad-1.1.0.tgz" || v["size"] != float64(11) {
		t.Errorf("unexpected version payload: %v", v)
	}

	text, isErr := h.call(t, "get_package", map[string]any{"repository": "web-npm", "name": "nope"})
	if !isErr || text != "package not found" {
		t.Errorf("unknown package: got (%q, %v)", text, isErr)
	}
}

func TestGetVulnerabilities(t *testing.T) {
	db := fixtureDB()
	h := newHarness(t, db)

	out := h.callJSON(t, "get_vulnerabilities", map[string]any{"repository": "web-npm"})

	totals, _ := out["totals"].(map[string]any)
	if totals["critical"] != float64(1) || totals["high"] != float64(1) || totals["low"] != float64(1) {
		t.Errorf("totals = %v", totals)
	}

	findings, _ := out["findings"].([]any)
	if len(findings) != 3 || findings[0].(map[string]any)["id"] != "CVE-2" {
		t.Errorf("findings must be ordered by severity, got %v", findings)
	}

	filtered := h.callJSON(t, "get_vulnerabilities", map[string]any{"repository": "web-npm", "package": "right-pad"})
	if f, _ := filtered["findings"].([]any); len(f) != 0 {
		t.Errorf("package filter should exclude left-pad findings: %v", f)
	}

	// Findings are capped.
	many := make([]models.Vulnerability, maxFindings+25)
	for i := range many {
		many[i] = models.Vulnerability{ID: fmt.Sprintf("CVE-%d", i), Severity: "LOW"}
	}

	db.scans = []*models.VulnScanResult{{PackageName: "p", PackageVersion: "1", ScanStatus: "completed", Vulnerabilities: many}}

	capped := h.callJSON(t, "get_vulnerabilities", map[string]any{"repository": "web-npm"})
	if f, _ := capped["findings"].([]any); len(f) != maxFindings || capped["findings_truncated"] != true {
		t.Errorf("findings = %d truncated=%v, want %d/true", len(f), capped["findings_truncated"], maxFindings)
	}
}

func TestClientSetupShapes(t *testing.T) {
	h := newHarness(t, fixtureDB())

	dockerText, _ := h.call(t, "client_setup", map[string]any{"repository": "images"})

	var docker map[string]any
	if err := json.Unmarshal([]byte(dockerText), &docker); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"docker login artifacts.example.com -u <USERNAME> -p <TOKEN>",
		"docker push artifacts.example.com/images:latest",
	} {
		if !strings.Contains(dockerText, want) {
			t.Errorf("docker setup missing %q in %s", want, dockerText)
		}
	}

	if docker["base_url"] != baseURL {
		t.Errorf("docker base_url = %v, want %s", docker["base_url"], baseURL)
	}

	npmText, _ := h.call(t, "client_setup", map[string]any{"repository": "web-npm"})

	var npm map[string]any
	if err := json.Unmarshal([]byte(npmText), &npm); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"npm config set registry " + baseURL + "/repos/web-npm/",
		"//artifacts.example.com/repos/web-npm/:_authToken <TOKEN>",
		"npm publish --registry",
	} {
		if !strings.Contains(npmText, want) {
			t.Errorf("npm setup missing %q in %s", want, npmText)
		}
	}

	clients, _ := npm["clients"].([]any)
	if len(clients) != 3 {
		t.Errorf("npm clients = %d, want npm/pnpm/yarn", len(clients))
	}
}

func TestInternalErrorsAreGenericAndLogged(t *testing.T) {
	tests := []struct {
		tool string
		args map[string]any
	}{
		{"list_repositories", nil},
		{"get_repository", map[string]any{"name": "web-npm"}},
		{"list_packages", map[string]any{"repository": "web-npm"}},
		{"get_package", map[string]any{"repository": "web-npm", "name": "x"}},
		{"get_vulnerabilities", map[string]any{"repository": "web-npm"}},
		{"client_setup", map[string]any{"repository": "web-npm"}},
		{"search_artifacts", map[string]any{"query": "x"}},
		{"get_artifact_analysis", map[string]any{"repository_id": "r-npm", "artifact_id": "c1"}},
		{"list_ai_analyses", map[string]any{"repository_id": "r-npm"}},
	}

	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			db := fixtureDB()
			db.err = errDB
			h := newHarness(t, db)

			text, isErr := h.call(t, tt.tool, tt.args)
			if !isErr || text != msgInternal {
				t.Fatalf("got (%q, %v), want generic %q error", text, isErr, msgInternal)
			}

			logged := h.logs.All()
			if len(logged) == 0 {
				t.Fatal("underlying error was not logged server-side")
			}

			if !strings.Contains(fmt.Sprint(logged[0].ContextMap()["error"]), "oa_internal") {
				t.Errorf("log entry lacks the underlying error: %v", logged[0].ContextMap())
			}
		})
	}
}

func TestServerInfoReportsBuildVersion(t *testing.T) {
	h := newHarness(t, fixtureDB())

	rec := h.rpc(t, "initialize", map[string]any{
		"protocolVersion": "2025-03-26", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "t", "version": "1"},
	})

	var parsed struct {
		Result struct {
			ServerInfo struct {
				Version string `json:"version"`
			} `json:"serverInfo"` //nolint:tagliatelle // MCP wire format is camelCase
		} `json:"result"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("status %d: %q: %v", rec.Code, rec.Body.String(), err)
	}

	if parsed.Result.ServerInfo.Version != "1.2.3" {
		t.Fatalf("serverInfo.version = %q, want 1.2.3 (body %s)", parsed.Result.ServerInfo.Version, rec.Body.String())
	}
}

func TestGetVulnerabilitiesRequiresManage(t *testing.T) {
	h := newHarness(t, fixtureDB())

	var asked []string

	h.s.SetAuthorizer(func(_ context.Context, repoName, action string) bool {
		asked = append(asked, action+":"+repoName)
		return action == "read" // reader without global manage
	})

	args := map[string]any{"repository": "web-npm"}

	text, isErr := h.call(t, "get_vulnerabilities", args)
	if !isErr || text != msgAdminRequired {
		t.Fatalf("reader: got (%q, %v), want %q", text, isErr, msgAdminRequired)
	}

	if !slices.Contains(asked, "manage:") {
		t.Errorf("manage must be evaluated globally (empty repo name), asked %v", asked)
	}

	// Other read tools stay available to the same principal.
	if _, isErr := h.call(t, "list_packages", args); isErr {
		t.Error("list_packages must remain readable without manage")
	}

	h.s.SetAuthorizer(func(context.Context, string, string) bool { return true })

	if _, isErr := h.call(t, "get_vulnerabilities", args); isErr {
		t.Error("admin must be allowed")
	}
}

// trigger_ai_analysis accepts the same artifact_format ids as the REST route:
// the repository format ids ("go", not the old component id "gomod").
func TestTriggerAIAnalysisValidatesFormat(t *testing.T) {
	h := newHarness(t, fixtureDB())

	args := func(format string) map[string]any {
		return map[string]any{"repository_id": "r-npm", "artifact_id": "x", "artifact_format": format, "manifest_text": "m"}
	}

	for _, format := range []string{"gomod", "rpmx"} {
		text, isErr := h.call(t, "trigger_ai_analysis", args(format))
		if !isErr || !strings.Contains(text, "artifact_format must be one of") {
			t.Errorf("format %q: got (%q, %v), want a format error", format, text, isErr)
		}
	}

	// A valid format passes validation and reaches the next check (no AI
	// worker is configured in this harness).
	for _, format := range []string{"go", "npm"} {
		text, isErr := h.call(t, "trigger_ai_analysis", args(format))
		if !isErr || !strings.Contains(text, "not enabled") {
			t.Errorf("format %q: got (%q, %v), want to reach the AI-enabled check", format, text, isErr)
		}
	}
}
