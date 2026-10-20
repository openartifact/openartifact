// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Authorizer reports whether the caller identified by ctx may perform action
// ("read", "write", ...) on the named repository.
type Authorizer func(ctx context.Context, repoName, action string) bool

// maxManifestTextBytes bounds manifest text accepted from MCP callers.
const maxManifestTextBytes = 64 * 1024

// Server wraps the MCP server with OpenArtifact dependencies.
type Server struct {
	authorizer Authorizer // nil only for the trusted local stdio transport
	httpMode   bool
	mcpServer  *server.MCPServer
	db         database.Database
	aiWorker   *worker.AIAnalysisWorker
	log        *logger.Logger
}

// NewServer creates a new MCP server with all OpenArtifact tools registered.
func NewServer(db database.Database, aiWorker *worker.AIAnalysisWorker, log *logger.Logger) *Server {
	s := &Server{
		db:       db,
		aiWorker: aiWorker,
		log:      log,
	}

	s.mcpServer = server.NewMCPServer(
		"OpenArtifact",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithRecovery(),
	)

	s.registerTools()

	return s
}

// SetAuthorizer installs the per-repository authorization check. It MUST be
// set before HTTPHandler is used; without it HTTP tool calls are denied.
func (s *Server) SetAuthorizer(a Authorizer) { s.authorizer = a }

// allowed enforces per-repository authorization. The stdio transport is a
// trusted local process and is only unrestricted when no HTTP handler exists.
func (s *Server) allowed(ctx context.Context, repoName, action string) bool {
	if s.authorizer == nil {
		return !s.httpMode
	}

	return s.authorizer(ctx, repoName, action)
}

// allowedByID resolves a repository ID to its name and checks authorization.
func (s *Server) allowedByID(ctx context.Context, repoID, action string) bool {
	repo, err := s.db.GetRepositoryByID(ctx, repoID)
	if err != nil || repo == nil {
		return false
	}

	return s.allowed(ctx, repo.Name, action)
}

// HTTPHandler returns an http.Handler for embedding in the main router (HTTP transport).
func (s *Server) HTTPHandler() http.Handler {
	s.httpMode = true

	return server.NewStreamableHTTPServer(s.mcpServer)
}

// ServeStdio runs the MCP server over stdio (for standalone MCP process mode).
func (s *Server) ServeStdio() error {
	return server.ServeStdio(s.mcpServer)
}

