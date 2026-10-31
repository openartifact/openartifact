// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// auditDetails is the part of details_json the audit views read.
type auditDetails struct {
	Message string `json:"message"`
	Actor   string `json:"actor"`
	Package string `json:"package"`
	Version string `json:"version"`
	Subject string `json:"subject"`
}

func parseAuditDetails(raw json.RawMessage) auditDetails {
	var d auditDetails

	// Rows whose details are another shape (the generic request rows) just
	// leave the fields empty; a partial decode is fine.
	_ = json.Unmarshal(raw, &d)

	return d
}

// resolvedAudit is the actor name and subject of one audit entry.
type resolvedAudit struct {
	details   auditDetails
	actorName string
	subject   *responses.AuditSubject
}

// resolveAuditEntriesStore is the part of the database resolveAuditEntries needs.
type resolveAuditEntriesStore interface {
	ResolveAuditNames(ctx context.Context, refs models.AuditNameRefs) (*models.AuditNames, error)
}

// resolveAuditEntries resolves the actor and subject names of a page of audit
// entries. Names are looked up in one batched query for the whole page; a
// failed lookup is logged and degrades to the names recorded in the entries
// (the listing itself must not fail because a display name is missing).
func resolveAuditEntries(ctx context.Context, db resolveAuditEntriesStore, log *logger.Logger, logs []*models.AuditLog) []resolvedAudit {
	out := make([]resolvedAudit, len(logs))
	refs := newAuditRefSet()

	for i, l := range logs {
		out[i].details = parseAuditDetails(l.DetailsJSON)
		refs.collect(l, out[i].details)
	}

	names := models.NewAuditNames()

	if r := refs.build(); !r.Empty() {
		resolved, err := db.ResolveAuditNames(ctx, r)
		if err != nil {
			log.Warnw("resolve audit names failed; entries keep their recorded names", "error", err)
		} else if resolved != nil {
			names = resolved
		}
	}

	for i, l := range logs {
		out[i].actorName = auditActorName(l, out[i].details, names)
		out[i].subject = auditSubjectOf(l, out[i].details, names)
	}

	return out
}

// auditRefSet gathers the distinct identifiers of a page, per kind.
type auditRefSet struct {
	users, groups, repos, projects, targets, keys, providers, bundles, tokens map[string]struct{}
}

func newAuditRefSet() *auditRefSet {
	return &auditRefSet{
		users: map[string]struct{}{}, groups: map[string]struct{}{}, repos: map[string]struct{}{},
		projects: map[string]struct{}{}, targets: map[string]struct{}{}, keys: map[string]struct{}{},
		providers: map[string]struct{}{}, bundles: map[string]struct{}{}, tokens: map[string]struct{}{},
	}
}

// auditResourceSets maps a resource_type whose resource_id is a row id to the
// set its name is looked up from.
func (s *auditRefSet) resourceSet(resourceType string) map[string]struct{} {
	switch resourceType {
	case "user", "project_member":
		return s.users
	case "group", "project_group":
		return s.groups
	case "repository":
		return s.repos
	case "project":
		return s.projects
	case "permission_target":
		return s.targets
	case "gpg_key":
		return s.keys
	case "sso_provider":
		return s.providers
	case "bundle":
		return s.bundles
	case "project_token":
		return s.tokens
	default:
		return nil
	}
}

func (s *auditRefSet) collect(l *models.AuditLog, d auditDetails) {
	if d.Actor == "" && l.ActorID != nil && isUUIDString(*l.ActorID) {
		s.users[*l.ActorID] = struct{}{}
	}

	if l.RepositoryID != nil && isUUIDString(*l.RepositoryID) {
		s.repos[*l.RepositoryID] = struct{}{}
	}

	if l.ProjectID != nil && isUUIDString(*l.ProjectID) {
		s.projects[*l.ProjectID] = struct{}{}
	}

	if l.ResourceType == nil || l.ResourceID == nil || !isUUIDString(*l.ResourceID) {
		return
	}

	if set := s.resourceSet(*l.ResourceType); set != nil {
		set[*l.ResourceID] = struct{}{}
	}
}

func (s *auditRefSet) build() models.AuditNameRefs {
	return models.AuditNameRefs{
		UserIDs: keysOf(s.users), GroupIDs: keysOf(s.groups), RepositoryIDs: keysOf(s.repos), ProjectIDs: keysOf(s.projects),
		PermissionTargetIDs: keysOf(s.targets), GPGKeyIDs: keysOf(s.keys), SSOProviderIDs: keysOf(s.providers),
		BundleIDs: keysOf(s.bundles), TokenIDs: keysOf(s.tokens),
	}
}

func keysOf(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}

