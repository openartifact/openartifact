<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Ivy and sbt

Publish and resolve Ivy and sbt artifacts. Use a hosted repository for your own artifacts, a proxy to cache an upstream Ivy or Maven-layout repository, and a group to put several of them behind one URL. Ivy and sbt are two formats with separate URL prefixes and the same behaviour.

| Item | Ivy | sbt |
|---|---|---|
| Repo URL | `HOST/repos/ivy/REPO` | `HOST/repos/sbt/REPO` |
| Format id | `ivy` | `sbt` |
| Repository types | hosted, proxy, group | hosted, proxy, group |
| Default upstream (proxy) | none | none |

See [hosted, proxy, group](../repositories/hosted-proxy-group.md) for the repository types.

## Client setup

The web UI connect panel of a repository shows the same commands with your own URL.

```scala
// build.sbt
resolvers += "REPO" at "HOST/repos/sbt/REPO"
credentials += Credentials("openartifact", "HOST", "<USERNAME>", "<TOKEN>")
ThisBuild / publishTo := Some("REPO" at "HOST/repos/sbt/REPO")  // then: sbt publish
```

```xml
<!-- ivysettings.xml -->
<ibiblio name="REPO" m2compatible="true" root="HOST/repos/ivy/REPO"/>
<!-- publish -->
<ivy:publish resolver="REPO" pubrevision="1.0" overwrite="true">
  <artifacts pattern="dist/[artifact]-[revision].[ext]"/>
</ivy:publish>
```

- `HOST` in the `Credentials` line is the host name of the server without a port. sbt matches credentials on the host alone, so `host:port` finds none.
- Credentials are Basic with the token as password.
- The realm must be exactly `openartifact`, the realm of the server's `WWW-Authenticate` challenge. sbt sends no credentials under any other realm, and the publish then fails with `401`.

## Publish

Run `sbt publish` or `ivy:publish` as shown above. Both handlers register `GET`, `HEAD`, `PUT` and `DELETE` on `/*` under their prefix.

