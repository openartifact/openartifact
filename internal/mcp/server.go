// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/search"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Authorizer reports whether the caller identified by ctx may perform action
// ("read", "write", ...) on the named repository.
type Authorizer func(ctx context.Context, repoName, action string) bool

// ReadFilterFunc resolves, once per call, a predicate reporting whether the
// caller identified by ctx may read a repository. Resolving it once avoids one
// permission evaluation per repository.
type ReadFilterFunc func(ctx context.Context) (func(repo *models.Repository) (bool, error), error)

// ArtifactSearcher runs the cross-repository artifact search restricted to the
// given readable repositories.
type ArtifactSearcher interface {
	Search(ctx context.Context, filter search.SearchFilter, limit, offset int, readableRepos map[uuid.UUID]struct{}) ([]search.SearchResult, error)
}

const (
	// maxManifestTextBytes bounds manifest text accepted from MCP callers.
	maxManifestTextBytes = 64 * 1024

	defaultPageSize = 20
	maxPageSize     = 100
	maxPage         = 1_000_000

	// repoScanBatch / maxRepoScanBatches bound the repository walk done to
	// filter by permission before paginating.
	repoScanBatch      = 100
	maxRepoScanBatches = 100

	maxSearchHits = 50

	msgNotFound = "not found or access denied"
	msgInternal = "internal error"

	msgAdminRequired = "admin access required"
)

// Options carries deployment values the tools need.
type Options struct {
	// Version is the build version reported in the MCP serverInfo.
	Version string
	// BaseURL is the externally reachable server origin used for download and
	// client-setup URLs (config BASE_URL). Empty yields relative download paths.
	BaseURL string
}

// Server wraps the MCP server with OpenArtifact dependencies.
type Server struct {
	authorizer Authorizer       // nil denies every repository-scoped call
	readFilter ReadFilterFunc   // nil hides every repository from listings
	searcher   ArtifactSearcher // nil disables cross-repository search
	mcpServer  *server.MCPServer
	db         database.Database
	aiWorker   *worker.AIAnalysisWorker
	log        *logger.Logger
	baseURL    string
}

// NewServer creates a new MCP server with all OpenArtifact tools registered.
func NewServer(db database.Database, aiWorker *worker.AIAnalysisWorker, log *logger.Logger, opts Options) *Server {
	version := opts.Version
	if version == "" {
		version = "dev"
	}

	s := &Server{
		db:       db,
		aiWorker: aiWorker,
		log:      log,
		baseURL:  strings.TrimRight(opts.BaseURL, "/"),
	}

	s.mcpServer = server.NewMCPServer(
		"OpenArtifact",
		version,
		server.WithToolCapabilities(true),
		server.WithRecovery(),
	)

	s.registerTools()

	return s
}

// SetAuthorizer installs the per-repository authorization check. It MUST be
// set before HTTPHandler is used; without it every tool call is denied.
func (s *Server) SetAuthorizer(a Authorizer) { s.authorizer = a }

// SetReadFilter installs the batch read check used to list and search across
// repositories. Without it repository listings and cross-repository search
// return nothing.
func (s *Server) SetReadFilter(f ReadFilterFunc) { s.readFilter = f }

// SetSearcher installs the SQL search backend for cross-repository search.
func (s *Server) SetSearcher(a ArtifactSearcher) { s.searcher = a }

// allowed enforces per-repository authorization. Without an authorizer every
// call is denied.
func (s *Server) allowed(ctx context.Context, repoName, action string) bool {
	if s.authorizer == nil {
		return false
	}

	return s.authorizer(ctx, repoName, action)
}

// resolveRepo loads a repository by name and checks authorization. A missing
// repository and a denied one yield the same tool error so existence is not
// leaked. The returned result is non-nil exactly when the caller must stop.
func (s *Server) resolveRepo(ctx context.Context, name string) (*models.Repository, *mcp.CallToolResult) {
	repo, err := s.db.GetRepositoryByName(ctx, name)
	if err != nil {
		return nil, s.internalError("get repository", err, "repository", name)
	}

	if repo == nil || !s.allowed(ctx, repo.Name, "read") {
		return nil, mcp.NewToolResultError(msgNotFound)
	}

	return repo, nil
}

// authorizeByID checks action on the repository with the given UUID and
// returns the tool error to send when the caller must stop, or nil.
func (s *Server) authorizeByID(ctx context.Context, repoID, action string) *mcp.CallToolResult {
	repo, err := s.db.GetRepositoryByID(ctx, repoID)
	if err != nil {
		return s.internalError("get repository by id", err, "repository_id", repoID)
	}

	if repo == nil || !s.allowed(ctx, repo.Name, action) {
		return mcp.NewToolResultError(msgNotFound)
	}

	return nil
}

// internalError logs err with context server-side and returns a generic tool
// error; underlying error text must never reach MCP clients.
func (s *Server) internalError(op string, err error, kv ...any) *mcp.CallToolResult {
	s.log.Errorw("mcp: "+op, append(kv, "error", err)...)

	return mcp.NewToolResultError(msgInternal)
}

// HTTPHandler returns an http.Handler for embedding in the main router. The
// caller must mount it behind the authenticated chain.
func (s *Server) HTTPHandler() http.Handler {
	return server.NewStreamableHTTPServer(s.mcpServer)
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
			mcp.WithDescription("Search the most recent packages of the repositories you can read by keyword. Matches package name, version, architecture and filename (case-insensitive substring)."),
			mcp.WithString("query",
				mcp.Required(),
				mcp.Description("Keyword or package name to look for"),
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

	s.registerPackageTools()
}