// auditActorName prefers the name recorded with the event (it survives a
// renamed or deleted user), then the user's current name, then "system" for
// background work.
func auditActorName(l *models.AuditLog, d auditDetails, names *models.AuditNames) string {
	if d.Actor != "" {
		return d.Actor
	}

	if l.ActorID != nil {
		if n, ok := names.Users[*l.ActorID]; ok {
			return n
		}
	}

	if l.ActorType != nil && *l.ActorType == "system" {
		return "system"
	}

	return ""
}

// isAuditPackageKind reports whether rt is the resource type of a package row:
// a kind a format registered in its storage descriptor, one of the docker kinds
// (docker keeps dedicated tables, no descriptor), the terraform provider kind
// that delete handlers still audit under, or the generic "artifact" that
// uploads and quarantine events are recorded with (they name the package in
// their details).
func isAuditPackageKind(rt string) bool {
	switch models.TrashKind(rt) { //nolint:exhaustive // only the non-descriptor kinds are listed
	case models.TrashKindDockerManifest, models.TrashKindDockerTag, models.TrashKindTerraformProvider:
		return true
	default:
		return rt == "artifact" || len(storage.TrashFormatsOfKind(rt)) > 0
	}
}

func derefOr(p *string) string {
	if p == nil {
		return ""
	}

	return *p
}

func namePtr(candidates ...string) *string {
	for _, c := range candidates {
		if c != "" {
			return &c
		}
	}

	return nil
}

// auditSubjectOf names what the entry acted on, or nil when the entry names
// nothing (login events, generic request rows). A subject that no longer
// exists keeps the name the event recorded, else a null name.
func auditSubjectOf(l *models.AuditLog, d auditDetails, names *models.AuditNames) *responses.AuditSubject {
	rt, rid := derefOr(l.ResourceType), derefOr(l.ResourceID)
	repoName := names.Repositories[derefOr(l.RepositoryID)]
	projectName := names.Projects[derefOr(l.ProjectID)]

	s := &responses.AuditSubject{Repository: repoName, Project: projectName}

	switch {
	case rt == "repository":
		s.Kind, s.Name = "repository", namePtr(names.Repositories[rid], repoName, d.Subject)
	case isAuditPackageKind(rt):
		s.Kind, s.Name, s.Version = "package", namePtr(d.Package, d.Subject), d.Version
	case rt == "project":
		s.Kind, s.Name = "project", namePtr(names.Projects[rid], projectName, d.Subject)
	case rt == "user" || rt == "project_member":
		s.Kind, s.Name = "user", namePtr(names.Users[rid], d.Subject)
	case rt == "group" || rt == "project_group":
		s.Kind, s.Name = "group", namePtr(names.Groups[rid], d.Subject)
	case rt == "project_token":
		s.Kind, s.Name = "token", namePtr(names.Tokens[rid], d.Subject)
	case rt == "permission_target":
		s.Kind, s.Name = rt, namePtr(names.PermissionTargets[rid], d.Subject)
	case rt == "gpg_key":
		s.Kind, s.Name = rt, namePtr(names.GPGKeys[rid], d.Subject)
	case rt == "sso_provider":
		s.Kind, s.Name = rt, namePtr(names.SSOProviders[rid], d.Subject)
	case rt == "bundle":
		s.Kind, s.Name = rt, namePtr(names.Bundles[rid], d.Subject)
	case rt == "system_setting":
		s.Kind, s.Name = "setting", namePtr(rid, d.Subject)
	case rt == "storage_tier" || rt == "storage_policy":
		// The audit rows of the tiering endpoints carry the name as the id.
		s.Kind, s.Name = rt, namePtr(rid, d.Subject)
	case rt != "":
		s.Kind, s.Name = rt, namePtr(d.Subject)
	case l.RepositoryID != nil:
		s.Kind, s.Name = "repository", namePtr(repoName, d.Subject)
	case l.ProjectID != nil:
		s.Kind, s.Name = "project", namePtr(projectName, d.Subject)
	default:
		return nil
	}

	return s
}

// activityEntriesStore is the part of the database activityEntries needs.
type activityEntriesStore interface {
	resolveAuditEntriesStore
}

// activityEntries builds the entries of a project or repository activity feed
// from one page of audit rows: actor and subject resolved in one batch, no
// client address.
func activityEntries(ctx context.Context, db activityEntriesStore, log *logger.Logger, logs []*models.AuditLog) []responses.ActivityEntry {
	resolved := resolveAuditEntries(ctx, db, log, logs)
	out := make([]responses.ActivityEntry, 0, len(logs))

	for i, l := range logs {
		out = append(out, responses.ActivityEntry{
			ProjectActivityEntry: activityEntryFrom(l, resolved[i].details),
			ActorName:            resolved[i].actorName,
			Subject:              resolved[i].subject,
		})
	}

	return out
}
