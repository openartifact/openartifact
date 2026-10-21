<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Migrating from Nexus and Artifactory

`oa import` copies the hosted repositories of a Sonatype Nexus 3 or JFrog Artifactory server into OpenArtifact. Files are streamed from the source into the normal upload endpoints, verified by SHA-256 against the source and against what the server stored, and keep their original creation time.

Supported formats:

| Format in OpenArtifact | Nexus 3 | Artifactory (package type) |
|---|---|---|
| `maven` | `maven2` | `maven` |
| `deb` | `apt` | `debian` |
| `rpm` | `yum` | `rpm` |
| `raw` | `raw` | `generic` |

Only hosted repositories are imported: Nexus `hosted`, Artifactory `local` (and `federated`). Proxy, group and virtual repositories, and any other format, are refused with an error for that `--repo`; recreate them in OpenArtifact (see [hosted, proxy and group repositories](../repositories/hosted-proxy-group.md)).

## Usage

```sh
# 1. Plan only: lists what would be uploaded, writes nothing.
oa import nexus --source-url https://nexus.example.com \
  --repo maven-releases:maven-releases --repo ubuntu-hosted:ubuntu --dry-run

# 2. Import. The source password/token is read from a file (or stdin with "-",
#    or $OA_IMPORT_SOURCE_TOKEN), never from the command line.
oa import nexus --source-url https://nexus.example.com \
  --source-user migrator --source-token-file ~/.nexus-token \
  --repo maven-releases:maven-releases --repo ubuntu-hosted:ubuntu \
  --create-repos --concurrency 8 --state-file nexus-import.state

# Artifactory: same flags; repository keys are the source names.
oa import artifactory --source-url https://acme.jfrog.io/artifactory \
  --source-user migrator --source-token-file ~/.jfrog-token \
  --repo libs-release-local:libs-release --repo rpm-local --state-file jfrog.state
```

Syntax: `oa [global flags] import <nexus|artifactory> --source-url URL --repo SRC[:DST] [--repo ...] [flags]`. The global flags `--server`, `--token` and `--format` (see [the CLI page](../integrations/cli.md)) go before `import`: `oa --format json import nexus ...`.

## Flags

