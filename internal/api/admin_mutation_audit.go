// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/database/models"
)

// auditAdmin records an explicit admin-plane audit event that names the target
// (resource type and id) and the change. The generic http.request row of the
// audit middleware only carries the route template, which cannot reconstruct
// who was changed. details must never contain secrets.
func auditAdmin(r *http.Request, action, resourceType, resourceID, details string) {
	auditAdminIn(r, "", action, resourceType, resourceID, details)
}

// auditRepository is auditAdmin for a change made inside a repository: the
// event also carries the repository, so it shows in the repository's audit view.
func auditRepository(r *http.Request, repo *models.Repository, action, resourceType, resourceID, details string) {
	auditAdminIn(r, repo.ID, action, resourceType, resourceID, details)
}

func auditAdminIn(r *http.Request, repositoryID, action, resourceType, resourceID, details string) {
	logAdminAudit(r, adminAuditEvent{
		repositoryID: repositoryID, action: action, resourceType: resourceType, resourceID: resourceID, details: details,
	})
}

// auditQuarantine records that an administrator quarantined or released an
// artifact from the review of one of its scans (kind: "vulnerability", "license"
// or "secret"). artifactID is the scan result's artifact id; pkg and version name
// the package version when the scan result carries them.
func auditQuarantine(r *http.Request, repo *models.Repository, quarantined bool, kind, artifactID, pkg, version string) {
	// A scan result records the artifact by a name@version id or by the row id
	// of the package; a row id is never shown as a name.
	subject := artifactID
	if isUUIDString(artifactID) {
		subject = strings.TrimSpace(pkg + " " + version)
	}

	if subject == "" {
		subject = "an artifact"
	}

	action, msg := audit.ActionArtifactReleased, "released from quarantine"
	if quarantined {
		action, msg = audit.ActionArtifactQuarantined, "quarantined"
	}

	logAdminAudit(r, adminAuditEvent{
		repositoryID: repo.ID, action: action, resourceType: "artifact",
		details: msg + " after " + kind + " review: " + subject,
		pkg:     pkg, version: version, subject: subject,
	})
}

// adminAuditEvent is one audit event of a request's principal. pkg, version and
// subject are optional: the package version the event concerns and the display
// name of what it acted on (see audit.AuditEvent).
type adminAuditEvent struct {
	repositoryID, action, resourceType, resourceID, details string
	pkg, version, subject                                   string
}

func logAdminAudit(r *http.Request, e adminAuditEvent) {
	l := audit.Default()
	if l == nil {
		return
	}

	ev := audit.AuditEvent{
		Action:       e.action,
		ActorType:    "system",
		ResourceType: &e.resourceType,
		Details:      e.details,
		Package:      e.pkg,
		Version:      e.version,
		Subject:      e.subject,
	}

	if e.resourceID != "" {
		ev.ResourceID = &e.resourceID
	}

	if e.repositoryID != "" {
		ev.RepositoryID = &e.repositoryID
	}

	if p := currentPrincipal(r); p != nil && !p.IsAnonymous {
		ev.ActorType = "user"
		ev.Actor = p.Username
		ev.ActorID = p.UserID

		if p.IsToken() {
			ev.ActorType = "token"
		}
	}

	l.Log(r.Context(), ev)
}

// requireUUIDParams rejects requests whose named path parameters are not
// UUIDs with 404 before they reach a uuid column (which would surface as a 500).
func requireUUIDParams(names ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, n := range names {
				if !isUUIDString(chi.URLParam(r, n)) {
					writeError(w, http.StatusNotFound, "not_found", "resource not found")
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}
