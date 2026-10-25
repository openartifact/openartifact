<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Ivy and sbt

Hosted-only repositories for Ivy and sbt on isolated URL prefixes.

| Format | Repo URL |
|---|---|
| Ivy | `HOST/repos/ivy/REPO` |
| sbt | `HOST/repos/sbt/REPO` |

Proxy and group are not supported for either format (see the [format matrix](README.md#format-matrix): 11 formats can be proxied and 9 grouped, neither includes Ivy or sbt). Creating one is rejected with `400 validation_failed`. A proxy or group of these formats can only exist if restored from a backup; requests to it answer `501`. See [hosted, proxy, group](../repositories/hosted-proxy-group.md).

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
