// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"strings"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/database/models"
)

// projectEvent is one explicit project.* audit event.
type projectEvent struct {
	action string
	// projectID is nil for project.deleted: the audit row's project_id
	// references the project and would dangle once it is gone.
	projectID    *string
	resourceType string // defaults to "project"
	resourceID   string
	message      string
}

// emit records ev for the request's principal. The generic http.request row of
// the audit middleware stays; these rows carry the project and the change.
func (h *ProjectsHandler) emit(r *http.Request, ev projectEvent) {
	l := audit.Default()
	if l == nil {
		return
	}

	if ev.resourceType == "" {
		ev.resourceType = "project"
	}

	out := audit.AuditEvent{
		Action:       ev.action,
		ActorType:    "system",
		ProjectID:    ev.projectID,
		ResourceType: &ev.resourceType,
		Details:      ev.message,
	}

	if ev.resourceID != "" {
		out.ResourceID = &ev.resourceID
	}

	if p := currentPrincipal(r); p != nil && !p.IsAnonymous {
		out.ActorType = "user"
		out.Actor = p.Username
		out.ActorID = p.UserID

		if p.IsToken() {
			out.ActorType = "token"
		}
	}

	l.Log(r.Context(), out)
}

// emitProjectUpdate records project.updated for changed display fields and
// project.policy_changed for changed policy fields (either, both or none).
func (h *ProjectsHandler) emitProjectUpdate(r *http.Request, before, after *models.Project) {
	if derefString(before.DisplayName) != derefString(after.DisplayName) || derefString(before.Description) != derefString(after.Description) {
		h.emit(r, projectEvent{
			action: audit.ActionProjectUpdated, projectID: &after.ID, resourceID: after.ID,
			message: "project " + after.Name + " updated",
		})
	}

	if changed := policyChanges(before, after); len(changed) > 0 {
		h.emit(r, projectEvent{
			action: audit.ActionProjectPolicyChanged, projectID: &after.ID, resourceID: after.ID,
			message: "project " + after.Name + " policy changed: " + strings.Join(changed, ", "),
		})
	}
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}
