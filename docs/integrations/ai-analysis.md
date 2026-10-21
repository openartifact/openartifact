<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# AI analysis

Optional, off by default: send a package's manifest text to an LLM and store a summary, search tags, a license verdict, security notes and a quarantine recommendation next to the artifact.

- The result is advisory. A `quarantine_recommended: true` is recorded and logged for human review; it never quarantines anything by itself, because the manifest is attacker-controlled prompt input.
- Analysis is **not** started automatically on upload. You trigger it per artifact: UI (Security, AI analysis), REST or [MCP](mcp.md), and you supply the manifest text in the request.

## Settings

| YAML key | Env var | Default | Notes |
|---|---|---|---|
| `ai.enabled` | `AI_ENABLED` | `false` | turn the feature on |
| `ai.provider` | `AI_PROVIDER` | `openai` | `openai`, `azure` or `ollama` |
| `ai.baseUrl` | `AI_BASE_URL` | empty | endpoint override; required for `azure` |
| `ai.apiKey` | `AI_API_KEY` | empty | required for `openai`/`azure`, empty for local Ollama |
| `ai.model` | `AI_MODEL` | `gpt-4o-mini` | must exist on the provider |
| `ai.maxTokens` | `AI_MAX_TOKENS` | `1024` | max completion tokens per request |
| `ai.timeoutSecs` | `AI_TIMEOUT_SECS` | `30` | HTTP timeout of the provider call (`0` means 30) |
| `ai.systemPrompt` | `AI_SYSTEM_PROMPT` | empty | empty uses the built-in prompt |

Full reference: [`.env.example`](../../.env.example), [`openartifact.example.yaml`](../../openartifact.example.yaml), [configuration](../getting-started/configuration.md).

### Provider behavior

All providers use the OpenAI chat-completions protocol: `POST <base>/chat/completions` for analysis and `GET <base>/models` for the model list, with `Authorization: Bearer <key>` when a key is set. The provider only changes the default base URL:

| Provider | Default base URL | Examples |
|---|---|---|
| `openai` | `https://api.openai.com/v1` | any OpenAI-compatible endpoint via `AI_BASE_URL` |
| `ollama` | `http://localhost:11434/v1` | |
| `azure` | none, set `AI_BASE_URL` | `https://<resource>.openai.azure.com/openai/deployments/<deployment>` |

No other provider is implemented (the example config comments also mention `vertex`; the server does not support it and the admin API rejects it).

### Runtime settings (UI / admin API)

Admins can also configure AI at `/admin/ai` in the UI or with `PUT /api/v1/admin/ai/config`. Those values are stored in the database (the API key encrypted with `OPENARTIFACT_SECRET_KEY`, and never returned: reads show a mask and `has_api_key`).

- Once `ai.enabled` has been saved there, the database decides `enabled`; every other field uses the stored value when non-empty and falls back to the env/YAML value otherwise. Without a saved setting, env/YAML apply.
- The effective settings are read **once at startup**: restart after changing them.
- `GET /api/v1/admin/ai/config` returns only the database-stored values, not the env/YAML ones.
- Validation: `provider` in `openai|azure|ollama`; `base_url` an absolute http(s) URL; `max_tokens` 0 to 200000; `timeout_secs` 0 to 600; `system_prompt` at most 16 KiB; `model`, `base_url`, `api_key` at most 2048 characters. Omitted fields are left unchanged; an empty string clears one.

## API

All of these need the global `manage` permission (administrator) when called over REST; see [REST API](api.md).

| Route | Purpose |
|---|---|
| `GET /api/v1/repositories/{name}/ai-analysis?status=&limit=&offset=` | list analyses; `status` is `pending`, `processing`, `completed` or `failed`; `limit` default 20, max 100 |
| `GET /api/v1/repositories/{name}/ai-analysis/{artifactID}` | one analysis, `404 not_found` if none |
| `POST /api/v1/repositories/{name}/ai-analysis/{artifactID}/trigger` | queue an analysis, returns `202 {"status":"queued"}` |
| `GET /api/v1/admin/ai/config`, `PUT /api/v1/admin/ai/config` | read / update runtime settings |
| `POST /api/v1/admin/ai/test` | list models from the provider; optional body with the same fields as `PUT` tests unsaved settings (a masked or empty key means the stored one). Returns `{"ok":true,"models":[...]}` or `{"ok":false,"error":"..."}` |
| `GET /api/v1/admin/ai/models` | model list from the stored settings; `502 upstream_error` on failure |

