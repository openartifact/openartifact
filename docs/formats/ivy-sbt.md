<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Ivy and sbt

Hosted, proxy and group repositories for Ivy and sbt on isolated URL prefixes.

| Format | Repo URL |
|---|---|
| Ivy | `HOST/repos/ivy/REPO` |
| sbt | `HOST/repos/sbt/REPO` |

Proxy and group repositories are supported for both formats; see [proxy and group](#proxy-and-group) below and [hosted, proxy, group](../repositories/hosted-proxy-group.md).

## Client setup

`internal/clientsetup` produces:

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

`HOST` is the host name of the server without a port (sbt matches credentials on the host alone, so `host:port` finds none). Credentials are Basic with the token as password. The realm must be exactly `openartifact`, the realm of the server's `WWW-Authenticate` challenge; sbt sends no credentials under any other realm and the publish fails with `401`.

## Routes and layout

Both handlers register `GET`, `HEAD`, `PUT` and `DELETE` on `/*` under their prefix. `HEAD` answers like `GET` without a body (status, `Content-Type`, `Content-Length`), which is what resolvers use to probe for a file before fetching it.

The path is one of two layouts:

| Layout | Path | Used by |
|---|---|---|
| Maven | `<group as directories>/<module>/<version>/<module>-<version>[-classifier].<ext>` | sbt's default publishing, `m2compatible` Ivy resolvers |
| Ivy | `<org>/<module>/<revision>/<filename>`, the file name may include subdirectories (`jars/lib-1.0.jar`) | Ivy's default pattern |

The Maven layout is recognised when the path has more than four segments and its file name starts with `<module>-<version>` (a `-SNAPSHOT` version directory also holds timestamped files named after the base version). Everything before the module is the group, joined with dots: `com/example/lib/1.0/lib-1.0.jar` is group `com.example`, module `lib`, version `1.0`. A group written as one dotted segment (`com.example/lib/1.0/lib-1.0.jar`) is the same coordinate. Any other path is read as Ivy: the first three segments are org, module and revision and the rest is the file name. Fewer than four segments, or an empty segment, is `400 invalid path` on `PUT` and `404` on `GET`, `HEAD` and `DELETE`.

## Behavior

- Upload limit 500 MiB; `201` with `{id, sha256}`; `DELETE` returns `204`.
- Publishing a file that exists is an overwrite: the same bytes are an idempotent `201`, different bytes replace the file and the old blob is removed once nothing references it. In a repository with [immutable releases](../operations/trash.md#immutable-releases) a different upload over a release, and a delete of a release, is `409` unless an admin passes `?force=true`; a `-SNAPSHOT` version stays mutable.
- Files published by earlier releases under a multi-segment group were stored under mis-split coordinates (org `com`, module `example`, ...). They are still served by `GET` and `HEAD` and removed by `DELETE` at their old URL, but publishing the path again creates a new record under the correct coordinates, and that one is served from then on; the old record stays behind it until deleted.
- The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is enforced on publish (`507` over quota); an overwrite releases the replaced file and a delete releases the file.
- Content-addressed storage with a sha256 per file; extension is taken from the file name.
- No `ivy.xml`, POM, `maven-metadata.xml` or checksum files are generated: only the files you `PUT` are served.
- Quarantine and webhooks (`artifact.uploaded`, `artifact.deleted`) apply as for other formats.

## Proxy and group

An Ivy or sbt proxy mirrors an upstream by path, like a [raw or Maven proxy](../repositories/hosted-proxy-group.md#proxy-repositories): `upstream_url` is the root of an Ivy-layout or Maven-layout repository and `HOST/repos/<format>/PROXY/<path>` is fetched from `<upstream_url>/<path>`. There is no default upstream. Both layouts above work, so the same proxy serves Ivy resolvers, sbt (including plugin resolution) and coursier. `https://repo.scala-sbt.org/scalasbt/sbt-plugin-releases` is the sbt plugin repository (Ivy layout):

[Package curation](../repositories/hosted-proxy-group.md#package-curation) applies to the files of a version: rules name a module `group:module` (the group dotted, as for Maven) and a version; the jar, POM and `ivy.xml` of a blocked version answer `403` with the reason, even when cached. `maven-metadata.xml`, checksum and signature files are not gated, so a resolver may still list the version.

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

```scala
// project/credentials.sbt: plugins are resolved by the build definition, so the credentials
// of a private repository belong here (realm and host as in the client setup above)
credentials += Credentials("openartifact", "<HOST>", "<USERNAME>", "<TOKEN>")
```

Ivy takes the same layout as an `url` resolver with `m2compatible="false"` and the pattern `[organisation]/[module]/(scala_[scalaVersion]/)(sbt_[sbtVersion]/)[revision]/[type]s/[artifact](-[classifier]).[ext]` under `HOST/repos/ivy/PROXY`; coursier takes it as `-r 'ivy:HOST/repos/sbt/PROXY/<pattern>'`.

- **Checksums.** A file is hashed while it is spooled. Once the upstream has answered `200`, the proxy asks it for the checksum published next to the file, `.sha256`, then `.sha1`, then `.md5` (the first one the upstream has), and compares. A mismatch, or a checksum request failing with anything other than `404`/`410`, answers `502` and caches nothing. A verified release file is immutable once cached (never revalidated, served with the upstream down). The checksum comes from the same upstream, so this detects corruption and truncated transfers, not an upstream that is itself compromised. A file with no usable checksum upstream is served and cached for `cache_ttl_seconds` only. A missing file costs one upstream request: checksums are fetched after the file exists.
- **Checksum, signature and metadata files** (`.md5`, `.sha1`, `.sha256`, `.sha512`, `.asc`, `maven-metadata*.xml`) and `-SNAPSHOT` files are mirrored as the upstream has them and cached for `cache_ttl_seconds`. A `200` HTML answer for a `.jar`, `.pom`, `.xml`, `.zip`, `.war`, `.ear`, `.aar`, `.module`, `.gz`, `.tgz` or checksum path (a captive portal) is refused with `502`.
- **Group.** Members are tried in order and the first one with the file answers (a `404` member is passed over, a failing one makes the group answer `502` when no member has it). `maven-metadata.xml` is not merged in Ivy and sbt groups: the first member that has it wins. The [hosted-wins rule](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name) applies as for other formats: when a readable hosted member holds a module (organisation or group, and module name) of the requested path, proxy members no longer answer for it, unless `merge_proxy_versions` is `true`. Ownership is read from the hosted member's records for either layout reading of the path, so it cannot be bypassed by writing a group in dotted or slashed form. A database error while checking ownership makes the group answer `502` rather than risk serving the proxy's file.
- **Not supported.** Dynamic revisions (`latest.integration`, `1.+`, version ranges) need a listing of an upstream directory, which neither the proxy nor the hosted repositories serve: pin versions or resolve them through `maven-metadata.xml` where the upstream publishes one. Uploads (`PUT`, `DELETE`) to a proxy or group answer `405`.
