---
description: "Authorization details: projects and project roles, delegated repo admin, auth cache invalidation, anonymous access"
globs:
  - internal/auth/**
  - internal/middleware/**
  - internal/api/projects*
alwaysApply: false
---
<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Projects, RBAC and auth cache

The one-paragraph AuthZ summary stays in AGENTS.md.

- **Projects are part of authorization** (`internal/auth/project_access.go`, role table in `project_roles.go`): members and granted groups of a project get access on repositories it owns (viewer → read, developer → read/write, maintainer/owner → read/write/delete; highest role wins), never `manage`, never from the system project (`models.SystemProjectID`), never for scoped tokens or anonymous. The control plane `/api/v1/projects/**` is gated by the same middleware: `routeScope.project` (`middleware/rbac.go`, route table in `middleware/rbac_projects.go`) is evaluated through `internal/auth/project_actions.go` `EvaluateProject` / `ProjectRole` (global admin = implicit owner; anonymous, `deploy` and scoped tokens get no project role). A non-member (or an unknown project) gets 404, a member lacking the role 403; unknown sub-paths and methods under `/api/v1/projects/**` fall to global `manage`. Repository administration is delegated to project maintainers/owners only through `routeScope.delegateRepo` + `Evaluator.EvaluateRepoAdmin` (`repo.admin`; handlers call `requireRepoAdmin`): never widen `Evaluate(manage)` for it, a repo-scoped `manage` target never implies it, and non-admin creation (`POST /projects/{p}/repositories`, shared `createRepository`) enforces the project policy while admins bypass it; the repository cap is checked in `CreateProjectRepository` under the project row lock. `POST /api/v1/projects` needs admin or the `projects.self_create` setting (`OPENARTIFACT_PROJECTS_SELF_CREATE`, admin-editable via `/api/v1/admin/projects/settings`; self-created projects take only name/display_name/description and get the default policy). Member and group mutations go through `PostgresDB.ChangeProjectMember` / `ChangeProjectGroup`, which lock the project row and run the escalation/last-owner guard callback; never write `project_members` / `project_groups` elsewhere. Changes go through `PostgresDB` methods that invalidate the auth cache; keep `docs/security/rbac.md` and `docs/security/projects.md` in sync.
- **Auth cache:** per-request auth lookups (token, user, groups, permission targets, repo by name) are cached for `DB_AUTH_CACHE_TTL_MS` (default 2000; 0 disables). Every write to those tables must live in a `PostgresDB` method that calls `defer p.authCache.invalidate()`; writes that bypass it (other replicas, `cmd/resetpw`) are seen only after the TTL.
- **Anonymous access:** `repositories.public` grants anonymous `read` (never write) independent of `allowAnonymousRead`; the evaluator in `internal/auth/permission*.go` is the single place that decides it.
