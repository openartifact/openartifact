<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Package formats

OpenArtifact serves 16 package formats natively, each on the URL its client expects, behind one permission model.

## URL conventions

Replace `HOST` with your server (for example `http://localhost:8080`) and `REPO` with the repository name (`^[a-zA-Z0-9_-]{1,100}$`).

- Every repository is served under `/repos/REPO/...`.
- Maven, sbt, Ivy and raw are served under a format prefix: `/repos/maven/REPO`, `/repos/sbt/REPO`, `/repos/ivy/REPO`, `/repos/raw/REPO`. Repositories named like these prefixes are not reachable by the generic URL.
- Docker / OCI is the exception: the registry API is at `HOST/v2/`, and the first path segment after `/v2/` is the repository name ([details](docker.md)).
- Set `BASE_URL` (`baseUrl`) to the externally visible URL: npm tarball URLs, Helm `index.yaml`, PyPI JSON and the Cargo `config.json` are built from it.

## Format matrix

`Format id` is the `format` value used when creating a repository. Hosted repositories support upload for every format; proxy and group are limited as shown (details: [hosted, proxy, group](../repositories/hosted-proxy-group.md)).

| Format | Format id | Client URL | Types | Upload | Page |
|---|---|---|---|---|---|
| Maven | `maven` | `HOST/repos/maven/REPO` | hosted, proxy, group | `PUT` per file (`mvn deploy`) | [maven](maven.md) |
| sbt | `sbt` | `HOST/repos/sbt/REPO` | hosted | `PUT` per file (`sbt publish`) | [ivy-sbt](ivy-sbt.md) |
| Ivy | `ivy` | `HOST/repos/ivy/REPO` | hosted | `PUT` per file (`ivy:publish`) | [ivy-sbt](ivy-sbt.md) |
| Raw | `raw` | `HOST/repos/raw/REPO/<path>` | hosted, proxy, group | `PUT <path>` (`curl -T`) | [raw](raw.md) |
| npm | `npm` | `HOST/repos/REPO/` | hosted, proxy, group | `npm publish` (`PUT /<pkg>`) | [npm](npm.md) |
| PyPI | `pypi` | `HOST/repos/REPO/simple/` | hosted, proxy, group | `twine upload` to `HOST/repos/REPO/legacy/` | [pypi](pypi.md) |
| Docker / OCI | `docker` | `HOST/v2/` (image `HOST/REPO[/image]`) | hosted, proxy, group | `docker push` | [docker](docker.md) |
| Helm | `helm` | `HOST/repos/REPO/` (`index.yaml`, `charts/`) | hosted, proxy, group | `POST /charts` (raw chart `.tgz`) | [helm](helm.md) |
| Debian (APT) | `deb` | `HOST/repos/REPO/` (`dists/`, `pool/`) | hosted, proxy, group | `POST /api/v1/repositories/REPO/debs` (multipart) | [debian](debian.md) |
| RPM (YUM/DNF) | `rpm` | `HOST/repos/REPO/` (`repodata/repomd.xml`, `Packages/`) | hosted, proxy, group | `POST /api/v1/repositories/REPO/packages` (multipart) | [rpm](rpm.md) |
| Go modules | `go` | `GOPROXY=HOST/repos/REPO` | hosted, proxy, group | `PUT <module>/@v/<version>` (module zip) | [go](go.md) |
| Cargo | `cargo` | `sparse+HOST/repos/REPO/index/` | hosted, proxy, group | `cargo publish` (`PUT /api/v1/crates/new`) | [cargo](cargo.md) |
| NuGet | `nuget` | `HOST/repos/REPO/v3/index.json` | hosted, proxy, group | `dotnet nuget push` (`PUT /v3/package`) | [nuget](nuget.md) |
| RubyGems | `rubygems` | `HOST/repos/REPO/` | hosted | `gem push` (`POST /api/v1/gems`) | [rubygems](rubygems.md) |
| Conda | `conda` | `HOST/repos/REPO/<platform>/repodata.json` | hosted | `POST /<platform>` with `X-Filename` | [conda](conda.md) |
| Conan (1.x and 2) | `conan` | `HOST/repos/REPO/` (`v2/conans/...`, `v1/conans/...`) | hosted | `conan upload` | [conan](conan.md) |

Proxy repositories exist for 11 formats (Maven, npm, PyPI, Helm, Go, Cargo, NuGet, Debian, RPM, Docker and raw); group repositories for the same 11. Creating any other combination is rejected with `400 validation_failed`. Proxy and group repositories are read-only: writes answer `405`.

## Authentication

Authentication is required unless `allowAnonymousRead` is enabled or the repository is marked public (anonymous read only; writes always need a token). Package clients authenticate with an API token: `Authorization: Bearer <TOKEN>`, or HTTP Basic with the token as the password (any username). Account passwords are accepted only by the web UI login. Create tokens as described in [authentication](../security/authentication.md); what a token may do is governed by [RBAC](../security/rbac.md).

Read requests map to the `read` action and writes (`PUT`, `POST`, `PATCH`, `DELETE`) to `write` on the repository. The Debian/RPM REST upload endpoints under `/api/v1/repositories/REPO/{debs,packages,comps}` are authorized the same way, against the repository.

## Behaviour shared by all formats

- Blobs are content-addressed by SHA-256 and deduplicated across repositories; a blob is removed from storage only when no package references it.
- Deletes are soft by default: the artifact, version or tag moves to the [trash](../operations/trash.md) (404 on the native protocol, absent from listings, search and generated indexes) and can be restored until its retention expires. A repository can enable `immutable_releases` to block deleting or overwriting released versions.
- Hosted repositories of raw, Maven, npm, Docker, Helm, PyPI, Cargo, Go and NuGet can be replicated between instances ([federation](../operations/federation.md)).
- Uploads emit `artifact.uploaded` and deletes `artifact.deleted` webhook events ([webhooks](../operations/webhooks.md)).
- Storage quotas are enforced on upload for every hosted format; proxy caches are not metered ([quotas](../repositories/cleanup-and-quotas.md#what-counts)).
- A repository with a [license policy](../security/scanning.md#blocking-licenses) whose action is `block` rejects an upload whose declared license is denied (`403`, code `LICENSE_BLOCKED`). The license is read from the package metadata; Conan, Go and raw uploads carry none and are not checked.
- Client snippets in the web UI and in the MCP `repository` tool come from [`internal/clientsetup`](../../internal/clientsetup/clientsetup.go) (UI counterpart: `web/src/lib/installCommands.ts`). Where this documentation differs from those snippets (PyPI `twine`, Go format id), the page says so.
