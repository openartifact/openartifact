<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Scanning, SBOM, licenses and quarantine

On-demand vulnerability, secret and license scans and SBOM generation per repository, plus quarantine to block downloads of flagged artifacts.

Scans are **API-driven and manual**: vulnerability, secret and SBOM scans never run on upload. The one automatic check is a [license policy with action `block`](#blocking-licenses), which rejects an upload whose package metadata declares a blocked license. All scan endpoints are under `/api/v1/repositories/{name}/…`, need an admin ([rbac.md](rbac.md)) and store their results in the database. Scans are keyed by a caller-chosen `artifact_id` (at most 1024 characters, no control characters). Use the identifier the artifact is served under, otherwise [quarantine](#quarantine) cannot match it.

| Scan | Input | Source of data |
|---|---|---|
| [Vulnerabilities](#vulnerability-scans) | package name, version, ecosystem | OSV API, or the `trivy` / `grype` binary ([installing them](#installing-trivy-and-grype)) |
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
| `GET …/vuln/scanners` | which scanners can run now and, if not, why ([readiness](#checking-scanner-readiness)) |
| `GET …/vuln/scans[?vulnerable=true]` | list results |
| `GET/DELETE …/vuln/scans/{id}` | read / delete one result |
| `POST …/vuln/scans/{id}/quarantine` | body `{"quarantined":true\|false}` |

- `POST` returns at once with `scan_status: "scanning"`; the scan runs in the background (120 s limit). Poll `GET …/vuln/scans/{id}` until the status is `completed`, `partial` or `failed`. At most 4 scans run at a time per server; further requests get 429 `too_many_scans` with `Retry-After: 10`.
- A result carries `vulnerabilities` (id, summary, severity, CVSS, fixed-in version, references, published date), the counts `vuln_count`, `critical_count`, `high_count`, `medium_count`, `low_count`, the `scanner` that ran (several are stored sorted and comma-joined, for example `osv,trivy`) and `failed_scanners`.
- `ecosystem` uses these names: `npm`, `pypi`, `maven`, `cargo`, `go`, `rubygems`, `nuget`, `deb`, `rpm`, `helm` (OSV); the other backends support a subset: Trivy `npm`, `pypi`, `maven`, `cargo`, `go`, `rubygems`, `nuget`; Grype `npm`, `pypi`, `maven`, `cargo`, `rubygems`, `nuget`, `deb`, `rpm`, `go`.
- `osv` needs outbound HTTPS to `api.osv.dev` and nothing else. `trivy` and `grype` are separate programs that the **release image does not contain**; see [Installing Trivy and Grype](#installing-trivy-and-grype). A scan that selects one that is not installed (or has no vulnerability database yet) is recorded as failed with an actionable `scan_error`.

### Scanner failures

A scanner that cannot produce a result is never reported as "no vulnerabilities":

| `scan_status` | Meaning |
|---|---|
| `completed` | every requested scanner ran; `vulnerabilities` is its (deduplicated, by id) findings |
| `partial` | several scanners were requested and some failed; `vulnerabilities` holds the findings of the ones that ran and `failed_scanners` names the others |
| `failed` | no scanner produced a result; `vulnerabilities` is empty and the counts are 0, which means *not scanned*, not *clean* |

For `partial` and `failed` results `scan_error` summarizes each failed scanner as `<scanner>: <reason>`, separated by `; `, for example `trivy: scanner is not available on the server (scanner_unavailable), setup guide: <url>; osv: scan timed out`. The reason is one of a fixed set, so the response never contains command lines, paths, stderr or upstream bodies (those are written to the server log only):

| Cause | `scan_error` reason |
|---|---|
| `trivy`/`grype` not installed or not startable | `scanner is not available on the server (scanner_unavailable), setup guide: <url>` |
| `trivy`/`grype` installed but its vulnerability database is not downloaded yet, or the CLI reports a missing/corrupt database | `scanner vulnerability database is not ready (scanner_db_unavailable), setup guide: <url>` |
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

## Installing Trivy and Grype

`osv` works out of the box. `trivy` and `grype` are external programs: the server runs them as subprocesses and keeps their vulnerability databases in a cache directory. Neither is part of the release image (it is shell-less and holds only the server), so enable them with one of:

| Where the server runs | How |
|---|---|
| Binary / VM | install `trivy` and/or `grype` so they are on the server's `PATH`, or set [`OPENARTIFACT_TRIVY_PATH` / `OPENARTIFACT_GRYPE_PATH`](../getting-started/configuration.md#vulnerability-scanners-scanners) |
| Docker Compose | `OPENARTIFACT_SCANNERS=trivy` (or `grype`, `all`) in `docker/.env`, then `docker compose build openartifact`; the compose file already mounts a `scanner_cache` volume |
| Your own build | `docker build -f docker/Dockerfile --build-arg SCANNERS=trivy .` (`none` default, `trivy`, `grype`, `all`) |
| Released image, Kubernetes | build a derived image (below), and give the pod a writable volume for the cache directory |

The server resolves the binaries when it starts and logs, per scanner, the path it found (`vulnerability scanner available`, with `binary`, `cache_dir`, `db_state`) or a warning when it is missing. A binary installed later is found on the next request (no restart), but its database download only starts at server start.

### Databases and the cache directory

- **Scans never download.** A scan runs the CLI offline against the databases already in the cache directory (`trivy --skip-db-update --offline-scan`, `GRYPE_DB_AUTO_UPDATE=false`), so a slow or blocked network cannot eat the 120 s scan limit. If the database is not there, the scan fails at once with `scanner_db_unavailable`.
- **A background updater downloads them.** At startup (and then every `OPENARTIFACT_SCANNER_UPDATE_INTERVAL`, default 6h, at least 1h, `0` = once) the server runs `trivy image --download-db-only` and `grype db update` for each scanner that is installed. A failure is logged and retried with exponential backoff (1 minute up to 1 hour). The first download takes from under a minute (Trivy) to a few minutes (Grype); until it finishes the scanner is `updating` (see [readiness](#checking-scanner-readiness)).
- **Cache directory**: `OPENARTIFACT_SCANNER_CACHE_DIR` (`scanners.cacheDir`, an absolute path). Trivy uses `<dir>/trivy`, Grype `<dir>/grype`. The default is `<user cache dir>/openartifact/scanners` (`$XDG_CACHE_HOME` or `~/.cache`, else the temp directory). The image sets `/var/cache/openartifact/scanners`, owned by uid 10001, and declares it a `VOLUME`; keep it on a volume when the root filesystem is read-only, and persist it so a restart does not download again. In the image the environment variable is already set, so change it with the variable rather than the YAML file.
- **Disk**: measured with Trivy 0.76.1 and Grype 0.121.0, the Trivy database takes about 1.6 GB on disk and the Grype database about 3.3 GB; a Grype download takes a few minutes on a fast link. Size the cache volume for both (plus headroom for the replacement file while updating). The Java index (`trivy-java-db`, several hundred MB) is **not** downloaded unless `OPENARTIFACT_TRIVY_JAVA_DB_REPOSITORY` is set: OpenArtifact scans package names and versions, not JARs.

### Network access

| Scanner | Needs outbound HTTPS to | Used for |
|---|---|---|
| `osv` | `api.osv.dev` | every scan |
| `trivy` | `mirror.gcr.io` (tried first) and `ghcr.io` | database download, by the background updater only |
| `grype` | `grype.anchore.io` | database download, by the background updater only |

The scan itself needs no network for `trivy` and `grype`. In a restricted network, either allow those hosts or point the updater at a mirror: `OPENARTIFACT_TRIVY_DB_REPOSITORY` (an OCI repository holding a copy of `trivy-db`) and `OPENARTIFACT_GRYPE_DB_URL` (the URL of a grype `listing.json`). An HTTP(S) proxy configured in the server's environment (`HTTPS_PROXY`, `NO_PROXY`) is passed to the CLIs.

### Offline (air-gapped) mode

`OPENARTIFACT_SCANNER_OFFLINE=true` stops the updater from ever contacting the network. Fill the cache directory yourself, for example:

```sh
trivy image --download-db-only --cache-dir /srv/scanners/trivy            # on a connected host
GRYPE_DB_CACHE_DIR=/srv/scanners/grype grype db update                    # on a connected host
# copy /srv/scanners to the server's OPENARTIFACT_SCANNER_CACHE_DIR, owned by the server's user
```

With offline mode on and no database in the directory, the scanner reports `missing` and scans fail with `scanner_db_unavailable`. Refresh the files by hand to update; the server notices new database files on the next request.

### Docker and Kubernetes

Build the image with the scanners bundled (the binaries are copied, digest-pinned, from the upstream `aquasec/trivy` and `anchore/grype` images; the final stage still has no shell):

```sh
docker build -f docker/Dockerfile --build-arg RUNTIME=distroless --build-arg SCANNERS=trivy -t openartifact:trivy .
```

To keep using a released image, derive from it:

```dockerfile
FROM ghcr.io/openartifact/openartifact:X.Y.Z
COPY --from=docker.io/aquasec/trivy:0.76.1@sha256:790fd66d1697395d8693d1acaa1ea702406a20f109b6fc7bac8db623eca6c06c /usr/local/bin/trivy /usr/local/bin/trivy
# grype: COPY --from=docker.io/anchore/grype:v0.121.0@sha256:621d56d1d68106b787022c81546f7e0a36202a25d1239f0c20195fab8c16f7ea /grype /usr/local/bin/grype
```

Kubernetes and Helm: mount a writable volume (an `emptyDir` is enough, a PVC avoids re-downloading after a restart) at the cache directory, writable by uid/gid 10001 (set the pod's `fsGroup: 10001`). Allow egress to the hosts above (or set the mirror variables), and run with a read-only root filesystem as usual. With several replicas each pod downloads its own copy.

### Checking scanner readiness

`GET /api/v1/repositories/{name}/vuln/scanners` (admin only) reports each scanner in the order `osv`, `trivy`, `grype`:

```json
{"scanners":[
  {"name":"osv","available":true,"ready":true,"db_state":null,"db_updated_at":null,"code":null,"message":null,"docs_url":null},
  {"name":"trivy","available":true,"ready":false,"db_state":"updating","db_updated_at":null,
   "code":"scanner_db_unavailable","message":"The vulnerability database is being downloaded: retry in a few minutes.","docs_url":"…"},
  {"name":"grype","available":false,"ready":false,"db_state":null,"db_updated_at":null,
   "code":"scanner_unavailable","message":"grype was not found on the server's PATH: …","docs_url":"…"}]}
```

`available` is whether the binary starts; `ready` is whether a scan can run now. `db_state` is `ready`, `updating`, `failed` (the last download failed: see the server log) or `missing`. The response never contains filesystem paths. The web UI uses it to disable scanners that are not ready in the *Scan package* form and to show `message` with a link to this section.

| `code` | Meaning | Fix |
|---|---|---|
| `scanner_unavailable` | the binary was not found or is not executable | install it, or set `OPENARTIFACT_TRIVY_PATH` / `OPENARTIFACT_GRYPE_PATH` |
| `scanner_db_unavailable` | no vulnerability database yet (still downloading, download failing, or offline without one) | wait, allow egress / set a mirror, or fill the cache in offline mode |

### Why external binaries

The design follows [zot](https://zotregistry.dev/)'s CVE scanning: scans run offline against a database that a background task refreshes on an interval with backoff, with a configurable database repository and readiness reporting. zot embeds Trivy as a Go library; OpenArtifact runs the CLIs instead, because that keeps the server binary at its current size and its module graph and vulnerability surface free of Trivy's, lets operators upgrade Trivy independently of the server, and is the only way to offer Grype too. Embedding would be reconsidered if Trivy exposed a stable library API.

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

SBOM generation (CycloneDX 1.4-1.6, SPDX 2.3) and VEX documents are described in [SBOM and VEX](sbom.md).

## Quarantine after upload

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
