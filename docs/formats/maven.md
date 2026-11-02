<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Maven

Use a Maven 2 layout repository to deploy and resolve artifacts with `mvn` and other Maven-protocol clients. It can be hosted, a proxy of an upstream repository, or a group of both. The server generates `maven-metadata.xml` and all checksums.

| Item | Value |
|---|---|
| Repo URL | `HOST/repos/maven/REPO` |
| Format id | `maven` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | none: `upstream_url` is required |

## Client setup

The web UI connect panel shows the same commands with your own URL. Use a token as the password:

```xml
<!-- ~/.m2/settings.xml -->
<settings>
  <servers>
    <server>
      <id>REPO</id>
      <username><USERNAME></username>
      <password><TOKEN></password>
    </server>
  </servers>
</settings>
```

Deploy:

```sh
mvn deploy -DaltDeploymentRepository=REPO::default::HOST/repos/maven/REPO
```

To resolve dependencies, add the repository to `pom.xml`. The `id` must match the `<server>` id when credentials are needed:

```xml
<repositories>
  <repository>
    <id>REPO</id>
    <url>HOST/repos/maven/REPO</url>
  </repository>
</repositories>
```

## Publish

Publishing is the plain Maven wire protocol: one request per file. There is no separate REST upload endpoint.

```text
PUT /repos/maven/REPO/<group/path>/<artifactId>/<version>/<file>
```

| Request | Behaviour |
|---|---|
| `PUT <path>` | stores the file; `201` when new, `200` when replaced or identical |
| `PUT <file>.md5`, `.sha1`, `.sha256`, `.sha512` | answered `200` and discarded; the server computes all four checksums while it receives the file |
| `PUT .../maven-metadata.xml` | answered `200` and discarded; metadata is always generated |
| `DELETE <path>` | removes one file, `204`; a path ending in `/` is `405` |
| `MKCOL` | answered `200` for WebDAV clients |

The response header `X-Oa-Artifact-Id` carries the package id used by the [properties API](../repositories/properties.md).

Rules checked on `PUT`:

- The path must parse as `group/artifactId/version/filename`, otherwise `400 invalid maven repository path`.
- The file name starts with `<artifactId>-<version>`, then an optional `-<classifier>` and the extension. Otherwise `400`.
- Timestamped snapshots use `<artifactId>-<base>-<yyyyMMdd.HHmmss>-<build>`.
- Extensions may contain dots (`tar.gz`, `jar.asc`). Signatures such as `.asc` are stored as ordinary files.
- `-SNAPSHOT` versions can be overwritten.
- Re-publishing identical release content is `200`.
- Overwriting a release with different content is `409` on every repository: `{"error": "release artifact already exists with different content"}`.

Limits:

| Limit | Value |
|---|---|
| File body | 1 GiB (`413` above it) |
| `.pom` file | 4 MiB (`413` above it) |
| Quota | enforced, see [quotas](../repositories/cleanup-and-quotas.md) |

Proxy and group repositories answer `PUT` and `DELETE` with `405` (`Allow: GET, HEAD`).

## Install and consume

Clients read files by path. `HEAD` answers like `GET` (same status, `ETag`, quarantine rules and real `Content-Length`) without a body.

- Checksum requests (`<file>.md5`, `.sha1`, `.sha256`, `.sha512`) return `<hash>  <filename>` from values stored at upload.
- A request for `<artifact>-<version>-SNAPSHOT[-<classifier>].<ext>` (or its checksum) resolves to the newest timestamped build of that classifier and extension. An unknown classifier is `404`.

`maven-metadata.xml` is generated at two levels:

| Path | Content |
|---|---|
| `group/path/artifactId/maven-metadata.xml` | `versions` in Maven version order, `latest`, `release` (newest non-snapshot), `lastUpdated` |
| `group/path/artifactId/<version>-SNAPSHOT/maven-metadata.xml` | current `timestamp` and `buildNumber`, plus the newest `snapshotVersion` per extension and classifier |

