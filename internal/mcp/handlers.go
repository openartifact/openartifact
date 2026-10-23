// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/search"
	"github.com/openartifact/openartifact/internal/worker"
)

// ── Tool Handlers ─────────────────────────────────────────────────────────────

func (s *Server) listRepositories(ctx context.Context, in listRepositoriesIn) (repositoryListResult, error) {
	page := clampInt(in.Page, 1, 1, maxPage)
	limit := clampInt(in.Limit, defaultPageSize, 1, maxPageSize)

	repos, err := s.readableRepos(ctx, in.Format)
	if err != nil {
		return repositoryListResult{}, s.internalError("list repositories", err, "format", in.Format)
	}

	total := len(repos)
	start := min((page-1)*limit, total)
	end := min(start+limit, total)

	items := make([]responses.RepositoryResponse, 0, end-start)
	for _, r := range repos[start:end] {
		items = append(items, toRepositoryDTO(r))
	}

	return repositoryListResult{provenance: newProvenance(), Total: total, Page: page, Limit: limit, Repositories: items}, nil
}

func (s *Server) searchArtifacts(ctx context.Context, in searchArtifactsIn) (searchResult, error) {
	queryLower := strings.ToLower(in.Query)

	var hits []searchHit

	if in.Repository != "" {
		repo, err := s.resolveRepo(ctx, in.Repository)
		if err != nil {
			return searchResult{}, err
		}

		components, _, err := s.db.ListComponentsByRepo(ctx, repo.ID, in.Format, 1, 50)
		if err != nil {
			return searchResult{}, s.internalError("search components", err, "repository", repo.Name)
		}

		hits = appendMatches(hits, repo.Name, components, queryLower)
	} else {
		var searchErr error

		hits, searchErr = s.searchAllRepos(ctx, in.Query, in.Format)
		if searchErr != nil {
			return searchResult{}, s.internalError("search artifacts", searchErr)
		}
	}

	if hits == nil {
		hits = []searchHit{}
	}

	return searchResult{provenance: newProvenance(), Query: in.Query, Results: hits, Count: len(hits)}, nil
}

func (s *Server) getArtifactAnalysis(ctx context.Context, in artifactAnalysisIn) (analysisResult, error) {
	if err := s.authorizeByID(ctx, in.RepositoryID, "read"); err != nil {
		return analysisResult{}, err
	}

	analysis, err := s.db.GetAIAnalysis(ctx, in.RepositoryID, in.ArtifactID)
	if err != nil {
		return analysisResult{}, s.internalError("get ai analysis", err, "repository_id", in.RepositoryID, "artifact_id", in.ArtifactID)
	}

	if analysis == nil {
		return analysisResult{}, errNoAnalysis
	}

	return analysisResult{provenance: newProvenance(), analysisDTO: toAnalysisDTO(analysis)}, nil
}

func (s *Server) listAIAnalyses(ctx context.Context, in listAnalysesIn) (analysisListResult, error) {
	if err := s.authorizeByID(ctx, in.RepositoryID, "read"); err != nil {
		return analysisListResult{}, err
	}

	if !validAnalysisStatus(in.Status) {
		return analysisListResult{}, errInvalidStatus
	}

	licenseConcern := strings.EqualFold(strings.TrimSpace(in.LicenseConcern), "true")
	limit := clampInt(in.Limit, defaultPageSize, 1, maxPageSize)
	offset := clampInt(in.Offset, 0, 0, maxPage*maxPageSize)

	analyses, total, err := s.db.ListAIAnalyses(ctx, in.RepositoryID, in.Status, licenseConcern, limit, offset)
	if err != nil {
		return analysisListResult{}, s.internalError("list ai analyses", err, "repository_id", in.RepositoryID)
	}

	items := make([]analysisDTO, 0, len(analyses))
	for _, a := range analyses {
		items = append(items, toAnalysisDTO(a))
	}

	return analysisListResult{provenance: newProvenance(), Analyses: items, Total: total, Limit: limit, Offset: offset}, nil
}

func validAnalysisStatus(status string) bool {
	switch status {
	case "", "pending", "processing", "completed", "failed":
		return true
	}

	return false
}

func (s *Server) triggerAIAnalysis(ctx context.Context, in triggerAnalysisIn) (queuedResult, error) {
	if err := s.authorizeByID(ctx, in.RepositoryID, "write"); err != nil {
		return queuedResult{}, err
	}

	if len(in.ManifestText) > maxManifestTextBytes {
		return queuedResult{}, errManifestTooLarge
	}

	if msg := api.ValidateAIArtifactIdentity(in.ArtifactFormat, in.ArtifactName, in.ArtifactVersion); msg != "" {
		return queuedResult{}, errors.New(msg) //nolint:err113 // validation message meant for the model
	}

	if s.aiWorker == nil || !s.aiWorker.Enabled() {
		return queuedResult{}, errAIDisabled
	}

	// The artifact must exist in this repository whether or not the caller
	// supplies the manifest: write access to one repository never lets a caller
	// queue work (and LLM spend) for an artifact id of another.
	blob, err := s.db.GetRepoComponentBlob(ctx, in.RepositoryID, in.ArtifactID)
	if err != nil {
		return queuedResult{}, s.internalError("get artifact", err, "artifact_id", in.ArtifactID)
	}

	if blob == nil {
		return queuedResult{}, errArtifactNotFound
	}

	if in.ManifestText == "" && !manifest.Supported(blob.Format, blob.Filename) {
		return queuedResult{}, errManifestUnsupported
	}

	if p := middleware.PrincipalFromContext(ctx); p != nil && !s.triggerLimiter.allow(principalKey(p)) {
		return queuedResult{}, errRateLimited
	}

	s.aiWorker.Notify(worker.AIAnalysisRequest{
		RepositoryID:    in.RepositoryID,
		ArtifactID:      in.ArtifactID,
		ArtifactFormat:  in.ArtifactFormat,
		ArtifactName:    in.ArtifactName,
		ArtifactVersion: in.ArtifactVersion,
		ManifestText:    in.ManifestText,
	})

	return queuedResult{
		Status:     "queued",
		ArtifactID: in.ArtifactID,
		Message:    "AI analysis has been queued and will complete asynchronously",
	}, nil
}

func (s *Server) getRepository(ctx context.Context, in repositoryNameIn) (repositoryResult, error) {
	repo, err := s.resolveRepo(ctx, in.Name)
	if err != nil {
		return repositoryResult{}, err
	}

	return repositoryResult{provenance: newProvenance(), RepositoryResponse: toRepositoryDTO(repo)}, nil
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

var (
	errNoAnalysis          = errors.New("no analysis found for this artifact")
	errInvalidStatus       = errors.New("status must be one of: pending, processing, completed, failed")
	errManifestTooLarge    = errors.New("manifest_text is too large")
	errAIDisabled          = errors.New("AI analysis is not enabled on this server")
	errArtifactNotFound    = errors.New("artifact not found in this repository")
	errManifestUnsupported = errors.New("manifest extraction is not supported for this artifact; supply manifest_text")
	errPackageNotFound     = errors.New("package not found")
)
