// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package quayapi

import (
	"context"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

// Store is the part of the database the facade needs.
type Store interface {
	GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error)
	GetUserByUsername(ctx context.Context, username string) (*models.User, error)

	CreateQuayRepository(ctx context.Context, spec database.QuayRepositorySpec) (*models.QuayRepository, error)
	GetQuayRepository(ctx context.Context, repositoryID, name string) (*models.QuayRepository, error)
	ListQuayRepositories(ctx context.Context, repositoryID string) ([]*models.QuayRepository, error)
	DeleteQuayRepository(ctx context.Context, q *models.QuayRepository) error
	ListQuayTags(ctx context.Context, repositoryID, image string, page, limit int) ([]*models.QuayTag, bool, error)

	SetQuayPermission(ctx context.Context, q *models.QuayRepository, principalType, principalID string, write bool) error
	RemoveQuayPermission(ctx context.Context, q *models.QuayRepository, principalType, principalID string) (bool, error)
	ListQuayUserPermissions(ctx context.Context, q *models.QuayRepository) ([]*models.QuayUserPermission, error)
	ListQuayTeamPermissions(ctx context.Context, groupID string) ([]*models.QuayTeamPermission, error)

	CreateQuayRobot(ctx context.Context, spec database.QuayRobotSpec) (*models.QuayRobot, error)
	GetQuayRobot(ctx context.Context, repositoryID, name string) (*models.QuayRobot, error)
	ListQuayRobots(ctx context.Context, repositoryID string) ([]*models.QuayRobot, error)
	RotateQuayRobotToken(ctx context.Context, b *models.QuayRobot, kind, name, hash, tokenEnc string) (*models.QuayRobot, error)
	DeleteQuayRobot(ctx context.Context, b *models.QuayRobot) error

	CreateQuayTeam(ctx context.Context, repositoryID, name, groupName string) (*models.QuayTeam, error)
	GetQuayTeam(ctx context.Context, repositoryID, name string) (*models.QuayTeam, error)
	ListUsersByGroup(ctx context.Context, groupID string) ([]*models.User, error)
	AddUserToGroup(ctx context.Context, userID, groupID string) error
	RemoveUserFromGroup(ctx context.Context, userID, groupID string) error
	DeleteGroup(ctx context.Context, id string) error

	CreateQuayNotification(ctx context.Context, n *models.QuayNotification) (*models.QuayNotification, error)
	GetQuayNotification(ctx context.Context, quayRepoID, id string) (*models.QuayNotification, error)
	ListQuayNotifications(ctx context.Context, quayRepoID string) ([]*models.QuayNotification, error)
	UpdateQuayNotification(ctx context.Context, n *models.QuayNotification) (*models.QuayNotification, error)
	DeleteQuayNotification(ctx context.Context, quayRepoID, id string) (bool, error)

	GetDockerTag(ctx context.Context, repoID, image, name string) (*models.DockerTag, error)
	DeleteDockerTag(ctx context.Context, repoID, image, name string) error
}