func (s *Server) registerTools() {
	// ── list_repositories ────────────────────────────────────────────────────
	s.mcpServer.AddTool(
		mcp.NewTool("list_repositories",
			mcp.WithDescription("List all artifact repositories in OpenArtifact. Returns repository names, formats, and descriptions."),
			mcp.WithString("format",
				mcp.Description("Filter by package format (e.g. npm, maven, deb, rpm, helm, pypi, docker). Leave empty for all."),
			),
			mcp.WithNumber("page",
				mcp.Description("Page number (1-based, default 1)"),
				mcp.DefaultNumber(1),
			),
			mcp.WithNumber("limit",
				mcp.Description("Results per page (default 20, max 100)"),
				mcp.DefaultNumber(20),
			),
		),
		s.handleListRepositories,
	)

	// ── search_artifacts ─────────────────────────────────────────────────────
	s.mcpServer.AddTool(
		mcp.NewTool("search_artifacts",
			mcp.WithDescription("Search for artifacts across repositories using natural language or keywords. Searches names, descriptions, and AI-generated tags."),
			mcp.WithString("query",
				mcp.Required(),
				mcp.Description("Search query — can be natural language (e.g. 'PDF generation library') or a package name"),
			),
			mcp.WithString("repository",
				mcp.Description("Restrict search to a specific repository name. Leave empty to search all."),
			),
			mcp.WithString("format",
				mcp.Description("Filter by package format (npm, maven, deb, rpm, helm, pypi, docker, etc.)"),
			),
		),
		s.handleSearchArtifacts,
	)

	// ── get_artifact_analysis ────────────────────────────────────────────────
	s.mcpServer.AddTool(
		mcp.NewTool("get_artifact_analysis",
			mcp.WithDescription("Retrieve the AI-generated analysis for a specific artifact: summary, tags, license status, security notes, and quarantine recommendation."),
			mcp.WithString("repository_id",
				mcp.Required(),
				mcp.Description("Repository ID (UUID)"),
			),
			mcp.WithString("artifact_id",
				mcp.Required(),
				mcp.Description("Artifact ID (component ID, npm version ID, etc.)"),
			),
		),
		s.handleGetArtifactAnalysis,
	)

	// ── list_ai_analyses ─────────────────────────────────────────────────────
	s.mcpServer.AddTool(
		mcp.NewTool("list_ai_analyses",
			mcp.WithDescription("List AI analysis results for a repository. Useful for auditing license compliance, finding quarantined packages, or reviewing security notes."),
			mcp.WithString("repository_id",
				mcp.Required(),
				mcp.Description("Repository ID (UUID)"),
			),
			mcp.WithString("status",
				mcp.Description("Filter by analysis status: pending, processing, completed, failed. Leave empty for all."),
				mcp.Enum("", "pending", "processing", "completed", "failed"),
			),
			mcp.WithString("license_concern",
				mcp.Description("Set to 'true' to only return artifacts with REVIEW_REQUIRED license status"),
			),
			mcp.WithNumber("limit",
				mcp.Description("Max results (default 20, max 100)"),
				mcp.DefaultNumber(20),
			),
			mcp.WithNumber("offset",
				mcp.Description("Pagination offset (default 0)"),
				mcp.DefaultNumber(0),
			),
		),
		s.handleListAIAnalyses,
	)

	// ── trigger_ai_analysis ──────────────────────────────────────────────────
	s.mcpServer.AddTool(
		mcp.NewTool("trigger_ai_analysis",
			mcp.WithDescription("Manually trigger an AI analysis for an artifact by providing its manifest text. Use this to re-analyze an artifact or analyze one that was missed."),
			mcp.WithString("repository_id",
				mcp.Required(),
				mcp.Description("Repository ID (UUID)"),
			),
			mcp.WithString("artifact_id",
				mcp.Required(),
				mcp.Description("Artifact ID"),
			),
			mcp.WithString("artifact_format",
				mcp.Required(),
				mcp.Description("Package format: npm, maven, deb, rpm, helm, pypi, cargo, etc."),
			),
			mcp.WithString("artifact_name",
				mcp.Description("Package name"),
			),
			mcp.WithString("artifact_version",
				mcp.Description("Package version"),
			),
			mcp.WithString("manifest_text",
				mcp.Required(),
				mcp.Description("Raw manifest content (package.json, pom.xml, go.mod, requirements.txt, etc.)"),
			),
		),
		s.handleTriggerAIAnalysis,
	)

	// ── get_repository ───────────────────────────────────────────────────────
	s.mcpServer.AddTool(
		mcp.NewTool("get_repository",
			mcp.WithDescription("Get details about a specific repository by name, including its format, description, and configuration."),
			mcp.WithString("name",
				mcp.Required(),
				mcp.Description("Repository name"),
			),
		),
		s.handleGetRepository,
	)
}

// ── Tool Handlers ─────────────────────────────────────────────────────────────

func (s *Server) handleListRepositories(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	format := req.GetString("format", "")
	page := int(req.GetFloat("page", 1))
	limit := int(req.GetFloat("limit", 20))

	limit = min(limit, 100)

	if page < 1 {
		page = 1
	}

	offset := (page - 1) * limit

	repos, total, err := s.db.ListComponentsByRepo(ctx, "", format, page, limit)

	_ = offset

	if err != nil {
		// Fall back to listing repositories directly
		repoList, listErr := s.db.ListRepositories(ctx, page, limit, format)
		if listErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to list repositories: %v", listErr)), nil
		}

		repoList = s.filterRepos(ctx, repoList)
		repoCount, _ := s.db.CountRepositories(ctx, format)
		result := map[string]any{
			"repositories": repoList,
			"total":        repoCount,
			"page":         page,
			"limit":        limit,
		}

		return jsonResult(result)
	}

	_ = repos

	repoList, _ := s.db.ListRepositories(ctx, page, limit, format)
	repoList = s.filterRepos(ctx, repoList)
	repoCount, _ := s.db.CountRepositories(ctx, format)
	_ = total

	result := map[string]any{
		"repositories": repoList,
		"total":        repoCount,
		"page":         page,
		"limit":        limit,
	}

	return jsonResult(result)
}

