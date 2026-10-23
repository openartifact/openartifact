<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Dependency-Track

OpenArtifact can feed [OWASP Dependency-Track](https://dependencytrack.org) (DT) by itself:

1. the artifact's CycloneDX **BOM** is pushed when the artifact is uploaded and again after each vulnerability scan;
2. DT analyses it with its own analyzers (internal, NVD, GitHub advisories, OSV, ...);
3. once DT is done, the **VEX decisions** an admin recorded in OpenArtifact ([VEX](sbom.md#vex)) are pushed, and DT applies them to its findings (`NOT_AFFECTED`, `RESOLVED`, ...).

The integration is **off until configured** and admin only. It is tested against DT 4.14 (`dependencytrack/bundled`).

## The flow

For one artifact (`name@version`), a push does, in order:

1. Builds the CycloneDX 1.5 SBOM exactly like [`format=cyclonedx`](sbom.md#sbom) but **without the `vulnerabilities` section**: DT does not import it, and its own analysis is what the VEX is matched against.
2. `POST /api/v1/bom` with `autoCreate=true`, the project name and version (below), and waits for DT to finish the returned *processing token* (`GET /api/v1/event/token/{token}`, polled until `processing` is `false`, at most five minutes). The processing chain includes DT's vulnerability analysis.
3. Looks up the project (`/api/v1/project/lookup`) and reads its findings.
4. Builds a CycloneDX VEX of the **admin-recorded decisions only** (statuses OpenArtifact derives from a scan, such as `fixed_in`, are not sent: DT decides those itself) for the vulnerabilities DT found, and uploads it with `POST /api/v1/vex`. It waits for that token too.
5. Reads the findings again and counts how many carry an analysis: that is the `vex_applied` figure shown in the UI and the API.

Pushes are queued in the background, de-duplicated per artifact (several uploads or scans in a row collapse into one push; a request that arrives while a push runs triggers one more afterwards) and never delay an upload. The outcome of the last push of every artifact (`queued`, `running`, `done`, `skipped`, `failed`, with a sanitized reason, the DT project and the counts) is kept and shown in the admin UI and by `GET /api/v1/repositories/{name}/dependency-track/pushes` or `oa dtrack pushes REPO`.

### How the VEX is matched

DT applies a VEX statement only when the statement's vulnerability **source and id match its own record**, and a CVE can be known to DT as `NVD`, `GITHUB` (a `GHSA-...` id with the CVE as alias) or `INTERNAL`. OpenArtifact therefore does not send the id it has: it looks the decided id up among the DT findings' ids **and aliases**, and sends DT's own id and source for it. Decisions for vulnerabilities DT did not report on the project are left out (the push detail then reads *no VEX decision matches a vulnerability Dependency-Track reports*). Each statement affects the project's own component, which DT applies to every vulnerable component of the project that matches.

If DT's analyzers find something later (a feed update), the next push (the next scan, or `POST …/push`) applies the decisions to it.

## Configuration

Admin UI: **Admin → Dependency-Track**. The same through the API:

```sh
curl -sS -X PUT HOST/api/v1/admin/dependency-track -H 'Authorization: Bearer <TOKEN>' \
  -H 'Content-Type: application/json' -d '{
    "enabled": true,
    "base_url": "https://dtrack.example.com",
    "api_key": "odt_xxxxxxxx",
    "project_name": "{repo}/{name}",
    "project_version": "{version}",
    "auto_push": true,
    "push_vex": true
  }'
curl -sS -X POST HOST/api/v1/admin/dependency-track/test -H 'Authorization: Bearer <TOKEN>'
```

| Field | |
|---|---|
| `enabled` | master switch; needs `base_url` and `api_key` |
| `base_url` | DT's base URL (the API is `…/api/v1`, so the API server, not the frontend). It goes through OpenArtifact's SSRF guard: a DT on a private or loopback address needs `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true`; cloud-metadata addresses are always refused |
| `api_key` | a DT team API key. **Write-only**: stored encrypted (`OPENARTIFACT_SECRET_KEY`, see [configuration](../getting-started/configuration.md)), never returned (`api_key_set` tells whether one exists); omit it on update to keep it, send `""` to remove it |
| `project_name`, `project_version` | templates, below |
| `auto_push` | push on upload and after each scan; off still allows manual pushes |
| `push_vex` | also push the VEX decisions after the BOM |

**Test connection** calls `/api/version` and `/api/v1/team/self` and lists the key's permissions and the ones missing for the enabled features. The key's team needs:

| Permission | For |
|---|---|
| `BOM_UPLOAD`, `PROJECT_CREATION_UPLOAD`, `VIEW_PORTFOLIO` | pushing BOMs and creating projects |
| `VIEW_VULNERABILITY`, `VULNERABILITY_ANALYSIS` | `push_vex`: reading findings and applying the VEX |

`PORTFOLIO_MANAGEMENT` covers all of them.

### Project naming

The DT project is `project_name` + `project_version`, rendered per artifact from the placeholders `{repo}` (repository name), `{name}` and `{version}` (the artifact id is split at its last `@`), `{artifact_id}` and `{format}`. Defaults: `{repo}/{name}` and `{version}`, so `libs/left-pad` `1.3.0`. Unknown placeholders are rejected when saving; a version of `unknown` renders as an empty version; a name that renders empty skips the push. The same name and version always address the same DT project, so a repeated push updates it.

### Per repository

A repository can override any of it (UI: *Per-repository override*; API: `PUT /api/v1/repositories/{name}/dependency-track`; `DELETE` removes the override): its own URL and key (a repository can feed another DT), name and version templates, and `auto_push` / `push_vex` as *inherit*, on or off. An override applies explicitly, and its `enabled` flag wins, so a repository can opt in when the global integration is off, or opt out when it is on. Empty strings and unset booleans inherit the global value. `GET` returns the override and the **effective** configuration (never a key).

`POST /api/v1/repositories/{name}/dependency-track/push` with `{"artifact_id":"my-lib@1.2.3"}` (`oa dtrack push REPO --artifact-id my-lib@1.2.3`) pushes one artifact now.

## Failures

A failed push is recorded with a sanitized reason and does not affect the upload or scan that caused it: `rejected the API key or it lacks permissions (HTTP 401/403)`, `timed out waiting for dependency-track`, `dependency-track project not found after upload`, or a generic message with the details in the server log. DT's response bodies, the URL and the key are never copied into the push state or the API.

## Pushing by hand

The documents are the ones the API serves, so a script can do the same (DT does not import the `vulnerabilities` section of a BOM; see [SBOM and VEX](sbom.md#dependency-track)).
