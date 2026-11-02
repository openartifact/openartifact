<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Package formats

OpenArtifact serves many package formats natively, each on the URL its client expects, behind one permission model. Use this page to find the URL, the upload command and the page for your format.

Start here:

1. [Install the server and create your first repository](../getting-started/installation.md).
2. [Create an API token](../security/authentication.md) for your client.
3. Open the page of your format in the matrix below, or copy ready-made commands from [client setup](client-setup.md).
4. To mirror a public registry or combine repositories, see [hosted, proxy and group repositories](../repositories/hosted-proxy-group.md).

## URL conventions

Replace `HOST` with your server (for example `http://localhost:8080`) and `REPO` with the repository name (`^[a-zA-Z0-9_-]{1,100}$`).

- Every repository is served under `/repos/REPO/...`.
- Ivy, Maven, raw and sbt are served under a format prefix: `/repos/ivy/REPO`, `/repos/maven/REPO`, `/repos/raw/REPO`, `/repos/sbt/REPO`. Repositories named like these prefixes are not reachable by the generic URL.
- Docker / OCI is the exception: the registry API is at `HOST/v2/`, and the first path segment after `/v2/` is the repository name ([details](docker.md)).
- `BASE_URL` (`baseUrl`) is optional for package clients. Set it when the server sits behind a proxy that does not forward the original host.
  - Helm, PyPI and Terraform metadata use relative references and need no host.
  - npm, Cargo, NuGet, Conan 1.x, Composer `dist.url` and Ansible use the origin of the request (`Host`, or `X-Forwarded-Host` and `X-Forwarded-Proto` from a trusted proxy). `BASE_URL` overrides it when set.
  - Details: [the public URL](../getting-started/installation.md#the-public-url-and-base_url).

## Format matrix

`Format id` is the `format` value you use when creating a repository. Every format can be a hosted, proxy or group repository (see [hosted, proxy, group](../repositories/hosted-proxy-group.md)). The Upload column shows how you publish to a hosted repository.

| Format | Format id | Client URL | Types | Upload | Page |
|---|---|---|---|---|---|
| Ansible collections | `ansible` | `HOST/repos/REPO/api/` (Galaxy API v3) | hosted, proxy, group | `ansible-galaxy collection publish` (`POST /api/v3/artifacts/collections/`, multipart) | [ansible](ansible.md) |
| Alpine (apk) | `apk` | `HOST/repos/REPO/<branch>/<repo>` (`<arch>/APKINDEX.tar.gz`) | hosted, proxy, group | `PUT <branch>/<repo>/<arch>/<name>-<version>.apk` or `POST /api/v1/repositories/REPO/apks` (multipart) | [apk](apk.md) |
| Cargo | `cargo` | `sparse+HOST/repos/REPO/index/` | hosted, proxy, group | `cargo publish` (`PUT /api/v1/crates/new`) | [cargo](cargo.md) |
| Composer (PHP) | `composer` | `HOST/repos/REPO` (`packages.json`, Composer repository v2) | hosted, proxy, group | `PUT /packages/upload/VENDOR/NAME/VERSION` (zip) | [composer](composer.md) |
| Conan (1.x and 2) | `conan` | `HOST/repos/REPO/` (`v2/conans/...`, `v1/conans/...`) | hosted, proxy, group | `conan upload` | [conan](conan.md) |
| Conda | `conda` | `HOST/repos/REPO/<platform>/repodata.json` | hosted, proxy, group | `POST /<platform>` with `X-Filename` | [conda](conda.md) |
| Debian (APT) | `deb` | `HOST/repos/REPO/` (`dists/`, `pool/`) | hosted, proxy, group | `POST /api/v1/repositories/REPO/debs` (multipart) | [debian](debian.md) |
| Docker / OCI | `docker` | `HOST/v2/` (image `HOST/REPO[/image]`) | hosted, proxy, group | `docker push` | [docker](docker.md) |
| Go modules | `go` | `GOPROXY=HOST/repos/REPO` | hosted, proxy, group | `PUT <module>/@v/<version>` (module zip) | [go](go.md) |
| Helm | `helm` | `HOST/repos/REPO/` (`index.yaml`, `charts/`) | hosted, proxy, group | `POST /charts` (raw chart `.tgz`) | [helm](helm.md) |
| Ivy | `ivy` | `HOST/repos/ivy/REPO` | hosted, proxy, group | `PUT` per file (`ivy:publish`) | [ivy-sbt](ivy-sbt.md) |
| Maven | `maven` | `HOST/repos/maven/REPO` | hosted, proxy, group | `PUT` per file (`mvn deploy`) | [maven](maven.md) |
| Nix | `nix` | `HOST/repos/REPO` (`/nix-cache-info`, `/<hash>.narinfo`, `/nar/`) | hosted, proxy, group | `nix copy --to 'HOST/repos/REPO' /nix/store/...` (netrc token) | [nix](nix.md) |
| npm | `npm` | `HOST/repos/REPO/` | hosted, proxy, group | `npm publish` (`PUT /<pkg>`) | [npm](npm.md) |
| NuGet | `nuget` | `HOST/repos/REPO/v3/index.json` | hosted, proxy, group | `dotnet nuget push` (`PUT /v3/package`) | [nuget](nuget.md) |
| Arch Linux (pacman) | `pacman` | `HOST/repos/REPO/os/<arch>` (`<name>.db`, packages) | hosted, proxy, group | `PUT /os/<arch>/<pkg>.pkg.tar.zst` (+ `.sig`) | [pacman](pacman.md) |
| PyPI | `pypi` | `HOST/repos/REPO/simple/` | hosted, proxy, group | `twine upload` to `HOST/repos/REPO/legacy/` | [pypi](pypi.md) |
| Raw | `raw` | `HOST/repos/raw/REPO/<path>` | hosted, proxy, group | `PUT <path>` (`curl -T`) | [raw](raw.md) |
| RPM (YUM/DNF) | `rpm` | `HOST/repos/REPO/` (`repodata/repomd.xml`, `Packages/`) | hosted, proxy, group | `POST /api/v1/repositories/REPO/packages` (multipart) | [rpm](rpm.md) |
| RubyGems | `rubygems` | `HOST/repos/REPO/` | hosted, proxy, group | `gem push` (`POST /api/v1/gems`) | [rubygems](rubygems.md) |
| sbt | `sbt` | `HOST/repos/sbt/REPO` | hosted, proxy, group | `PUT` per file (`sbt publish`) | [ivy-sbt](ivy-sbt.md) |
| Terraform / OpenTofu | `terraform` | registry host `HOST`, source `HOST/REPO/...` (discovery at `/.well-known/terraform.json`) | hosted, proxy, group | `PUT /_api/modules/MODULE/SYSTEM/VERSION`, `PUT /_api/providers/TYPE/VERSION/OS/ARCH` (relative to `HOST/repos/REPO`) | [terraform](terraform.md) |

## Upload from the web UI

The *Upload* button on a hosted repository (users with write permission) sends the files with the browser session, using the same routes as the clients above. Where a file carries the coordinates, the form reads them from it; otherwise it asks.

| Format | What the form asks | What it reads from the file |
| --- | --- | --- |
| raw | optional directory | file name |
| npm | dist-tag (default `latest`) | name and version from `package.json` in the tarball; the browser builds the publish document |
| PyPI | none | name, version and type from the wheel or sdist file name |
| Maven | group ID, artifact ID, version | nothing; file names must start with `artifactId-version` and checksums and `maven-metadata.xml` are generated |
| Helm, RubyGems, NuGet, Ansible | none | everything is in the package |
| Conda | platform (default `noarch`) | file name |
| Composer | `vendor/name` and version | nothing |
| Terraform | module or provider; module name, system and version | provider type, version, OS and arch from `terraform-provider-TYPE_VERSION_OS_ARCH.zip` |
| Go | none | module path and version from the first entry of the module zip |
| APK, Debian, RPM, pacman | branch/repo/arch (apk), architecture (pacman) | the package |

Not available in the browser:

- Cargo: `cargo publish` computes the dependency metadata the registry stores.
- Conan, Docker, Ivy and sbt: many files in a client-specific layout.
- Nix: `nix copy` pushes the NAR and `.narinfo`; the page shows the command.

Use the clients in the matrix for those. You can drop several files at once; each shows progress and can be cancelled.

Debian and RPM have no upload route under `/repos/REPO/`. Upload them through the REST endpoints under `/api/v1/repositories/REPO/`. A `POST` or `PUT` to `HOST/repos/REPO/upload` of such a repository answers `404` and names the endpoint to use.

## Proxy and group repositories

Every format in the matrix can be a proxy or a group repository. Proxy and group repositories are read-only: writes answer `405`. Each format page has a Proxy and a Group section with the format-specific behaviour.

## Authentication

Authentication is required unless `allowAnonymousRead` is enabled or the repository is marked public (anonymous read only; writes always need a token). Package clients authenticate with an API token: `Authorization: Bearer <TOKEN>`, or HTTP Basic with the token as the password (any username). Account passwords are accepted only by the web UI login. Create tokens as described in [authentication](../security/authentication.md); what a token may do is governed by [RBAC](../security/rbac.md).

Read requests map to the `read` action and writes (`PUT`, `POST`, `PATCH`, `DELETE`) to `write` on the repository. The Debian/RPM REST upload endpoints under `/api/v1/repositories/REPO/{debs,packages,comps}` are authorized the same way, against the repository.

## Behaviour shared by all formats

- Blobs are content-addressed by SHA-256 and deduplicated across repositories; a blob is removed from storage only when no package references it.
- Deletes are soft by default: the artifact, version or tag moves to the [trash](../operations/trash.md) (404 on the native protocol, absent from listings, search and generated indexes) and can be restored until its retention expires. A repository can enable `immutable_releases` to block deleting or overwriting released versions.
- Hosted repositories of Cargo, Docker, Go, Helm, Maven, npm, NuGet, PyPI and raw can be replicated between instances ([federation](../operations/federation.md)).
- Uploads emit `artifact.uploaded` and deletes `artifact.deleted` webhook events ([webhooks](../operations/webhooks.md)).
- Storage quotas are enforced on upload for every hosted format; proxy caches are not metered ([quotas](../repositories/cleanup-and-quotas.md#what-counts)).
- A repository with a [license policy](../security/scanning.md#blocking-licenses) whose action is `block` rejects an upload whose declared license is denied (`403`, code `LICENSE_BLOCKED`). The license is read from the package metadata; Conan, Go, Terraform and raw uploads carry none and are not checked.
- [Client setup](client-setup.md) renders the snippets for every client with your own server URL and repository name. The web UI and the MCP `repository` tool show the same snippets.
