// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package quayapi

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	maxRobotNameLen = 255
	robotTokenName  = "quay robot"
	// robotUserPrefix names the service account behind a robot; the robot's
	// Quay name can exceed a username's 64 characters, so it is not used.
	robotUserPrefix = "quay-robot-"

	msgRobotNotFound = "Could not find robot with specified username"
)

// robotNameRe is Quay's robot short name.
var robotNameRe = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)

type robotJSON struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Created     string `json:"created"`
	Token       string `json:"token,omitempty"`
	Message     string `json:"message,omitempty"`
}

func robotView(b *models.QuayRobot, token string) robotJSON {
	return robotJSON{
		Name:        b.OrgName + "+" + b.Name,
		Description: b.Description,
		Created:     b.CreatedAt.UTC().Format(time.RFC1123Z),
		Token:       token,
	}
}

// robotShortName turns a robot name as the API accepts it, "robot" or
// "org+robot", into the short name; "" when it is not a valid name of org.
func robotShortName(org, name string) string {
	short := strings.TrimPrefix(name, org+"+")
	if short == "" || len(short) > maxRobotNameLen || !robotNameRe.MatchString(short) {
		return ""
	}

	return short
}

// robotRoute serves /organization/{org}/robots[/{name}[/regenerate]].
func (c *call) robotRoute(orgName string, rest []string) bool {
	switch {
	case len(rest) == 0:
		return c.serve(methods{http.MethodGet: func() { c.listRobots(orgName) }})
	case len(rest) == 1:
		return c.serve(methods{
			http.MethodPut:    func() { c.createRobot(orgName, rest[0]) },
			http.MethodGet:    func() { c.getRobot(orgName, rest[0]) },
			http.MethodDelete: func() { c.deleteRobot(orgName, rest[0]) },
		})
	case len(rest) == 2 && rest[1] == "regenerate":
		return c.serve(methods{http.MethodPost: func() { c.regenerateRobot(orgName, rest[0]) }})
	}

	return false
}

// robot resolves the robot of a request. A missing one is answered with the
// 400 Quay uses for it, which the image-controller recognizes by its message.
func (c *call) robot(org *models.Repository, name string) *models.QuayRobot {
	short := robotShortName(org.Name, name)
	if short == "" {
		writeError(c.w, http.StatusBadRequest, msgRobotNotFound)
		return nil
	}

	b, err := c.h.db.GetQuayRobot(c.r.Context(), org.ID, short)
	if err != nil {
		c.internalError("look up robot", err)
		return nil
	}

	if b == nil {
		writeError(c.w, http.StatusBadRequest, msgRobotNotFound)
		return nil
	}

	return b
}

// robotToken opens the robot's sealed token.
func (c *call) robotToken(b *models.QuayRobot) (string, bool) {
	token, err := crypto.Decrypt(b.TokenEnc)
	if err != nil {
		c.internalError("open robot token (regenerate it if OPENARTIFACT_SECRET_KEY changed)", err)
		return "", false
	}

	return token, true
}

// robotSecret is a freshly minted robot token: the token itself (shown to the
// client), its hash (the credential's identity) and the sealed copy stored so
// that the Quay API can hand the token out again.
type robotSecret struct {
	token, hash, sealed string
}

// mintRobotToken mints a deploy token for a robot and seals it for storage.
func (c *call) mintRobotToken() (robotSecret, bool) {
	token, hash, err := auth.NewTokenSecret(auth.TokenKindDeploy)
	if err != nil {
		c.internalError("mint robot token", err)
		return robotSecret{}, false
	}

	sealed, err := crypto.Encrypt(token)
	if err != nil {
		c.internalError("seal robot token", err)
		return robotSecret{}, false
	}

	return robotSecret{token: token, hash: hash, sealed: sealed}, true
}

func (c *call) createRobot(orgName, name string) {
	org := c.org(orgName)
	if org == nil {
		return
	}

	short := robotShortName(org.Name, name)
	if short == "" {
		writeError(c.w, http.StatusBadRequest, "Invalid robot name")
		return
	}

	var req struct {
		Description string `json:"description"`
	}

	if !c.decode(&req) {
		return
	}

	secret, ok := c.mintRobotToken()
	if !ok {
		return
	}

	id := uuid.NewString()

	b, err := c.h.db.CreateQuayRobot(c.r.Context(), database.QuayRobotSpec{
		ID:           id,
		RepositoryID: org.ID,
		Name:         short,
		Description:  req.Description,
		Username:     robotUserPrefix + strings.ReplaceAll(id, "-", "")[:12],
		TokenKind:    auth.TokenKindDeploy,
		TokenName:    robotTokenName,
		TokenHash:    secret.hash,
		TokenEnc:     secret.sealed,
	})
	if database.IsUniqueViolation(err) {
		writeError(c.w, http.StatusBadRequest, "Existing robot with name: "+org.Name+"+"+short)
		return
	}

	if err != nil {
		c.internalError("create robot", err)
		return
	}

	writeJSON(c.w, http.StatusCreated, robotView(b, secret.token))
}

func (c *call) getRobot(orgName, name string) {
	org := c.org(orgName)
	if org == nil {
		return
	}

	b := c.robot(org, name)
	if b == nil {
		return
	}

	token, ok := c.robotToken(b)
	if !ok {
		return
	}

	writeJSON(c.w, http.StatusOK, robotView(b, token))
}

func (c *call) listRobots(orgName string) {
	org := c.org(orgName)
	if org == nil {
		return
	}

	robots, err := c.h.db.ListQuayRobots(c.r.Context(), org.ID)
	if err != nil {
		c.internalError("list robots", err)
		return
	}

	views := make([]robotJSON, 0, len(robots))
	for _, b := range robots {
		views = append(views, robotView(b, ""))
	}

	writeJSON(c.w, http.StatusOK, map[string]any{"robots": views})
}

func (c *call) deleteRobot(orgName, name string) {
	org := c.org(orgName)
	if org == nil {
		return
	}

	short := robotShortName(org.Name, name)
	if short == "" {
		writeError(c.w, http.StatusNotFound, "Robot not found")
		return
	}

	b, err := c.h.db.GetQuayRobot(c.r.Context(), org.ID, short)
	if err != nil {
		c.internalError("look up robot", err)
		return
	}

	if b == nil {
		writeError(c.w, http.StatusNotFound, "Robot not found")
		return
	}

	if err := c.h.db.DeleteQuayRobot(c.r.Context(), b); err != nil {
		c.internalError("delete robot", err)
		return
	}

	noContent(c.w)
}

func (c *call) regenerateRobot(orgName, name string) {
	org := c.org(orgName)
	if org == nil {
		return
	}

	b := c.robot(org, name)
	if b == nil {
		return
	}

	secret, ok := c.mintRobotToken()
	if !ok {
		return
	}

	rotated, err := c.h.db.RotateQuayRobotToken(c.r.Context(), b, auth.TokenKindDeploy, robotTokenName, secret.hash, secret.sealed)
	if err != nil {
		c.internalError("regenerate robot token", err)
		return
	}

	writeJSON(c.w, http.StatusOK, robotView(rotated, secret.token))
}
