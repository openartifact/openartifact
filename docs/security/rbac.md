<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# RBAC: users, groups and permission targets

Access is decided per request from the caller's permission targets: regex filters on repository and path plus a set of actions. How callers authenticate is in [authentication.md](authentication.md).

## Model

A **permission target** has:

| Field | Rule |
|---|---|
| `name` | at most 128 characters |
| `repository_filter` | regex matched against the repository name |
| `path_pattern` | regex matched against the artifact path |
| `actions` | any of `read`, `write`, `delete`, `manage` (`annotate` is accepted but not enforced anywhere) |

Patterns are anchored (`^(?:pattern)$`), `*` alone is shorthand for `.*`, and an empty pattern is rejected. An invalid stored pattern grants nothing (a warning is logged).

A target is **assigned** to a user or a group (`{"principal_type":"user"|"group","principal_id":"…"}`). A caller holds the targets assigned to the user plus those assigned to the user's groups. The first matching target that contains the requested action grants access; there are no deny rules, so access is the union of everything assigned.

Two targets are seeded by the first migration:

| Target | Filters | Actions | Assigned to |
|---|---|---|---|
| `admin-all` | `.*`, `.*` | read, write, delete, manage, annotate | the `admin` user |
| `anonymous-read` | `.*`, `.*` | read | consulted for anonymous callers when `ALLOW_ANONYMOUS_READ` is on |

**"Admin" is not a flag**: a principal is an admin when it holds `manage` on repository `""` and path `""`, i.e. via a target whose repository filter matches everything and whose actions include `manage`. `GET /api/v1/auth/me` reports it as `is_admin`.

## Which action a request needs