func (s *Server) handleSearchArtifacts(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query, err := req.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	repoName := req.GetString("repository", "")
	format := req.GetString("format", "")

	// Build a search summary using available DB methods
	var results []map[string]any

	if repoName != "" {
		repo, err := s.db.GetRepositoryByName(ctx, repoName)
		if err != nil || repo == nil || !s.allowed(ctx, repo.Name, "read") {
			return mcp.NewToolResultError(fmt.Sprintf("repository %q not found", repoName)), nil //nolint:nilerr // MCP tool errors are returned as tool results, not Go errors
		}

		components, _, err := s.db.ListComponentsByRepo(ctx, repo.ID, format, 1, 50)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
		}

		queryLower := strings.ToLower(query)

		for _, c := range components {
			if matchesQuery(c, queryLower) {
				results = append(results, map[string]any{
					"repository": repoName,
					"component":  c,
				})
			}
		}
	} else {
		// Search across all repos
		repos, _ := s.db.ListRepositories(ctx, 1, 100, format)
		queryLower := strings.ToLower(query)

		for _, repo := range repos {
			if !s.allowed(ctx, repo.Name, "read") {
				continue
			}

			components, _, err := s.db.ListComponentsByRepo(ctx, repo.ID, format, 1, 20)
			if err != nil {
				continue
			}

			for _, c := range components {
				if matchesQuery(c, queryLower) {
					results = append(results, map[string]any{
						"repository": repo.Name,
						"component":  c,
					})
				}
			}

			if len(results) >= 50 {
				break
			}
		}
	}

	return jsonResult(map[string]any{
		"query":   query,
		"results": results,
		"count":   len(results),
	})
}

func (s *Server) handleGetArtifactAnalysis(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	repoID, err := req.RequireString("repository_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	artifactID, err := req.RequireString("artifact_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if !s.allowedByID(ctx, repoID, "read") {
		return mcp.NewToolResultError("repository not found or access denied"), nil
	}

	analysis, err := s.db.GetAIAnalysis(ctx, repoID, artifactID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("database error: %v", err)), nil
	}

	if analysis == nil {
		return mcp.NewToolResultText(fmt.Sprintf(`{"error":"no analysis found for artifact %q in repository %q"}`, artifactID, repoID)), nil
	}

	return jsonResult(analysis)
}

func (s *Server) handleListAIAnalyses(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	repoID, err := req.RequireString("repository_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if !s.allowedByID(ctx, repoID, "read") {
		return mcp.NewToolResultError("repository not found or access denied"), nil
	}

	status := req.GetString("status", "")
	limit := int(req.GetFloat("limit", 20))
	offset := int(req.GetFloat("offset", 0))

	limit = min(limit, 100)

	analyses, total, err := s.db.ListAIAnalyses(ctx, repoID, status, limit, offset)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("database error: %v", err)), nil
	}

	return jsonResult(map[string]any{
		"analyses": analyses,
		"total":    total,
		"limit":    limit,
		"offset":   offset,
	})
}

func (s *Server) handleTriggerAIAnalysis(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	repoID, err := req.RequireString("repository_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	artifactID, err := req.RequireString("artifact_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	format, err := req.RequireString("artifact_format")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	manifestText, err := req.RequireString("manifest_text")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	name := req.GetString("artifact_name", "")
	version := req.GetString("artifact_version", "")

	if !s.allowedByID(ctx, repoID, "write") {
		return mcp.NewToolResultError("repository not found or access denied"), nil
	}

	if len(manifestText) > maxManifestTextBytes {
		return mcp.NewToolResultError(fmt.Sprintf("manifest_text exceeds %d bytes", maxManifestTextBytes)), nil
	}

	if s.aiWorker == nil {
		return mcp.NewToolResultError("AI analysis worker is not configured (AI_ENABLED=false)"), nil
	}

	s.aiWorker.Notify(worker.AIAnalysisRequest{
		RepositoryID:    repoID,
		ArtifactID:      artifactID,
		ArtifactFormat:  format,
		ArtifactName:    name,
		ArtifactVersion: version,
		ManifestText:    manifestText,
	})

	return mcp.NewToolResultText(fmt.Sprintf(`{"status":"queued","artifact_id":%q,"message":"AI analysis has been queued and will complete asynchronously"}`, artifactID)), nil
}

func (s *Server) handleGetRepository(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := req.RequireString("name")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	repo, err := s.db.GetRepositoryByName(ctx, name)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("database error: %v", err)), nil
	}

	if repo == nil || !s.allowed(ctx, repo.Name, "read") {
		return mcp.NewToolResultText(fmt.Sprintf(`{"error":"repository %q not found"}`, name)), nil
	}

	return jsonResult(repo)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// filterRepos drops repositories the caller may not read.
func (s *Server) filterRepos(ctx context.Context, repos []*models.Repository) []*models.Repository {
	out := make([]*models.Repository, 0, len(repos))

	for _, r := range repos {
		if r != nil && s.allowed(ctx, r.Name, "read") {
			out = append(out, r)
		}
	}

	return out
}

func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal error: %v", err)), nil
	}

	return mcp.NewToolResultText(string(b)), nil
}

func matchesQuery(c any, queryLower string) bool {
	b, err := json.Marshal(c)
	if err != nil {
		return false
	}

	return strings.Contains(strings.ToLower(string(b)), queryLower)
}
