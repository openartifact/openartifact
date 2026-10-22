// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

var orgNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

const (
	orgRoleOwner  = "owner"
	orgRoleAdmin  = "admin"
	orgRoleMember = "member"
)

type OrgsHandler struct {
	db  database.Database
	log *logger.Logger
}

func NewOrgsHandler(db database.Database, log *logger.Logger) *OrgsHandler {
	return &OrgsHandler{db: db, log: log}
}

func (h *OrgsHandler) RegisterRoutes(r chi.Router) {
	r.Post("/api/v1/orgs", h.handleCreate)
	r.Get("/api/v1/orgs", h.handleList)
	r.Get("/api/v1/orgs/{org}", h.handleGet)
	r.Put("/api/v1/orgs/{org}", h.handleUpdate)
	r.Delete("/api/v1/orgs/{org}", h.handleDelete)
	r.Get("/api/v1/orgs/{org}/members", h.handleListMembers)
	r.Post("/api/v1/orgs/{org}/members", h.handleAddMember)
	r.Put("/api/v1/orgs/{org}/members/{user_id}", h.handleUpdateMember)
	r.Delete("/api/v1/orgs/{org}/members/{user_id}", h.handleRemoveMember)
}

// validOrgMemberRole reports whether level is an organization-wide access
// level (the access plain members get on the organization's repositories).
func validOrgMemberRole(level string) bool {
	return level == models.OrgAccessRead || level == models.OrgAccessWrite || level == models.OrgAccessAdmin
}

const orgMemberRoleMessage = "member_role must be read, write, or admin"

func validOrgRole(role string) bool {
	return role == orgRoleOwner || role == orgRoleAdmin || role == orgRoleMember
}

// orgAccess describes what the calling user may do in an organization.
type orgAccess struct {
	org      *models.Organization
	userID   string
	isMember bool
	// isOwner: the organization's owner_id, or a member with role owner.
	isOwner bool
	// isManager: owner or admin.
	isManager bool
}

// loadOrgAccess authenticates the caller, loads the organization named in the
// URL and resolves the caller's role. It writes the error response and returns
// false on failure.
func (h *OrgsHandler) loadOrgAccess(w http.ResponseWriter, r *http.Request) (*orgAccess, bool) {
	p := currentPrincipal(r)
	if p == nil || p.IsAnonymous || p.UserID == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return nil, false
	}

	return h.loadOrgAccessFor(w, r, p)
}