| Request | Action | Repository / path |
|---|---|---|
| `/api/v1/*` (management, scans, users, settings…) | `manage` | global |
| `GET/HEAD /api/v1/repositories`, `/api/v1/search` | any authenticated principal | results are filtered per repository by `read` |
| `GET/HEAD /api/v1/repository/{name}` | `read` | `{name}` |
| `/api/v1/repositories/{name}/debs…`, `…/packages…`, `…/comps…` (Debian/RPM upload), `…/uploads…` (presigned raw upload: create and complete) | GET/HEAD `read`, POST/PUT `write`, DELETE `delete` | `{name}`, empty path |
| `GET/PUT/PATCH /api/v1/repositories/{name}/artifacts/{id}/properties`, `DELETE …/properties/{key}`, `GET …/artifacts/search` (artifact properties) | GET `read`; PUT, PATCH and DELETE `write` | `{name}`, empty path |
| `POST /api/v1/bundles`, `/api/v1/bundles/{name}/{version}…` (release bundles) | any authenticated principal, then decided per bundle by the handler, see [bundle permissions](../repositories/release-bundles.md#permissions) | the bundle's source and target repositories |
| `GET /api/v1/bundles` (list), `/api/v1/pipelines…` | `manage` | global |
| `GET /api/v1/repositories/{name}/trash`, `POST /api/v1/repositories/{name}/trash/{kind}/{id}/restore` | `delete` | `{name}`, empty path |
| `DELETE /api/v1/repositories/{name}/trash` (empty the repository trash) | `manage` | `{name}`, empty path |
| `GET /api/v1/trash`, `POST /api/v1/trash/{kind}/{id}/restore`, `DELETE /api/v1/trash/{kind}/{id}` | `manage` | global |
| everything else (package data) | GET/HEAD/OPTIONS `read`, DELETE `delete`, other methods `write` | see below |

Beyond `write`, completing a presigned upload additionally requires being the user who created it or an admin (`403` otherwise); see [Presigned uploads](../operations/storage.md#presigned-uploads).

Repository and path extraction for data requests:

- `/repos/{name}/<path>`: repository `name`, path after it. For `raw`, `maven`, `sbt` and `ivy` the marker form `/repos/{raw|maven|sbt|ivy}/{name}/<path>` is also accepted.
- `/v2/<name>/…` (Docker/OCI): the first segment of `<name>` is the repository and the rest is the image; the RBAC path is `image/<kind>/<ref>` (for example `org/team/app/manifests/1`). A single-segment name addresses the repository's default image.

Trash permissions ([trash](../operations/trash.md)): a principal with `delete` on a repository can list that repository's trash and restore its items, so whoever can delete an artifact can undo it. The global trash list, restoring or purging a whole repository, and purging any item now (`DELETE /api/v1/trash/...`) are admin operations (`manage`). Deleting a repository (`DELETE /api/v1/repository/{name}`) stays an admin operation and now also needs the repository name as confirmation. The `?force=true` override of [immutable releases](../operations/trash.md#immutable-releases) is honoured only for admins; for anyone else it is ignored and the `409` stands. Trashed items and repositories are invisible to every principal on the native protocols and in listings; only the trash endpoints see them.

Rules that apply on top:

- Paths containing `..` segments, or an encoded `/` or `\` in a repository-naming segment, are rejected with 400 `invalid_path`.
- While a user has `must_change_password` set, everything except change-password and logout returns 403 `password_change_required`.
- Reads through proxy and group repositories are re-authorized for each member repository as if requested directly, so a public group never exposes a private member. See [hosted, proxy and group repositories](../repositories/hosted-proxy-group.md).
- The [MCP server](../integrations/mcp.md) uses the same evaluator, per tool rather than per HTTP method: the HTTP layer only requires an authenticated principal, tools need `read` on the repository, `trigger_ai_analysis` needs `write`, and `get_vulnerabilities` needs global `manage`. A denied or missing repository returns `not found or access denied`.
- Static web UI assets are served without RBAC. Self-service endpoints (change own password, own tokens, edit own profile) skip permission targets but need a session.
- A token's `scopes` can only narrow its owner's targets, and a `deploy` token is never granted `manage` ([tokens](authentication.md#api-tokens)).

Example: CI may publish to `releases-*` repositories but only read elsewhere:

```sh
# as an admin, via the API (session or access token with manage)
curl -sS -X POST HOST/api/v1/permission-targets -H 'Authorization: Bearer <TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{"name":"ci-publish","repository_filter":"releases-.*","path_pattern":".*","actions":["read","write"]}'
curl -sS -X POST HOST/api/v1/permission-targets/<TARGET_ID>/assignments -H 'Authorization: Bearer <TOKEN>' \
  -H 'Content-Type: application/json' -d '{"principal_type":"group","principal_id":"<GROUP_ID>"}'
```

## Management API

All of these need admin, except the self-service rows.

| Endpoint | Notes |
|---|---|
| `POST/GET /api/v1/permission-targets` | create / list |
| `GET/PATCH/DELETE /api/v1/permission-targets/{id}` | |
| `POST /api/v1/permission-targets/{id}/assignments` | body `principal_type`, `principal_id` |
| `DELETE /api/v1/permission-targets/{id}/assignments?principal_type=&principal_id=` | |
| `POST/GET /api/v1/users` | create body: `username` (3–64 chars), `email`, `full_name`, `password` (optional), `disabled`, `must_change_password` |
| `GET/PATCH/DELETE /api/v1/users/{id}` | admin PATCH may set `disabled`, `must_change_password`, `password`; disabling keeps the account, blocks login and revokes sessions, and an admin cannot disable themselves. `DELETE` permanently removes the account with its tokens, sessions, memberships and SSO link (use `disabled` to keep it); it is refused for your own account and for the last enabled admin (`409`). Self-edit (session only) is limited to `email` and `full_name` |
| `POST/GET /api/v1/groups`, `GET/PATCH/DELETE /api/v1/groups/{id}` | body `name`, `description` |
| `GET/PUT /api/v1/groups/{id}/members` | `PUT` body `{"members":["<USER_ID>",…]}` replaces the list |
| `POST/DELETE /api/v1/groups/{id}/members/{userId}` | add / remove one member |
| `GET /api/v1/users/{id}/groups` | |

Groups managed by SSO mappings are described in [sso.md](sso.md).

## Projects

A project owns repositories, and its members get access to all of them. This is added to permission targets (union, no deny rules); it never replaces them.

### What membership grants

A user is a member of project P through a `project_members` row or by being P's recorded owner (`owner_id`). Users also get access through groups granted on the project (`project_groups`). On every repository whose project is P, regardless of the repository's name or the artifact path:

| Role in P | Actions |
|---|---|
| `viewer` | read |
| `developer` | read, write |
| `maintainer`, `owner` | read, write, delete |

Group grants (`viewer`, `developer` or `maintainer`; `owner` is personal and cannot be granted to a group) apply to every member of the group. When a user holds several grants (personal membership and any number of groups), the highest role wins.

Limits:

- Project roles **never grant `manage`**. Administration (`/api/v1/*`, users, settings) stays decided by permission targets, so project roles never make anyone a global admin.
- The **built-in `system` project** (id `00000000-0000-0000-0000-000000000001`) is the default project and grants nothing: membership of it, its roles and its group grants are ignored. Repositories created without a `project` live there and stay governed by permission targets only.
- Applies to sessions and to API tokens without scopes (a token inherits its owner's access, like with targets). A token with non-empty `scopes` gets **no** project access: scopes are permission-target IDs and only narrow the owner's targets. Anonymous callers never get project access. A `deploy` token is still never granted `manage`.
- Repository listings (`GET /api/v1/repositories`) and search use the same rule, so a member sees the project's repositories.
- A group repository is authorized by its own project only; its members are still re-authorized individually (see above), so access to a member repository in another project is needed too.

### How repositories get a project

`POST /api/v1/repositories` accepts an optional `project` (project name); without it the repository is created in the `system` project. An unknown name returns 404. A global admin can move a repository with `PATCH /api/v1/repository/{name}` and `{"project":"<name>"}` (use `"system"` to move it back); access changes immediately. Repository responses carry the project name in `project`. Repository names are unique across projects. Maintainers and owners of a project create repositories in it with `POST /api/v1/projects/{project}/repositories`, subject to the project's policy, and administer them (update, delete, cleanup policies, quota, trash emptying) through the delegated `repo.admin` check; owners of two projects can transfer a repository between them. See [projects](projects.md#repository-self-service). `POST /api/v1/repositories` and changing a repository's `project` through `PATCH` stay admin-only. A project that still owns repositories cannot be deleted (409).

### Managing projects

The project routes under `/api/v1/projects/**` are project-scoped: the RBAC middleware decides them from the caller's role in `{project}` (`routeScope.project`, evaluated by `EvaluateProject`), not from the global `manage` permission. A global admin is an implicit owner of every project. Project roles still never grant `manage` for any other route.

Gate rules:

- `GET /api/v1/projects` is open to every authenticated user (the list is the caller's projects; `?all=true` is admin-only). `POST /api/v1/projects` needs an admin, or any human user while the `projects.self_create` setting is on.
- `/api/v1/projects/{project}` and its documented sub-routes need the minimum role listed below. A caller with no role in the project gets `404`, the same answer as for an unknown project; a member whose role is too low gets `403`. Anonymous callers and `deploy` tokens are denied; scoped tokens have no project role.
- Any other sub-path or method under `/api/v1/projects/**` is not project-scoped and falls back to global `manage` (deny by default).
- Maintainers may only add, change or remove members and groups below `maintainer`; changes involving `maintainer` or `owner` need an owner. The recorded owner and the last owner cannot be demoted or removed (`409`). Member and group changes are serialized under a project lock.
- Policy fields (`allowed_formats`, `allowed_repo_types`, `repo_name_prefix`, `allow_public_repos`, `max_repositories`, `token_max_ttl_days`) can only be set by a global admin; an owner sending one gets `403`.

Roles, creation rules, audit events and the full route list: [Projects](projects.md).

| Endpoint | Notes |
|---|---|
| `POST/GET /api/v1/projects` | create (admin, or any user with self-create on, who becomes the owner and may send only `name`, `display_name` and `description`; name `^[a-zA-Z0-9_-]{1,100}$`; admins may also send `owner` (username) and policy fields; the response carries a `slug` derived from the name: lowercased, every run of characters other than letters and digits becomes one hyphen, so `Acme_Team` is `acme-team`; a name or slug collision, such as `Acme-Corp` after `acme-corp`, returns 409, and a name with no letter or digit or a slug over 64 characters returns 400) / list the caller's projects with `my_role` (`{"projects":[…],"can_create":bool}`; `?all=true` for admins) |
| `GET/PATCH/DELETE /api/v1/projects/{project}` | `GET` viewer+; `PATCH` (owner; was `PUT`) is a partial update of `display_name` and `description`, plus the policy fields for admins; `DELETE` (owner) returns 204, or 409 while repositories are still owned and for the `system` project |
| `GET /api/v1/projects/{project}/repositories` | viewer+; paginated (`page`, `limit`) |
| `GET/POST /api/v1/projects/{project}/members`, `PATCH/DELETE /api/v1/projects/{project}/members/{user_id}` | list viewer+, changes maintainer+ (see escalation rules); `POST` takes `user_id` or an exact `username`; `PATCH` (was `PUT`) body `{"role"}`; roles `viewer` (default), `developer`, `maintainer`, `owner` |
| `GET /api/v1/projects/{project}/groups`, `PUT/DELETE /api/v1/projects/{project}/groups/{group}` | group grants; `{group}` is a group id or its exact name; `PUT` body `{"role":"viewer\|developer\|maintainer"}`; changes maintainer+ |
| `GET /api/v1/projects/{project}/activity` | viewer+; audit events of the project and its repositories, keyset paging, no client addresses ([Projects](projects.md#project-activity)) |
| `GET/PUT /api/v1/admin/projects/settings` | admin; `{"self_create": bool}` |

### Caching

Project memberships, group grants and a repository's project are cached per request path like the other auth lookups (`DB_AUTH_CACHE_TTL_MS`, default 2000 ms). Every membership, project or repository change made through this server invalidates the cache at once. Another replica, or a change made directly in the database, is seen after at most the TTL, so a removed member can keep access that long there.