```sh
curl -sS -X POST "HOST/api/v1/repositories/REPO/ai-analysis/<ARTIFACT_ID>/trigger" \
  -H "Authorization: Bearer <TOKEN>" -H 'Content-Type: application/json' \
  -d '{"artifact_format":"npm","artifact_name":"left-pad","artifact_version":"1.3.0",
       "manifest_text":"{\"name\":\"left-pad\",\"version\":\"1.3.0\",\"license\":\"MIT\"}"}'

curl -sS -H "Authorization: Bearer <TOKEN>" \
  "HOST/api/v1/repositories/REPO/ai-analysis?status=completed&limit=20"
```

Trigger body: `manifest_text` (max 256 KiB; only the first 64 KiB reach the model), `artifact_format` (one of the repository formats), `artifact_name` and `artifact_version` (max 512 characters each). `artifactID` is the package (component) ID from the package list. The server does not read the manifest from the stored package: a trigger without `manifest_text` ends as `failed` (`no manifest text available for analysis`). This includes the UI's re-analyze button, which sends no manifest.

Related MCP tools (per-repository `read`/`write` permission instead of global `manage`; they take the repository UUID): `list_ai_analyses`, `get_artifact_analysis`, `trigger_ai_analysis` (manifest max 64 KiB). See [MCP](mcp.md).

## Processing

1. A trigger is debounced for 2 s per repository and artifact (repeated triggers collapse into one), then put on an in-memory queue of 500 entries processed by a single worker. A full queue drops the request with a log warning, and queued work is lost on restart.
2. The record becomes `processing`, then `completed` or `failed`. Each analysis has a 60 s overall limit.
3. With AI disabled the trigger is still accepted, but nothing is sent anywhere: the record stays `pending`.
4. Provider or parse failures store a generic `failed` message (`ai analysis failed; see server logs`); details go to the server log only.

## What is sent to the provider

Per analysis, one request to the configured endpoint (OpenAI cloud, Azure, or your Ollama/compatible host) containing:

- the system prompt (built-in, or `AI_SYSTEM_PROMPT`),
- the `manifest_text` you submitted, cut to 64 KiB, as the user message,
- the model name, `max_tokens`, `temperature: 0.1`, and the API key as a bearer token.

Not sent: package files, package name/version/format (they are stored locally only), repository names, usernames. But the manifest itself (`package.json`, `pom.xml`, `go.mod`, dependency lists, internal registry URLs, occasionally credentials left in a file) is exactly what leaves your network when the endpoint is external. Point `AI_BASE_URL` at a self-hosted Ollama or compatible server to keep it in-house, and submit only manifests you are willing to share.

The provider's reply is capped at 64 KiB and must be one JSON object (code fences are stripped). It is untrusted input and is normalized before it is stored.

## What is stored

Postgres, one record per repository and artifact (re-analysis overwrites it):

| Field | Notes |
|---|---|
| `status`, `error_message` | lifecycle above |
| `summary` | max 2000 bytes |
| `tags` | at most 10, 64 bytes each |
| `license_status`, `license_name` | `SAFE`, `REVIEW_REQUIRED` or `UNKNOWN` (any other value is stored as `UNKNOWN`); name max 200 bytes |
| `security_notes` | max 4000 bytes |
| `quarantine_recommended` | advisory flag |
| `prompt_tokens`, `completion_tokens` | usage reported by the provider |
| `artifact_format`, `artifact_name`, `artifact_version`, `created_at`, `updated_at` | from the trigger |

The manifest text itself is not stored. The REST API returns all fields; the MCP tools omit `error_message` and the token counts.