func (h *OrgsHandler) loadOrgAccessFor(w http.ResponseWriter, r *http.Request, p *auth.Principal) (*orgAccess, bool) {
	org, err := h.db.GetOrganizationByName(r.Context(), urlparams.Vars(r)["org"])
	if err != nil {
		h.log.Errorw("get organization: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return nil, false
	}

	if org == nil {
		writeError(w, http.StatusNotFound, "not_found", "organization not found")
		return nil, false
	}

	member, err := h.db.GetOrgMember(r.Context(), org.ID, *p.UserID)
	if err != nil {
		h.log.Errorw("get org member: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return nil, false
	}

	access := &orgAccess{org: org, userID: *p.UserID}
	access.isOwner = org.OwnerID != nil && *org.OwnerID == *p.UserID
	access.isMember = member != nil || access.isOwner

	if member != nil {
		access.isOwner = access.isOwner || member.Role == orgRoleOwner
		access.isManager = member.Role == orgRoleOwner || member.Role == orgRoleAdmin
	}

	access.isManager = access.isManager || access.isOwner

	return access, true
}

func (h *OrgsHandler) requireMember(w http.ResponseWriter, r *http.Request) (*orgAccess, bool) {
	access, ok := h.loadOrgAccess(w, r)
	if !ok {
		return nil, false
	}

	if !access.isMember {
		writeError(w, http.StatusForbidden, "forbidden", "access denied")
		return nil, false
	}

	return access, true
}

func (h *OrgsHandler) requireManager(w http.ResponseWriter, r *http.Request) (*orgAccess, bool) {
	access, ok := h.loadOrgAccess(w, r)
	if !ok {
		return nil, false
	}

	if !access.isManager {
		writeError(w, http.StatusForbidden, "forbidden", "access denied")
		return nil, false
	}

	return access, true
}

// countOwners returns the number of members holding the owner role.
func (h *OrgsHandler) countOwners(r *http.Request, orgID string) (int, error) {
	members, err := h.db.ListOrgMembers(r.Context(), orgID)
	if err != nil {
		return 0, err //nolint:wrapcheck // logged by caller
	}

	n := 0

	for _, m := range members {
		if m.Role == orgRoleOwner {
			n++
		}
	}

	return n, nil
}

func (h *OrgsHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r)
	if p == nil || p.IsAnonymous || p.UserID == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	var req struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Description string `json:"description"`
		MemberRole  string `json:"member_role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}

	if !orgNameRe.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, "validation_failed", "organization name must be 1-100 alphanumeric characters, hyphens, or underscores")
		return
	}

	slug := models.OrgSlug(req.Name)
	if slug == "" || len(slug) > models.MaxOrgSlugLen {
		writeError(w, http.StatusBadRequest, "validation_failed", "organization name must yield a URL slug of 1-64 lowercase letters, digits and hyphens")
		return
	}

	if req.MemberRole == "" {
		req.MemberRole = models.OrgAccessRead
	}

	if !validOrgMemberRole(req.MemberRole) {
		writeError(w, http.StatusBadRequest, "validation_failed", orgMemberRoleMessage)
		return
	}

	now := time.Now()
	org := &models.Organization{
		Name:        req.Name,
		DisplayName: &req.DisplayName,
		Description: &req.Description,
		OwnerID:     p.UserID,
		MemberRole:  req.MemberRole,
		Slug:        slug,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// The unique constraints are authoritative: no check-then-insert race.
	if err := h.db.CreateOrganization(r.Context(), org); err != nil {
		if database.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "organization name or slug already taken")
			return
		}

		h.log.Errorw("create organization: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if err := h.db.AddOrgMember(r.Context(), org.ID, *p.UserID, orgRoleOwner); err != nil {
		h.log.Errorw("add org owner member: db error", "error", err)

		// Do not leave an organization with no owner membership behind.
		if delErr := h.db.DeleteOrganization(r.Context(), org.ID); delErr != nil {
			h.log.Errorw("rollback organization create failed", "org_id", org.ID, "error", delErr)
		}

		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusCreated, orgToMap(org))
}

func (h *OrgsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r)
	if p == nil || p.IsAnonymous || p.UserID == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	orgs, err := h.db.ListOrganizations(r.Context(), *p.UserID)
	if err != nil {
		h.log.Errorw("list organizations: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	result := make([]responses.OrganizationResponse, 0, len(orgs))
	for _, org := range orgs {
		result = append(result, orgToMap(org))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"organizations": result,
	})
}

func (h *OrgsHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	access, ok := h.requireMember(w, r)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, orgToMap(access.org))
}

func (h *OrgsHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	access, ok := h.requireManager(w, r)
	if !ok {
		return
	}

	// Pointer fields: omitted fields keep their current value.
	var req struct {
		DisplayName *string `json:"display_name"`
		Description *string `json:"description"`
		MemberRole  *string `json:"member_role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}

	if req.MemberRole != nil && !validOrgMemberRole(*req.MemberRole) {
		writeError(w, http.StatusBadRequest, "validation_failed", orgMemberRoleMessage)
		return
	}

	org := access.org

	if req.MemberRole != nil {
		org.MemberRole = *req.MemberRole
	}

	if req.DisplayName != nil {
		org.DisplayName = req.DisplayName
	}

	if req.Description != nil {
		org.Description = req.Description
	}

	org.UpdatedAt = time.Now()

	if err := h.db.UpdateOrganization(r.Context(), org); err != nil {
		h.log.Errorw("update organization: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, orgToMap(org))
}

func (h *OrgsHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	access, ok := h.loadOrgAccess(w, r)
	if !ok {
		return
	}

	// Only the organization's recorded owner may delete it.
	if access.org.OwnerID == nil || *access.org.OwnerID != access.userID {
		writeError(w, http.StatusForbidden, "forbidden", "only organization owner can delete")
		return
	}

	if err := h.db.DeleteOrganization(r.Context(), access.org.ID); err != nil {
		if database.IsForeignKeyViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "organization still owns repositories (including repositories in the trash, which must be purged or restored first)")
			return
		}

		h.log.Errorw("delete organization: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeNoContent(w)
}

func (h *OrgsHandler) handleListMembers(w http.ResponseWriter, r *http.Request) {
	access, ok := h.requireMember(w, r)
	if !ok {
		return
	}

	members, err := h.db.ListOrgMembers(r.Context(), access.org.ID)
	if err != nil {
		h.log.Errorw("list organization members: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	result := make([]responses.OrgMemberResponse, 0, len(members))
	for _, m := range members {
		result = append(result, memberToMap(m))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"members": result,
	})
}

func (h *OrgsHandler) handleAddMember(w http.ResponseWriter, r *http.Request) {
	access, ok := h.requireManager(w, r)
	if !ok {
		return
	}

	var req struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}

	if req.Role == "" {
		req.Role = orgRoleMember
	}

	if !validOrgRole(req.Role) {
		writeError(w, http.StatusBadRequest, "validation_failed", "role must be owner, admin, or member")
		return
	}

	// Only owners may grant the owner role (prevents admin -> owner escalation).
	if req.Role == orgRoleOwner && !access.isOwner {
		writeError(w, http.StatusForbidden, "forbidden", "only an owner can grant the owner role")
		return
	}

	user, err := h.db.GetUserByID(r.Context(), req.UserID)
	if err != nil {
		h.log.Errorw("get user: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if user == nil {
		writeError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}

	existing, err := h.db.GetOrgMember(r.Context(), access.org.ID, req.UserID)
	if err != nil {
		h.log.Errorw("get org member: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if existing != nil {
		writeError(w, http.StatusConflict, "conflict", "user is already a member")
		return
	}

	if err := h.db.AddOrgMember(r.Context(), access.org.ID, req.UserID, req.Role); err != nil {
		if database.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "user is already a member")
			return
		}

		h.log.Errorw("add org member: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	newMember, err := h.db.GetOrgMember(r.Context(), access.org.ID, req.UserID)
	if err != nil {
		h.log.Errorw("get org member: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusCreated, memberToMap(newMember))
}

// guardOwnerChange enforces the owner-protection rules for changing or
// removing target (whose new role is newRole, or "" when removing). It writes
// the error response and returns false when the change is not allowed.
func (h *OrgsHandler) guardOwnerChange(w http.ResponseWriter, r *http.Request, access *orgAccess, target *models.OrgMember, newRole string) bool {
	if newRole == target.Role {
		return true
	}

	isRecordedOwner := access.org.OwnerID != nil && *access.org.OwnerID == target.UserID

	if isRecordedOwner {
		writeError(w, http.StatusConflict, "conflict", "the organization owner cannot be demoted or removed")
		return false
	}

	// Granting or revoking the owner role requires being an owner.
	if (newRole == orgRoleOwner || target.Role == orgRoleOwner) && !access.isOwner {
		writeError(w, http.StatusForbidden, "forbidden", "only an owner can grant, revoke or remove the owner role")
		return false
	}

	if target.Role == orgRoleOwner {
		owners, err := h.countOwners(r, access.org.ID)
		if err != nil {
			h.log.Errorw("count org owners: db error", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")

			return false
		}

		if owners <= 1 {
			writeError(w, http.StatusConflict, "conflict", "cannot remove or demote the last owner")
			return false
		}
	}

	return true
}

func (h *OrgsHandler) handleUpdateMember(w http.ResponseWriter, r *http.Request) {
	access, ok := h.requireManager(w, r)
	if !ok {
		return
	}

	userID := urlparams.Vars(r)["user_id"]

	var req struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}

	if !validOrgRole(req.Role) {
		writeError(w, http.StatusBadRequest, "validation_failed", "role must be owner, admin, or member")
		return
	}

	targetMember, err := h.db.GetOrgMember(r.Context(), access.org.ID, userID)
	if err != nil {
		h.log.Errorw("get org member: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if targetMember == nil {
		writeError(w, http.StatusNotFound, "not_found", "member not found")
		return
	}

	if !h.guardOwnerChange(w, r, access, targetMember, req.Role) {
		return
	}

	if err := h.db.UpdateOrgMemberRole(r.Context(), access.org.ID, userID, req.Role); err != nil {
		h.log.Errorw("update org member role: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	updatedMember, err := h.db.GetOrgMember(r.Context(), access.org.ID, userID)
	if err != nil {
		h.log.Errorw("get org member: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, memberToMap(updatedMember))
}

func (h *OrgsHandler) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	access, ok := h.requireManager(w, r)
	if !ok {
		return
	}

	userID := urlparams.Vars(r)["user_id"]

	targetMember, err := h.db.GetOrgMember(r.Context(), access.org.ID, userID)
	if err != nil {
		h.log.Errorw("get org member: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if targetMember == nil {
		writeError(w, http.StatusNotFound, "not_found", "member not found")
		return
	}

	if !h.guardOwnerChange(w, r, access, targetMember, "") {
		return
	}

	if err := h.db.RemoveOrgMember(r.Context(), access.org.ID, userID); err != nil {
		h.log.Errorw("remove org member: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeNoContent(w)
}

// Helper functions

func orgToMap(org *models.Organization) responses.OrganizationResponse {
	return responses.OrganizationResponse{
		ID:          org.ID,
		Name:        org.Name,
		DisplayName: org.DisplayName,
		Description: org.Description,
		OwnerID:     org.OwnerID,
		MemberRole:  org.MemberRole,
		Slug:        org.Slug,
		CreatedAt:   formatTime(org.CreatedAt),
		UpdatedAt:   formatTime(org.UpdatedAt),
	}
}

func memberToMap(member *models.OrgMember) responses.OrgMemberResponse {
	return responses.OrgMemberResponse{
		ID:        member.ID,
		OrgID:     member.OrgID,
		UserID:    member.UserID,
		Role:      member.Role,
		CreatedAt: formatTime(member.CreatedAt),
	}
}
