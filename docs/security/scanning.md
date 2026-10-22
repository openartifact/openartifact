<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Scanning, SBOM, licenses and quarantine

On-demand vulnerability, secret and license scans and SBOM generation per repository, plus quarantine to block downloads of flagged artifacts.

Scans are **API-driven and manual**: vulnerability, secret and SBOM scans never run on upload. The one automatic check is a [license policy with action `block`](#blocking-licenses), which rejects an upload whose package metadata declares a blocked license. All scan endpoints are under `/api/v1/repositories/{name}/…`, need an admin ([rbac.md](rbac.md)) and store their results in the database. Scans are keyed by a caller-chosen `artifact_id` (at most 1024 characters, no control characters). Use the identifier the artifact is served under, otherwise [quarantine](#quarantine) cannot match it.

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
| `POST …/vuln/scan` | body `artifact_id`, `package_name`, `package_version`, `ecosystem` (all required), `scanner` (`osv` default, `trivy`, `grype`) and/or `scanners` (a list of those names, to run several at once); an unknown name is `400 validation_failed` |
| `GET …/vuln/scans[?vulnerable=true]` | list results |
| `GET/DELETE …/vuln/scans/{id}` | read / delete one result |
| `POST …/vuln/scans/{id}/quarantine` | body `{"quarantined":true\|false}` |

- `POST` returns at once with `scan_status: "scanning"`; the scan runs in the background (120 s limit). Poll `GET …/vuln/scans/{id}` until the status is `completed`, `partial` or `failed`. At most 4 scans run at a time per server; further requests get 429 `too_many_scans` with `Retry-After: 10`.
- A result carries `vulnerabilities` (id, summary, severity, CVSS, fixed-in version, references, published date), the counts `vuln_count`, `critical_count`, `high_count`, `medium_count`, `low_count`, the `scanner` that ran (several are stored sorted and comma-joined, for example `osv,trivy`) and `failed_scanners`.
- `ecosystem` uses these names: `npm`, `pypi`, `maven`, `cargo`, `go`, `rubygems`, `nuget`, `deb`, `rpm`, `helm` (OSV); the other backends support a subset: Trivy `npm`, `pypi`, `maven`, `cargo`, `go`, `rubygems`, `nuget`; Grype `npm`, `pypi`, `maven`, `cargo`, `rubygems`, `nuget`, `deb`, `rpm`, `go`.
- `osv` needs outbound HTTPS to `api.osv.dev`. `trivy` and `grype` run the CLI found on the server's `PATH`; the release image is shell-less and does not contain them; build a derived image that copies the static binaries in.

### Scanner failures

A scanner that cannot produce a result is never reported as "no vulnerabilities":

| `scan_status` | Meaning |
|---|---|
| `completed` | every requested scanner ran; `vulnerabilities` is its (deduplicated, by id) findings |
| `partial` | several scanners were requested and some failed; `vulnerabilities` holds the findings of the ones that ran and `failed_scanners` names the others |
| `failed` | no scanner produced a result; `vulnerabilities` is empty and the counts are 0, which means *not scanned*, not *clean* |

For `partial` and `failed` results `scan_error` summarizes each failed scanner as `<scanner>: <reason>`, separated by `; `, for example `trivy: scanner is not available on the server; osv: scan timed out`. The reason is one of a fixed set, so the response never contains command lines, paths, stderr or upstream bodies (those are written to the server log only):

| Cause | `scan_error` reason |
|---|---|
| `trivy`/`grype` not installed or not startable | `scanner is not available on the server` |
| 120 s limit reached | `scan timed out` |
| the scan was canceled | `scan was canceled` |
| the CLI exited with an error | `scanner exited with an error` |
| the CLI or OSV printed something that is not valid JSON | `scanner output could not be parsed` |
| the CLI printed more than 32 MiB | `scanner output exceeded the size limit` |
| ecosystem unknown to that scanner | `ecosystem is not supported by this scanner` |
| OSV unreachable or answering non-200 | `vulnerability database request failed` |
| anything else | `scan failed` |

Failed vulnerability scans stored before this behavior (which kept the raw scanner error) have their `scan_error` replaced by `scan failed` when the database is migrated. The web UI shows them as *Not scanned* and *Partial* instead of as clean.

The MCP tool `get_vulnerabilities` summarizes stored results and counts `failed` and `partial` scans in `failed_scans` ([MCP](../integrations/mcp.md)).

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

The result has `scan_status` `clean` or `flagged`, `finding_count` and `findings`. Findings never contain the secret: `type`, `description`, `severity`, `redacted_value` (at most the first eighth of the value, up to four characters, then `****`) and a `line_hint` around the match with every detected secret in the window masked.

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
| `POST …/license/scan` | body `{"artifact_id":"…"}`; returns the licenses, `policy_violations` (always an array), `quarantined` and `quarantine_approved` |
| `GET …/license/scans`, `GET …/license/scans/{id}` | results (with `quarantined` / `quarantine_approved`; `policy_violations` are only returned by `POST …/license/scan`) |
| `POST …/license/scans/{id}/quarantine` | body `{"quarantined":true\|false}`; quarantine an artifact by hand or [approve](#quarantine-after-upload) it |
| `GET/POST …/license/policies`, `PUT/DELETE …/license/policies/{id}` | policies of this repository (the list also shows global ones) |
| `GET/POST /api/v1/admin/license/policies` | global policies (apply to every repository) |

Policy fields: `name` (required, max 255), `description`, `action` (`warn` default, `block`, `allow`), `allowed_licenses`, `denied_licenses`, `enabled`. **`enabled` defaults to `false` when omitted on create**, and disabled policies are ignored.

Evaluation: a license is a violation when any identifier in it is in `denied_licenses`; and if `allowed_licenses` is set, a violation is raised when none of the artifact's licenses is allowed. Matching is case-insensitive and treats the spellings of one license version as the same: `GPL-3.0`, `GPL-3.0-only`, `GPL-3.0+`, `GPL-3.0-or-later` and `GPLv3` all match one policy entry, so a policy entry written with any of them covers the others (`GPL-3.0` is a different entry from `GPL-2.0`). SPDX expressions are split: `A AND B` needs every identifier allowed, `A OR B` needs one. Each violation is returned with `policy_id`, `policy_name`, `action` and `violation`. **An artifact that declares no license never violates a policy**, not even an allow list: policies match licenses, they do not require one. License fields given as objects (`{"type": "MIT"}`, the legacy npm and Maven shapes) are read like plain strings. A declared name that is not a known alias or SPDX identifier is compared as written: it cannot match a deny entry, but it is not in an allow list either.

What the `action` does:

| `action` | License scan, properties change | Upload |
|---|---|---|
| `allow` | violations are reported; nothing else happens | not evaluated |
| `warn` | violations are reported; nothing else happens | the violation is logged and the upload goes through |
| `block` | the artifact is [quarantined](#quarantine-after-upload) | rejected with `403 LICENSE_BLOCKED` |

Only enabled policies count, and a repository's own policies apply together with the global ones.

### Blocking licenses

When the repository has an enabled policy with `action: "block"`, an upload is rejected if a license declared in the package metadata is in `denied_licenses`, or if `allowed_licenses` is set and none of the declared licenses is allowed. The check runs after the metadata has been read and validated and before anything is stored, so a rejected upload leaves no package behind:

```http
HTTP/1.1 403 Forbidden
{"error":"upload rejected: the package license is blocked by a license policy (license GPL-3.0-only is denied)",
 "code":"LICENSE_BLOCKED","licenses":["GPL-3.0-only"],
 "policies":[{"policy_id":"…","policy_name":"no copyleft","action":"block","violation":"license GPL-3.0-only is denied"}]}
```

`licenses` lists the normalized licenses that were read from the package and `policies` the violated blocking policies. If the policies cannot be read the upload is refused rather than admitted unchecked: `500` with `code` `LICENSE_POLICY_UNAVAILABLE`.

The license is read from the upload itself, per format:

| Format | Where the license is read |
|---|---|
| npm | `license` (a string or `{"type": …}`) or the legacy `licenses` array, of every version in the publish body. Unpublish (`-rev`) requests are not checked |
| Cargo | `license` (an SPDX expression) in the publish metadata |
| RubyGems | the gemspec `licenses`. Checked before the duplicate-version check, so re-pushing a version under a block policy answers 403, not 409 |
| PyPI | `license_expression`, else a single-line `license` of at most 100 characters (longer free text is not a license name and is ignored), plus the `License ::` classifiers |
| NuGet | `<license type="expression">` in the `.nuspec`. A package that only has a `licenseUrl` carries no identifier and is not checked |
| Maven | the `<licenses><license><name>` of an uploaded `.pom`. A rejected `.pom` also quarantines the sibling files of the same group, artifact and version that were already uploaded (best effort), because `mvn deploy` sends the jar first |
| Ivy / sbt | `<license name>` of an uploaded `ivy.xml`, or `<licenses>` of a `.pom`; other files of the module are not checked |
| Debian | the `License:` field of the control file (few `.deb` files have one) |
| RPM | the `License` tag of the package header |
| Conda | `license` in `info/index.json`, for both `.tar.bz2` and `.conda` |
| Helm | the `artifacthub.io/license` chart annotation |
| Docker / OCI | the `org.opencontainers.image.licenses` annotation of the manifest, or the same label in the image config when that blob is already stored in the repository. Checked when the manifest is pushed; layers pushed before it stay stored as unreferenced blobs |
| Conan, Go, raw | the upload carries no license: not checked at upload. Set the license as a [property](../repositories/properties.md) and the [after-upload path](#quarantine-after-upload) applies |

Names are matched after the normalization described above. Free text that is not a known name or SPDX identifier (a Maven `<name>` such as `Our Company License`) cannot match a deny list, so only an allow list rejects it. Proxy-cache fills and group repositories are not upload paths and are not checked on the way in; an artifact fetched through a proxy can still be quarantined by a license scan or a license property. Creating or changing a policy does not re-examine artifacts that are already stored: scan them again.

### Quarantine after upload

A license that is only known after the upload (a license scan, or a change of the artifact's license [properties](../repositories/properties.md) through `PUT`/`PATCH`, which also covers raw, Go, Conan and imported artifacts) cannot reject anything any more. Instead, when a `block` policy is violated the artifact is **quarantined**: downloads answer `403 ARTIFACT_QUARANTINED` (see [Quarantine](#quarantine)) until an admin approves it.

- A quarantined artifact stays quarantined across re-scans and edits of its license properties; changing the license to something allowed is not a way around the block. Only `POST …/license/scans/{id}/quarantine` with `{"quarantined":false}` releases it (and `true` quarantines it again by hand). The web UI's license scan page has the same Approve and Quarantine buttons.
- An approval is stored with the licenses it was granted for (`quarantine_approved: true`). Re-scanning the same licenses keeps it approved; if the licenses recorded later differ, including a different spelling of the version such as `GPL-3.0-or-later` after approving `GPL-3.0-only`, and a blocking policy is violated, the artifact is quarantined again.
- If a property change is saved but the policy cannot be evaluated, the request answers `500` ("properties saved, but the license policy could not be evaluated") and the artifact is not quarantined until the next scan.
- The quarantine matches on the scan's `artifact_id`, exactly like the other scan types, so scan under the identifier the artifact is served under.
- Licenses suggested by [AI analysis](../integrations/ai-analysis.md) are advisory and are not evaluated against policies.

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

Quarantining a vulnerability, secret or license scan result makes the server refuse downloads of that artifact in every repository format, via the hosted, proxy-cache and API download paths, with:

```http
HTTP/1.1 403 Forbidden
{"error":"artifact is quarantined","code":"ARTIFACT_QUARANTINED"}
```

If the quarantine state cannot be read the request fails closed with 500. Set `quarantined` back to `false` (or delete a vulnerability or secret scan result) to release the artifact. License scan results cannot be deleted: approve them with `POST …/license/scans/{id}/quarantine` (see [Quarantine after upload](#quarantine-after-upload)); a block policy quarantines license results automatically, the other scan types only when you set `quarantined`.

An artifact matches when its scan's `artifact_id` equals one of the identifiers the download handler knows it by (a leading slash is ignored). Examples from the format handlers: npm `name@version` or `name/-/<tarball>`; Maven the repository path (`com/acme/lib/1.0/lib-1.0.jar`); PyPI, Debian, RPM, Helm, RubyGems and Conda the file name or the component/package ID returned by the repository package listing; raw the file path. A scan recorded under an identifier the client never requests quarantines nothing.

AI analysis may *recommend* quarantine but never applies it: the recommendation is stored for review ([AI analysis](../integrations/ai-analysis.md)). The web UI's vulnerability, secret and license scan pages expose the same quarantine toggle.
