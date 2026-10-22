// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/audit"
)

// auditAdmin records an explicit admin-plane audit event that names the target
// (resource type and id) and the change. The generic http.request row of the
// audit middleware only carries the route template, which cannot reconstruct
// who was changed. details must never contain secrets.
func auditAdmin(r *http.Request, action, resourceType, resourceID, details string) {
	l := audit.Default()
	if l == nil {
		return
	}

	ev := audit.AuditEvent{
		Action:       action,
		ActorType:    "system",
		ResourceType: &resourceType,
		Details:      details,
	}

	if resourceID != "" {
		ev.ResourceID = &resourceID
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
