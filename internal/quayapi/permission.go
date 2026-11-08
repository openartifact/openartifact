// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package quayapi

import (
	"net/http"
	"strings"
)

const (
	roleRead  = "read"
	roleWrite = "write"
)

// account is a user or robot a permission is granted to.
type account struct {
	userID string
	name   string // as Quay names it: "org+robot" for a robot, the username otherwise
	robot  bool
}

type userPermissionJSON struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	IsRobot     bool   `json:"is_robot"`
	IsOrgMember bool   `json:"is_org_member"`
}

type roleRequest struct {
	Role string `json:"role"`
}

// permissionMethods routes the sub-resources of /repository/{org}/{repo}/permissions:
// user, user/{name} and team/{team}. It returns nil for any other shape.
func permissionMethods(rest []string) func(*repoCall) methods {
	switch {
	case len(rest) == 1 && rest[0] == "user":
		return func(rc *repoCall) methods { return methods{http.MethodGet: rc.listUserPermissions} }
	case len(rest) == 2 && rest[0] == "user":
		return func(rc *repoCall) methods {
			return methods{
				http.MethodPut:    func() { rc.setUserPermission(rest[1]) },
				http.MethodDelete: func() { rc.removeUserPermission(rest[1]) },
			}
		}
	case len(rest) == 2 && rest[0] == "team":
		return func(rc *repoCall) methods { return methods{http.MethodPut: func() { rc.setTeamPermission(rest[1]) }} }
	}

	return nil
}

// lookupAccount finds the account named raw: "org+robot" is a robot of the
// organization, anything else an OpenArtifact user. A nil account with a
// false second result means it does not exist; true means the lookup failed and
// the 500 is already written.
func (rc *repoCall) lookupAccount(raw string) (*account, bool) {
	ctx := rc.r.Context()

	if strings.Contains(raw, "+") {
		short := robotShortName(rc.org.Name, raw)
		if short == "" {
			return nil, false
		}

		b, err := rc.h.db.GetQuayRobot(ctx, rc.org.ID, short)
		if err != nil {
			rc.internalError("look up robot", err)
			return nil, true
		}

		if b == nil {
			return nil, false
		}

		return &account{userID: b.UserID, name: b.OrgName + "+" + b.Name, robot: true}, false
	}

	u, err := rc.h.db.GetUserByUsername(ctx, raw)
	if err != nil {
		rc.internalError("look up user", err)
		return nil, true
	}

	if u == nil {
		return nil, false
	}

	return &account{userID: u.ID, name: u.Username}, false
}

// parseRole reads and validates the {"role": ...} body. The first result reports
// a write role, the second that the body was valid.
func (rc *repoCall) parseRole() (bool, bool) {
	var req roleRequest
	if !rc.decode(&req) {
		return false, false
	}

	switch req.Role {
	case roleRead, roleWrite:
		return req.Role == roleWrite, true
	}

	writeError(rc.w, http.StatusBadRequest, "Role must be read or write")

	return false, false
}

func roleName(write bool) string {
	if write {
		return roleWrite
	}

	return roleRead
}

func (rc *repoCall) setUserPermission(raw string) {
	write, ok := rc.parseRole()
	if !ok {
		return
	}

	acct, failed := rc.lookupAccount(raw)
	if failed {
		return
	}

	if acct == nil {
		msg := "User does not exist"
		if strings.Contains(raw, "+") {
			msg = msgRobotNotFound
		}

		writeError(rc.w, http.StatusBadRequest, msg)

		return
	}

	if err := rc.h.db.SetQuayPermission(rc.r.Context(), rc.q, "user", acct.userID, write); err != nil {
		rc.internalError("set permission", err)
		return
	}

	writeJSON(rc.w, http.StatusOK, userPermissionJSON{Name: acct.name, Role: roleName(write), IsRobot: acct.robot})
}

func (rc *repoCall) removeUserPermission(raw string) {
	acct, failed := rc.lookupAccount(raw)
	if failed {
		return
	}

	if acct == nil {
		writeError(rc.w, http.StatusNotFound, "User not found")
		return
	}

	removed, err := rc.h.db.RemoveQuayPermission(rc.r.Context(), rc.q, "user", acct.userID)
	if err != nil {
		rc.internalError("remove permission", err)
		return
	}

	if !removed {
		writeError(rc.w, http.StatusBadRequest, "User does not have permission for repo")
		return
	}

	noContent(rc.w)
}

func (rc *repoCall) listUserPermissions() {
	perms, err := rc.h.db.ListQuayUserPermissions(rc.r.Context(), rc.q)
	if err != nil {
		rc.internalError("list permissions", err)
		return
	}

	out := make(map[string]userPermissionJSON, len(perms))

	for _, p := range perms {
		name, robot := p.Username, p.RobotName != ""
		if robot {
			name = rc.org.Name + "+" + p.RobotName
		}

		out[name] = userPermissionJSON{Name: name, Role: roleName(p.Write), IsRobot: robot}
	}

	writeJSON(rc.w, http.StatusOK, map[string]any{"permissions": out})
}

func (rc *repoCall) setTeamPermission(teamName string) {
	write, ok := rc.parseRole()
	if !ok {
		return
	}

	team := rc.team(rc.org, teamName)
	if team == nil {
		return
	}

	if err := rc.h.db.SetQuayPermission(rc.r.Context(), rc.q, "group", team.GroupID, write); err != nil {
		rc.internalError("set team permission", err)
		return
	}

	writeJSON(rc.w, http.StatusOK, map[string]string{"name": team.Name, "role": roleName(write)})
}
