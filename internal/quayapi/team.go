// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package quayapi

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	maxTeamNameLen = 255
	// teamGroupPrefix names the group behind a team. The team's Quay name is
	// not used: a group name is at most 64 characters.
	teamGroupPrefix = "quay-team-"
)

var teamNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type memberJSON struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	IsRobot bool   `json:"is_robot"`
	Invited bool   `json:"invited"`
}

func memberView(u *models.User) memberJSON {
	return memberJSON{Name: u.Username, Kind: "user"}
}

// teamRoute serves /organization/{org}/team/{team}[/members[/{user}]|/permissions].
func (c *call) teamRoute(orgName, teamName string, rest []string) bool {
	switch {
	case len(rest) == 0:
		return c.serve(methods{
			http.MethodPut:    func() { c.createTeam(orgName, teamName) },
			http.MethodDelete: func() { c.deleteTeam(orgName, teamName) },
		})
	case len(rest) == 1 && rest[0] == "members":
		return c.serve(methods{http.MethodGet: func() { c.listTeamMembers(orgName, teamName) }})
	case len(rest) == 2 && rest[0] == "members":
		return c.serve(methods{
			http.MethodPut:    func() { c.addTeamMember(orgName, teamName, rest[1]) },
			http.MethodDelete: func() { c.removeTeamMember(orgName, teamName, rest[1]) },
		})
	case len(rest) == 1 && rest[0] == "permissions":
		return c.serve(methods{http.MethodGet: func() { c.listTeamPermissions(orgName, teamName) }})
	}

	return false
}

// team resolves a team of org, answering 404 and returning nil when absent.
func (c *call) team(org *models.Repository, name string) *models.QuayTeam {
	t, err := c.h.db.GetQuayTeam(c.r.Context(), org.ID, name)
	if err != nil {
		c.internalError("look up team", err)
		return nil
	}

	if t == nil {
		writeError(c.w, http.StatusNotFound, "Team not found")
		return nil
	}

	return t
}

// teamInOrg resolves the organization and the team of a request.
func (c *call) teamInOrg(orgName, teamName string) *models.QuayTeam {
	org := c.org(orgName)
	if org == nil {
		return nil
	}

	return c.team(org, teamName)
}

// createTeam creates the team, or confirms it exists: the image-controller
// calls it before every membership change.
func (c *call) createTeam(orgName, name string) {
	org := c.org(orgName)
	if org == nil {
		return
	}

	if len(name) > maxTeamNameLen || !teamNameRe.MatchString(name) {
		writeError(c.w, http.StatusBadRequest, "Invalid team name")
		return
	}

	var req roleRequest
	if !c.decode(&req) {
		return
	}

	groupName := teamGroupPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]

	_, err := c.h.db.CreateQuayTeam(c.r.Context(), org.ID, name, groupName)
	if err != nil && !database.IsUniqueViolation(err) {
		c.internalError("create team", err)
		return
	}

	writeJSON(c.w, http.StatusOK, map[string]string{"name": name, "role": "member"})
}

func (c *call) deleteTeam(orgName, name string) {
	t := c.teamInOrg(orgName, name)
	if t == nil {
		return
	}

	if err := c.h.db.DeleteGroup(c.r.Context(), t.GroupID); err != nil {
		c.internalError("delete team", err)
		return
	}

	noContent(c.w)
}

func (c *call) listTeamMembers(orgName, name string) {
	t := c.teamInOrg(orgName, name)
	if t == nil {
		return
	}

	users, err := c.h.db.ListUsersByGroup(c.r.Context(), t.GroupID)
	if err != nil {
		c.internalError("list team members", err)
		return
	}

	members := make([]memberJSON, 0, len(users))
	for _, u := range users {
		members = append(members, memberView(u))
	}

	writeJSON(c.w, http.StatusOK, map[string]any{"name": t.Name, "members": members, "can_edit": true})
}

// teamMember resolves the team and the user of a membership request. A user
// that does not exist is answered 400, which the image-controller treats as a
// permanent failure.
func (c *call) teamMember(orgName, teamName, username string) (*models.QuayTeam, *models.User) {
	t := c.teamInOrg(orgName, teamName)
	if t == nil {
		return nil, nil
	}

	u, err := c.h.db.GetUserByUsername(c.r.Context(), username)
	if err != nil {
		c.internalError("look up user", err)
		return nil, nil
	}

	if u == nil {
		writeError(c.w, http.StatusBadRequest, "User does not exist")
		return nil, nil
	}

	return t, u
}

func (c *call) addTeamMember(orgName, teamName, username string) {
	t, u := c.teamMember(orgName, teamName, username)
	if t == nil {
		return
	}

	if err := c.h.db.AddUserToGroup(c.r.Context(), u.ID, t.GroupID); err != nil {
		c.internalError("add team member", err)
		return
	}

	writeJSON(c.w, http.StatusOK, memberView(u))
}

func (c *call) removeTeamMember(orgName, teamName, username string) {
	t, u := c.teamMember(orgName, teamName, username)
	if t == nil {
		return
	}

	if err := c.h.db.RemoveUserFromGroup(c.r.Context(), u.ID, t.GroupID); err != nil {
		c.internalError("remove team member", err)
		return
	}

	noContent(c.w)
}

type teamPermissionJSON struct {
	Repository struct {
		Name     string `json:"name"`
		IsPublic bool   `json:"is_public"`
	} `json:"repository"`
	Role string `json:"role"`
}

func (c *call) listTeamPermissions(orgName, name string) {
	t := c.teamInOrg(orgName, name)
	if t == nil {
		return
	}

	perms, err := c.h.db.ListQuayTeamPermissions(c.r.Context(), t.GroupID)
	if err != nil {
		c.internalError("list team permissions", err)
		return
	}

	out := make([]teamPermissionJSON, 0, len(perms))

	for _, p := range perms {
		var view teamPermissionJSON

		view.Repository.Name, view.Repository.IsPublic, view.Role = p.Repository, p.IsPublic, roleName(p.Write)
		out = append(out, view)
	}

	writeJSON(c.w, http.StatusOK, map[string]any{"permissions": out})
}
