// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// ProjectStore owns projects, their members, groups and tokens.
type ProjectStore struct{ *core }

// idString returns v as a string; sqlc maps some uuid columns to any.
func idString(v any) string {
	s, _ := v.(string)

	return s
}

func projectFromRow(row *dbsqlc.Project) *models.Project {
	return &models.Project{
		ID:               row.ID,
		Name:             row.Name,
		DisplayName:      row.DisplayName,
		Description:      row.Description,
		OwnerID:          row.OwnerID,
		Slug:             row.Slug,
		AllowedFormats:   row.AllowedFormats,
		AllowedRepoTypes: row.AllowedRepoTypes,
		RepoNamePrefix:   row.RepoNamePrefix,
		AllowPublicRepos: row.AllowPublicRepos,
		MaxRepositories:  int(row.MaxRepositories),
		TokenMaxTTLDays:  int(row.TokenMaxTtlDays),
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

func (p *ProjectStore) CreateProject(ctx context.Context, project *models.Project) error {
	// The recorded owner is an implicit member: cached grants must not outlive this write.
	defer p.authCache.invalidate()

	if project.Slug == "" {
		project.Slug = models.ProjectSlug(project.Name) // e.g. restored from a backup that predates slugs
	}

	// Unset policy fields take the column defaults.
	if project.AllowedRepoTypes == nil {
		project.AllowedRepoTypes = []string{models.ProjectRepoTypeHosted}
	}

	row, err := p.q.CreateProject(ctx, dbsqlc.CreateProjectParams{
		Name:             project.Name,
		Slug:             project.Slug,
		DisplayName:      project.DisplayName,
		Description:      project.Description,
		OwnerID:          project.OwnerID,
		AllowedFormats:   project.AllowedFormats,
		AllowedRepoTypes: project.AllowedRepoTypes,
		RepoNamePrefix:   project.RepoNamePrefix,
		AllowPublicRepos: project.AllowPublicRepos,
		MaxRepositories:  int32(project.MaxRepositories), //nolint:gosec // validated range by callers, column is integer
		TokenMaxTtlDays:  int32(project.TokenMaxTTLDays), //nolint:gosec // validated range by callers, column is integer
	})

	return finishCreate(insertedRow(row), err, "create project", &project.ID, &project.CreatedAt, &project.UpdatedAt)
}

func (p *ProjectStore) GetProject(ctx context.Context, id string) (*models.Project, error) {
	row, err := p.q.GetProject(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}

	return projectFromRow(&row), nil
}

func (p *ProjectStore) GetProjectByName(ctx context.Context, name string) (*models.Project, error) {
	row, err := p.q.GetProjectByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get project by name: %w", err)
	}

	return projectFromRow(&row), nil
}

func (p *ProjectStore) UpdateProject(ctx context.Context, project *models.Project) error {
	// owner_id changes what members may do.
	defer p.authCache.invalidate()

	err := p.q.UpdateProject(ctx, dbsqlc.UpdateProjectParams{
		DisplayName:      project.DisplayName,
		Description:      project.Description,
		OwnerID:          project.OwnerID,
		AllowedFormats:   project.AllowedFormats,
		AllowedRepoTypes: project.AllowedRepoTypes,
		RepoNamePrefix:   project.RepoNamePrefix,
		AllowPublicRepos: project.AllowPublicRepos,
		MaxRepositories:  int32(project.MaxRepositories), //nolint:gosec // validated range by callers, column is integer
		TokenMaxTtlDays:  int32(project.TokenMaxTTLDays), //nolint:gosec // validated range by callers, column is integer
		ID:               project.ID,
	})
	if err != nil {
		return fmt.Errorf("failed to update project: %w", err)
	}

	return nil
}

func (p *ProjectStore) DeleteProject(ctx context.Context, id string) error {
	defer p.authCache.invalidate()

	err := p.q.DeleteProject(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to delete project: %w", err)
	}

	return nil
}

func projectsFromRows(rows []dbsqlc.Project) []*models.Project {
	projects := make([]*models.Project, 0, len(rows))

	for i := range rows {
		projects = append(projects, projectFromRow(&rows[i]))
	}

	return projects
}

// ListProjects returns the projects the user belongs to (recorded owner,
// member, or through a group).
func (p *ProjectStore) ListProjects(ctx context.Context, userID string) ([]*models.Project, error) {
	rows, err := p.q.ListProjectsByUser(ctx, &userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}

	return projectsFromRows(rows), nil
}

func (p *ProjectStore) ListAllProjects(ctx context.Context) ([]*models.Project, error) {
	rows, err := p.q.ListAllProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list all projects: %w", err)
	}

	return projectsFromRows(rows), nil
}

