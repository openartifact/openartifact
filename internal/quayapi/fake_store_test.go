// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package quayapi

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

// fakeStore is an in-memory Store. It keeps the state the real tables would:
// uniqueness, the cascade of a repository delete and the permission
// assignments, so a request sequence can be driven end to end without Postgres.
type fakeStore struct {
	seq int

	orgs  map[string]*models.Repository
	users map[string]*models.User

	repos         map[string]*models.QuayRepository // key: orgID/name
	targets       map[string]models.PermissionTarget
	robots        map[string]*models.QuayRobot // key: orgID/name
	revokedTokens []string
	assignments   map[string]map[string]bool // target id -> "type:principal id"
	teams         map[string]*models.QuayTeam
	members       map[string][]string // group id -> user ids
	notifications map[string]*models.QuayNotification
	tags          map[string][]*models.DockerTag // key: orgID/image
	deletedTags   []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		orgs: map[string]*models.Repository{
			"konflux":  {ID: "org-1", Name: "konflux", Format: "docker", RepoType: "hosted"},
			"open":     {ID: "org-2", Name: "open", Format: "docker", RepoType: "hosted", Public: true},
			"npm-repo": {ID: "org-3", Name: "npm-repo", Format: "npm", RepoType: "hosted"},
			"proxy":    {ID: "org-4", Name: "proxy", Format: "docker", RepoType: "proxy"},
			"locked":   {ID: "org-5", Name: "locked", Format: "docker", RepoType: "hosted", ImmutableReleases: true},
		},
		users:         map[string]*models.User{"alice": {ID: "user-alice", Username: "alice"}},
		repos:         map[string]*models.QuayRepository{},
		targets:       map[string]models.PermissionTarget{},
		robots:        map[string]*models.QuayRobot{},
		assignments:   map[string]map[string]bool{},
		teams:         map[string]*models.QuayTeam{},
		members:       map[string][]string{},
		notifications: map[string]*models.QuayNotification{},
		tags:          map[string][]*models.DockerTag{},
	}
}

func (s *fakeStore) next(prefix string) string {
	s.seq++

	return fmt.Sprintf("%s-%d", prefix, s.seq)
}

func uniqueViolation() error { return &pgconn.PgError{Code: "23505"} }

func (s *fakeStore) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return s.orgs[name], nil
}

func (s *fakeStore) GetUserByUsername(_ context.Context, username string) (*models.User, error) {
	return s.users[username], nil
}

func (s *fakeStore) orgByID(id string) *models.Repository {
	for _, o := range s.orgs {
		if o.ID == id {
			return o
		}
	}

	return nil
}

func (s *fakeStore) CreateQuayRepository(_ context.Context, spec database.QuayRepositorySpec) (*models.QuayRepository, error) {
	key := spec.RepositoryID + "/" + spec.Name
	if _, ok := s.repos[key]; ok {
		return nil, uniqueViolation()
	}

	readID, writeID := s.next("target"), s.next("target")
	s.targets[readID], s.targets[writeID] = spec.ReadTarget, spec.WriteTarget

	q := &models.QuayRepository{
		ID: spec.ID, RepositoryID: spec.RepositoryID, OrgName: s.orgByID(spec.RepositoryID).Name,
		Name: spec.Name, Description: spec.Description, ReadTargetID: readID, WriteTargetID: writeID,
	}
	s.repos[key] = q

	return q, nil
}

func (s *fakeStore) GetQuayRepository(_ context.Context, repositoryID, name string) (*models.QuayRepository, error) {
	return s.repos[repositoryID+"/"+name], nil
}

func (s *fakeStore) ListQuayRepositories(_ context.Context, repositoryID string) ([]*models.QuayRepository, error) {
	var out []*models.QuayRepository

	for _, q := range s.repos {
		if q.RepositoryID == repositoryID {
			out = append(out, q)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out, nil
}

func (s *fakeStore) DeleteQuayRepository(_ context.Context, q *models.QuayRepository) error {
	delete(s.repos, q.RepositoryID+"/"+q.Name)
	delete(s.tags, q.RepositoryID+"/"+q.Name)

	for _, id := range []string{q.ReadTargetID, q.WriteTargetID} {
		delete(s.targets, id)
		delete(s.assignments, id)
	}

	for id, n := range s.notifications {
		if n.QuayRepositoryID == q.ID {
			delete(s.notifications, id)
		}
	}

	return nil
}

func (s *fakeStore) ListQuayTags(_ context.Context, repositoryID, image string, page, limit int) ([]*models.QuayTag, bool, error) {
	all := slices.Clone(s.tags[repositoryID+"/"+image])
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })

	from := min((page-1)*limit, len(all))
	to := min(from+limit, len(all))

	out := make([]*models.QuayTag, 0, to-from)
	for _, t := range all[from:to] {
		out = append(out, &models.QuayTag{Name: t.Name, ManifestDigest: t.ManifestDigest, UpdatedAt: t.UpdatedAt})
	}

	return out, to < len(all), nil
}

