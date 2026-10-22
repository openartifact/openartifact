<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# MCP server

OpenArtifact can expose a [Model Context Protocol](https://modelcontextprotocol.io) server so AI agents can browse repositories, inspect packages and vulnerabilities, and look up client setup commands. It is disabled by default.

## Settings

| Setting | Env var | Default | |
|---|---|---|---|
| `mcp.enabled` | `MCP_ENABLED` | `false` | set `true` to enable |
| `mcp.transport` | `MCP_TRANSPORT` | `http` | only `http` is supported; any other value (including `stdio`) is rejected at startup when MCP is enabled |
| `mcp.httpPath` | `MCP_HTTP_PATH` | `/mcp` | mount path of the endpoint |

See [`openartifact.example.yaml`](../../openartifact.example.yaml) and [`.env.example`](../../.env.example); general configuration is in [configuration](../getting-started/configuration.md).

## Endpoint and authentication

The endpoint is a Streamable HTTP server at `<BASE_URL>/mcp` (or your `mcp.httpPath`). It is mounted behind the same RBAC, CSRF and audit chain as the REST API and is never served as an SPA page; an unauthenticated MCP call (`POST`) gets 401.

- Send an API token as `Authorization: Bearer <TOKEN>`. Create one in the web UI under Settings, Tokens (or `POST /api/v1/auth/tokens` from a login session, see [REST API](api.md#authentication)). API tokens cannot create other tokens.
- `BASE_URL` must be the address clients reach: download URLs (`get_package`) and client-setup URLs (`client_setup`) are built from it.

### Permissions

Authorization happens twice:

1. **HTTP gate.** The request itself is evaluated like any non-API route: `POST` (every MCP call) needs the `write` action, `GET` needs `read`, against an empty repository name and path `/mcp`. A non-admin principal therefore needs a permission target that includes `write` and whose repository filter matches the empty name (for example `*`). Tokens limited to specific repository patterns are refused at this gate.
2. **Per tool.** Every repository-scoped tool then checks the caller's permission on that repository:
   - `read` for all tools except below
   - `write` for `trigger_ai_analysis`
   - `get_vulnerabilities` additionally needs the global `manage` permission (administrator), matching the REST scan endpoints

A repository you cannot read behaves as if it does not exist: the tool returns `not found or access denied` in both cases. `list_repositories` and `search_artifacts` filter by `read` before paginating. Internal errors return a generic `internal error` and are logged server-side. Results are JSON with snake_case keys and never contain upstream credentials or other secrets.

Give the agent a token whose permission targets cover only the repositories it should see. `deploy`-kind tokens are never granted the `manage` action, so `get_vulnerabilities` is unavailable to them ([authentication](../security/authentication.md)).

## Tools

| Tool | Purpose |
|---|---|
| `list_repositories(format?, page?, limit?)` | repositories you can read; `page` from 1, `limit` default 20, max 100; returns `total`, `page`, `limit`, `repositories` |
| `get_repository(name)` | one repository (same shape as the REST response) |
| `list_packages(repository, format?, page?, limit?)` | packages of any format, newest first (`limit` default 20, max 100): id, name, version, format, arch, size, sha256, filename, created_at |
| `get_package(repository, name)` | all versions of an exact package name with filename, sha256, size, created_at and download URL (`<BASE_URL>/api/v1/repositories/{repo}/packages/{id}/download`). Scans at most 5,000 listing entries and returns at most 200 versions; `truncated: true` when a bound was hit |
| `get_vulnerabilities(repository, package?, version?)` | severity counts (`critical`, `high`, `medium`, `low`, `unknown`), scan counters (`scanned_packages`, `vulnerable_packages`, `pending_scans`, `failed_scans`, which counts `failed` and `partial` scans) and the most severe findings from stored scans, sorted by severity then CVSS, max 100 (`findings_truncated`); summaries cut at 500 characters, 5 references per finding. Administrators only (global `manage`) |
| `client_setup(repository)` | `base_url` and configure/publish commands per client; credentials are `<USERNAME>` / `<TOKEN>` placeholders. Same snippets as the UI and `internal/clientsetup` |
| `search_artifacts(query, repository?, format?)` | case-insensitive substring match on name, version, arch and filename over *recent* packages only: the newest 50 of one repository, or the newest 20 of each readable repository, stopping at 50 hits. Not a full-text index; use `GET /api/v1/search` for that |
| `list_ai_analyses(repository_id, status?, license_concern?, limit?, offset?)` | stored AI analyses of a repository; `status` is `pending`, `processing`, `completed` or `failed`; `limit` default 20, max 100. `license_concern` is declared in the schema but not applied by the server |
| `get_artifact_analysis(repository_id, artifact_id)` | one stored AI analysis |
| `trigger_ai_analysis(repository_id, artifact_id, manifest_text?, artifact_format?, artifact_name?, artifact_version?)` | queue an analysis; without `manifest_text` (max 64 KiB) the server reads the manifest from the stored artifact; returns `queued` immediately and the result appears asynchronously |

The three AI tools take the repository **UUID** (`repository_id`), not its name, and `artifact_id` is the package/component ID. They only produce results when AI analysis is enabled; see [AI analysis](ai-analysis.md).

## Client configuration

Replace `<BASE_URL>` with your server address (for example `https://artifacts.example.com`) and `<TOKEN>` with an API token. Prefer the environment-variable forms below over writing the token into a file you commit.

**Claude Code**

```sh
claude mcp add --transport http openartifact <BASE_URL>/mcp \
  --header "Authorization: Bearer <TOKEN>"
```

**Claude Desktop** and other clients that read a `mcpServers` JSON file but cannot set HTTP headers: bridge with [`mcp-remote`](https://github.com/geelen/mcp-remote) (`claude_desktop_config.json`). The header value is passed through an environment variable because spaces in `args` can be mangled on some platforms.

```json
{
  "mcpServers": {
    "openartifact": {
      "command": "npx",
      "args": ["mcp-remote", "<BASE_URL>/mcp", "--header", "Authorization:${OA_AUTH_HEADER}"],
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
