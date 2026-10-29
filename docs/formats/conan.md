<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Conan

Conan remote for Conan 2 and Conan 1.x clients, with recipe and package revisions: hosted, proxy of another Conan remote, and group.

| | |
|---|---|
| Repo URL | `HOST/repos/REPO/` (`v2/conans/...` for Conan 2, `v1/conans/...` for Conan 1) |
| Repository types | hosted, proxy, group. See [hosted, proxy, group](../repositories/hosted-proxy-group.md) |

## Client setup

`internal/clientsetup` produces:

```sh
conan remote add REPO HOST/repos/REPO
conan remote login REPO <USERNAME> -p <TOKEN>
conan upload "my-lib/1.0" -r REPO --confirm
```

The login password is an API token; see [authentication](../security/authentication.md). `conan remote login` sends it as Basic credentials to `users/authenticate`, which hands the same token back; the client then sends it as a Bearer token. Anything but a valid API token is `401`, so the client asks for the credentials again.

## Endpoints

Relative to `HOST/repos/REPO`. `<ref>` is `v2/conans/<name>/<version>/<user>/<channel>` (a recipe without user and channel uses `_/_`), `<rrev>` a recipe revision, `<pid>` a package id and `<prev>` a package revision.

| Method and path | Purpose |
|---|---|
| `GET /v1/ping`, `GET /v2/ping` | `200` with `X-Conan-Server-Capabilities: revisions,checksum_deploy` |
| `GET /v2/users/authenticate` (also `/v1/`) | exchange the Basic credentials for the token |
| `GET /v2/users/check_credentials` (also `/v1/`) | the authenticated user's name, `401` otherwise |
| `GET /v2/conans/search?q=` | recipes whose reference matches the glob (`zlib/*`, `*`; `ignorecase=False` for case sensitive) |
| `GET /<ref>/latest` | newest published recipe revision, `{"revision", "time"}` |
| `GET /<ref>/revisions` | published recipe revisions, newest first |
| `GET /<ref>/revisions/<rrev>/files` | `{"files": {"<name>": {"size": N}}}` of the recipe revision |
| `GET`, `HEAD`, `PUT /<ref>/revisions/<rrev>/files/<filename>` | download, probe, upload (raw body) of one recipe file |
| `GET /<ref>/revisions/<rrev>/search` | packages of the revision |
| `GET /<ref>/revisions/<rrev>/packages/<pid>/latest` and `/revisions` | newest and all revisions of a package |
| `GET /<ref>/revisions/<rrev>/packages/<pid>/revisions/<prev>/files` | file list of a package revision |
| `GET`, `HEAD`, `PUT /<ref>/revisions/<rrev>/packages/<pid>/revisions/<prev>/files/<filename>` | download, probe, upload of one package file |
| `DELETE /<ref>` | remove every revision of the recipe with its packages |
| `DELETE /<ref>/revisions/<rrev>` | remove one recipe revision with its packages |
| `DELETE /<ref>/revisions/<rrev>/packages` | remove every package of the revision |
| `DELETE /<ref>/revisions/<rrev>/packages/<pid>/revisions/<prev>` | remove one package revision |