func (s *fakeStore) assignmentKey(principalType, principalID string) string {
	return principalType + ":" + principalID
}

func (s *fakeStore) SetQuayPermission(_ context.Context, q *models.QuayRepository, principalType, principalID string, write bool) error {
	key := s.assignmentKey(principalType, principalID)

	for _, id := range []string{q.ReadTargetID, q.WriteTargetID} {
		delete(s.assignments[id], key)
	}

	target := q.ReadTargetID
	if write {
		target = q.WriteTargetID
	}

	if s.assignments[target] == nil {
		s.assignments[target] = map[string]bool{}
	}

	s.assignments[target][key] = true

	return nil
}

func (s *fakeStore) RemoveQuayPermission(_ context.Context, q *models.QuayRepository, principalType, principalID string) (bool, error) {
	key, removed := s.assignmentKey(principalType, principalID), false

	for _, id := range []string{q.ReadTargetID, q.WriteTargetID} {
		if s.assignments[id][key] {
			delete(s.assignments[id], key)

			removed = true
		}
	}

	return removed, nil
}

func (s *fakeStore) userByID(id string) *models.User {
	for _, u := range s.users {
		if u.ID == id {
			return u
		}
	}

	return nil
}

func (s *fakeStore) robotByUser(userID string) *models.QuayRobot {
	for _, b := range s.robots {
		if b.UserID == userID {
			return b
		}
	}

	return nil
}

func (s *fakeStore) ListQuayUserPermissions(_ context.Context, q *models.QuayRepository) ([]*models.QuayUserPermission, error) {
	var out []*models.QuayUserPermission

	for _, target := range []string{q.ReadTargetID, q.WriteTargetID} {
		for key := range s.assignments[target] {
			id, ok := strings.CutPrefix(key, "user:")
			if !ok {
				continue
			}

			perm := &models.QuayUserPermission{UserID: id, Write: target == q.WriteTargetID}
			if u := s.userByID(id); u != nil {
				perm.Username = u.Username
			}

			if b := s.robotByUser(id); b != nil {
				perm.RobotName = b.Name
			}

			out = append(out, perm)
		}
	}

	return out, nil
}

func (s *fakeStore) ListQuayTeamPermissions(_ context.Context, groupID string) ([]*models.QuayTeamPermission, error) {
	var out []*models.QuayTeamPermission

	for _, q := range s.repos {
		for _, target := range []string{q.ReadTargetID, q.WriteTargetID} {
			if s.assignments[target]["group:"+groupID] {
				out = append(out, &models.QuayTeamPermission{
					Repository: q.Name, IsPublic: s.orgByID(q.RepositoryID).Public, Write: target == q.WriteTargetID,
				})
			}
		}
	}

	return out, nil
}

