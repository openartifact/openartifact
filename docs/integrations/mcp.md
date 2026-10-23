<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# MCP server

OpenArtifact can expose a [Model Context Protocol](https://modelcontextprotocol.io) server so AI agents can browse repositories, inspect packages and vulnerabilities, and look up client setup commands. It is disabled by default.

The server implements MCP revision 2026-07-28 with the official [Go SDK](https://github.com/modelcontextprotocol/go-sdk). Older revisions (2025-03-26, 2025-06-18, 2025-11-25) are negotiated by the SDK; legacy clients that call `initialize` are served through an ephemeral session, nothing is stored between requests.

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

JSON-RPC batch arrays are refused with HTTP 400 and JSON-RPC error `-32600` when the `MCP-Protocol-Version` header is `2025-06-18` or later (batching was removed in that revision). Without the header (2025-03-26 clients) batches are processed.

## Authentication

Only `Authorization: Bearer <credential>` is accepted. The session cookie and Basic credentials are **not** accepted on the endpoint: a browser attaches them to requests made by any page, so ignoring them removes the CSRF risk of ambient credentials. There is therefore no CSRF token to send.

| Credential | Notes |
|---|---|
| API token `oa_pat_…` / `oa_dep_…` | create one in the web UI under Settings, Tokens (or `POST /api/v1/auth/tokens` from a login session, see [REST API](api.md#authentication)); API tokens cannot create other tokens. Project tokens (service accounts of a [project](../security/projects.md#project-tokens)) are refused with 403 |
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

For OAuth discovery the server publishes [RFC 9728](https://www.rfc-editor.org/rfc/rfc9728) metadata, public and unauthenticated (CORS `*`, cached for 60 seconds), at `GET /.well-known/oauth-protected-resource` and `GET /.well-known/oauth-protected-resource<mcp.httpPath>`:

| Field | Value |
|---|---|
| `resource` | the canonical URI `<BASE_URL><mcp.httpPath>`; derived from the request when `BASE_URL` is unset |
| `authorization_servers` | the issuers of the enabled OIDC SSO providers (cached for 30 seconds) |
| `bearer_methods_supported` | `["header"]` |

With no OIDC provider configured, `authorization_servers` is **omitted**. RFC 9728 makes the field optional, but the 2026-07-28 specification says a server must include it; this is a deliberate deviation. Such clients cannot discover an authorization server and must use API tokens.

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
| `get_vulnerabilities` | global `manage` (administrator), matching the REST scan endpoints, plus `read` |

Token `scopes` restrict as everywhere. A repository you cannot read behaves as if it does not exist: the tool returns `not found or access denied` in both cases, and `list_repositories` and `search_artifacts` filter by `read` before paginating. Refusals are tool errors (`isError: true`), not HTTP 403. Internal errors return a generic `internal error` and are logged server-side. Results never contain upstream credentials or other secrets. `deploy`-kind tokens are never granted `manage`, so `get_vulnerabilities` is unavailable to them ([authentication](../security/authentication.md#api-tokens)).

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

Then, in a login session of that user, create the token with `"scopes":["<TARGET_ID>"]` (Settings, Tokens, or `POST /api/v1/auth/tokens`, see [authentication](../security/authentication.md#api-tokens)). The token can then read repositories and packages but `trigger_ai_analysis` and `get_vulnerabilities` are refused.

## Tools

Ten tools. Arguments are validated against each tool's JSON schema and unknown arguments are rejected; numeric limits are clamped (`0` or omitted means the default).

| Tool | Purpose |
|---|---|
| `list_repositories(format?, page?, limit?)` | repositories you can read; `page` from 1, `limit` default 20, max 100; returns `total`, `page`, `limit`, `repositories` |
| `get_repository(name)` | one repository (same shape as the REST response) |
| `list_packages(repository, format?, page?, limit?)` | packages of any format, newest first (`limit` default 20, max 100): id, name, version, format, arch, size, sha256, filename, created_at |
| `get_package(repository, name)` | all versions of an exact package name with filename, sha256, size, created_at and download URL (`<BASE_URL>/api/v1/repositories/{repo}/packages/{id}/download`). Scans at most 5,000 listing entries and returns at most 200 versions; `truncated: true` when a bound was hit |
| `get_vulnerabilities(repository, package?, version?)` | severity counts (`critical`, `high`, `medium`, `low`, `unknown`), scan counters (`scanned_packages`, `vulnerable_packages`, `pending_scans`, `failed_scans`, which counts `failed` and `partial` scans) and the most severe findings from stored scans, sorted by severity then CVSS, max 100 (`findings_truncated`); summaries cut at 500 bytes, 5 references per finding. Administrators only (global `manage`) |
| `client_setup(repository)` | `base_url` and configure/publish commands per client; credentials are `<USERNAME>` / `<TOKEN>` placeholders. Same snippets as the UI and `internal/clientsetup` |
| `search_artifacts(query, repository?, format?)` | case-insensitive substring match on name, version, arch and filename over *recent* packages only: the newest 50 of one repository, or the newest 20 of each readable repository, stopping at 50 hits. Not a full-text index; use `GET /api/v1/search` for that |
| `list_ai_analyses(repository_id, status?, license_concern?, limit?, offset?)` | stored AI analyses of a repository; `status` is `pending`, `processing`, `completed` or `failed` (anything else is a tool error); `license_concern` is the string `'true'` to keep only artifacts with `REVIEW_REQUIRED` license status; `limit` default 20, max 100 |
| `get_artifact_analysis(repository_id, artifact_id)` | one stored AI analysis |
| `trigger_ai_analysis(repository_id, artifact_id, manifest_text?, artifact_format?, artifact_name?, artifact_version?)` | queue an analysis; the artifact must exist in the given repository (also when `manifest_text` is supplied); without `manifest_text` (max 64 KiB) the server reads the manifest from the stored artifact; returns `queued` immediately and the result appears asynchronously |

The three AI tools take the repository **UUID** (`repository_id`), not its name, and `artifact_id` is the package/component ID. They only produce results when AI analysis is enabled; see [AI analysis](ai-analysis.md).

### Results and annotations

Results are JSON with snake_case keys, returned as `structuredContent` and as the same JSON in a text block. Every tool advertises an `outputSchema` and `annotations` (`title`, `readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`). All tools are read-only and idempotent except `trigger_ai_analysis` (`readOnlyHint: false`, `destructiveHint: false`, `idempotentHint: false`, `openWorldHint: true` because it calls the AI provider). Annotations are hints for clients, not a security boundary: enforcement is the permission check above.

### Untrusted content

Package names, versions, filenames, descriptions, vulnerability summaries and AI analyses are written by third parties or by a language model, so they can carry prompt injection. Every result that contains them (all tools except `trigger_ai_analysis` and `client_setup`) has top-level `content_trust: "untrusted"` and `notice` fields in `structuredContent` and a second text block starting with `NOTE:`. AI `summary` and `security_notes` are capped at 4,000 bytes and vulnerability summaries at 500. The server `instructions` repeat the warning. Clients should treat these fields as data and never as instructions.

### Errors

- Malformed requests (unknown tool, params that are not a valid `tools/call`) are JSON-RPC errors (`-32602`).
- Argument problems (missing, ill-typed or unknown argument, bad `status`, over-long manifest) and permission refusals are **tool errors**: `isError: true` with a message the model can read, as the 2026-07-28 tools specification (SEP-1303) prescribes.
- Transport refusals use HTTP statuses: 401, 403 (origin, project tokens), 405, 413, 429, and 400 for batches.

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
