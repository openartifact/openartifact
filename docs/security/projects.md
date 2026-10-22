<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Projects

A project is the unit of ownership for repositories. Every repository belongs to exactly one project (the built-in `system` project when none is given), and the people and groups who hold a role in the project get access to all of its repositories. Project owners and maintainers manage membership themselves, without being global admins.

Project access is added to [permission targets](rbac.md) (union, no deny rules); it never replaces them. How the data-plane access is derived from a role is in [RBAC](rbac.md#projects).

## Roles

Roles rank `viewer` < `developer` < `maintainer` < `owner`. A user's effective role is the highest of their personal membership (or recorded ownership) and any group grant. A global admin is an implicit `owner` of every project: `my_role` reports `owner` for an admin who holds no role.

| Capability | viewer | developer | maintainer | owner | global admin only |
|---|:-:|:-:|:-:|:-:|:-:|
| See the project, its repositories, members and groups | x | x | x | x | |
| Read artifacts in the project's repositories | x | x | x | x | |
| Write artifacts | | x | x | x | |
| Delete artifacts, list and restore repository trash | | | x | x | |
| Create repositories in the project (policy applies) | | | x | x | |
| Update, delete and configure the project's repositories (cleanup policies, quota, trash emptying) | | | x | x | |
| Move a repository to another project (owner of both) | | | | x | |
| Add, change and remove members and groups below `maintainer` | | | x | x | |
| Grant, change or remove `maintainer` and `owner` | | | | x | |
| Edit `display_name` and `description` | | | | x | |
| Delete the project (when it owns no repositories) | | | | x | |
| Create a project with a chosen `owner` or policy fields | | | | | x |
| Change policy fields (`allowed_formats`, `allowed_repo_types`, `repo_name_prefix`, `allow_public_repos`, `max_repositories`, `token_max_ttl_days`) | | | | | x |

Project roles never grant the global `manage` permission: users, groups, permission targets, settings and every other `/api/v1/*` route outside `/api/v1/projects/**` stay admin-only. The one exception is the **delegated repository administration** of the table above: for a repository owned by a (non-system) project, a maintainer or owner of that project passes the checks of `PATCH` and `DELETE /repository/{name}`, `/repositories/{name}/cleanup-policies[...]`, `GET`, `PUT`, `DELETE /repositories/{name}/quota` and `DELETE /repositories/{name}/trash`. This is a separate check (`repo.admin`): it is never satisfied by a repository-scoped `manage` permission target, and it is never granted by a scoped, deploy or anonymous credential. A caller who cannot even read the repository gets `404`. Webhooks, quota reconcile, group-member management and every other repository sub-route stay admin-only. Repository responses report the caller's `permissions.admin`.

### Repository self-service

`POST /projects/{project}/repositories` takes the body of `POST /repositories` (`project` is ignored; the path decides) and creates the repository in the project, with the creator needing no permission target: the project roles give access. For non-admins the project policy is enforced:

| Policy field | Violation |
|---|---|
| `repo_name_prefix` | name must start with it: 400 |
| `allowed_formats`, `allowed_repo_types` | 400 |
| `allow_public_repos` false | `public: true` is 403 |
| `max_repositories` | 409 `project_repository_limit` (checked under a lock on the project row, so concurrent creates cannot overshoot) |
| group `member_repos` | each member must be readable by the caller: 403 |

Global admins bypass the policy, on both routes. A non-admin `PATCH /repository/{name}` may change `description`, retention, immutability and the upstream settings but not `project`, `blocked` or `presign_downloads`, and may not make the repository public unless the project allows it (403).

`POST /projects/{project}/repositories/{name}/transfer` with `{"to_project": "<name>"}` moves a repository between projects. The caller must be owner of both (admins always); a caller with no role in the target gets 404, a lower role 403. A non-admin transfer must also satisfy the target's policy (prefix, format, type, visibility, `max_repositories`). Storage usage counters are not moved.

Group grants take `viewer`, `developer` or `maintainer`; `owner` is personal and cannot be given to a group. The built-in `system` project grants nothing and cannot be deleted (409).

### Escalation rules

Enforced on every member and group mutation, under a lock on the project row so concurrent requests cannot break them:

- A **maintainer** can only add, change or remove roles **below** `maintainer`, for members and for groups. Granting `maintainer` or `owner`, or touching a user or group that already holds one of them, needs an **owner**.
- Nobody can grant a role above their own.
- The project's **recorded owner** and its **last owner** cannot be demoted or removed (409).
- Groups never get `owner`.

### 404 and 403

On `/api/v1/projects/{project}/**`:

| Caller | Result |
|---|---|
| not authenticated | 401 |
| not a member (or the project does not exist) | **404**, identical in both cases, so project names do not leak |
| a member whose role is too low for the action | **403** |
| anonymous callers, `deploy` tokens | denied, never a project role |

Tokens with non-empty `scopes` get no project role (scopes are permission-target IDs; they only narrow). A token without scopes acts with its owner's roles.

Sub-paths and methods that are not listed in the API table below fall back to the global `manage` permission (deny by default), so a project owner cannot reach an undocumented route.

## Creating projects

By default only a global admin creates projects (`POST /api/v1/projects`, `oa project create`, Admin > Projects). The admin may name the `owner` (a username, default: the caller) and set policy fields in the same request.

Setting **`projects.self_create`** lets every authenticated human user create a project and become its owner. Sessions and `access` tokens without scopes qualify; `deploy` tokens, the legacy key and scoped tokens do not.

| Where | How |
|---|---|
| Config file | `projects.selfCreate: true` ([`openartifact.example.yaml`](../../openartifact.example.yaml)) |
| Environment | `OPENARTIFACT_PROJECTS_SELF_CREATE=true` |
| Admin UI | the self-service toggle on the Admin > Projects page |
| API | `GET` / `PUT /api/v1/admin/projects/settings` with `{"self_create": true}` (global admin) |

The config value or environment variable is the default until an admin saves the setting through the UI or API; a saved value takes precedence. If the stored value cannot be read, self-creation is off.

A self-created project:

- is owned by the creator, who is added as `owner`;
- accepts only `name`, `display_name` and `description`; a request with `owner` or any policy field is answered with 403;
- starts with the default policy: hosted repositories only, all formats, repository name prefix `<slug>-`, no public repositories, at most 50 repositories, token TTL cap of 365 days. The owner cannot change these; a global admin can. The policy is enforced when non-admins [create or transfer repositories](#repository-self-service).

`GET /api/v1/projects` returns `can_create` (admin, or self-creation on) so clients can show or hide the create action.

## Project tokens

A project token is a CI credential that belongs to one project. Each token is a deploy token of its own **service account** (a hidden user that cannot log in, is never listed with users or members, and holds exactly the token's role in the project):

- `viewer` tokens read the project's repositories, `developer` tokens also write. Other projects, permission targets and the whole management API (`/api/v1/**`, including `/auth/me`) are unreachable with them: the same token against another project's repository gets `403`.
- Minting needs a **session** (an API token cannot mint tokens). The role may not exceed the creator's own role and is capped at `developer`; global admins count as owners.
- The expiry is required when the project limits token lifetime (`token_max_ttl_days`) and may not exceed it.
- A project has at most 100 active tokens, and active names are unique per project (`409` otherwise). A revoked token's name can be reused.
- Maintainers, owners and admins list and revoke every token of the project; other members only their own. Revocation is immediate on every replica that shares the database (the auth cache is invalidated with the write).
- When a token's creator is removed from the project, disabled or deleted, all their tokens in that project are revoked in the same transaction. Access a creator holds through a group stays a membership, so remove the group grant too.
- Expired and revoked tokens and their service accounts are purged by the auth purge worker.
- Project tokens are **not included in backups**: after a restore, mint them again.

`project.token_created` and `project.token_revoked` are audited with the token as the resource. The admin token list (`GET /api/v1/admin/tokens`) shows `project`, `project_role` and `created_by` for project tokens.

## Audit events

Every project mutation writes an audit entry with the project in `project_id`:

| Action | When |
|---|---|
| `project.created`, `project.updated`, `project.deleted` | project lifecycle and `display_name` / `description` changes |
| `project.policy_changed` | a policy field changed (admin) |
| `project.member_added`, `project.member_removed`, `project.member_role_changed` | membership changes |
| `project.group_added`, `project.group_removed`, `project.group_role_changed` | group grants |
| `project.token_created`, `project.token_revoked` | project tokens |

`project.deleted` carries the project ID as the resource ID instead of `project_id`: the audit row's project reference is cleared when the project goes away. Changing the self-creation setting is recorded as `system.setting_changed`. Read the log with [`GET /api/v1/admin/audit`](../integrations/api.md) or `oa audit list`.

## API

All paths are under `/api/v1`, JSON is snake_case, errors are `{"error": message, "code": code}`. `{project}` is the project name. Minimum role in the second column; "admin" means global admin.

| Route | Who | Notes |
|---|---|---|
| `GET /projects` | any authenticated user | `{projects, can_create}`: the caller's projects with `my_role`; `?all=true` is admin-only |
| `POST /projects` | admin, or any human user with `projects.self_create` | body `name`, `display_name`, `description`; admin may add `owner` and policy fields |
| `GET /projects/{project}` | viewer | includes `my_role`, `repository_count`, `is_system` and the policy fields |
| `PATCH /projects/{project}` | owner | `display_name`, `description`; policy fields need an admin (403 for an owner) |
| `DELETE /projects/{project}` | owner | 204; 409 while it owns repositories and for the `system` project |
| `GET /projects/{project}/repositories?page=&limit=` | viewer | `{repositories, total, page, limit}`, live repositories, newest first |
| `POST /projects/{project}/repositories` | maintainer | body of `POST /repositories`; 201; policy errors 400/403, 409 `project_repository_limit` |
| `POST /projects/{project}/repositories/{name}/transfer` | owner (and owner of the target) | body `{to_project}`; 404 for a repository of another project |
| `GET /projects/{project}/members` | viewer | each member has `user_id`, `username`, `role` |
| `POST /projects/{project}/members` | maintainer | body `user_id` or `username` (exact), plus `role` (default `viewer`); 201 |
| `PATCH /projects/{project}/members/{user_id}` | maintainer | body `{role}` |
| `DELETE /projects/{project}/members/{user_id}` | maintainer | 204 |
| `GET /projects/{project}/groups` | viewer | |
| `PUT /projects/{project}/groups/{group}` | maintainer | `{group}` is a group id or its exact name; body `{role}` (`viewer`, `developer`, `maintainer`) |
| `DELETE /projects/{project}/groups/{group}` | maintainer | id or name |
| `GET /projects/{project}/tokens` | viewer | maintainers see all tokens, others their own; `{tokens}` without secrets |
| `POST /projects/{project}/tokens` | viewer | session only; body `name`, `role` (`viewer`, `developer`, at most your own), `expires_at`; 201 with the secret in `token`, shown once; 409 on duplicate name or 100 active tokens |
| `DELETE /projects/{project}/tokens/{token_id}` | viewer | maintainer for tokens of others; 204 |
| `GET`, `PUT /admin/projects/settings` | admin | `{self_create: bool}` |

`GET /api/v1/auth/me` lists the caller's own roles in `projects: [{name, role}]` (admins get no implicit entries there).

### From the CLI

```sh
oa project create platform --display-name "Platform team"
oa project show platform                      # includes my_role
oa project update platform --description "Build and release tooling"
oa project add-member platform --username alice --role developer
oa project set-role platform <user-id> maintainer
oa project add-group platform "ci users" --role developer   # group name or id
```

The full command list is in the [CLI reference](../integrations/cli.md).
