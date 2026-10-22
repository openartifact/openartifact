// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"encoding/json"
	"math"
	"strings"

	"github.com/google/uuid"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/search"
	"github.com/openartifact/openartifact/internal/worker"
)

// ── Tool Handlers ─────────────────────────────────────────────────────────────

func (s *Server) handleListRepositories(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	format := req.GetString("format", "")
	page := intArg(req, "page", 1, 1, maxPage)
	limit := intArg(req, "limit", defaultPageSize, 1, maxPageSize)

	repos, err := s.readableRepos(ctx, format)
	if err != nil {
		return s.internalError("list repositories", err, "format", format), nil
	}

	total := len(repos)
	start := min((page-1)*limit, total)
	end := min(start+limit, total)

	items := make([]responses.RepositoryResponse, 0, end-start)
	for _, r := range repos[start:end] {
		items = append(items, toRepositoryDTO(r))
	}

	return jsonResult(repositoryListResult{Total: total, Page: page, Limit: limit, Repositories: items})
}

func (s *Server) handleSearchArtifacts(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query, err := req.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	repoName := req.GetString("repository", "")
	format := req.GetString("format", "")
	queryLower := strings.ToLower(query)

	var hits []searchHit

	if repoName != "" {
		repo, denied := s.resolveRepo(ctx, repoName)
		if denied != nil {
			return denied, nil
		}

		components, _, err := s.db.ListComponentsByRepo(ctx, repo.ID, format, 1, 50)
		if err != nil {
			return s.internalError("search components", err, "repository", repo.Name), nil
		}

		hits = appendMatches(hits, repo.Name, components, queryLower)
	} else {
		var searchErr error

		hits, searchErr = s.searchAllRepos(ctx, query, format)
		if searchErr != nil {
			return s.internalError("search artifacts", searchErr), nil
		}
	}

	if hits == nil {
		hits = []searchHit{}
	}

	return jsonResult(searchResult{Query: query, Results: hits, Count: len(hits)})
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

	if denied := s.authorizeByID(ctx, repoID, "read"); denied != nil {
		return denied, nil
	}

	analysis, err := s.db.GetAIAnalysis(ctx, repoID, artifactID)
	if err != nil {
		return s.internalError("get ai analysis", err, "repository_id", repoID, "artifact_id", artifactID), nil
	}

	if analysis == nil {
		return mcp.NewToolResultError("no analysis found for this artifact"), nil
	}

	return jsonResult(toAnalysisDTO(analysis))
}

func (s *Server) handleListAIAnalyses(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	repoID, err := req.RequireString("repository_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if denied := s.authorizeByID(ctx, repoID, "read"); denied != nil {
		return denied, nil
	}

	status := req.GetString("status", "")
	licenseConcern := strings.EqualFold(strings.TrimSpace(req.GetString("license_concern", "")), "true")
	limit := intArg(req, "limit", defaultPageSize, 1, maxPageSize)
	offset := intArg(req, "offset", 0, 0, maxPage*maxPageSize)

	analyses, total, err := s.db.ListAIAnalyses(ctx, repoID, status, licenseConcern, limit, offset)
	if err != nil {
		return s.internalError("list ai analyses", err, "repository_id", repoID), nil
	}

	items := make([]analysisDTO, 0, len(analyses))
	for _, a := range analyses {
		items = append(items, toAnalysisDTO(a))
	}

	return jsonResult(analysisListResult{Analyses: items, Total: total, Limit: limit, Offset: offset})
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

	format := req.GetString("artifact_format", "")
	manifestText := req.GetString("manifest_text", "")

	if denied := s.authorizeByID(ctx, repoID, "write"); denied != nil {
		return denied, nil
	}

	if len(manifestText) > maxManifestTextBytes {
		return mcp.NewToolResultError("manifest_text is too large"), nil
	}

	artifactName := req.GetString("artifact_name", "")
	artifactVersion := req.GetString("artifact_version", "")

	if msg := api.ValidateAIArtifactIdentity(format, artifactName, artifactVersion); msg != "" {
		return mcp.NewToolResultError(msg), nil
	}

	if s.aiWorker == nil || !s.aiWorker.Enabled() {
		return mcp.NewToolResultError("AI analysis is not enabled on this server"), nil
	}

	// Without manifest_text the server reads the manifest from the stored
	// artifact, which must exist in the repository and be readable.
	if manifestText == "" {
		blob, err := s.db.GetRepoComponentBlob(ctx, repoID, artifactID)
		if err != nil {
			return s.internalError("get artifact", err, "artifact_id", artifactID), nil
		}

		if blob == nil {
			return mcp.NewToolResultError("artifact not found in this repository"), nil
		}

		if !manifest.Supported(blob.Format, blob.Filename) {
			return mcp.NewToolResultError("manifest extraction is not supported for this artifact; supply manifest_text"), nil
		}
	}

	s.aiWorker.Notify(worker.AIAnalysisRequest{
		RepositoryID:    repoID,
		ArtifactID:      artifactID,
		ArtifactFormat:  format,
		ArtifactName:    artifactName,
		ArtifactVersion: artifactVersion,
		ManifestText:    manifestText,
	})

	return jsonResult(queuedResult{
		Status:     "queued",
		ArtifactID: artifactID,
		Message:    "AI analysis has been queued and will complete asynchronously",
	})
}

