// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package quayapi

import (
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

// maxRepositoryNameLen is Quay's limit on a repository path.
const maxRepositoryNameLen = 255

var (
	// repositoryNameRe is an OCI repository path: lowercase components joined by "/".
	repositoryNameRe = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`)
)

// isResourceSegment reports whether s selects a sub-resource of a repository.
// A repository path may not contain one, which keeps
// "/repository/{org}/{ns}/{component}/tag/" unambiguous.
func isResourceSegment(s string) bool {
	switch s {
	case "changevisibility", "permissions", "tag", "notification":
		return true
	}

	return false
}

type repositoryRequest struct {
	Namespace   string `json:"namespace"`
	Visibility  string `json:"visibility"`
	Repository  string `json:"repository"`
	Description string `json:"description"`
}

type repositoryJSON struct {
	Name           string `json:"name"`
	Namespace      string `json:"namespace"`
	Description    string `json:"description"`
	IsPublic       bool   `json:"is_public"`
	IsOrganization bool   `json:"is_organization"`
	Kind           string `json:"kind"`
	CanAdmin       bool   `json:"can_admin"`
	CanWrite       bool   `json:"can_write"`
}

// repositoryView renders a Quay repository. Its visibility is its
// organization's: OpenArtifact grants anonymous reads per hosted repository.
func repositoryView(org *models.Repository, q *models.QuayRepository) repositoryJSON {
	return repositoryJSON{
		Name:           q.Name,
		Namespace:      org.Name,
		Description:    q.Description,
		IsPublic:       org.Public,
		IsOrganization: true,
		Kind:           "image",
		CanAdmin:       true,
		CanWrite:       true,
	}
}

// repoCall is a request addressed to one Quay repository.
type repoCall struct {
	*call

	org *models.Repository
	q   *models.QuayRepository
}

// repositoryRoot serves /repository: create and list.
func (c *call) repositoryRoot() bool {
	return c.serve(methods{
		http.MethodPost: c.createRepository,
		http.MethodGet:  c.listRepositories,
	})
}

// splitRepositoryPath splits the segments after the organization into the
// repository path and the sub-resource path.
func splitRepositoryPath(rest []string) (string, []string) {
	for i := 1; i < len(rest); i++ {
		if isResourceSegment(rest[i]) {
			return strings.Join(rest[:i], "/"), rest[i:]
		}
	}

	return strings.Join(rest, "/"), nil
}

// repositoryRoute serves /repository/{org}/{repo}[/{sub-resource}].
func (c *call) repositoryRoute(orgName string, rest []string) bool {
	name, sub := splitRepositoryPath(rest)
	if name == "" {
		return false
	}

	var m func(*repoCall) methods

	switch {
	case len(sub) == 0:
		m = func(rc *repoCall) methods {
			return methods{http.MethodGet: rc.getRepository, http.MethodDelete: rc.deleteRepository}
		}
	case sub[0] == "changevisibility" && len(sub) == 1:
		m = func(rc *repoCall) methods { return methods{http.MethodPost: rc.changeVisibility} }
	case sub[0] == "permissions":
		m = permissionMethods(sub[1:])
	case sub[0] == "tag":
		m = tagMethods(sub[1:])
	case sub[0] == "notification":
		m = notificationMethods(sub[1:])
	}

	if m == nil {
		return false
	}

	rc := c.repository(orgName, name)
	if rc == nil {
		return true
	}

	return rc.serve(m(rc))
}

// repository resolves the organization and the Quay repository of a request,
// answering 404 and returning nil when either is absent.
func (c *call) repository(orgName, name string) *repoCall {
	org := c.org(orgName)
	if org == nil {
		return nil
	}

	q, err := c.h.db.GetQuayRepository(c.r.Context(), org.ID, name)
	if err != nil {
		c.internalError("look up repository", err)
		return nil
	}

	if q == nil {
		writeError(c.w, http.StatusNotFound, "Repository not found")
		return nil
	}

	return &repoCall{call: c, org: org, q: q}
}

// validateRepositoryName rejects a name Quay or the registry would not accept.
func validateRepositoryName(name string) string {
	switch {
	case len(name) > maxRepositoryNameLen || !repositoryNameRe.MatchString(name):
		return "Invalid repository name"
	case slices.ContainsFunc(strings.Split(name, "/"), isResourceSegment):
		return "Repository name may not contain the path segment changevisibility, permissions, tag or notification"
	}

	return ""
}

// visibilityMismatch explains why a visibility request cannot be met: public
// read is a property of the whole organization.
func visibilityMismatch(org *models.Repository, visibility string) string {
	if org.Public == (visibility == "public") {
		return ""
	}

	state := "private"
	if org.Public {
		state = "public"
	}

	return "Visibility is set per organization: " + org.Name + " is " + state +
		"; make the request match, or change the repository's visibility in OpenArtifact"
}

func (c *call) createRepository() {
	var req repositoryRequest
	if !c.decode(&req) {
		return
	}

	if msg := validateRepositoryName(req.Repository); msg != "" {
		writeError(c.w, http.StatusBadRequest, msg)
		return
	}

	if req.Visibility != "public" && req.Visibility != "private" {
		writeError(c.w, http.StatusBadRequest, "Visibility must be public or private")
		return
	}

	org := c.org(req.Namespace)
	if org == nil {
		return
	}

	if msg := visibilityMismatch(org, req.Visibility); msg != "" {
		writeError(c.w, http.StatusBadRequest, msg)
		return
	}

	id := uuid.NewString()

	q, err := c.h.db.CreateQuayRepository(c.r.Context(), database.QuayRepositorySpec{
		ID:           id,
		RepositoryID: org.ID,
		Name:         req.Repository,
		Description:  req.Description,
		ReadTarget:   imageTarget(org.Name, req.Repository, id, "read", []string{"read"}),
		WriteTarget:  imageTarget(org.Name, req.Repository, id, "write", []string{"read", "write"}),
	})
	if database.IsUniqueViolation(err) {
		// image-controller treats exactly this answer as success.
		writeError(c.w, http.StatusBadRequest, "Repository already exists")
		return
	}

	if err != nil {
		c.internalError("create repository", err)
		return
	}

	writeJSON(c.w, http.StatusCreated, repositoryView(org, q))
}

// imageTarget is the permission target that scopes a principal to one image of
// the organization: the registry authorizes an image's manifests, tags and
// blobs by their path below the image.
func imageTarget(orgName, image, id, role string, actions []string) models.PermissionTarget {
	return models.PermissionTarget{
		Name:             "quay-" + id + "-" + role,
		RepositoryFilter: regexp.QuoteMeta(orgName),
		PathPattern:      regexp.QuoteMeta(image) + "/.*",
		Actions:          actions,
	}
}

func (c *call) listRepositories() {
	orgName := c.r.URL.Query().Get("namespace")
	if orgName == "" {
		writeError(c.w, http.StatusBadRequest, "namespace is required")
		return
	}

	org := c.org(orgName)
	if org == nil {
		return
	}

	repos, err := c.h.db.ListQuayRepositories(c.r.Context(), org.ID)
	if err != nil {
		c.internalError("list repositories", err)
		return
	}

	views := make([]repositoryJSON, 0, len(repos))
	for _, q := range repos {
		views = append(views, repositoryView(org, q))
	}

	// Quay pages this list with next_page; one page holds every repository.
	writeJSON(c.w, http.StatusOK, map[string]any{"repositories": views, "next_page": ""})
}

func (rc *repoCall) getRepository() {
	writeJSON(rc.w, http.StatusOK, repositoryView(rc.org, rc.q))
}

func (rc *repoCall) deleteRepository() {
	if err := rc.h.db.DeleteQuayRepository(rc.r.Context(), rc.q); err != nil {
		rc.internalError("delete repository", err)
		return
	}

	noContent(rc.w)
}

func (rc *repoCall) changeVisibility() {
	var req struct {
		Visibility string `json:"visibility"`
	}

	if !rc.decode(&req) {
		return
	}

	if req.Visibility != "public" && req.Visibility != "private" {
		writeError(rc.w, http.StatusBadRequest, "Visibility must be public or private")
		return
	}

	if msg := visibilityMismatch(rc.org, req.Visibility); msg != "" {
		writeError(rc.w, http.StatusBadRequest, msg)
		return
	}

	writeJSON(rc.w, http.StatusOK, map[string]bool{"success": true})
}
