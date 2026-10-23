<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Conan

Hosted Conan remote for Conan 2 and Conan 1.x clients, with recipe and package revisions.

| | |
|---|---|
| Repo URL | `HOST/repos/REPO/` (`v2/conans/...` for Conan 2, `v1/conans/...` for Conan 1) |
| Repository types | hosted only. See [hosted, proxy, group](../repositories/hosted-proxy-group.md) |

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

Earlier releases stored one record per recipe under revision `0`, ignored the revision and file name in the URL, and could not serve the files a conan client uploads. Migration `019` adds the package id, package revision and file name columns and replaces the unique index. Records stored by the old code do not carry file names, so the protocol endpoints do not list them: upload those recipes again (`conan upload "*" -r REPO --confirm`). The old records stay visible in the UI listing and are removed by `DELETE /v2/conans/<name>/<version>/<user>/<channel>`.

## Conan 1.x

A Conan 1.x client (revisions disabled, its default) speaks the `v1` API: it asks for a map of file name to URL (`upload_urls`, `download_urls`, `digest`) and then transfers each file with a plain `GET` or `PUT` on that URL. The URLs are absolute (built from `BASE_URL`, so set it to the address clients use) and point at `v1/files/...` of the same repository.

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

## Checksum deploy

The server advertises `checksum_deploy`. Before sending a file, Conan 1.x (a monolithic server, as this one) and Conan 2 send an empty `PUT` with `X-Checksum-Deploy: true` and the file's SHA-1 in `X-Checksum-Sha1`. When the same repository already holds a file with that SHA-1, the new file is stored as a reference to the same content (`201`, no bytes transferred); otherwise `404` and the client uploads the file normally. A malformed checksum is `400`.

A deployed file passes the same checks as an upload: the recipe must be published for a package file, immutable releases refuse replacing different content (`409`), the quota counts the new file (`507`), the `artifact.uploaded` webhook fires. The lookup never leaves the repository and never reuses a quarantined file. Files stored before migration `057` have no SHA-1 recorded and are not deploy sources until a Conan 1 client has listed them or they are uploaded again.
