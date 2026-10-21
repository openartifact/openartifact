<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Scanning, SBOM, licenses and quarantine

On-demand vulnerability, secret and license scans and SBOM generation per repository, plus quarantine to block downloads of flagged artifacts.

Everything here is **API-driven and manual**: nothing is scanned on upload. All endpoints are under `/api/v1/repositories/{name}/…`, need an admin ([rbac.md](rbac.md)) and store their results in the database. Scans are keyed by a caller-chosen `artifact_id` (at most 1024 characters, no control characters). Use the identifier the artifact is served under, otherwise [quarantine](#quarantine) cannot match it.

| Scan | Input | Source of data |
|---|---|---|
| [Vulnerabilities](#vulnerability-scans) | package name, version, ecosystem | OSV API, or the `trivy` / `grype` binary |
| [Secrets](#secret-scans) | text and/or metadata JSON you send | built-in regex rules |
| [Licenses](#license-scans-and-policies) | `artifact_id` | the artifact's stored [properties](../repositories/properties.md) |
| [SBOM](#sbom) | `artifact_id`, format | `artifact_id` plus the stored `dependencies` property |

The scanners do not open stored package files: vulnerability scans look up the name/version you pass, secret scans inspect only the content you pass, and license and SBOM data come from properties you set.

## Vulnerability scans

```sh
curl -sS -X POST HOST/api/v1/repositories/REPO/vuln/scan -H 'Authorization: Bearer <TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{"artifact_id":"lodash@4.17.20","package_name":"lodash","package_version":"4.17.20","ecosystem":"npm","scanner":"osv"}'
curl -sS -H 'Authorization: Bearer <TOKEN>' 'HOST/api/v1/repositories/REPO/vuln/scans?vulnerable=true'
```

| Endpoint | |
|---|---|
| `POST …/vuln/scan` | body `artifact_id`, `package_name`, `package_version`, `ecosystem` (all required), `scanner` (`osv` default, `trivy`, `grype`) |
| `GET …/vuln/scans[?vulnerable=true]` | list results |
| `GET/DELETE …/vuln/scans/{id}` | read / delete one result |
| `POST …/vuln/scans/{id}/quarantine` | body `{"quarantined":true\|false}` |

- `POST` returns at once with `scan_status: "scanning"`; the scan runs in the background (120 s limit). Poll `GET …/vuln/scans/{id}` until `completed` or `failed` (`scan_error` is set). At most 4 scans run at a time per server; further requests get 429 `too_many_scans` with `Retry-After: 10`.
- A result carries `vulnerabilities` (id, summary, severity, CVSS, fixed-in version, references, published date) and the counts `vuln_count`, `critical_count`, `high_count`, `medium_count`, `low_count`.
- `ecosystem` uses these names: `npm`, `pypi`, `maven`, `cargo`, `gomod`, `rubygems`, `nuget`, `deb`, `rpm`, `helm` (OSV); the other backends support a subset: Trivy `npm`, `pypi`, `maven`, `cargo`, `gomod`, `rubygems`, `nuget`; Grype `npm`, `pypi`, `maven`, `cargo`, `rubygems`, `nuget`, `deb`, `rpm`, `gomod`.
- **An unsupported ecosystem, a non-200 answer from OSV or a failing `trivy`/`grype` call ends as `completed` with zero vulnerabilities** (the reason is only in the server log), so treat an empty result with care.
- `osv` needs outbound HTTPS to `api.osv.dev`. `trivy` and `grype` run the CLI found on the server's `PATH`; the project's `docker/Dockerfile` does not install them.
- The MCP tool `get_vulnerabilities` summarizes stored results ([MCP](../integrations/mcp.md)).

## Secret scans

```sh
curl -sS -X POST HOST/api/v1/repositories/REPO/secrets/scan -H 'Authorization: Bearer <TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{"artifact_id":"app-1.0.tgz","content":"<text to inspect>","metadata":{"name":"app"}}'
```

| Endpoint | |
|---|---|
| `POST …/secrets/scan` | `artifact_id` required; `content` (text) and/or `metadata` (object) are scanned |
| `GET …/secrets/scans[?flagged=true]` | list |
| `GET/DELETE …/secrets/scans/{id}` | read / delete |
| `POST …/secrets/scans/{id}/quarantine` | `{"quarantined":true\|false}`; applies to the scan's `artifact_id` |

The result has `scan_status` `clean` or `flagged`, `finding_count` and `findings`. Findings never contain the secret: `type`, `description`, `severity`, `redacted_value` (first four characters + `****`) and a `line_hint` around the match with the value masked.

| Rule (`type`) | Severity |
|---|---|
| `aws_access_key`, `aws_secret_key`, `stripe_key`, `private_key` | critical |
| `github_token`, `github_oauth`, `github_app_token`, `slack_token`, `google_api_key`, `npm_token`, `docker_hub_token` | high |
| `generic_api_key`, `generic_password`, `jwt_token` | medium |

The rule set is built in and not configurable.

## License scans and policies

Licenses are read from the artifact's properties (set them with the properties API, see [properties](../repositories/properties.md)): `license`, `License`, `spdx_id`, `spdxId`, or a comma-separated `licenses` / `Licenses`. Common names are normalized to SPDX ids (for example `MIT License` → `MIT`, `Apache 2.0` → `Apache-2.0`, `gpl-3.0` → `GPL-3.0-only`); anything else is kept as written. A request containing `metadata` is rejected.

| Endpoint | |
|---|---|
| `POST …/license/scan` | body `{"artifact_id":"…"}`; returns the licenses and `policy_violations` |
| `GET …/license/scans`, `GET …/license/scans/{id}` | results |
| `GET/POST …/license/policies`, `PUT/DELETE …/license/policies/{id}` | policies of this repository (the list also shows global ones) |
| `GET/POST /api/v1/admin/license/policies` | global policies (apply to every repository) |

Policy fields: `name` (required, max 255), `description`, `action` (`warn` default, `block`, `allow`), `allowed_licenses`, `denied_licenses`, `enabled`. **`enabled` defaults to `false` when omitted on create**, and disabled policies are ignored.

Evaluation per scan: a license is a violation when any identifier in it is in `denied_licenses`; and if `allowed_licenses` is set, a violation is raised when none of the artifact's licenses is allowed. Matching is case-insensitive and ignores a trailing `+`. SPDX expressions are split: `A AND B` needs every identifier allowed, `A OR B` needs one. Each violation is returned with `policy_id`, `policy_name`, `action` and `violation`.

The policy `action` is reported back and nothing more: license scans do not block uploads or downloads and cannot quarantine. To block an artifact, [quarantine](#quarantine) a vulnerability or secret scan result for it.

## SBOM

```sh
curl -sS -X POST HOST/api/v1/repositories/REPO/sbom/generate -H 'Authorization: Bearer <TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{"artifact_id":"my-lib@1.2.3","format":"cyclonedx","spec_version":"1.4"}'
curl -sS -OJ -H 'Authorization: Bearer <TOKEN>' HOST/api/v1/repositories/REPO/sbom/<ID>/download
```

| Endpoint | |
|---|---|
| `POST …/sbom/generate` | `artifact_id`, `format` (`cyclonedx` with `spec_version` `1.4`, or `spdx` with `2.3`); returns 201 |
| `GET …/sbom`, `GET …/sbom/{id}` | list / report (format, spec version, content type, component count, timestamps) |
| `GET …/sbom/{id}/download` | the document: CycloneDX JSON (`application/json`) or SPDX tag-value text (`text/spdx`), served as attachment `sbom.cyclonedx` / `sbom.spdx` |
| `DELETE …/sbom/{id}` | delete |

The document names the package from `artifact_id` (split at the last `@` into name and version; version is `unknown` if there is none) with a `pkg:` identifier typed from the repository format, and lists as components the entries of the artifact's `dependencies` property, a JSON array string such as `[{"name":"left-pad","version":"1.3.0"}]`. `metadata` in the request is rejected. Documents are stored in the blob store.

## Quarantine

Quarantining a vulnerability or secret scan result makes the server refuse downloads of that artifact in every repository format, via the hosted, proxy-cache and API download paths, with:

```http
HTTP/1.1 403 Forbidden
{"error":"artifact is quarantined","code":"ARTIFACT_QUARANTINED"}
```

If the quarantine state cannot be read the request fails closed with 500. Set `quarantined` back to `false` (or delete the scan result) to release the artifact.

An artifact matches when its scan's `artifact_id` equals one of the identifiers the download handler knows it by (a leading slash is ignored). Examples from the format handlers: npm `name@version` or `name/-/<tarball>`; Maven the repository path (`com/acme/lib/1.0/lib-1.0.jar`); PyPI, Debian, RPM, Helm, RubyGems and Conda the file name or the component/package ID returned by the repository package listing; raw the file path. A scan recorded under an identifier the client never requests quarantines nothing.

AI analysis may *recommend* quarantine but never applies it: the recommendation is stored for review ([AI analysis](../integrations/ai-analysis.md)). The web UI's vulnerability and secret scan pages expose the same quarantine toggle.
