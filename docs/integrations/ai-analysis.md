<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# AI analysis

Use AI analysis to send a package's manifest to an LLM and store a summary, search tags, a license verdict, security notes and a quarantine recommendation next to the artifact. It is off by default. The server reads the manifest from the stored package itself (see [Manifest extraction](#manifest-extraction)).

## Run your first analysis

1. Enable it with `AI_ENABLED=true`, `AI_API_KEY` and, if needed, `AI_PROVIDER` and `AI_MODEL` (see [Settings](#settings)). Admins can also use **Settings > AI analysis**.
2. Trigger an analysis for one package. You need an admin token. `ARTIFACT_ID` is the package ID from the package list.

   ```sh
   curl -sS -X POST "HOST/api/v1/repositories/REPO/ai-analysis/ARTIFACT_ID/trigger" \
     -H "Authorization: Bearer <TOKEN>"
   ```

3. Read the result.

   ```sh
   curl -sS -H "Authorization: Bearer <TOKEN>" "HOST/api/v1/repositories/REPO/ai-analysis/ARTIFACT_ID"
   ```

In the web UI, use **Security > AI analysis**. You can also trigger it through [MCP](mcp.md).

Two rules to know:

- The result is advisory. `quarantine_recommended: true` is recorded for human review and never quarantines anything, because the manifest is attacker-controlled input to the model.
- Analysis does not start on upload unless you turn on `ai.autoAnalyze` (off by default).

## Settings

| YAML key | Env var | Default | Notes |
|---|---|---|---|
| `ai.enabled` | `AI_ENABLED` | `false` | turn the feature on |
| `ai.autoAnalyze` | `AI_AUTO_ANALYZE` | `false` | analyse every upload right after it is stored (needs `ai.enabled`) |
| `ai.provider` | `AI_PROVIDER` | `openai` | `openai`, `azure`, `ollama` or `custom` |
| `ai.baseUrl` | `AI_BASE_URL` | empty | endpoint override; required for `azure` and `custom` |
| `ai.apiKey` | `AI_API_KEY` | empty | required for `openai`/`azure`, empty for local Ollama |
| `ai.model` | `AI_MODEL` | `gpt-4o-mini` | must exist on the provider |
| `ai.maxTokens` | `AI_MAX_TOKENS` | `1024` | max completion tokens per request |
| `ai.timeoutSecs` | `AI_TIMEOUT_SECS` | `30` | HTTP timeout of the provider call (`0` means 30) |
| `ai.systemPrompt` | `AI_SYSTEM_PROMPT` | empty | empty uses the built-in prompt |

Full reference: [`.env.example`](../../.env.example), [`openartifact.example.yaml`](../../openartifact.example.yaml), [configuration](../getting-started/configuration.md).

### Provider behavior

All providers use the OpenAI chat-completions protocol: `POST <base>/chat/completions` for analysis and `GET <base>/models` for the model list, with `Authorization: Bearer <key>` when a key is set. A stored key that cannot be decrypted (`OPENARTIFACT_SECRET_KEY` changed or removed) is never sent: analysis and model listing fail until the key is entered again. The provider only changes the default base URL:

| Provider | Default base URL | Examples |
|---|---|---|
| `openai` | `https://api.openai.com/v1` | any OpenAI-compatible endpoint via `AI_BASE_URL` |
| `ollama` | `http://localhost:11434/v1` | private and loopback endpoints are blocked; set `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true` to use a local Ollama |
| `azure` | none, set `AI_BASE_URL` | `https://<resource>.openai.azure.com/openai/deployments/<deployment>` |
| `custom` | none, set `AI_BASE_URL` | vLLM, LiteLLM, LM Studio or any other OpenAI-compatible server; the admin API refuses `custom` without a base URL |

No other provider is implemented (`vertex` is not supported and the admin API rejects it).

### Change settings at runtime

Admins can configure AI in **Settings > AI analysis** (`/settings/ai`) or with `PUT /api/v1/admin/ai/config`. These values are stored in the database. The API key is stored encrypted with `OPENARTIFACT_SECRET_KEY` and is never returned; reads show a mask and `has_api_key`.

How saved settings combine with env and YAML:

- Once `ai.enabled` has been saved, the database decides `enabled`. Every other field uses the stored value when it is not empty, and the env or YAML value otherwise.
- Without a saved setting, env and YAML apply.
- `auto_analyze` is stored as `ai.auto_analyze` and wins over `AI_AUTO_ANALYZE` whenever it is saved, independent of `ai.enabled`.
- `GET /api/v1/admin/ai/config` returns the effective settings, so AI enabled through env or YAML shows as enabled.

When changes take effect:

- Saving in the UI or API applies immediately. Analyses already running finish on the old settings.
- Other processes (API replicas, `worker` [role](../operations/scaling.md)) pick up stored settings within 30 seconds, without a restart.
- A failed read leaves the settings in use unchanged.
- Env and YAML changes need a restart.

The stored key goes only to the endpoint it was saved for. A `PUT` that changes the provider or base URL must also carry a new `api_key` (or `""` to clear it). Otherwise it answers `400 api_key_required` and saves nothing.

Validation:

- `provider` is `openai`, `azure`, `ollama` or `custom`.
- `base_url` is an absolute http(s) URL.
- `max_tokens` is 0 to 200000, and `timeout_secs` is 0 to 600.
- `system_prompt` is at most 16 KiB. `model`, `base_url` and `api_key` are at most 2048 characters.
- Omitted fields are left unchanged; an empty string clears one.

## API

All of these need the global `manage` permission (administrator) when called over REST; see [REST API](api.md).

| Route | Purpose |
|---|---|
| `GET /api/v1/repositories/{name}/ai-analysis?status=&limit=&offset=` | list analyses; `status` is `pending`, `processing`, `completed` or `failed`; `limit` default 20, max 100 |
| `GET /api/v1/repositories/{name}/ai-analysis/{artifactID}` | one analysis, `404 not_found` if none |
| `POST /api/v1/repositories/{name}/ai-analysis/{artifactID}/trigger` | queue an analysis, returns `202 {"status":"queued"}` |
| `GET /api/v1/admin/ai/config`, `PUT /api/v1/admin/ai/config` | read / update runtime settings |
| `POST /api/v1/admin/ai/test` | list models from the provider; optional body with the same fields as `PUT` tests unsaved settings (a masked or empty key means the stored one, but only while the provider and base URL resolve to the stored endpoint; testing another endpoint needs its key in the request, or an explicit `"api_key": ""` for a keyless one, else `400 api_key_required`). Returns `{"ok":true,"models":[...]}` or `{"ok":false,"error":"..."}` |
| `GET /api/v1/admin/ai/models` | model list from the stored settings; `502 upstream_error` on failure |

```sh
curl -sS -X POST "HOST/api/v1/repositories/REPO/ai-analysis/<ARTIFACT_ID>/trigger" \
  -H "Authorization: Bearer <TOKEN>" -H 'Content-Type: application/json' \
  -d '{"artifact_format":"npm","artifact_name":"left-pad","artifact_version":"1.3.0",
       "manifest_text":"{\"name\":\"left-pad\",\"version\":\"1.3.0\",\"license\":\"MIT\"}"}'

curl -sS -H "Authorization: Bearer <TOKEN>" \
  "HOST/api/v1/repositories/REPO/ai-analysis?status=completed&limit=20"
```

Trigger body (all fields optional): `manifest_text` (max 256 KiB; only the first 64 KiB reach the model), `artifact_format` (one of the repository formats), `artifact_name` and `artifact_version` (max 512 characters each); the last three default to the stored package's values. `artifactID` is the package (component) ID from the package list.

Without `manifest_text` the server reads the manifest from the stored package, so the request is answered `404 not_found` when the package is not in the repository and `422 unsupported_artifact` for kinds a manifest cannot be read from (docker images, raw files). It is `503 ai_disabled` while AI is off. Problems found while extracting (a package without a manifest, an unreadable archive) end the analysis as `failed` with a fixed reason in `error_message`. The UI's re-analyze button sends no manifest and therefore works.

### Manifest extraction

The manifest is read from the stored blob, streaming and bounded (at most 64 KiB of text, archives are read in a single pass with a decompression cap):

| Format | Text sent to the model |
|---|---|
| cargo | `Cargo.toml` of the crate |
| conan | the recipe's `conanfile.py` / `conandata.yml` (the binary package's `conaninfo.txt` is not used) |
| conda | `info/index.json` and `info/about.json` (`.tar.bz2` and `.conda`) |
| deb | control fields (package, version, depends, description, ...) |
| docker, raw | not supported on purpose: images and arbitrary files are never sent to an external model; supply `manifest_text` instead |
| go | `go.mod` (`.mod` file, or inside the module `.zip`) |
| helm | `Chart.yaml` and `requirements.yaml` |
| ivy, maven, sbt | `.pom` / `ivy.xml` as stored; for a `.jar`, `META-INF/maven/*/pom.xml` and `META-INF/MANIFEST.MF` |
| npm | `package.json` of the tarball |
| nuget | `.nuspec` |
| pypi | wheel/egg `METADATA`, sdist `PKG-INFO` (description body cut to 2 KiB) |
| rpm | header summary: name, version, license, summary, requires/provides |
| rubygems | `metadata.gz` of the gem |

With automatic analysis on, ivy, maven and sbt are analysed only for their `.pom` / `ivy.xml` files, not for every `.jar`, sources or javadoc artifact.

### Automatic analysis

With `ai.autoAnalyze` on, every upload in every format is queued for analysis without blocking the client. Proxied (cached) packages are not analysed automatically.

- Repeated uploads of the same package within 2 seconds collapse into one analysis.
- The queue holds 500 entries. When it is full, new requests are dropped with a log warning.
- Queued work is lost on restart, as for manual triggers.

Related MCP tools (per-repository `read` or `write` permission instead of global `manage`; they take the repository UUID): `list_ai_analyses`, `get_artifact_analysis` and `trigger_ai_analysis` (optional `manifest_text`, max 64 KiB). See [MCP](mcp.md).

## Processing

1. A trigger waits 2 s per repository and artifact, so repeated triggers collapse into one. It then joins a queue of 500 entries processed one at a time. When the queue is full the request is dropped with a log warning, and queued work is lost on restart.
2. The record becomes `processing`, then `completed` or `failed`. Each analysis has a 60 s limit.
3. A re-analysis that is `processing` or `failed` keeps the result fields of the last `completed` run. Only the status and error message are new. A new `completed` run replaces them.
4. With AI disabled the trigger is refused with `503`. A request already queued when AI was switched off stays `pending`, and nothing is sent anywhere.
5. Provider or parse failures store a generic `failed` message (`ai analysis failed; see server logs`). The details go to the server log only.

## What is sent to the provider

Per analysis, one request to the configured endpoint (OpenAI cloud, Azure, or your Ollama/compatible host) containing:

- the system prompt (built-in, or `AI_SYSTEM_PROMPT`),
- the manifest (extracted from the package, or the `manifest_text` you submitted), cut to 64 KiB, as the user message,
- the model name, `max_tokens`, `temperature: 0.1`, and the API key as a bearer token.

Not sent: the package's other files, package name/version/format (they are stored locally only), repository names, usernames. But the manifest itself (`package.json`, `pom.xml`, `go.mod`, dependency lists, internal registry URLs, occasionally credentials left in a file) is exactly what leaves your network when the endpoint is external. Point `AI_BASE_URL` at a self-hosted Ollama or compatible server to keep it in-house, and, if your packages are private, leave `ai.autoAnalyze` off or use a provider you trust: with it on, the manifest of every upload is sent without anyone reviewing it.

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
| `model_used` | the model name the provider reported for the request (falls back to the configured model) |
| `artifact_format`, `artifact_name`, `artifact_version` | from the trigger, else from the stored package |
| `created_at`, `updated_at` | |

The manifest text itself is not stored. The REST API returns all fields; the MCP tools omit `error_message` and the token counts.
