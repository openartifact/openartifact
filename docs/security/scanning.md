<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Scanning, licenses and quarantine

Use this page to scan packages for vulnerabilities, secrets and licenses, export the results, and block downloads of flagged artifacts. Scanners and the local OSV mirror have their own pages: [scanner setup](scanner-setup.md) and [OSV mirror](osv-mirror.md).

## Run your first scan

The `osv` scanner needs no setup, only outbound HTTPS to `api.osv.dev`. You need an admin token ([RBAC](rbac.md)).

1. Start a scan. The server answers at once with `scan_status: "scanning"`.

   ```sh
   curl -sS -X POST HOST/api/v1/repositories/REPO/vuln/scan \
     -H 'Authorization: Bearer <TOKEN>' -H 'Content-Type: application/json' \
     -d '{"artifact_id":"lodash@4.17.20","package_name":"lodash","package_version":"4.17.20","ecosystem":"npm","scanner":"osv"}'
   ```

2. Read the result with the `id` from the response. Repeat until `scan_status` is `completed`, `partial` or `failed`.

   ```sh
   curl -sS -H 'Authorization: Bearer <TOKEN>' HOST/api/v1/repositories/REPO/vuln/scans/ID
   ```

3. List the scans that found something.

   ```sh
   curl -sS -H 'Authorization: Bearer <TOKEN>' 'HOST/api/v1/repositories/REPO/vuln/scans?vulnerable=true'
   ```