func (s *fakeStore) CreateQuayRobot(_ context.Context, spec database.QuayRobotSpec) (*models.QuayRobot, error) {
	key := spec.RepositoryID + "/" + spec.Name
	if _, ok := s.robots[key]; ok {
		return nil, uniqueViolation()
	}

	userID := s.next("user-robot")
	s.users[spec.Username] = &models.User{ID: userID, Username: spec.Username, Kind: models.UserKindServiceAccount}

	b := &models.QuayRobot{
		ID: spec.ID, RepositoryID: spec.RepositoryID, OrgName: s.orgByID(spec.RepositoryID).Name, Name: spec.Name,
		Description: spec.Description, UserID: userID, TokenID: s.next("token"), TokenEnc: spec.TokenEnc,
		CreatedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	s.robots[key] = b

	return b, nil
}

func (s *fakeStore) GetQuayRobot(_ context.Context, repositoryID, name string) (*models.QuayRobot, error) {
	return s.robots[repositoryID+"/"+name], nil
}

func (s *fakeStore) ListQuayRobots(_ context.Context, repositoryID string) ([]*models.QuayRobot, error) {
	var out []*models.QuayRobot

	for _, b := range s.robots {
		if b.RepositoryID == repositoryID {
			out = append(out, b)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out, nil
}

func (s *fakeStore) RotateQuayRobotToken(_ context.Context, b *models.QuayRobot, _, _, _, tokenEnc string) (*models.QuayRobot, error) {
	s.revokedTokens = append(s.revokedTokens, b.TokenID)
	b.TokenID, b.TokenEnc = s.next("token"), tokenEnc

	return b, nil
}

func (s *fakeStore) DeleteQuayRobot(_ context.Context, b *models.QuayRobot) error {
	delete(s.robots, b.RepositoryID+"/"+b.Name)

	for id := range s.assignments {
		delete(s.assignments[id], "user:"+b.UserID)
	}

	return nil
}

func (s *fakeStore) CreateQuayTeam(_ context.Context, repositoryID, name, _ string) (*models.QuayTeam, error) {
	key := repositoryID + "/" + name
	if _, ok := s.teams[key]; ok {
		return nil, uniqueViolation()
	}

	t := &models.QuayTeam{ID: s.next("team"), RepositoryID: repositoryID, Name: name, GroupID: s.next("group")}
	s.teams[key] = t

	return t, nil
}

func (s *fakeStore) GetQuayTeam(_ context.Context, repositoryID, name string) (*models.QuayTeam, error) {
	return s.teams[repositoryID+"/"+name], nil
}

func (s *fakeStore) ListUsersByGroup(_ context.Context, groupID string) ([]*models.User, error) {
	var out []*models.User

	for _, id := range s.members[groupID] {
		out = append(out, s.userByID(id))
	}

	return out, nil
}

func (s *fakeStore) AddUserToGroup(_ context.Context, userID, groupID string) error {
	if !slices.Contains(s.members[groupID], userID) {
		s.members[groupID] = append(s.members[groupID], userID)
	}

	return nil
}

func (s *fakeStore) RemoveUserFromGroup(_ context.Context, userID, groupID string) error {
	s.members[groupID] = slices.DeleteFunc(s.members[groupID], func(id string) bool { return id == userID })

	return nil
}

func (s *fakeStore) DeleteGroup(_ context.Context, id string) error {
	for key, t := range s.teams {
		if t.GroupID == id {
			delete(s.teams, key)
		}
	}

	delete(s.members, id)

	for target := range s.assignments {
		delete(s.assignments[target], "group:"+id)
	}

	return nil
}

func (s *fakeStore) CreateQuayNotification(_ context.Context, n *models.QuayNotification) (*models.QuayNotification, error) {
	created := *n
	created.ID = s.next("notification")

	if len(created.Config) == 0 {
		created.Config = []byte(`{}`)
	}

	if len(created.EventConfig) == 0 {
		created.EventConfig = []byte(`{}`)
	}

	s.notifications[created.ID] = &created

	return &created, nil
}

func (s *fakeStore) GetQuayNotification(_ context.Context, quayRepoID, id string) (*models.QuayNotification, error) {
	if n := s.notifications[id]; n != nil && n.QuayRepositoryID == quayRepoID {
		return n, nil
	}

	return nil, nil
}

func (s *fakeStore) ListQuayNotifications(_ context.Context, quayRepoID string) ([]*models.QuayNotification, error) {
	var out []*models.QuayNotification

	for _, n := range s.notifications {
		if n.QuayRepositoryID == quayRepoID {
			out = append(out, n)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	return out, nil
}

func (s *fakeStore) UpdateQuayNotification(ctx context.Context, n *models.QuayNotification) (*models.QuayNotification, error) {
	existing, _ := s.GetQuayNotification(ctx, n.QuayRepositoryID, n.ID)
	if existing == nil {
		return nil, nil
	}

	existing.Title, existing.Event, existing.Method = n.Title, n.Event, n.Method
	existing.Config, existing.EventConfig = n.Config, n.EventConfig

	return existing, nil
}

func (s *fakeStore) DeleteQuayNotification(ctx context.Context, quayRepoID, id string) (bool, error) {
	existing, _ := s.GetQuayNotification(ctx, quayRepoID, id)
	if existing == nil {
		return false, nil
	}

	delete(s.notifications, id)

	return true, nil
}

func (s *fakeStore) GetDockerTag(_ context.Context, repoID, image, name string) (*models.DockerTag, error) {
	for _, t := range s.tags[repoID+"/"+image] {
		if t.Name == name {
			return t, nil
		}
	}

	return nil, nil
}

func (s *fakeStore) DeleteDockerTag(_ context.Context, repoID, image, name string) error {
	key := repoID + "/" + image
	s.tags[key] = slices.DeleteFunc(s.tags[key], func(t *models.DockerTag) bool { return t.Name == name })
	s.deletedTags = append(s.deletedTags, name)

	return nil
}
