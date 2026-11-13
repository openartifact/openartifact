<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# MCP server

Use the MCP server to let AI agents browse repositories, inspect packages, vulnerabilities, licenses and SBOMs, find out why a package version is not installable, read a repository's activity, and look up client setup commands through the [Model Context Protocol](https://modelcontextprotocol.io). It is disabled by default.

## Quick start

1. Enable it: set `mcp.enabled: true` (`MCP_ENABLED=true`) and set `BASE_URL` to the address clients reach.
2. Create an API token, ideally a [read-only one](#read-only-token), under **Settings > Access tokens**.
3. Point your client at `<BASE_URL>/mcp` with the header `Authorization: Bearer <TOKEN>`. Copy-paste setups for common clients are in [Client configuration](#client-configuration).

```sh
claude mcp add --transport http openartifact <BASE_URL>/mcp \
  --header "Authorization: Bearer <TOKEN>"
```

The server implements MCP revision 2026-07-28 with the official [Go SDK](https://github.com/modelcontextprotocol/go-sdk). It also negotiates the older revisions 2025-03-26, 2025-06-18 and 2025-11-25. Clients that call `initialize` get a temporary session; nothing is stored between requests.

## Settings

| Setting | Env var | Default | |
|---|---|---|---|
| `mcp.enabled` | `MCP_ENABLED` | `false` | set `true` to enable |
| `mcp.transport` | `MCP_TRANSPORT` | `http` | only `http` is supported; any other value (including `stdio`) is rejected at startup when MCP is enabled |
| `mcp.httpPath` | `MCP_HTTP_PATH` | `/mcp` | mount path of the endpoint |
| `mcp.allowedOrigins` | `MCP_ALLOWED_ORIGINS` | none | browser origins accepted in `Origin` besides the `BASE_URL` origin, see [Origin validation](#origin-validation) |
| `mcp.maxBodyBytes` | `MCP_MAX_BODY_BYTES` | `1048576` | max size of one request body (413 above) |
| `mcp.rateLimitPerMinute` | `MCP_RATE_LIMIT_PER_MINUTE` | `600` | requests per authenticated principal per minute; `0` disables |
| `mcp.aiTriggerPerMinute` | `MCP_AI_TRIGGER_PER_MINUTE` | `10` | `trigger_ai_analysis` calls per principal per minute; `0` disables |

See [`openartifact.example.yaml`](../../openartifact.example.yaml) and [`.env.example`](../../.env.example); general configuration is in [configuration](../getting-started/configuration.md).

## Endpoint and transport

The endpoint is a stateless Streamable HTTP server at `<BASE_URL>/mcp` (or your `mcp.httpPath`). Only `POST` is served and replies are plain JSON; `GET` and `DELETE` get 405. It is never served as an SPA page. The global IP allowlist and rate limit ([authentication](../security/authentication.md#network-controls)) apply before any MCP check, and `BASE_URL` must be the address clients reach: download URLs (`get_package`) and client-setup URLs (`client_setup`) are built from it.

JSON-RPC batch arrays (several calls in one request) are refused with HTTP 400 and JSON-RPC error `-32600` when the `MCP-Protocol-Version` header is `2025-06-18` or later. Without the header, batches are processed.

## Authentication

Only `Authorization: Bearer <credential>` is accepted. The session cookie and Basic credentials are **not** accepted on the endpoint: a browser attaches them to requests made by any page, so ignoring them removes the CSRF risk of ambient credentials. There is therefore no CSRF token to send.

| Credential | Notes |
|---|---|
| API token `oa_pat_…` / `oa_dep_…` | create one in the web UI under **Settings > Access tokens** (or `POST /api/v1/auth/tokens` from a login session, see [REST API](api.md#authentication)); API tokens cannot create other tokens. Project tokens (service accounts of a [project](../security/projects.md#project-tokens)) are refused with 403 |
| OIDC access token (JWT) | from an enabled [OIDC SSO provider](../security/sso.md); see below |
| legacy `API_KEY` | deprecated, full admin; see [authentication](../security/authentication.md#legacy-api-key). Do not use it for agents |

A missing or invalid credential gets 401 with

```text
WWW-Authenticate: Bearer realm="openartifact", resource_metadata="<BASE_URL>/.well-known/oauth-protected-resource/mcp", error="invalid_token"
```

`error="invalid_token"` is only added when a credential was sent, and `resource_metadata` only when `BASE_URL` is set.

### OIDC access tokens

A client that supports OAuth can obtain an access token from your identity provider and send it as a bearer. The server accepts it when:

- it is a JWT signed by an enabled OIDC SSO provider (matched by `iss`), unexpired, and its `aud` contains the canonical resource URI `<BASE_URL><mcp.httpPath>` (for example `https://artifacts.example.com/mcp`; a trailing slash is ignored);
- its subject is already linked to an **existing** local user through that provider. Access tokens never create users, and disabled users are refused;
- `BASE_URL` is set. Without it OIDC access tokens are not accepted (API tokens still work).

Opaque (non-JWT) access tokens are not supported. The ID token minted for web login is rejected because its `aud` is the client id. Scope claims are ignored: the user's full permissions apply, so use an API token with a [read-only scope](#read-only-token) when the agent must be restricted. SSO setup is in [sso](../security/sso.md).

### Protected resource metadata

OAuth-capable clients use a standard discovery document ([RFC 9728](https://www.rfc-editor.org/rfc/rfc9728), "OAuth protected resource metadata") to learn which login servers the endpoint trusts. The server publishes it without authentication, allows any origin (CORS `*`) and caches it for 60 seconds, at `GET /.well-known/oauth-protected-resource` and `GET /.well-known/oauth-protected-resource<mcp.httpPath>`:

| Field | Value |
|---|---|
| `resource` | the canonical URI `<BASE_URL><mcp.httpPath>`; derived from the request when `BASE_URL` is unset |
| `authorization_servers` | the issuers of the enabled OIDC SSO providers (cached for 30 seconds) |
| `bearer_methods_supported` | `["header"]` |

With no OIDC provider configured, `authorization_servers` is omitted. The MCP 2026-07-28 specification asks servers to include it, so a client that insists on it cannot discover a login server. Use API tokens with such clients.

### Origin validation

A request that carries an `Origin` header must match the origin of `BASE_URL` or an entry of `mcp.allowedOrigins`, otherwise it gets 403. The check runs before authentication. Requests without `Origin` (CLIs, desktop agents) pass; `Origin: null` is refused. Add the origin of a browser-based MCP client hosted elsewhere, comma-separated, for example `MCP_ALLOWED_ORIGINS=https://client.example.com`.

### Limits

| Limit | Result |
|---|---|
| body above `mcp.maxBodyBytes` | 413 |
| more than `mcp.rateLimitPerMinute` requests per principal | 429 with `Retry-After`. Token bucket with a one-minute burst, in-process (each replica counts separately) |
| more than `mcp.aiTriggerPerMinute` calls of `trigger_ai_analysis` per principal | tool error `rate limit exceeded; retry later` |

## Permissions

Authorization is per tool, not per HTTP method. The HTTP layer only requires an authenticated principal, so a read-only token can use the read tools. Each tool then evaluates the caller's own permission through the same evaluator as the REST API ([RBAC](../security/rbac.md)):

| Tool | Needs |
|---|---|
| all repository-scoped tools except below | `read` on the repository |
| `trigger_ai_analysis` | `write` on the repository |
| `get_vulnerabilities`, `get_licenses`, `get_sbom` | global `manage` (administrator), matching the REST scan, license and SBOM endpoints, plus `read` |
| `get_repository_activity` | `read` on the repository; the categories `cleanup`, `settings`, `curation` and `federation` also need repository admin (a global administrator, or a maintainer of its project) |

Token `scopes` restrict as everywhere. A repository you cannot read behaves as if it does not exist: the tool returns `not found or access denied` in both cases, and `list_repositories` and `search_artifacts` filter by `read` before paginating. Refusals are tool errors (`isError: true`), not HTTP 403. Internal errors return a generic `internal error` and are logged server-side. Results never contain upstream credentials or other secrets. `deploy`-kind tokens are never granted `manage`, so `get_vulnerabilities`, `get_licenses` and `get_sbom` are unavailable to them ([authentication](../security/authentication.md#api-tokens)).

### Read-only token

An agent that should only look, never trigger analyses, needs a token limited to `read`. A token's `scopes` are permission-target IDs and only narrow what its owner holds, so the owner must hold the read-only target too. As an administrator:

```sh
# 1. a read-only target covering every repository (use a narrower repository_filter to limit further)
curl -sS -X POST HOST/api/v1/permission-targets -H 'Authorization: Bearer <ADMIN_TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{"name":"agent-read","repository_filter":".*","path_pattern":".*","actions":["read"]}'
# 2. assign it to the user (or a group) who will own the token
curl -sS -X POST HOST/api/v1/permission-targets/<TARGET_ID>/assignments -H 'Authorization: Bearer <ADMIN_TOKEN>' \
  -H 'Content-Type: application/json' -d '{"principal_type":"user","principal_id":"<USER_ID>"}'
```

Then, in a login session of that user, create the token with `"scopes":["<TARGET_ID>"]` (**Settings > Access tokens**, or `POST /api/v1/auth/tokens`, see [authentication](../security/authentication.md#api-tokens)). The token can then read repositories and packages but `trigger_ai_analysis`, `get_vulnerabilities`, `get_licenses` and `get_sbom` are refused, and `get_repository_activity` leaves out the administrator categories.

## Tools

Sixteen tools. Arguments are validated against each tool's JSON schema and unknown arguments are rejected; numeric limits are clamped (`0` or omitted means the default).

| Tool | Purpose |
|---|---|
| `list_repositories(format?, page?, limit?)` | repositories you can read; `page` from 1, `limit` default 20, max 100; returns `total`, `page`, `limit`, `repositories` |
| `get_repository(name)` | one repository (same shape as the REST response); a group adds `members`, its member repositories (`name`, `format`, `repo_type`, `priority`) in group order: the members serving consults, so not nested groups, members of another format, blocked members or the ones you cannot read. The first member in that order that has a version decides it, also when it holds the version back (that member's 403 fails the install), and unless `merge_proxy_versions` is set a package name a hosted member holds is decided by the hosted members only |
| `list_packages(repository, format?, page?, limit?)` | packages of any format, newest first (`limit` default 20, max 100): id, name, version, format, arch, size, sha256, filename, created_at |
| `get_package(repository, name)` | all versions of an exact package name with filename, sha256, size, created_at and download URL (`<BASE_URL>/api/v1/repositories/{repo}/packages/{id}/download`). Scans at most 5,000 listing entries and returns at most 200 versions; `truncated: true` when a bound was hit |
| `get_vulnerabilities(repository, package?, version?)` | severity counts (`critical`, `high`, `medium`, `low`, `unknown`), scan counters (`scanned_packages`, `vulnerable_packages`, `pending_scans`, `failed_scans`, which counts `failed` and `partial` scans) and the most severe findings from stored scans, sorted by severity then CVSS, max 100 (`findings_truncated`); summaries cut at 500 bytes, 5 references per finding. Administrators only (global `manage`) |
| `client_setup(repository)` | `base_url` and configure/publish commands per client; credentials are `<USERNAME>` / `<TOKEN>` placeholders. Same snippets as the web UI |
| `search_artifacts(query, repository?, format?)` | case-insensitive substring match on name, version, arch and filename over *recent* packages only: the newest 50 of one repository, or the newest 20 of each readable repository, stopping at 50 hits. Not a full-text index; use `GET /api/v1/search` for that. Without `repository`, `query` takes the qualifiers of `GET /api/v1/search` (`name:`, `format:`, `repo:`, `project:`, `project_role:`, `prop:KEY=VALUE`) with the same rules: an unknown `project_role`, more than 50 `project:` or `project_role:` values, or a malformed or 11th `prop:` is a tool error |
| `list_ai_analyses(repository_id, status?, license_concern?, limit?, offset?)` | stored AI analyses of a repository; `status` is `pending`, `processing`, `completed` or `failed` (anything else is a tool error); `license_concern` is the string `'true'` to keep only artifacts with `REVIEW_REQUIRED` license status; `limit` default 20, max 100 |
| `get_artifact_analysis(repository_id, artifact_id)` | one stored AI analysis |
| `trigger_ai_analysis(repository_id, artifact_id, manifest_text?, artifact_format?, artifact_name?, artifact_version?)` | queue an analysis; the artifact must exist in the given repository (also when `manifest_text` is supplied); without `manifest_text` (max 64 KiB) the server reads the manifest from the stored artifact; returns `queued` immediately and the result appears asynchronously |

### Availability, licenses, SBOMs and activity

These seven tools answer what an assistant is asked after "install it": why a version cannot be installed, which versions are coming, what licenses and components a package has, and what changed in a repository. They are read-only and never start a scan, a fetch from the upstream or an SBOM generation.

| Tool | Use it when | Example question |
|---|---|---|
| `check_availability(repository, package, version)` | an install fails with 404 or 403, or before you pin a version | "Why can't I install left-pad 1.1.0 from web-npm?" |
| `check_dependencies(repository, packages)` | before an install, for a lockfile or several dependencies at once, also through a group | "Will everything in this composer.lock install from php?" |
| `list_held_back_versions(repository, package)` | you need the versions a proxy hides and when each unlocks | "When does the newest left-pad become installable?" |
| `list_proxy_versions(repository, package)` | you need every upstream version, whether it is held and whether it is cached | "Which left-pad versions can I install offline?" |
| `get_licenses(repository, artifact_id)` | you need the licenses of one scanned artifact | "What licenses does this artifact declare, and is it quarantined?" |
| `get_sbom(repository, artifact_id, format?, limit?)` | you need the components of an artifact's stored SBOM | "Which packages does this image contain?" |
| `get_repository_activity(repository, category?, limit?, cursor?)` | you need to know who changed what and when | "Who deleted left-pad 1.0.0 from images last week?" |

`check_availability` returns `available` and a list of `reasons`. Each reason has a `source`:

| Source | Meaning |
|---|---|
| `min_age` | the version is younger than the proxy's minimum age; `until` is when it unlocks |
| `curation` | a curation rule or the allowlist hides it; `rule_id` names the rule and `detail` quotes its reason |
| `quarantine`, `license_policy` | a scan or the license policy holds a stored version |
| `repository_blocked` | the repository is blocked |
| `not_found` | a hosted repository stores no such package version, judged on the names `get_package` lists |

The answer is the one of `GET /api/v1/repositories/{name}/availability` (see [minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [package curation](../repositories/hosted-proxy-group.md#package-curation)), except that `not_found` exists only here. `min_age` is `off` without a minimum age, `checked` when the publish time is known and `unknown` when the proxy has not cached the version's metadata yet. The upstream is never asked. A group repository has no verdict: use `check_dependencies`, which answers for a group, or ask its members, which `get_repository` lists in group order. The rule's pattern is not returned, only its id and reason.

`check_dependencies` takes up to 1000 `{package, version}` entries and returns one result per entry with `status` (`available`, `held`, `not_found` or `unknown`) and `repository`, the repository that decided. It is `POST /api/v1/repositories/{name}/availability`, including how a group decides between its members; see [check many versions at once](../repositories/hosted-proxy-group.md#check-many-versions-at-once).

`list_held_back_versions` and `list_proxy_versions` work on proxy repositories only and return at most 200 versions, newest first, with `truncated: true` when more exist. `supported: false` means the format cannot list versions or nothing is cached for the package yet; fetch the package once.

`get_sbom` returns the stored SBOM's `report_id`, `format`, `spec_version`, `generated_at`, `component_count` and its components (`name`, `version`, `purl`, `licenses`): at most `limit` of them (default 100, max 500), with `truncated: true` when the document has more. `format` is `cyclonedx`, `spdx-json`, `spdx3-json` or `spdx`; the newest stored one is used by default and `available_formats` lists the others. An artifact without a stored SBOM is a tool error: [generate one](../security/sbom.md) first. `get_licenses` takes the same `artifact_id` and never returns the scan's error text.

`get_repository_activity` pages with `next_cursor`, passed back as `cursor`. The categories open to anyone who can read the repository are `uploads`, `deletions`, `trash`, `promotions` and `quarantine`. Asking for `cleanup`, `settings`, `curation` or `federation` without repository admin is a tool error. Without `category` you get every category you may see.

The three AI tools take the repository **UUID** (`repository_id`), not its name, and `artifact_id` is the package/component ID. They only produce results when AI analysis is enabled; see [AI analysis](ai-analysis.md).

### Results and annotations

Results are JSON with snake_case keys, returned as `structuredContent` and as the same JSON in a text block. Every tool advertises an `outputSchema` and `annotations` (`title`, `readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`). All tools are read-only and idempotent except `trigger_ai_analysis` (`readOnlyHint: false`, `destructiveHint: false`, `idempotentHint: false`, `openWorldHint: true` because it calls the AI provider). Annotations are hints for clients, not a security boundary: enforcement is the permission check above.

### Untrusted content

Package names, versions, filenames, descriptions, vulnerability summaries, licenses, SBOM components, activity messages, curation rule reasons and AI analyses are written by third parties or by a language model, so they can carry prompt injection. Every result that contains them (all tools except `trigger_ai_analysis` and `client_setup`) has top-level `content_trust: "untrusted"` and `notice` fields in `structuredContent` and a second text block starting with `NOTE:`. AI `summary` and `security_notes` are capped at 4,000 bytes, vulnerability summaries at 500, curation reasons at 4,000, license and SBOM text at 500 and 300 bytes per field, and activity messages at 1,000. The server `instructions` repeat the warning. Clients should treat these fields as data and never as instructions.

### Errors

- Malformed requests (unknown tool, params that are not a valid `tools/call`) are JSON-RPC errors (`-32602`).
- Argument problems (missing, ill-typed or unknown argument, bad `status`, over-long manifest) and permission refusals are **tool errors**: `isError: true` with a message the model can read, as the MCP 2026-07-28 specification prescribes. This lets the model read the message and correct its call.
- Transport refusals use HTTP statuses: 401, 403 (origin, project tokens), 405, 413, 429, and 400 for batches.

## Install the agent plugin in Claude Code or Cursor

The [openartifact-agent-plugin](https://github.com/openartifact/openartifact-agent-plugin) connects Claude Code or Cursor to this server and adds skills that tell the agent when to use which tool:

| Skill | What the agent does |
|---|---|
| `configure-project` | points Composer, npm, pip and other package managers at a repository, keeping the token out of the project |
| `check-dependency` | checks a version before adding or upgrading it, and picks one the repository serves |
| `check-lockfile` | checks every dependency of a `composer.lock` or `package-lock.json` in one `check_dependencies` call |
| `find-artifact` | finds packages across repositories by name, project, project role or property |
| `explain-install-failure` | turns a `403` or `404` into the reason, and a fix that respects it |

The skills only read, and tell the agent never to work around a hold (no public-registry fallback). In Claude Code:

```sh
claude plugin marketplace add openartifact/openartifact-agent-plugin
claude plugin install openartifact@openartifact
```

Then set the server URL and an access token with `/plugin configure openartifact@openartifact` inside Claude Code. Claude Code keeps the token in secure storage. A [read-only token](#read-only-token) is enough.

`check-lockfile` uses the `check_dependencies` tool. On a server without that tool, it checks the direct dependencies one by one instead.

Cursor setup is in the plugin's README.

Without the plugin, configure the client by hand as below.

## Client configuration

Replace `<BASE_URL>` with your server address (for example `https://artifacts.example.com`) and `<TOKEN>` with an API token, ideally a [read-only one](#read-only-token). Every client must send it as `Authorization: Bearer <TOKEN>`; cookies and Basic auth do not work on the endpoint. Prefer the environment-variable forms below over writing the token into a file you commit.

**Claude Code**

```sh
claude mcp add --transport http openartifact <BASE_URL>/mcp \
  --header "Authorization: Bearer <TOKEN>"
```

**Claude Desktop** and other clients that read a `mcpServers` JSON file but cannot set HTTP headers: the config file only supports local (stdio) servers, so bridge with [`mcp-remote`](https://github.com/geelen/mcp-remote) (`claude_desktop_config.json`). The header value is passed through an environment variable because spaces in `args` can be mangled on some platforms. Remote connectors that support OAuth can use the [OIDC access token](#oidc-access-tokens) path instead of a token.

```json
{
  "mcpServers": {
    "openartifact": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "<BASE_URL>/mcp", "--header", "Authorization:${OA_AUTH_HEADER}"],
      "env": { "OA_AUTH_HEADER": "Bearer <TOKEN>" }
    }
  }
}
```

**Cursor** (`.cursor/mcp.json` or `~/.cursor/mcp.json`)

```json
{
  "mcpServers": {
    "openartifact": {
      "url": "<BASE_URL>/mcp",
      "headers": { "Authorization": "Bearer ${env:OPENARTIFACT_TOKEN}" }
    }
  }
}
```

**VS Code** (`.vscode/mcp.json`); the token is prompted for and stored by VS Code

```json
{
  "inputs": [
    { "type": "promptString", "id": "openartifact-token", "description": "OpenArtifact API token", "password": true }
  ],
  "servers": {
    "openartifact": {
      "type": "http",
      "url": "<BASE_URL>/mcp",
      "headers": { "Authorization": "Bearer ${input:openartifact-token}" }
    }
  }
}
```

**OpenAI Codex CLI** (`~/.codex/config.toml`); Codex reads the token from the named environment variable and sends it as a bearer token

```toml
[mcp_servers.openartifact]
url = "<BASE_URL>/mcp"
bearer_token_env_var = "OPENARTIFACT_TOKEN"
```

**oh-my-pi (omp)** (`~/.omp/agent/mcp.json` or project `.omp/mcp.json`)

```json
{
  "mcpServers": {
    "openartifact": {
      "type": "http",
      "url": "<BASE_URL>/mcp",
      "headers": { "Authorization": "Bearer ${OPENARTIFACT_TOKEN}" }
    }
  }
}
```

**Any other client:** point it at `<BASE_URL>/mcp` as a Streamable HTTP server and add the `Authorization: Bearer <TOKEN>` header; see the client's documentation for the exact file format.