- The upload limit is 500 MiB. The answer is `201` with `{id, sha256}`.
- Publishing a file that exists is an overwrite. The same bytes are an idempotent `201`. Different bytes replace the file, and the old blob is removed once nothing references it.
- In a repository with [immutable releases](../operations/trash.md#immutable-releases), a different upload over a release and a delete of a release are `409` unless an admin passes `?force=true`. A `-SNAPSHOT` version stays mutable.
- The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is enforced on publish (`507` over quota). An overwrite releases the replaced file.
- Only the files you `PUT` are stored. No `ivy.xml`, POM, `maven-metadata.xml` or checksum files are generated.
- Files are stored by content with a sha256 per file. The extension is taken from the file name.
- Quarantine and webhooks (`artifact.uploaded`, `artifact.deleted`) apply as for other formats.

If you published files under a multi-segment group with an older release, see [upgrading](../getting-started/upgrading.md).

## Install and consume

Point the resolver at the repository URL as in [client setup](#client-setup). `HEAD` answers like `GET` without a body (status, `Content-Type`, `Content-Length`). Resolvers use it to probe for a file before fetching it.

### Routes and layout

The path is one of two layouts:

| Layout | Path | Used by |
|---|---|---|
| Maven | `<group as directories>/<module>/<version>/<module>-<version>[-classifier].<ext>` | sbt's default publishing, `m2compatible` Ivy resolvers |
| Ivy | `<org>/<module>/<revision>/<filename>` | Ivy's default pattern |

In the Ivy layout, the file name may include subdirectories (`jars/lib-1.0.jar`).

- The Maven layout is recognised when the path has more than four segments and its file name starts with `<module>-<version>`. A `-SNAPSHOT` version directory also holds timestamped files named after the base version.
- Everything before the module is the group, joined with dots. `com/example/lib/1.0/lib-1.0.jar` is group `com.example`, module `lib`, version `1.0`. A group written as one dotted segment (`com.example/lib/1.0/lib-1.0.jar`) is the same coordinate.
- Any other path is read as Ivy: the first three segments are org, module and revision, and the rest is the file name.
- Fewer than four segments, or an empty segment, is `400 invalid path` on `PUT` and `404` on `GET`, `HEAD` and `DELETE`.

Dynamic revisions (`latest.integration`, `1.+`, version ranges) are not supported. They need a listing of a directory, which neither the proxy nor hosted repositories serve. Pin versions, or resolve them through `maven-metadata.xml` where the upstream publishes one.

<a id="proxy-and-group"></a>
## Proxy

An Ivy or sbt proxy mirrors an upstream by path, like a [raw or Maven proxy](../repositories/hosted-proxy-group.md#proxy-repositories). `upstream_url` is the root of an Ivy-layout or Maven-layout repository. `HOST/repos/<format>/PROXY/<path>` is fetched from `<upstream_url>/<path>`. There is no default upstream.

Both layouts work, so the same proxy serves Ivy resolvers, sbt (including plugin resolution) and coursier. `https://repo.scala-sbt.org/scalasbt/sbt-plugin-releases` is the sbt plugin repository (Ivy layout).

```sh
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' \
  -d '{"name":"sbt-plugins","format":"sbt","repo_type":"proxy",
       "upstream_url":"https://repo.scala-sbt.org/scalasbt/sbt-plugin-releases"}'
```

```
# ~/.sbt/repositories (start sbt with -Dsbt.override.build.repos=true to use only these)
[repositories]
  local
  maven-central
  oa-plugins: HOST/repos/sbt/sbt-plugins/, [organization]/[module]/(scala_[scalaVersion]/)(sbt_[sbtVersion]/)[revision]/[type]s/[artifact](-[classifier]).[ext]
```

sbt resolves plugins in the build definition, so credentials for a private repository belong in the project's build definition:

```scala
// project/credentials.sbt
credentials += Credentials("openartifact", "HOST", "<USERNAME>", "<TOKEN>")
```

Ivy takes the same layout as a `url` resolver with `m2compatible="false"` and the pattern below under `HOST/repos/ivy/PROXY`. Coursier takes it as `-r 'ivy:HOST/repos/sbt/PROXY/<pattern>'`.

```
[organisation]/[module]/(scala_[scalaVersion]/)(sbt_[sbtVersion]/)[revision]/[type]s/[artifact](-[classifier]).[ext]
```

Uploads (`PUT`, `DELETE`) to a proxy answer `405`.

- **Checksums.** Once the upstream has answered `200`, the proxy asks it for the checksum published next to the file: `.sha256`, then `.sha1`, then `.md5`, the first one the upstream has. It compares that with the hash of the file. A mismatch, or a checksum request failing with anything other than `404` or `410`, answers `502` and caches nothing.
- **Immutable files.** A verified release file is immutable once cached: never revalidated, and served with the upstream down. The checksum comes from the same upstream, so this detects corruption and truncated transfers, not an upstream that is itself compromised.
- **No checksum upstream.** A file with no usable checksum upstream is served and cached for `cache_ttl_seconds` only. A missing file costs one upstream request, because checksums are fetched after the file exists.
- **Metadata files.** Checksum, signature and metadata files (`.md5`, `.sha1`, `.sha256`, `.sha512`, `.asc`, `maven-metadata*.xml`) and `-SNAPSHOT` files are mirrored as the upstream has them and cached for `cache_ttl_seconds`.
- **HTML answers.** A `200` HTML answer for a `.jar`, `.pom`, `.xml`, `.zip`, `.war`, `.ear`, `.aar`, `.module`, `.gz`, `.tgz` or checksum path (a captive portal) is refused with `502`.

### Minimum age and curation

The general rules are in [minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [package curation](../repositories/hosted-proxy-group.md#package-curation). The Ivy and sbt specifics are:

- **Minimum age.** `min_age_hours` has no effect: the upstream metadata dates no version.
- **Curation.** Rules name a module as `group:module` (the group dotted, as for Maven) and a version as the version directory shows it.
- The jar, POM and `ivy.xml` of a blocked version answer `403` with the reason, even when cached.
- `maven-metadata.xml`, checksum and signature files are not gated, so a resolver may still list the version.

## Group

A group tries its members in order, and the first one with the file answers. Groups are read-only: `PUT` and `DELETE` answer `405`.

- A member that answers `404` is passed over. A failing member makes the group answer `502` when no member has the file.
- `maven-metadata.xml` is not merged in Ivy and sbt groups: the first member that has it wins.
- Hosted members win a package name: when a readable hosted member holds a module (organisation or group, and module name) of the requested path, proxy members no longer answer for it, unless `merge_proxy_versions` is `true`. See [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).
- Ownership is read from the hosted member's records for both layout readings of the path, so writing a group in dotted or slashed form does not bypass the rule.
- A database error while checking ownership makes the group answer `502` instead of serving the proxy's file.

## Limits and behaviour

| Limit or behaviour | Value |
|---|---|
| Upload body | 500 MiB |
| Publish answer | `201` with `{id, sha256}` |
| Delete answer | `204` |
| Over quota | `507` |
| Release overwrite or delete in an `immutable_releases` repository | `409` unless `?force=true` by an admin |
| Write to a proxy or group | `405` |

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `sbt publish` fails with `401` | wrong realm, or the credentials host includes a port | use realm `openartifact` and the host name alone |
| `400 invalid path` on publish | fewer than four path segments or an empty segment | check the coordinates and the publish pattern |
| `404` for a file you published | the path is read in the other layout, or the coordinates differ | check the layout table above |
| `409` on publish | the file is a release in an `immutable_releases` repository | publish a new version, or an admin sends `?force=true` |
| `507` on publish | repository quota exceeded | free space or raise the quota |
| `405` on publish | target is a proxy or group | publish to a hosted repository |
| `502` on a proxy download | checksum mismatch or the upstream failed | retry; check the upstream |
| `403` on a jar or POM through a proxy | a curation rule blocks the version | ask an admin to change the rule |
| a `latest.integration` or `1.+` revision does not resolve | dynamic revisions are not supported | pin the version |