func (p *ProjectStore) AddProjectMember(ctx context.Context, projectID, userID, role string) error {
	defer p.authCache.invalidate()

	_, err := p.q.AddProjectMember(ctx, dbsqlc.AddProjectMemberParams{
		ProjectID: projectID,
		UserID:    userID,
		Role:      role,
	})
	if err != nil {
		return fmt.Errorf("failed to add project member: %w", err)
	}

	return nil
}

func (p *ProjectStore) ListProjectMembers(ctx context.Context, projectID string) ([]*models.ProjectMember, error) {
	rows, err := p.q.ListProjectMembers(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list project members: %w", err)
	}

	members := make([]*models.ProjectMember, 0, len(rows))

	for _, row := range rows {
		members = append(members, &models.ProjectMember{
			ID:        idString(row.ID),
			ProjectID: row.ProjectID,
			UserID:    row.UserID,
			Username:  row.Username,
			Role:      row.Role,
			CreatedAt: row.CreatedAt,
		})
	}

	return members, nil
}

func (p *ProjectStore) GetProjectMember(ctx context.Context, projectID, userID string) (*models.ProjectMember, error) {
	row, err := p.q.GetProjectMember(ctx, dbsqlc.GetProjectMemberParams{
		ProjectID: projectID,
		UserID:    userID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get project member: %w", err)
	}

	return &models.ProjectMember{
		ID:        idString(row.ID),
		ProjectID: row.ProjectID,
		UserID:    row.UserID,
		Username:  row.Username,
		Role:      row.Role,
		CreatedAt: row.CreatedAt,
	}, nil
}

func (p *ProjectStore) ListProjectGroups(ctx context.Context, projectID string) ([]*models.ProjectGroup, error) {
	rows, err := p.q.ListProjectGroups(ctx, projectID)

	return mapRows(rows, err, "list project groups", projectGroupFromRow)
}

// projectGroupRow lists the sqlc row types projectGroupFromRow converts.
type projectGroupRow interface {
	dbsqlc.GetProjectGroupRow | dbsqlc.ListProjectGroupsRow
}

func projectGroupFromRow[R projectGroupRow](in R) *models.ProjectGroup {
	row := dbsqlc.GetProjectGroupRow(in)

	return &models.ProjectGroup{
		ProjectID: row.ProjectID,
		GroupID:   row.GroupID,
		GroupName: row.GroupName,
		Role:      row.Role,
		CreatedBy: row.CreatedBy,
		CreatedAt: row.CreatedAt,
	}
}

func (p *ProjectStore) GetProjectGroup(ctx context.Context, projectID, groupID string) (*models.ProjectGroup, error) {
	row, err := p.q.GetProjectGroup(ctx, dbsqlc.GetProjectGroupParams{ProjectID: projectID, GroupID: groupID})

	return mapRow(row, err, "get project group", projectGroupFromRow)
}

// SetProjectGroup attaches the group to the project with role, or changes the
// role of an existing attachment.
func (p *ProjectStore) SetProjectGroup(ctx context.Context, projectID, groupID, role string, createdBy *string) error {
	defer p.authCache.invalidate()

	err := p.q.SetProjectGroup(ctx, dbsqlc.SetProjectGroupParams{
		ProjectID: projectID,
		GroupID:   groupID,
		Role:      role,
		CreatedBy: createdBy,
	})
	if err != nil {
		return fmt.Errorf("failed to set project group: %w", err)
	}

	return nil
}