A background worker regenerates metadata about 500 ms after each upload or delete, so it can lag a just-finished deploy by a moment. A `maven-metadata.xml` sent by a client is never stored.

## Proxy

Create a proxy with `repo_type: "proxy"` and the upstream repository as `upstream_url`:

```sh
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' \
  -d '{"name":"maven-central","format":"maven","repo_type":"proxy","upstream_url":"https://repo.maven.apache.org/maven2"}'
```

Requests are mirrored by path from `upstream_url`, cached and recorded under the repository.

- Released artifacts are immutable once cached. This covers their `.md5`, `.sha1`, `.sha256`, `.sha512` and `.asc` files.
- `maven-metadata.xml`, `archetype-catalog.xml` and `-SNAPSHOT` files (with their checksums) are refetched after `cache_ttl_seconds`.
- An HTML answer for a `.jar`, `.pom` or `.zip` path (a captive portal page) is refused with `502` and not cached.

Negative caching, stale serving and upstream credentials are described in [hosted, proxy, group](../repositories/hosted-proxy-group.md).

### Minimum age and curation

Minimum age has no effect on Maven proxies: the upstream metadata dates no version. `min_age_hours` is accepted but ignored. See [minimum age](../repositories/hosted-proxy-group.md#minimum-age).

[Package curation](../repositories/hosted-proxy-group.md#package-curation) works with these differences:

- Rules name the artifact as `group:artifact` and the version as the version directory shows it.
- The files of a blocked version answer `403` with the reason, even when cached. This covers `.jar`, `.pom`, `.module`, classifier files and archives.
- `maven-metadata.xml` still lists the blocked version.
- Checksum and signature files are not gated.

## Group

A group is an ordered list of hosted and proxy Maven members. The first member holding a file answers. Each member is authorized for the caller as if requested directly.

- `maven-metadata.xml` and its checksums are merged across readable members. Versions are united and ordered, and `latest`, `release` and `lastUpdated` are recomputed. A single contributing member's document is served unchanged. An erroring member is ignored while another contributes.
- Hosted members win a package name first: if a hosted member holds a `group/artifact` path, its metadata is built from hosted members only and its files come only from hosted members. See [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).

Clients use `HOST/repos/maven/GROUP` as the repository URL. Group repositories are read-only.

## Limits and behaviour

- Files are served with `ETag: "<sha256>"`.
- `Cache-Control` is `public, max-age=300` for anonymous reads of a public repository and `private, no-cache` otherwise.
- Deleting a file moves it to the [trash](../operations/trash.md) by default.
- With `immutable_releases` on the repository, deleting a release file is `409` unless an admin passes `?force=true`. See [immutable releases](../operations/trash.md#immutable-releases).
- When the last file of an artifact is deleted, its generated `maven-metadata.xml` is removed too, after the short regeneration delay.
- Maven repositories have no repository-level signature and no `public.key`. GPG keys configured for the server sign only Debian and RPM metadata ([signing](../security/signing.md)).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `400 invalid maven repository path` | the path is not `group/artifactId/version/filename` | fix the deploy URL or the coordinates |
| `400` on a file name | the name does not start with `<artifactId>-<version>` | check that the file name matches the path |
| `409 release artifact already exists with different content` | a release file with the same path exists with other bytes | publish a new version |
| `409` on delete | `immutable_releases` is on | ask an admin to delete with `?force=true` |
| `413` on a `.pom` | the file is larger than 4 MiB | shrink the POM |
| `405` on deploy | the repository is a proxy or a group | deploy to a hosted repository |
| `maven-metadata.xml` misses a version just deployed | metadata is regenerated shortly after the upload | wait a moment and retry |
| `502` for a `.jar` through a proxy | the upstream answered an HTML page | check `upstream_url` and the upstream's network access |
| `403` on a file of a blocked version | a curation rule blocks it | change the rule, see [package curation](../repositories/hosted-proxy-group.md#package-curation) |