A `PUT` to a file URL that carries `X-Checksum-Deploy: true` and `X-Checksum-Sha1` and has no body is a [checksum deploy](#checksum-deploy).

Every delete answers `200` (the conan client treats other success codes as failure), or `404` when nothing matches.

## Behavior and limits

- Each stored file is its own record, addressed by recipe revision, package id, package revision and file name (`conanfile.py`, `conan_export.tgz`, `conan_sources.tgz`, `conanmanifest.txt`, `conan_package.tgz`, `conaninfo.txt`, ...). Uploading the several files of one recipe, and of each package, works; before 0.5.0 the second file of a recipe failed with `500`.
- Upload body limit 200 MiB; success is `201` with `{id, name, sha256}`. Download serves the file named in the URL with its real size and `Content-Length`; `HEAD` returns the headers only; an unknown file is `404`.
- A revision is published (listed by `latest`, `revisions` and search) once its `conanmanifest.txt` is stored, which the client uploads last. The revision time is the time of that manifest, newest first. A package can only be uploaded to a recipe revision that has its manifest, otherwise `404`.
- Uploading the same bytes again is an idempotent `201`. Different bytes replace the stored file, and the old blob is removed once no other file references it. In a repository with [immutable releases](../operations/trash.md#immutable-releases) a different upload to an existing file, and every delete, is `409` unless an admin passes `?force=true`; every conan version counts as a release.
- The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is enforced per stored file (`507` over quota); replacing a file releases the old content, deleting a revision releases its files (at purge when the trash is enabled).
- File names, revisions and package ids are validated (no `..`, no empty segment, bounded length); a malformed address is `400`.
- Quarantine and webhooks (`artifact.uploaded`, `artifact.deleted`) apply as for other formats. The listing and search in the UI show one entry per recipe revision, not one per file.
- With the trash enabled, deleting a revision moves each of its files to the [trash](../operations/trash.md) as its own item (all with the same name, version and deletion time); restore every file to bring the revision back.

## Upgrading from before 0.5.0

Releases before 0.14 stored one record per recipe under revision `0` and could not serve the files a conan client uploads. 0.15 starts from a clean schema and `oa import` republishes every conan file through the normal upload, so a 0.15 repository holds only revision-keyed rows; to move from a 0.13 or older store, upload the recipes again (`conan upload "*" -r REPO --confirm`).

## Conan 1.x

A Conan 1.x client (revisions disabled, its default) speaks the `v1` API: it asks for a map of file name to URL (`upload_urls`, `download_urls`, `digest`) and then transfers each file with a plain `GET` or `PUT` on that URL. The URLs are absolute (the request origin, or `BASE_URL` when set; a relative URL would resolve against the remote URL without its trailing slash) and point at `v1/files/...` of the same repository.

```sh
conan remote add REPO HOST/repos/REPO
conan user -p <TOKEN> -r REPO <USERNAME>
conan upload "my-lib/1.0" -r REPO --all --confirm
conan install my-lib/1.0@ -r REPO
```

A Conan 1 client with `general.revisions_enabled=1` uses the `v2` endpoints above instead, exactly like Conan 2.

`<ref>` below is `v1/conans/<name>/<version>/<user>/<channel>` (`_/_` for a recipe without user and channel), relative to `HOST/repos/REPO`.

| Method and path | Purpose |
|---|---|
| `GET /v1/conans/search?q=` | recipe search, as the `v2` one |
| `GET /<ref>` | `{"<file>": "<md5>"}` of the newest published recipe revision, `404` when there is none |
| `GET /<ref>/digest` | `{"conanmanifest.txt": "<url>"}` |
| `GET /<ref>/download_urls` | `{"<file>": "<url>"}` of every recipe file |
| `POST /<ref>/upload_urls` | body `{"<file>": <size>}`, answers `{"<file>": "<url>"}` for revision `0` |
| `POST /<ref>/remove_files` | body `{"files": [...]}`, removes those recipe files of revision `0` (`200` even when none exist) |
| `GET /<ref>/search` | packages of the recipe, `{"<pid>": {"content": "<conaninfo.txt>"}}` |
| `POST /<ref>/packages/delete` | body `{"package_ids": [...]}`; an empty list means every package; `404` when nothing matches |
| `DELETE /<ref>` | remove the recipe with all its revisions and packages |
| `GET /<ref>/packages/<pid>`, `/digest`, `/download_urls` | the same three answers for the newest revision of a package |
| `POST /<ref>/packages/<pid>/upload_urls` | upload URLs of a package, attached to the newest published recipe revision (`404` when the recipe has none) |
| `GET`, `HEAD`, `PUT /v1/files/<name>/<version>/<user>/<channel>/<rrev>/export/<file>` | one recipe file |
| `GET`, `HEAD`, `PUT /v1/files/<name>/<version>/<user>/<channel>/<rrev>/package/<pid>/<prev>/<file>` | one package file |

How the two protocols share a repository:

- Files are stored once, whichever client uploaded them. Conan 1 has no revisions, so its uploads are stored under recipe revision `0` and package revision `0`, as `conan_server` does; a Conan 2 client sees them as revision `0`.
- A Conan 1 download serves the newest published recipe revision (and its newest package revision), so it also installs what a Conan 2 client uploaded.
- Package search for a Conan 1 client looks in every recipe revision, newest first. The answer carries `conaninfo.txt` as `content`, which Conan 1.48 and later read; older 1.x clients cannot list the binaries of a remote.
- `remove_files` and `packages/delete` are POSTs, but a token needs the delete permission for them, not just write.
- Authentication is the `v1/users/authenticate` exchange: `conan user -p <TOKEN>` sends the API token as the Basic password and gets it back as the bearer token. The `oauth_token` capability is not advertised.

## Proxy and group repositories

A conan proxy caches another Conan remote (default upstream `https://center2.conan.io`) and a conan group serves several repositories as one remote. Clients are set up as for a hosted repository (`conan remote add REPO HOST/repos/REPO`, `conan remote login`); the repository is read-only, so `conan upload` is `405`. How the proxy caches, authenticates to the upstream and what a group merges: [Conan proxies](../repositories/hosted-proxy-group.md#conan-proxies) and [Group repositories](../repositories/hosted-proxy-group.md#group-repositories).

```sh
conan remote add conan-proxy HOST/repos/conan-proxy
conan remote login conan-proxy <USERNAME> -p <TOKEN>
conan install --requires=zlib/1.3.1 -r conan-proxy
```

- **Conan 2 API.** The proxy serves the read endpoints of [Endpoints](#endpoints) from the upstream's `v2` API with revisions: `latest`, `revisions`, package `latest` and `revisions`, file lists, files, and the recipe and package searches. The upstream must offer that API.
- **Conan 1.x clients** (revisions disabled) are served from the same upstream documents. A proxy answers `download_urls` and `digest` of the newest recipe revision (and of a package, its newest package revision) with `v1/files/...` URLs of the proxy itself, and serves those URLs as the Conan 2 file requests, so a file is cached once for both kinds of client. `GET /v1/conans/search`, the package search `GET /<ref>/search` and the snapshot `GET /<ref>` (file to MD5) are passed through to the upstream's own `v1` endpoints, so for those the upstream must still serve Conan 1. A Conan 1 client can only install what was built for it: binaries on a Conan 2 only remote such as ConanCenter are built for Conan 2 package ids and are not found by a Conan 1 client.
- **Ping.** A proxy or group advertises `revisions` and not `checksum_deploy`, since nothing can be uploaded.
- **Group.** Hosted members win a recipe name over proxy members (see [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name)), so a name published in the hosted repository cannot be replaced by a public recipe of the same name; a Conan 1 or 2 client that asks the group for a name the hosted member holds never sees the proxy's versions. The group search is the union of the members' results.

## Checksum deploy

The server advertises `checksum_deploy`. Before sending a file, Conan 1.x (a monolithic server, as this one) and Conan 2 send an empty `PUT` with `X-Checksum-Deploy: true` and the file's SHA-1 in `X-Checksum-Sha1`. When the same repository already holds a file with that SHA-1, the new file is stored as a reference to the same content (`201`, no bytes transferred); otherwise `404` and the client uploads the file normally. A malformed checksum is `400`.

A deployed file passes the same checks as an upload: the recipe must be published for a package file, immutable releases refuse replacing different content (`409`), the quota counts the new file (`507`), the `artifact.uploaded` webhook fires. The lookup never leaves the repository and never reuses a quarantined file. A file whose SHA-1 is not recorded is not a deploy source until a Conan 1 client has listed it or it is uploaded again.
