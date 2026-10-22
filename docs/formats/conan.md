<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Conan

Hosted Conan 2 remote with recipe and package revisions.

| | |
|---|---|
| Repo URL | `HOST/repos/REPO/` (`v2/conans/...`) |
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
| `GET /v1/ping`, `GET /v2/ping` | `200` with `X-Conan-Server-Capabilities: revisions` |
| `GET /v2/users/authenticate` | exchange the Basic credentials for the token |
| `GET /v2/users/check_credentials` | the authenticated user's name, `401` otherwise |
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

Every delete answers `200` (the conan client treats other success codes as failure), or `404` when nothing matches.

## Behavior and limits

- Each stored file is its own record, addressed by recipe revision, package id, package revision and file name (`conanfile.py`, `conan_export.tgz`, `conan_sources.tgz`, `conanmanifest.txt`, `conan_package.tgz`, `conaninfo.txt`, ...). Uploading the several files of one recipe, and of each package, works; before 0.5.0 the second file of a recipe failed with `500`.
- Upload body limit 200 MiB; success is `201` with `{id, name, sha256}`. Download serves the file named in the URL with its real size and `Content-Length`; `HEAD` returns the headers only; an unknown file is `404`.
- A revision is published (listed by `latest`, `revisions` and search) once its `conanmanifest.txt` is stored, which the client uploads last. The revision time is the time of that manifest, newest first. A package can only be uploaded to a recipe revision that has its manifest, otherwise `404`.
- Uploading the same bytes again is an idempotent `201`. Different bytes replace the stored file, and the old blob is removed once no other file references it. In a repository with [immutable releases](../operations/trash.md#immutable-releases) a different upload to an existing file, and every delete, is `409` unless an admin passes `?force=true`; every conan version counts as a release.
- File names, revisions and package ids are validated (no `..`, no empty segment, bounded length); a malformed address is `400`.
- Quarantine and webhooks (`artifact.uploaded`, `artifact.deleted`) apply as for other formats. The listing and search in the UI show one entry per recipe revision, not one per file.
- With the trash enabled, deleting a revision moves each of its files to the [trash](../operations/trash.md) as its own item (all with the same name, version and deletion time); restore every file to bring the revision back.

## Upgrading from before 0.5.0

Earlier releases stored one record per recipe under revision `0`, ignored the revision and file name in the URL, and could not serve the files a conan client uploads. Migration `019` adds the package id, package revision and file name columns and replaces the unique index. Records stored by the old code do not carry file names, so the protocol endpoints do not list them: upload those recipes again (`conan upload "*" -r REPO --confirm`). The old records stay visible in the UI listing and are removed by `DELETE /v2/conans/<name>/<version>/<user>/<channel>`.

## Not supported

Only the Conan 2 REST API (`v2/...`) is served. The Conan 1 endpoints (`v1/conans/...`, upload URL negotiation) are not.