func (s *Server) handleGetRepository(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := req.RequireString("name")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	repo, denied := s.resolveRepo(ctx, name)
	if denied != nil {
		return denied, nil
	}

	return jsonResult(toRepositoryDTO(repo))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// searchAllRepos runs one SQL search over every repository the caller may
// read. The readable set is resolved with a single batched permission lookup.
func (s *Server) searchAllRepos(ctx context.Context, query, format string) ([]searchHit, error) {
	if s.searcher == nil {
		return nil, nil
	}

	repos, err := s.readableRepos(ctx, format)
	if err != nil {
		return nil, err
	}

	readable := make(map[uuid.UUID]struct{}, len(repos))

	for _, r := range repos {
		if id, perr := uuid.Parse(r.ID); perr == nil {
			readable[id] = struct{}{}
		}
	}

	filter := search.ParseQuery(query)
	if format != "" {
		filter.Formats = append(filter.Formats, format)
	}

	results, err := s.searcher.Search(ctx, filter, maxSearchHits, 0, readable)
	if err != nil {
		return nil, err //nolint:wrapcheck // logged by the caller with context
	}

	hits := make([]searchHit, 0, len(results))
	for _, r := range results {
		hits = append(hits, searchHit{Repository: r.Repository, Package: packageDTO{
			Name:      r.Name,
			Version:   r.Version,
			Format:    r.Format,
			Size:      r.Size,
			SHA256:    r.SHA256,
			Filename:  r.Path,
			CreatedAt: formatTime(r.CreatedAt),
		}})
	}

	return hits, nil
}

// readableRepos returns every repository (optionally of one format) the caller
// may read, in listing order. Permission filtering happens before pagination
// so totals and pages only count visible repositories.
func (s *Server) readableRepos(ctx context.Context, format string) ([]*models.Repository, error) {
	var out []*models.Repository

	if s.readFilter == nil {
		return out, nil
	}

	canRead, err := s.readFilter(ctx)
	if err != nil {
		return nil, err //nolint:wrapcheck // logged by the caller with context
	}

	for dbPage := 1; dbPage <= maxRepoScanBatches; dbPage++ {
		batch, err := s.db.ListRepositories(ctx, dbPage, repoScanBatch, format)
		if err != nil {
			return nil, err //nolint:wrapcheck // logged by the caller with context
		}

		for _, r := range batch {
			if r == nil {
				continue
			}

			if ok, rerr := canRead(r); rerr == nil && ok {
				out = append(out, r)
			}
		}

		if len(batch) < repoScanBatch {
			break
		}
	}

	return out, nil
}

func appendMatches(hits []searchHit, repoName string, components []*models.Component, queryLower string) []searchHit {
	for _, c := range components {
		if matchesQuery(c, queryLower) {
			hits = append(hits, searchHit{Repository: repoName, Package: toPackageDTO(c)})
		}
	}

	return hits
}

func matchesQuery(c *models.Component, queryLower string) bool {
	for _, field := range []string{c.Name, c.Version, c.Arch, c.Filename} {
		if strings.Contains(strings.ToLower(field), queryLower) {
			return true
		}
	}

	return false
}

// intArg reads a numeric argument clamped to [lo, hi]; missing or NaN values
// yield def.
func intArg(req mcp.CallToolRequest, key string, def, lo, hi int) int {
	f := req.GetFloat(key, float64(def))
	if math.IsNaN(f) {
		return def
	}

	return int(math.Max(float64(lo), math.Min(float64(hi), f)))
}

func jsonResult(v any) (*mcp.CallToolResult, error) {
	// HTML escaping is disabled so placeholders like <TOKEN> stay readable.
	var buf strings.Builder

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")

	if err := enc.Encode(v); err != nil {
		return mcp.NewToolResultError(msgInternal), nil //nolint:nilerr // MCP tool errors are tool results, not Go errors
	}

	return mcp.NewToolResultText(strings.TrimRight(buf.String(), "\n")), nil
}
