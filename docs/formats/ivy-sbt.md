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

Proxy and group are not supported for either format (only Maven, Debian, RPM and raw can be proxied; only Maven and raw grouped). Creating one is rejected; a request to an existing one answers `501`. See [hosted, proxy, group](../repositories/hosted-proxy-group.md).

## Client setup

`internal/clientsetup` produces:

```scala
// build.sbt
resolvers += "REPO" at "HOST/repos/sbt/REPO"
credentials += Credentials("OpenArtifact", "HOST_AND_PORT", "<USERNAME>", "<TOKEN>")
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

`HOST_AND_PORT` is the `host[:port]` of the server (the credentials realm host). Credentials are Basic with the token as password.

## Routes and layout

Both handlers register `GET`, `PUT` and `DELETE` on `/*` under their prefix. The path is split on `/`:

| | Path |
|---|---|
| Ivy | `<org>/<module>/<revision>/<filename>` |
| sbt | `<group>/<artifact>/<version>/<filename>` |

The first three segments are single path elements, the rest (which may contain `/`) is the file name; fewer than four segments is `400 invalid path` on `PUT` and `404` on `GET`/`DELETE`. A Maven-style `com/example/lib/1.0/lib-1.0.jar` is therefore split as org `com`, module `example`, revision `lib`: use a dotted group (`com.example`) as one segment.

## Behavior

- Upload limit 500 MiB; `201` with `{id, sha256}`; `DELETE` returns `204`.
- Content-addressed storage with a sha256 per file; extension is taken from the file name.
- No `ivy.xml`, POM, `maven-metadata.xml` or checksum files are generated: only the files you `PUT` are served.
- Quarantine and webhooks (`artifact.uploaded`, `artifact.deleted`) apply as for other formats.
