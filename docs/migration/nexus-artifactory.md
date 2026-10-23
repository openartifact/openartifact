<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Migrating from Nexus and Artifactory

`oa import` copies the hosted repositories of a Sonatype Nexus 3 or JFrog Artifactory server into OpenArtifact, for every format OpenArtifact hosts that the source offers. Files are streamed from the source into the normal upload endpoints, verified by SHA-256 against the source and against what the server stored, and keep their original creation time.

Supported formats:

| Format in OpenArtifact | Nexus 3 | Artifactory (package type) |
|---|---|---|
| `maven` | `maven2` | `maven`, `gradle` |
| `deb` | `apt` | `debian` |
| `rpm` | `yum` | `rpm` |
| `raw` | `raw` | `generic` |
| `npm` | `npm` | `npm` |
| `pypi` | `pypi` | `pypi` |
| `docker` | `docker` | `docker` |
| `helm` | `helm` | `helm` |
| `nuget` | `nuget` | `nuget` |
| `rubygems` | `rubygems` | `gems` |
| `cargo` | `cargo` | `cargo` |
| `go` | `go` | `go` |
| `conda` | `conda` | `conda` |
| `conan` | `conan` (layout like Artifactory's) | `conan` (Conan 2 layout) |
| `ivy` | none | `ivy` |
| `sbt` | none | `sbt` |

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
| `--artifactory-listing aql\|storage` | How Artifactory is enumerated. `aql` (default) uses one AQL query per 1000 files and carries creation time and properties; `storage` uses the storage API with extra calls per file. Artifactory OSS rejects AQL sorting, so the importer repeats the query unsorted there; the storage API is Pro-only. |
| `--path-prefix P` | Only import assets whose repository path starts with `P` (for example `com/acme/`), to migrate or test in slices. |
| `--retries N` | Retries after a transient failure of a source request or an upload (default `3`; `0` disables). See [Retries](#retries). |
| `--retry-max-wait D` | Longest wait between retries, a Go duration (default `30s`, must be positive). |
| `--quiet` | Only print the summary. |

`--format json` (global flag) prints the summary as JSON: `dry_run`, `uploaded`, `bytes`, `skipped_present`, `skipped_state`, `ignored`, `failed`, `retries`, `interrupted`, `failures[]` (`repo`, `path`, `error`). Progress goes to stderr, the summary to stdout.

Exit code: `0` on success, `1` when any asset failed or the run was interrupted (`SIGINT`/`SIGTERM`), `2` for invalid arguments or a missing token.

## Requirements

Use an OpenArtifact **admin** access token (`oa auth login`, `--token` or `OA_TOKEN`; see [authentication](../security/authentication.md)). The importer calls the repository management and property APIs and sends `X-OA-Import-Created-At`, which the server honors only for principals with the `manage` action. Deploy tokens cannot import. A run without a token fails immediately, except with `--dry-run`.

## Behavior

- **Selection.** Per format, only payload files are imported; indexes, signatures and checksum sidecars are regenerated by the server. Skipped (counted as "not importable"): paths starting with `.` (Artifactory internals) and, per format:

  | Format | Imported | Not imported |
  |---|---|---|
  | `maven` | files with at least four path segments | checksum files (`.md5`, `.sha1`, `.sha256`, `.sha512`), `maven-metadata.xml` |
  | `deb` / `rpm` | `*.deb` / `*.rpm` | `dists/`, `repodata/` |
  | `raw` | every file | |
  | `npm` | tarballs (`<pkg>/-/<file>.tgz`, scoped included) | packuments, `.npm/` metadata |
  | `pypi` | `.whl`, `.tar.gz`, `.zip`, `.egg`, `.tar.bz2`, `.tar.xz`, `.tar` | signatures, `simple/` indexes, `.whl.metadata`, installers (`.exe`, `.msi`, ...) |
  | `docker` | one image per **tag** (Nexus `v2/<image>/manifests/<tag>`, Artifactory `<image>/<tag>/manifest.json`) | untagged manifests, referrers, foreign layers, schema 1 manifests |
  | `helm` | `*.tgz` charts | `index.yaml`, `.prov` |
  | `nuget` | `*.nupkg` (Nexus `<Id>/<version>`) | symbol packages (`.snupkg`, `.symbols.nupkg`), `.nuspec` |
  | `rubygems` | `gems/*.gem` | `quick/`, `specs.4.8.gz` and the other indexes |
  | `cargo` | `*.crate` | the sparse index, `config.json` |
  | `go` | `<module>/@v/<version>.zip` | `.mod`, `.info`, `list`, `@latest` (the server derives them from the zip) |
  | `conda` | `<subdir>/<file>.conda`, `.tar.bz2` | `repodata.json*`, `channeldata.json`, `index.html` |
  | `conan` | Conan 2 layout: `.../<rrev>/export/*` and `.../<rrev>/package/<id>/<prev>/*` | Conan 1 layout, `index.json` |
  | `ivy` / `sbt` | every file with at least four path segments | checksum files, `maven-metadata.xml` |

  Package identity comes from the source's own component metadata (Nexus components, Artifactory package properties such as `npm.name`, `pypi.version`, `nuget.id`) and otherwise from the file name or the metadata inside the archive (`package.json`, `.nuspec`, `Cargo.toml`, `PKG-INFO`).
- **Idempotent.** Maven and raw assets are matched by path and SHA-256; every other format by SHA-256 (or, for docker, by tag and manifest digest) against the destination's package listing, because the server derives file names from package metadata. A source that reports no SHA-256 for a file (Nexus does not for npm and NuGet) falls back to name and version where the format allows it; an npm upload that meets an identical package (`409`, same digest) is accepted, but counted as uploaded rather than already present. An asset already present is not uploaded again, but its properties are (re)applied. Assets whose source reports no SHA-256 cannot be matched this way and are uploaded again. This works with or without `--state-file`.
- **Verification.** The source's SHA-256 is preferred, SHA-1 is the fallback; size is compared when the source reports it. The digest of what was sent is then compared with what the server stored. A copy that fails verification is removed from the destination and reported as a failure, unless it replaced pre-existing content, which is left in place. Formats whose API has no hard delete cannot be cleaned up: a bad RubyGems copy is yanked, and bad Cargo, Go and Conan copies are reported and left in place (a re-run from a corrected source replaces Conan files). npm's removal is refused by the server's 72-hour unpublish rule for versions imported with an older creation time.
- **Memory and disk.** Files are streamed. npm, NuGet, PyPI and Cargo uploads need metadata from inside the archive, so those files are first copied to a temporary file in `$TMPDIR` (one per concurrent transfer) and read from there; memory use does not grow with file size.
- **Order.** Within one package the importer works in a fixed order: npm versions oldest first (so the `latest` tag ends on the highest release), Conan recipe files before package files with `conanmanifest.txt` last, docker blobs and child manifests before the tag.
- **Re-run.** Re-run the same command to retry only what is missing.

### Retries

Transient failures are retried up to `--retries` times with exponential backoff (0.5 s, 1 s, 2 s, ...; capped at `--retry-max-wait`; each wait is randomized between half and the full value).

- Retried: connection errors and timeouts, a response body that ends early, and HTTP 408, 429 and 5xx.
- Never retried: any other 4xx (400, 401, 403, 404, 409, ...), verification failures, and cancellation.
- `Retry-After` (seconds or an HTTP date) on a 429 or 503 replaces the backoff. When it exceeds `--retry-max-wait` the asset fails at once, so the run can be resumed later.
- Source reads (listings, per-file details, downloads) are safe to repeat. Every attempt re-opens the source stream; a download that dies mid-stream restarts from byte 0.
- Uploads are repeated only when that cannot create a duplicate. Maven, raw, ivy, sbt and Conan files are `PUT`s that overwrite in place. For every other format the destination is first searched by SHA-256: if the earlier attempt did land (only its response was lost) the package counts as uploaded, otherwise it is sent again. An asset whose source reports no SHA-256 cannot be checked, so its failed upload is not retried.
- The summary counts the retries performed; a failure that exhausted its retries says so in its message.

## What is preserved

- `created_at`: from Nexus `blobCreated` (falling back to `lastModified`) or Artifactory `created` (with `--artifactory-listing storage`, the file info's `created`). It feeds age-based [cleanup policies](../repositories/cleanup-and-quotas.md). `updated_at` is the import time. A date less than a minute old or in the future is not sent, so the upload time stands.
- Properties: the properties selected by `--properties` become OpenArtifact [artifact properties](../repositories/properties.md); multi-valued properties are comma-joined. Only Artifactory has per-asset properties; Nexus has none.
- Debian placement, as below.
- Docker tags keep their digests: blobs and manifests are copied byte for byte, so image digests are identical to the source. Only the tag is pushed (blobs are shared between tags and uploaded once per run); manifest lists are copied with all their children.

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
- Only the `latest` npm dist-tag is derived; other dist-tags are not copied. Cargo yanked state, RubyGems yanked gems and Conan 1 packages are not imported.
- Artifactory build-info, Nexus tags, users, permissions, cleanup policies, release bundles and other server configuration.

## Server contract

What the importer relies on, usable by your own tooling:

| Item | Detail |
|---|---|
| `X-OA-Import-Created-At: <RFC 3339>` | Accepted on every hosted upload: Maven, raw, ivy and sbt `PUT`; Debian (`POST /api/v1/repositories/REPO/debs`) and RPM (`POST /api/v1/repositories/REPO/packages`); npm publish, PyPI `legacy`, Helm `charts`, NuGet `v3/package`, RubyGems `api/v1/gems`, Cargo `crates/new`, Go `@v/<version>`, Conda and Conan file uploads; and docker manifest `PUT` (stamping the manifest and the tag). Honored only for principals with the `manage` action; for every other authenticated caller (and anonymous ones) it is silently ignored, so it cannot backdate artifacts. From a `manage` principal, a malformed timestamp or one more than one minute in the future is rejected with `400`. |
| Artifact id | Maven `PUT` returns it in the `X-OA-Artifact-Id` response header; Debian and RPM return it as `data.id` in the JSON body, raw as top-level `id`. For the other formats the importer reads it from the response or, when the response has none, from `GET /api/v1/repositories/REPO/packages` (the id used for properties). |
| Properties | Attached afterwards with `PATCH /api/v1/repositories/REPO/artifacts/ID/properties` (see [properties](../repositories/properties.md)). |