Use the identifier the artifact is served under as `artifact_id` (at most 1024 characters, no control characters). Otherwise [quarantine](#quarantine) cannot match it.

## What can be scanned

Scans are started through the API and never run on upload. The one automatic check is a [license policy with action `block`](#blocking-licenses), which rejects an upload whose package metadata declares a blocked license. All scan endpoints live under `/api/v1/repositories/{name}/…`, need an admin and store results in the database.

| Scan | Input | Source of data |
|---|---|---|
| [Vulnerabilities](#vulnerability-scans) | package name, version, ecosystem | OSV, or the `trivy` / `grype` programs ([setup](scanner-setup.md)) |
| [Secrets](#secret-scans) | text or metadata JSON you send | built-in rules |
| [Licenses](#license-scans-and-policies) | `artifact_id` | the stored package file ([how](licenses.md)) and the artifact's [properties](../repositories/properties.md) |
| [SBOM](sbom.md) | `artifact_id`, format | the stored package file and the `dependencies` property |

Vulnerability scans look up the name and version you pass. Secret scans inspect only the content you pass.

## Vulnerability scans

| Endpoint | Purpose |
|---|---|
| `POST …/vuln/scan` | start a scan |
| `GET …/vuln/scanners` | show which scanners can run now and why not ([readiness](scanner-setup.md#checking-scanner-readiness)) |
| `GET …/vuln/scans[?vulnerable=true]` | list the results of one repository |
| `GET /api/v1/vuln/scans` | [overview](#the-overview-across-repositories) of all repositories, worst first |
| `GET /api/v1/vuln/scans/summary` | counts for the overview cards |
| `GET`, `DELETE …/vuln/scans/{id}` | read or delete one result |
| `GET …/vuln/scans/{id}/export` | one scan's findings as [SARIF, CSV or JSON](#exporting-findings-sarif-csv-json) |
| `GET /api/v1/vuln/scans/export` | findings of all scans the overview filters select |
| `POST …/vuln/scans/{id}/quarantine` | body `{"quarantined":true}` or `false` |

The request body of `POST …/vuln/scan` has these fields:

- `artifact_id`, `package_name`, `package_version` and `ecosystem` are required.
- `scanner` is `osv` (default), `trivy` or `grype`.
- `scanners` is a list of those names, to run several at once.
- An unknown scanner name is `400 validation_failed`.

How a scan runs:

- The scan runs in the background with a 120 s limit.
- At most 4 scans run at a time per server. More requests get `429 too_many_scans` with `Retry-After: 10`.
- A result has `vulnerabilities` (id, summary, severity, CVSS, fixed-in version, references, published date), the counts `vuln_count`, `critical_count`, `high_count`, `medium_count` and `low_count`, the `scanner` that ran, and `failed_scanners`.
- When several scanners ran, `scanner` lists them sorted and comma-joined, for example `osv,trivy`.

### Ecosystems and scanners

`osv` accepts `cargo`, `deb`, `go`, `helm`, `maven`, `npm`, `nuget`, `pypi`, `rpm` and `rubygems`. `trivy` and `grype` support fewer:

| Scanner | Ecosystems |
|---|---|
| `trivy` | `cargo`, `go`, `maven`, `npm`, `nuget`, `pypi`, `rubygems`; `deb` and `rpm` for Debian, Ubuntu, RHEL and Amazon Linux releases |
| `grype` | `cargo`, `go`, `maven`, `npm`, `nuget`, `pypi`, `rubygems`; `deb` and `rpm` once the distribution release is known |

See [Debian and RPM packages](#debian-and-rpm-packages) for how the release is found. Both scanners check the one package you name, not its dependencies.

`osv` needs outbound HTTPS to `api.osv.dev` only, unless the [local OSV mirror](osv-mirror.md) is enabled and populated. The release image does not contain `trivy` or `grype`; see [scanner setup](scanner-setup.md). A scan that selects a scanner that is not installed, or has no database yet, is recorded as failed with a `scan_error` that tells you what to do.

### Exploitation signals

Each vulnerability and each result row also carries CISA KEV and FIRST EPSS signals (`kev`, `epss`, `priority`, and `kev_count`, `max_epss` on rows), and the lists can be sorted and filtered by them. See [Exploitation signals](exploit-signals.md).

### Debian and RPM packages

A `deb` or `rpm` package belongs to one distribution release, and `grype` and `trivy` match it against that release's advisories. Without a release, `grype` reports no match at all, which would look like a clean package. For example, `openssl 3.0.11-1~deb12u2` gives 49 matches for `debian-12` and 0 without a release.

The server works the release out; the request needs no extra field. It uses these sources in order.

| Package | Release comes from |
|---|---|
| `deb` | the release tag in the version, then the name of the repository distribution |
| `rpm` | the distribution tag in the release, read from the stored package |

For a `deb`:

- A release tag in the version wins: `+deb12u1` or `~deb12u1` is Debian 12, `~bpo12+1` is Debian 12 backports, `ubuntu0.22.04.1` or `~22.04.1` is Ubuntu 22.04.
- Otherwise the distribution the package is stored in counts when it is a release codename such as `bookworm` or `jammy-security`. `artifact_id` must be the package's id.
- A name like `stable` (also the upload default) or one you chose says nothing about the release.
- Known Debian codenames: `stretch`, `buster`, `bullseye`, `bookworm`, `trixie`, `forky`. Known Ubuntu codenames: `trusty`, `xenial`, `bionic`, `focal`, `jammy`, `noble`, `plucky`, `questing`, `resolute`.

For an `rpm`:

- `.el9` or `.el9_2` is RHEL 9 (CentOS, Rocky and AlmaLinux packages carry it too), `.fc39` is Fedora 39, `.amzn2` and `.amzn2023` are Amazon Linux.
- The release and epoch come from the stored package when `artifact_id` is its id.
- Without a stored package, pass `package_version` as `[epoch:]version-release`.

Keep the epoch. For `openssl 3.0.7-24.el9`, `grype` finds 97 matches without `epoch=1` and 76 with it; the extra ones are already fixed. A `deb` carries its epoch inside the version (`2:9.0.1378-2`).

When the release is not known:

- `grype` and `trivy` are replaced by `osv`. The scan is neither refused nor reported clean.
- `osv` matches by name and version across the whole ecosystem, so it can report more than a scan with a release would.
- The `scanner` field names the scanners that really ran. Asking for `grype` gives `osv`; asking for `grype` and `trivy` gives `osv,trivy`.
- A scan that asked for `osv` alone is unchanged.

`trivy` does not support Fedora. A Fedora rpm is replaced by `osv` for `trivy`, while `grype` still scans it.

A stored scan does not keep the release it ran against. Reports and exports name a deb or rpm with a generic Package URL such as `pkg:generic/openssl@3.0.11-1?ecosystem=deb`.

### The overview across repositories

`GET /api/v1/vuln/scans` (admin) lists the scans of every repository. The **Security** inbox filtered to vulnerabilities (`/security?type=vuln`) shows the same data.

Rows are summaries without the findings: repository name and format, package, counts, status, scanners and quarantine flag. Read the findings with `GET …/vuln/scans/{id}`. The order is fixed: most critical findings first, then high, medium and low, then newest.

| Query | Meaning |
|---|---|
| `repo` | only this repository (by name) |
| `severity` | `critical`, `high`, `medium` or `low`: scans with at least one finding of that severity **or worse** |
| `status` | `completed`, `partial`, `failed` or `scanning` (pending or running) |
| `scanner` | `osv`, `trivy` or `grype`; a scan run by several matches each |
| `quarantined` | `true` or `false` |
| `sort` | `severity` (default) or `priority` ([exploitation priority](exploit-signals.md#priority) first) |
| `kev` | `true`: scans with a KEV vulnerability; `false`: none |
| `epss_min` | 0 to 1: scans whose highest EPSS score is at least that |
| `priority` | `kev`, `epss`, `critical`, `high`, `medium` or `low`: scans whose priority is exactly that |
| `limit` | page size, default 25, at most 100 |
| `cursor` | the `next_cursor` of the previous page; send the same filters (and `sort`) with it |

Rows also carry `kev_count`, `max_epss` and `priority`; see [Exploitation signals](exploit-signals.md#fields).

The response is `{scans, has_more, next_cursor}`. Pass `next_cursor` as `cursor` with the same filters to get the next page. An unknown filter value or a malformed cursor is `400 validation_failed`.

`GET /api/v1/vuln/scans/summary[?repo=NAME]` returns `{total, completed, partial, failed, scanning, quarantined, severity_at_least: {critical, high, medium, low}}`. `severity_at_least.high` counts scans with a critical or a high finding, the same as the `severity` filter.

### Scanner failures

A scanner that cannot produce a result is never reported as "no vulnerabilities". Check `scan_status` first:

| `scan_status` | Meaning |
|---|---|
| `completed` | every requested scanner ran |
| `partial` | several scanners were requested and some failed |
| `failed` | no scanner produced a result |

- On `completed`, `vulnerabilities` holds the findings, deduplicated. Findings that share an id or an alias count as one: the OSV `GHSA-…` id and the `CVE-…` it lists as an alias are the same finding.
- A merged finding keeps the first scanner's id and the highest severity and CVSS score. The other ids go to `aliases`, and references and CWEs are combined.
- On `partial`, `vulnerabilities` holds the findings of the scanners that ran, and `failed_scanners` names the others.
- On `failed`, `vulnerabilities` is empty and the counts are 0. That means *not scanned*, not *clean*.

For `partial` and `failed` results, `scan_error` lists each failed scanner as `<scanner>: <reason>`, separated by `; `. For example: `trivy: scanner is not available on the server (scanner_unavailable), setup guide: <url>; osv: scan timed out`. The reason comes from a fixed set. The response never contains command lines, paths, stderr or upstream bodies; those go to the server log only.

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

The web UI shows failed scans as *Not scanned* and partial scans as *Partial*, never as clean.

The MCP tool `get_vulnerabilities` summarizes stored results and counts `failed` and `partial` scans in `failed_scans` ([MCP](../integrations/mcp.md)).

## Exporting findings (SARIF, CSV, JSON)

Vulnerability and secret scans can be downloaded for other tools. All export routes need an admin, take `?format=sarif|csv|json` (default `json`; anything else is `400 validation_failed`) and answer with `Content-Disposition: attachment`. The **Security** inbox has an **Export** menu (CSV, JSON, VEX) that exports the whole selection of its active filters (all pages, not just the loaded rows), see [Security findings](findings.md#exporting). The SARIF export is the per-scan API route below.

| Export | Route |
|---|---|
| One vulnerability scan | `GET /api/v1/repositories/{name}/vuln/scans/{id}/export` |
| Vulnerability scans across repositories | `GET /api/v1/vuln/scans/export`, with the [overview's](#the-overview-across-repositories) `repo`, `severity`, `status`, `scanner` and `quarantined` filters (`limit` and `cursor` are ignored; at most 2000 scans are read) |
| Secret scans of a repository | `GET /api/v1/repositories/{name}/secrets/scans/export[?flagged=true]` |
| One secret scan | `GET /api/v1/repositories/{name}/secrets/scans/{id}/export` |

**SARIF** is 2.1.0 (`application/sarif+json`), checked against the official `sarif-schema-2.1.0.json` by an integration-tagged test that fetches it from the OASIS repository:

- One **run** per scanner (`osv`, `trivy`, `grype`; the secret export has one run, `openartifact-secrets`). A scan that ran several scanners stores one merged finding list that does not say which scanner found what, so it gets its own run named after all of them (`osv+trivy`) instead of being duplicated across runs. A `partial` scan marks its run `executionSuccessful: false`; failed and pending scans have no findings and add none. With nothing to report the file holds one empty run.
- One **rule** per vulnerability id (the CVE/GHSA/OSV id; `helpUri` is `https://osv.dev/vulnerability/<id>`) or per secret type. The severity is the result `level` (critical and high are `error`, medium `warning`, low and unknown `note`) and the rule's `security-severity` property, which GitHub turns into its critical/high/medium/low label: the finding's CVSS score when the scanner gave one, otherwise 9.5, 8.0, 5.5, 3.0 (0.1 for unknown).
- One **result** per package and vulnerability (or secret finding), located by the relative artifact path `<repository>/<artifact_id>` (`artifactLocation.uri`, each segment percent-encoded, line 1) with the package URL as a logical location (`pkg:npm/lodash@4.17.20`; `pkg:generic/...?ecosystem=deb` for ecosystems without a Package URL type) and `partialFingerprints["openartifact/v1"]`, a hash of the repository, artifact, package and vulnerability id, so an upload of the same finding matches the earlier one.
- GitHub's limits are respected: rule ids are at most 255 characters (a longer id is cut and given a hash suffix), messages at most 1024, and at most 25000 results. If there are more, the **least severe are dropped**, the affected runs get `properties.truncated: true` and the response has `X-Export-Truncated: true` (also set when the cross-repository export hit its 2000-scan cap).
- Secret exports contain the secret type, description, severity and artifact only: no value, not even the redacted one, and no `line_hint`.

**CSV** has one row per finding: `repository, artifact_id, package_name, package_version, ecosystem, scanner, scan_id, scan_status, vulnerability_id, severity, cvss, fixed_in, summary, published_at, references` for vulnerabilities and `repository, artifact_id, scan_id, scan_status, type, description, severity` for secrets. Every cell that starts with `=`, `+`, `-`, `@`, a tab or a carriage return is prefixed with `'`, so a spreadsheet does not evaluate an advisory summary as a formula.

**JSON** is the scan as the read route returns it (`vulnerabilities` included); the cross-repository export is `{"scans": [...], "truncated": false}` with a `repository_name` on each scan, and the secret export drops `redacted_value` and `line_hint`.

### Uploading to GitHub code scanning

```sh
curl -sS -H 'Authorization: Bearer <TOKEN>' \
  'HOST/api/v1/vuln/scans/export?format=sarif&severity=high' -o openartifact.sarif

gh api repos/OWNER/REPO/code-scanning/sarifs \
  -f commit_sha="$(git rev-parse HEAD)" -f ref=refs/heads/main \
  -f sarif="$(gzip -c openartifact.sarif | base64 -w0)"
```

The API takes the gzipped, base64-encoded file (at most 10 MB compressed) and needs a token with `security_events` write access. In Actions use `github/codeql-action/upload-sarif` with `sarif_file: openartifact.sarif` instead. GitHub shows each run as its own tool; artifact paths are not files of the GitHub repository, so alerts appear in the Security tab without a source line.

### Importing into DefectDojo

Import the file as scan type **SARIF** (UI: *Import Scan Results*, or `POST /api/v2/import-scan/` with `scan_type=SARIF` and `file=@openartifact.sarif`). Each result's `level`, `ruleId` and artifact path become the finding's severity, title and file path; use *Reimport* so later uploads update the same findings.

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
| `GET …/secrets/scans/export[?flagged=true]`, `GET …/secrets/scans/{id}/export` | findings of all (or flagged) scans of the repository, or of one scan, as [SARIF, CSV or JSON](#exporting-findings-sarif-csv-json); never a secret value |
| `POST …/secrets/scans/{id}/quarantine` | `{"quarantined":true\|false}`; applies to the scan's `artifact_id` |

The result has `scan_status` `clean` or `flagged`, `finding_count` and `findings`. Findings never contain the secret: `type`, `description`, `severity`, `redacted_value` (at most the first eighth of the value, up to four characters, then `****`) and a `line_hint` around the match with every detected secret in the window masked (secrets that touch or overlap are masked as one `****`).

A text or a metadata object yields at most 1000 findings each: when it holds more matches, the findings of the rules the scanner tries first (`aws_access_key`, `aws_secret_key`, `github_token` and so on, in the built-in order; within a rule, the first matches in text order) are reported, the rest are dropped, still masked in the hints, and the server logs `secret scan findings truncated`. Fix the secrets you are shown and scan again to see the others. The scan itself takes time linear in the size of the text.

| Rule (`type`) | Severity |
|---|---|
| `aws_access_key`, `aws_secret_key`, `stripe_key`, `private_key` | critical |
| `github_token`, `github_oauth`, `github_app_token`, `slack_token`, `google_api_key`, `npm_token`, `docker_hub_token` | high |
| `generic_api_key`, `generic_password`, `jwt_token` | medium |

The rule set is built in and not configurable.

## License scans and policies

A scan evaluates two sources and uses both:

1. **The stored package file**, when `artifact_id` is the id of a stored package of a format whose file declares licenses (Cargo, Ivy descriptors, Maven, npm, NuGet, PyPI, RubyGems): the licenses the package declares in its own metadata. See [License metadata](licenses.md) for what is read and in which order.
2. **The artifact's properties** (set them with the properties API, see [properties](../repositories/properties.md)): `license`, `License`, `spdx_id`, `spdxId`, or a comma-separated `licenses` / `Licenses`. This is how a license is recorded for Conan, Go, raw, Terraform and imported artifacts, and for ids that are not a stored package.

A property can add a license; it cannot hide the one the package file declares. Common names are normalized to SPDX ids (for example `MIT License` → `MIT`, `Apache 2.0` → `Apache-2.0`, `gpl-3.0` → `GPL-3.0-only`); anything else is kept as written. Entries that are not SPDX expressions after normalization (a raw `BSD`, the classifier `BSD License`, free text) are listed in `unresolved_licenses` and flagged in the web UI: they are kept as written because they do not name one license, so a policy can only match them by that exact text. Each result carries `license_source` (`artifact`, or `client_declared` when the licenses only come from properties). A package file whose metadata is oversized or ambiguous makes the scan answer `422 unsafe_artifact`; with a blocking policy, so does a package file that cannot be read (`422 unreadable_artifact`). A request containing `metadata` is rejected.

| Endpoint | |
|---|---|
| `POST …/license/scan` | body `{"artifact_id":"…"}`; returns the licenses, `policy_violations` (always an array), `quarantined` and `quarantine_approved` |
| `GET …/license/scans`, `GET …/license/scans/{id}` | results (with `quarantined` / `quarantine_approved`; `policy_violations` are only returned by `POST …/license/scan`) |
| `POST …/license/scans/{id}/quarantine` | body `{"quarantined":true\|false}`; quarantine an artifact by hand or [approve](#quarantine-after-upload) it |
| `GET/POST …/license/policies`, `PUT/DELETE …/license/policies/{id}` | policies of this repository (the list also shows global ones) |
| `GET/POST /api/v1/admin/license/policies` | global policies (apply to every repository) |

Policy fields: `name` (required, max 255), `description`, `action` (`warn` default, `block`, `allow`), `allowed_licenses`, `denied_licenses`, `enabled`. **`enabled` defaults to `false` when omitted on create**, and disabled policies are ignored.

Evaluation: a license is a violation when any identifier in it is in `denied_licenses`; and if `allowed_licenses` is set, a violation is raised when none of the artifact's licenses is allowed. Matching is case-insensitive and treats the spellings of one license version as the same: `GPL-3.0`, `GPL-3.0-only`, `GPL-3.0+`, `GPL-3.0-or-later` and `GPLv3` all match one policy entry, so a policy entry written with any of them covers the others (`GPL-3.0` is a different entry from `GPL-2.0`). SPDX expressions are split: `A AND B` needs every identifier allowed, `A OR B` needs one. Package metadata that joins licenses with `/` (`MIT/Apache-2.0`, the old Cargo form), `;` or `|` is split at those characters too, and because such a string does not say which license applies it is treated like `AND`: every identifier must be allowed (`MIT/GPL-3.0` is refused by an allow list of `MIT`, admitted by one of `MIT` and `GPL-3.0`), and any denied identifier is a violation. Each violation is returned with `policy_id`, `policy_name`, `action` and `violation`. **An artifact that declares no license never violates a policy**, not even an allow list: policies match licenses, they do not require one. License fields given as objects (`{"type": "MIT"}`, the legacy npm and Maven shapes) are read like plain strings. A declared name that is not a known alias or SPDX identifier is compared as written: it cannot match a deny entry, but it is not in an allow list either.

What the `action` does:

| `action` | License scan, properties change | Upload |
|---|---|---|
| `allow` | violations are reported; nothing else happens | not evaluated |
| `warn` | violations are reported; nothing else happens | the violation is logged and the upload goes through |
| `block` | the artifact is [quarantined](#quarantine-after-upload) | rejected with `403 LICENSE_BLOCKED` |

Only enabled policies count, and a repository's own policies apply together with the global ones.

### Blocking licenses

When the repository has an enabled policy with `action: "block"`, an upload is rejected if a license declared in the package file itself is in `denied_licenses`, or if `allowed_licenses` is set and none of the declared licenses is allowed. The check runs after the metadata has been read and validated and before anything is stored, so a rejected upload leaves no package behind:

```http
HTTP/1.1 403 Forbidden
{"error":"upload rejected: the package license is blocked by a license policy (license GPL-3.0-only is denied)",
 "code":"LICENSE_BLOCKED","licenses":["GPL-3.0-only"],
 "policies":[{"policy_id":"…","policy_name":"no copyleft","action":"block","violation":"license GPL-3.0-only is denied"}]}
```

`licenses` lists the normalized licenses that were read from the package and `policies` the violated blocking policies. If the policies cannot be read the upload is refused rather than admitted unchecked: `500` with `code` `LICENSE_POLICY_UNAVAILABLE`.

The license is read from the uploaded package file itself, not from what the client says about it (details and precedence in [License metadata](licenses.md)); the client's own fields are only a fallback for a file that declares none. Per format:

| Format | Where the license is read |
|---|---|
| Cargo | `package.license` of the `Cargo.toml` inside the `.crate`; the publish metadata's `license` is only the fallback. The crate is read before the duplicate-version check |
| Conan, Go, raw, Terraform | the upload carries no license: not checked at upload. Set the license as a [property](../repositories/properties.md) and the [after-upload path](#quarantine-after-upload) applies |
| Conda | `license` in `info/index.json`, for both `.tar.bz2` and `.conda` |
| Debian | the `License:` field of the control file (few `.deb` files have one) |
| Docker / OCI | the `org.opencontainers.image.licenses` annotation of the manifest, or the same label in the image config when that blob is already stored in the repository. Checked when the manifest is pushed; layers pushed before it stay stored as unreferenced blobs |
| Helm | the `artifacthub.io/license` chart annotation |
| Ivy / sbt | `<license name>` of an uploaded `ivy.xml`, or `<licenses>` of a `.pom`; other files of the module are not checked |
| Maven | the `<licenses><license><name>` of an uploaded `.pom` (the `<url>` when a license has no name and the URL is a canonical one); licenses are not inherited from a parent pom. A rejected `.pom` also quarantines the sibling files of the same group, artifact and version that were already uploaded (best effort), because `mvn deploy` sends the jar first |
| npm | `license` (a string or `{"type": …}`), else the legacy `licenses` array, of the `package.json` inside the tarball of every version in the publish body; the manifest in the publish JSON is only the fallback. Unpublish (`-rev`) requests are not checked |
| NuGet | `<license type="expression">` in the root `.nuspec`, else a `licenseUrl` that names one license (`https://licenses.nuget.org/<expression>` or a canonical URL such as apache.org or opensource.org). Any other `licenseUrl` carries no identifier and is not checked |
| PyPI | the wheel's `METADATA` or the sdist's `PKG-INFO` (`.tar.gz`, `.zip`): `License-Expression`, else a short single-line `License:`, else the `License ::` classifiers. The form fields `license_expression`, `license` and `classifiers` are only the fallback for files that declare none or are not a wheel or sdist |
| RPM | the `License` tag of the package header |
| RubyGems | the gemspec `licenses` in the gem's `metadata.gz`. Checked before the duplicate-version check, so re-pushing a version under a block policy answers 403, not 409 |

A package whose own licenses cannot be read from its file while the repository has a blocking policy (a PyPI `.egg`, a damaged archive, an unparsable `.pom`) is refused with `422` and code `LICENSE_METADATA_UNREADABLE`, because the client's own fields must not decide a blocking policy; without one the fields are the fallback. A package whose own metadata cannot be read safely (a metadata file or decompressed stream over the size limits, or two candidate metadata files in one archive) is refused with `422` and code `LICENSE_METADATA_UNSAFE`. Names are matched after the normalization described above. Free text that is not a known name or SPDX identifier (a Maven `<name>` such as `Our Company License`) cannot match a deny list, so only an allow list rejects it. Proxy-cache fills and group repositories are not upload paths and are not checked on the way in; an artifact fetched through a proxy can still be quarantined by a license scan or a license property. Creating or changing a policy does not re-examine artifacts that are already stored: scan them again.

### Quarantine after upload

A license that is only known after the upload (a license scan, or a change of the artifact's license [properties](../repositories/properties.md) through `PUT`/`PATCH`, which also covers raw, Go, Conan, Terraform and imported artifacts) cannot reject anything any more. Instead, when a `block` policy is violated the artifact is **quarantined**: downloads answer `403 ARTIFACT_QUARANTINED` (see [Quarantine](#quarantine)) until an admin approves it.

- A quarantined artifact stays quarantined across re-scans and edits of its license properties; changing the license to something allowed is not a way around the block. Only `POST …/license/scans/{id}/quarantine` with `{"quarantined":false}` releases it (and `true` quarantines it again by hand). The web UI has the same Approve and Quarantine buttons on license findings in **Security** (`/security?type=license`).
- An approval is stored with the licenses it was granted for (`quarantine_approved: true`). Re-scanning the same licenses keeps it approved; if the licenses recorded later differ, including a different spelling of the version such as `GPL-3.0-or-later` after approving `GPL-3.0-only`, and a blocking policy is violated, the artifact is quarantined again.
- If a property change is saved but the policy cannot be evaluated, the request answers `500` ("properties saved, but the license policy could not be evaluated") and the artifact is not quarantined until the next scan.
- The quarantine matches on the scan's `artifact_id`, exactly like the other scan types, so scan under the identifier the artifact is served under.
- Licenses suggested by [AI analysis](../integrations/ai-analysis.md) are advisory and are not evaluated against policies.

## SBOM and VEX

SBOM generation, formats, downloads and VEX documents are described in [SBOM and VEX](sbom.md).

## Quarantine

Quarantining a vulnerability, secret or license scan result makes the server refuse downloads of that artifact. This applies in every repository format, on the hosted, proxy-cache and API download paths:

```http
HTTP/1.1 403 Forbidden
{"error":"artifact is quarantined","code":"ARTIFACT_QUARANTINED"}
```

To release an artifact, set `quarantined` back to `false`. For a vulnerability or secret scan you can also delete the result. License results cannot be deleted; approve them with `POST …/license/scans/{id}/quarantine` (see [Quarantine after upload](#quarantine-after-upload)).

- A `block` license policy quarantines license results automatically. The other scan types are quarantined only when you set `quarantined`.
- If the quarantine state cannot be read, the request fails closed with `500`.
- AI analysis may recommend quarantine but never applies it. The recommendation is stored for review ([AI analysis](../integrations/ai-analysis.md)).
- The web UI has the same toggle on the findings of the **Security** inbox and on the **Security** tab of the artifact page.

### How an artifact is matched

An artifact matches when its scan's `artifact_id` equals an identifier the download handler knows it by. A leading slash is ignored. Every format knows a package under the ID the package listing returns, the one the web UI and the OSV worker scan under. A scan recorded under an ID the client never requests quarantines nothing.

| Format | What a quarantined ID blocks |
|---|---|
| Docker | the manifest the tag points at (by tag or digest) and the config and layer blobs it references |
| Terraform | the download; the check runs again when the 15-minute download ticket is redeemed |
| Conan | every file and binary package of the recipe revision |
| Go | the `.zip`, `.mod` and `.info` of the version |
| Maven, npm, raw | also the file path, for example `com/acme/lib/1.0/lib-1.0.jar`, `name@version` or `name/-/<tarball>`, or the raw file path |

For Docker, blob `GET` and `HEAD`, cross-repository mount and promotion are blocked. A layer shared with another image is blocked for both while the quarantined image holds it.
