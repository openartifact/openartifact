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
| everything else (package data) | GET/HEAD/OPTIONS `read`, DELETE `delete`, other methods `write` | see below |

Repository and path extraction for data requests:

- `/repos/{name}/<path>`: repository `name`, path after it. For `raw`, `maven`, `sbt` and `ivy` the marker form `/repos/{raw|maven|sbt|ivy}/{name}/<path>` is also accepted.
- `/v2/<name>/…` (Docker/OCI): the first segment of `<name>` is the repository and the rest is the image; the RBAC path is `image/<kind>/<ref>` (for example `org/team/app/manifests/1`). A single-segment name addresses the repository's default image.

Rules that apply on top:

- Paths containing `..` segments, or an encoded `/` or `\` in a repository-naming segment, are rejected with 400 `invalid_path`.
- While a user has `must_change_password` set, everything except change-password and logout returns 403 `password_change_required`.
- Reads through proxy and group repositories are re-authorized for each member repository as if requested directly, so a public group never exposes a private member. See [hosted, proxy and group repositories](../repositories/hosted-proxy-group.md).
- The [MCP server](../integrations/mcp.md) uses the same evaluator; `get_vulnerabilities` needs global `manage`.
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
| `GET/PATCH/DELETE /api/v1/users/{id}` | admin PATCH may set `disabled`, `must_change_password`, `password`; disabling revokes sessions and an admin cannot disable themselves. Self-edit (session only) is limited to `email` and `full_name` |
| `POST/GET /api/v1/groups`, `GET/PATCH/DELETE /api/v1/groups/{id}` | body `name`, `description` |
| `GET/PUT /api/v1/groups/{id}/members` | `PUT` body `{"members":["<USER_ID>",…]}` replaces the list |
| `POST/DELETE /api/v1/groups/{id}/members/{userId}` | add / remove one member |
| `GET /api/v1/users/{id}/groups` | |

Groups managed by SSO mappings are described in [sso.md](sso.md).

## Organizations

An organization owns repositories, and its members get access to all of them. This is added to permission targets (union, no deny rules); it never replaces them.

### What membership grants

A user is a member of organization O through an `organization_members` row or by being O's recorded owner (`owner_id`). On every repository whose organization is O, regardless of the repository's name or the artifact path:

| Membership role in O | Access on O's repositories | Actions |
|---|---|---|
| `owner` or `admin` | `admin` | read, write, delete |
| `member` | the organization's `member_role` (`read` by default) | `read`: read. `write`: read, write. `admin`: read, write, delete |

`member_role` (`read`, `write` or `admin`) is a property of the organization, set at creation or with `PUT /api/v1/orgs/{org}`; it applies to every plain member.

Limits:

- Organization roles **never grant `manage`**. Administration (`/api/v1/*`, users, settings) stays decided by permission targets, so org roles never make anyone a global admin.
- The **built-in `system` organization** (id `00000000-0000-0000-0000-000000000001`) is the legacy default organization and grants nothing: membership of it, its roles and its `member_role` are ignored. Repositories created before this feature, and repositories created without an `org`, live there and stay governed by permission targets only.
- Applies to sessions and to API tokens without scopes (a token inherits its owner's access, like with targets). A token with non-empty `scopes` gets **no** organization access: scopes are permission-target IDs and only narrow the owner's targets. Anonymous callers never get organization access. A `deploy` token is still never granted `manage`.
- Group membership does not confer organization membership.
- Repository listings (`GET /api/v1/repositories`) and search use the same rule, so a member sees the organization's repositories.
- A group repository is authorized by its own organization only; its members are still re-authorized individually (see above), so access to a member repository in another organization is needed too.

### How repositories get an organization

`POST /api/v1/repositories` accepts an optional `org` (organization name); without it the repository is created in the `system` organization. An unknown name returns 404. A global admin can move a repository with `PATCH /api/v1/repository/{name}` and `{"org":"<name>"}` (use `"system"` to move it back); access changes immediately. Repository responses carry the organization name in `org`. Repository names are unique across organizations. An organization that still owns repositories cannot be deleted (409).

### Managing organizations

| Endpoint | Notes |
|---|---|
| `POST/GET /api/v1/orgs` | create (name `^[a-zA-Z0-9_-]{1,100}$`, optional `member_role`; the creator becomes owner and member; the response carries a `slug` derived from the name: lowercased, every run of characters other than letters and digits becomes one hyphen, so `Acme_Team` is `acme-team`; a name or slug collision, such as `Acme-Corp` after `acme-corp`, returns 409, and a name with no letter or digit or a slug over 64 characters returns 400) / list the organizations the caller belongs to |
| `GET/PUT/DELETE /api/v1/orgs/{org}` | update (`display_name`, `description`, `member_role`) needs an org owner or admin; delete needs the owner and returns 409 while repositories are still owned |
| `GET/POST /api/v1/orgs/{org}/members`, `PUT/DELETE /api/v1/orgs/{org}/members/{user_id}` | roles `owner`, `admin`, `member` |

All of these sit behind `/api/v1/*`, so they need the global `manage` permission: only global admins can call them, and the per-organization owner/admin checks apply on top of that. Org roles do not open this API to non-admins.

### Caching

Organization memberships, `member_role` and a repository's organization are cached per request path like the other auth lookups (`DB_AUTH_CACHE_TTL_MS`, default 2000 ms). Every membership, organization or repository change made through this server invalidates the cache at once. Another replica, or a change made directly in the database, is seen after at most the TTL, so a removed member can keep access that long there.