| Flag | Meaning |
|---|---|
| `--source-url` | Base URL of the source (`$OA_IMPORT_SOURCE_URL`); `http` or `https`. Credentials embedded in the URL are refused, and so are source credentials over plain HTTP (except loopback hosts). Required. |
| `--source-user` | Source user name (`$OA_IMPORT_SOURCE_USER`). |
| `--source-token-file` | File holding the source password/token; `-` reads the first line of stdin. When omitted, `$OA_IMPORT_SOURCE_TOKEN` is used. With a user: HTTP Basic (user, token); with only a token: `Bearer`. Anonymous access works when the source allows it. Credentials are sent only to the source's own origin. |
| `--repo SRC[:DST]` | Repository to import, repeatable, at least one required. `DST` defaults to `SRC`. |
| `--dry-run` | Print the plan and a summary; no repository is created and nothing is uploaded. The source and the destination are still read. |
| `--create-repos` | Create missing destination repositories as **hosted** repositories of the matching format. Without it they must already exist. An existing destination must be hosted and of the same format as the source. |
| `--concurrency N` | Parallel transfers (default `4`, minimum 1). |
| `--state-file F` | Append-only resume file (JSON lines, mode `0600`). Completed assets are skipped on the next run when their source SHA-256 is unchanged. |
| `--properties` | Properties to copy: comma-separated keys or `prefix*` patterns. Default `deb.*,rpm.*,build.name,build.number,vcs.*`; `none` disables. |
| `--deb-distribution`, `--deb-component` | Fallback Debian placement, see [Debian placement](#debian-placement). |
| `--artifactory-listing aql\|storage` | How Artifactory is enumerated. `aql` (default) uses one AQL query per 1000 files and carries creation time and properties; `storage` uses the storage API with extra calls per file. |
| `--path-prefix P` | Only import assets whose repository path starts with `P` (for example `com/acme/`), to migrate or test in slices. |
| `--retries N` | Retries after a transient failure of a source request or an upload (default `3`; `0` disables). See [Retries](#retries). |
| `--retry-max-wait D` | Longest wait between retries, a Go duration (default `30s`, must be positive). |
| `--quiet` | Only print the summary. |

`--format json` (global flag) prints the summary as JSON: `dry_run`, `uploaded`, `bytes`, `skipped_present`, `skipped_state`, `ignored`, `failed`, `retries`, `interrupted`, `failures[]` (`repo`, `path`, `error`). Progress goes to stderr, the summary to stdout.

Exit code: `0` on success, `1` when any asset failed or the run was interrupted (`SIGINT`/`SIGTERM`), `2` for invalid arguments or a missing token.

## Requirements

Use an OpenArtifact **admin** access token (`oa auth login`, `--token` or `OA_TOKEN`; see [authentication](../security/authentication.md)). The importer calls the repository management and property APIs and sends `X-OA-Import-Created-At`, which the server honors only for principals with the `manage` action. Deploy tokens cannot import. A run without a token fails immediately, except with `--dry-run`.

## Behavior

- **Selection.** Per format, only payload files are imported. Skipped (counted as "not importable"): paths starting with `.` (Artifactory internals); Maven checksum files (`.md5`, `.sha1`, `.sha256`, `.sha512`), `maven-metadata.xml` and paths with fewer than four segments; Debian `dists/` and anything not ending in `.deb`; RPM `repodata/` and anything not ending in `.rpm`. Raw imports every file.
- **Idempotent.** Maven and raw assets are matched by path and SHA-256; Debian and RPM packages by SHA-256 anywhere in the destination repository (the server derives their file names from package metadata). An asset already present is not uploaded again, but its properties are (re)applied. Assets whose source reports no SHA-256 cannot be matched this way and are uploaded again. This works with or without `--state-file`.
- **Verification.** The source's SHA-256 is preferred, SHA-1 is the fallback; size is compared when the source reports it. The digest of what was sent is then compared with what the server stored. A copy that fails verification is removed from the destination and reported as a failure, unless it replaced pre-existing content, which is left in place.
- **Memory.** Every file is streamed; memory use does not grow with file size.
- **Re-run.** Re-run the same command to retry only what is missing.

### Retries

Transient failures are retried up to `--retries` times with exponential backoff (0.5 s, 1 s, 2 s, ...; capped at `--retry-max-wait`; each wait is randomized between half and the full value).

- Retried: connection errors and timeouts, a response body that ends early, and HTTP 408, 429 and 5xx.
- Never retried: any other 4xx (400, 401, 403, 404, 409, ...), verification failures, and cancellation.
- `Retry-After` (seconds or an HTTP date) on a 429 or 503 replaces the backoff. When it exceeds `--retry-max-wait` the asset fails at once, so the run can be resumed later.
- Source reads (listings, per-file details, downloads) are safe to repeat. Every attempt re-opens the source stream; a download that dies mid-stream restarts from byte 0.
- Uploads are repeated only when that cannot create a duplicate. Maven and raw are `PUT`s that overwrite in place. For Debian and RPM the destination is first searched by SHA-256: if the earlier attempt did land (only its response was lost) the package counts as uploaded, otherwise it is sent again. A Debian/RPM asset whose source reports no SHA-256 cannot be checked, so its failed upload is not retried.
- The summary counts the retries performed; a failure that exhausted its retries says so in its message.

## What is preserved

- `created_at`: from Nexus `blobCreated` (falling back to `lastModified`) or Artifactory `created` (with `--artifactory-listing storage`, the file info's `created`). It feeds age-based [cleanup policies](../repositories/cleanup-and-quotas.md). `updated_at` is the import time. A date less than a minute old or in the future is not sent, so the upload time stands.
- Properties: the properties selected by `--properties` become OpenArtifact [artifact properties](../repositories/properties.md); multi-valued properties are comma-joined. Only Artifactory has per-asset properties; Nexus has none.
- Debian placement, as below.

### Debian placement

The distribution and component of each `.deb` are taken from, in this order:

1. the asset's own `deb.distribution` / `deb.component` property (Artifactory);
2. the Nexus apt repository configuration (`distribution`; read from the Nexus API, which often needs elevated rights, in which case this step is skipped);
3. the `dists/<dist>/<component>/binary-*` layout of the source, when it names exactly one distribution/component;
4. `--deb-distribution` / `--deb-component`.

An asset with no answer is reported as a failure (pass the flags to fix it). Note that step 1 applies even when `--properties` excludes `deb.*`.

## Not imported

- Proxy, group and virtual repositories (see above).
- Repository metadata is regenerated, not copied: Maven checksum files and `maven-metadata.xml` are rebuilt by the server; apt `dists/` and yum `repodata/` indexes are rebuilt and signed with OpenArtifact's single global GPG key. Clients must trust that key instead of the old repository key (see [signing](../security/signing.md)).
- Artifactory build-info, Nexus tags, users, permissions, cleanup policies, release bundles and other server configuration.

## Server contract

What the importer relies on, usable by your own tooling:

| Item | Detail |
|---|---|
| `X-OA-Import-Created-At: <RFC 3339>` | Accepted on Maven and raw `PUT` (`/repos/maven/REPO/PATH`, `/repos/raw/REPO/PATH`) and on Debian (`POST /api/v1/repositories/REPO/debs`) and RPM (`POST /api/v1/repositories/REPO/packages`) uploads. Honored only for principals with the `manage` action; for every other authenticated caller (and anonymous ones) it is silently ignored, so it cannot backdate artifacts. From a `manage` principal, a malformed timestamp or one more than one minute in the future is rejected with `400`. |
| Artifact id | Maven `PUT` returns it in the `X-OA-Artifact-Id` response header; Debian and RPM return it as `data.id` in the JSON body, raw as top-level `id`. |
| Properties | Attached afterwards with `PATCH /api/v1/repositories/REPO/artifacts/ID/properties` (see [properties](../repositories/properties.md)). |
