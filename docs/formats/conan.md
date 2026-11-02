<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Conan

Use a Conan repository as a private Conan remote for C and C++ packages, for Conan 2 and Conan 1.x clients. It stores recipe and package revisions. A proxy caches another Conan remote, and a group serves several repositories as one remote.

| Item | Value |
|---|---|
| Remote URL | `HOST/repos/REPO` |
| Conan 2 API | `v2/conans/...` |
| Conan 1 API | `v1/conans/...` |
| Format id | `conan` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | `https://center2.conan.io` |

## Client setup

Create an API token as described in [authentication](../security/authentication.md). The web UI connect panel shows the same commands with your own URL.

### Conan 2

```sh
conan remote add REPO HOST/repos/REPO
conan remote login REPO <USERNAME> -p <TOKEN>
conan upload "my-lib/1.0" -r REPO --confirm
```

The login password is the API token. `conan remote login` sends it as Basic credentials to `users/authenticate`, which hands the same token back. The client then sends it as a Bearer token. Anything but a valid API token is `401`, so the client asks for the credentials again.

### Conan 1.x

A Conan 1.x client with revisions disabled (its default) speaks the `v1` API. It asks for a map of file name to URL (`upload_urls`, `download_urls`, `digest`), then transfers each file with a plain `GET` or `PUT` on that URL. The URLs are absolute, on the request origin or on `BASE_URL` when it is set. They point at `v1/files/...` of the same repository.

```sh
conan remote add REPO HOST/repos/REPO
conan user -p <TOKEN> -r REPO <USERNAME>
conan upload "my-lib/1.0" -r REPO --all --confirm
conan install my-lib/1.0@ -r REPO
```

A Conan 1 client with `general.revisions_enabled=1` uses the `v2` endpoints instead, exactly like Conan 2.

Authentication is the `v1/users/authenticate` exchange. `conan user -p <TOKEN>` sends the API token as the Basic password and gets it back as the bearer token. The `oauth_token` capability is not advertised.

## Publish

```sh
conan upload "my-lib/1.0" -r REPO --confirm
```

Each stored file is its own record, addressed by recipe revision, package id, package revision and file name (`conanfile.py`, `conan_export.tgz`, `conan_sources.tgz`, `conanmanifest.txt`, `conan_package.tgz`, `conaninfo.txt`, ...). A recipe and each of its packages can have several files.

- A successful upload of a file is `201` with `{id, name, sha256}`.
- The body limit is 200 MiB per file.
- A revision is published, and listed by `latest`, `revisions` and search, once its `conanmanifest.txt` is stored. The client uploads that file last. The revision time is the time of that manifest.
- A package can only be uploaded to a recipe revision that has its manifest. Otherwise the answer is `404`.
- Uploading the same bytes again is an idempotent `201`. Different bytes replace the stored file, and the server removes the old blob once no other file references it.
- File names, revisions and package ids are validated: no `..`, no empty segment, bounded length. A malformed address is `400`.

In a repository with [immutable releases](../operations/trash.md#immutable-releases), a different upload to an existing file and every delete answer `409` unless an admin passes `?force=true`. Every Conan version counts as a release.

The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is enforced per stored file (`507` over quota). Replacing a file releases the old content. Deleting a revision releases its files, at purge when the trash is enabled.

Quarantine and webhooks (`artifact.uploaded`, `artifact.deleted`) apply as for other formats. The UI listing and search show one entry per recipe revision, not one per file.

### Checksum deploy

The server advertises `checksum_deploy`. Before it sends a file, Conan sends an empty `PUT` with `X-Checksum-Deploy: true` and the file's SHA-1 in `X-Checksum-Sha1`. When the same repository already holds a file with that SHA-1, the server stores the new file as a reference to the same content. The answer is `201` and no bytes move. Otherwise the answer is `404` and the client uploads the file normally. A malformed checksum is `400`.

A deployed file passes the same checks as an upload:

- The recipe must be published for a package file.
- Immutable releases refuse replacing different content (`409`).
- The quota counts the new file (`507`).
- The `artifact.uploaded` webhook fires.

The lookup never leaves the repository and never reuses a quarantined file. A file whose SHA-1 is not recorded is not a deploy source until a Conan 1 client has listed it or it is uploaded again.

## Install and consume

```sh
conan install --requires=my-lib/1.0 -r REPO
```

Paths are relative to `HOST/repos/REPO`. In the Conan 2 table, `<ref>` is `v2/conans/<name>/<version>/<user>/<channel>` (a recipe without user and channel uses `_/_`), `<rrev>` is a recipe revision, `<pid>` a package id and `<prev>` a package revision.

| Method and path | Purpose |
|---|---|
| `GET /v1/ping`, `GET /v2/ping` | `200` with `X-Conan-Server-Capabilities: revisions,checksum_deploy` |
| `GET /v2/users/authenticate` (also `/v1/`) | Exchange the Basic credentials for the token |
| `GET /v2/users/check_credentials` (also `/v1/`) | The authenticated user's name, `401` otherwise |
| `GET /v2/conans/search?q=` | Recipes whose reference matches the glob (`zlib/*`, `*`; `ignorecase=False` for case sensitive) |
| `GET /<ref>/latest` | Newest published recipe revision, `{"revision", "time"}` |
| `GET /<ref>/revisions` | Published recipe revisions, newest first |
| `GET /<ref>/revisions/<rrev>/files` | `{"files": {"<name>": {"size": N}}}` of the recipe revision |
| `GET`, `HEAD`, `PUT /<ref>/revisions/<rrev>/files/<filename>` | Download, probe, upload (raw body) of one recipe file |
| `GET /<ref>/revisions/<rrev>/search` | Packages of the revision |
| `GET /<ref>/revisions/<rrev>/packages/<pid>/latest` and `/revisions` | Newest and all revisions of a package |
| `GET /<ref>/revisions/<rrev>/packages/<pid>/revisions/<prev>/files` | File list of a package revision |
| `GET`, `HEAD`, `PUT /<ref>/revisions/<rrev>/packages/<pid>/revisions/<prev>/files/<filename>` | Download, probe, upload of one package file |
| `DELETE /<ref>` | Remove every revision of the recipe with its packages |
| `DELETE /<ref>/revisions/<rrev>` | Remove one recipe revision with its packages |
| `DELETE /<ref>/revisions/<rrev>/packages` | Remove every package of the revision |
| `DELETE /<ref>/revisions/<rrev>/packages/<pid>/revisions/<prev>` | Remove one package revision |

A download serves the file named in the URL with its real size and `Content-Length`. `HEAD` returns the headers only. An unknown file is `404`.

Every delete answers `200`, because the Conan client treats other success codes as failure, or `404` when nothing matches.

### Conan 1 endpoints

In this table `<ref>` is `v1/conans/<name>/<version>/<user>/<channel>` (`_/_` for a recipe without user and channel).

| Method and path | Purpose |
|---|---|
| `GET /v1/conans/search?q=` | Recipe search, as the `v2` one |
| `GET /<ref>` | `{"<file>": "<md5>"}` of the newest published recipe revision, `404` when there is none |
| `GET /<ref>/digest` | `{"conanmanifest.txt": "<url>"}` |
| `GET /<ref>/download_urls` | `{"<file>": "<url>"}` of every recipe file |
| `POST /<ref>/upload_urls` | Body `{"<file>": <size>}`, answers `{"<file>": "<url>"}` for revision `0` |
| `POST /<ref>/remove_files` | Body `{"files": [...]}`, removes those recipe files of revision `0` (`200` even when none exist) |
| `GET /<ref>/search` | Packages of the recipe, `{"<pid>": {"content": "<conaninfo.txt>"}}` |
| `POST /<ref>/packages/delete` | Body `{"package_ids": [...]}`; an empty list means every package; `404` when nothing matches |
| `DELETE /<ref>` | Remove the recipe with all its revisions and packages |
| `GET /<ref>/packages/<pid>`, `/digest`, `/download_urls` | The same three answers for the newest revision of a package |
| `POST /<ref>/packages/<pid>/upload_urls` | Upload URLs of a package, attached to the newest published recipe revision (`404` when the recipe has none) |
| `GET`, `HEAD`, `PUT /v1/files/<name>/<version>/<user>/<channel>/<rrev>/export/<file>` | One recipe file |
| `GET`, `HEAD`, `PUT /v1/files/<name>/<version>/<user>/<channel>/<rrev>/package/<pid>/<prev>/<file>` | One package file |

How the two protocols share a repository:

- The server stores files once, whichever client uploaded them.
- Conan 1 has no revisions. Its uploads are stored under recipe revision `0` and package revision `0`, as `conan_server` does. A Conan 2 client sees them as revision `0`.
- A Conan 1 download serves the newest published recipe revision and its newest package revision. It also installs what a Conan 2 client uploaded.
- Package search for a Conan 1 client looks in every recipe revision, newest first. The answer carries `conaninfo.txt` as `content`, which Conan 1.48 and later read. Older 1.x clients cannot list the binaries of a remote.
- `remove_files` and `packages/delete` are POSTs, but a token needs the delete permission for them, not only write.

<a id="proxy-and-group-repositories"></a>

## Proxy

Create a proxy with `repo_type: "proxy"`. Without `upstream_url` the default is `https://center2.conan.io`. The upstream must serve the Conan 2 REST API with revisions (`v2/conans/...`). A conan proxy is a Conan remote, not a path mirror. Clients use `HOST/repos/REPO` as their remote, as with a hosted repository. A proxy is read-only, so `conan upload` and `conan remove` answer `405` (`Allow: GET, HEAD`).

```sh
conan remote add conan-proxy HOST/repos/conan-proxy
conan remote login conan-proxy <USERNAME> -p <TOKEN>
conan install --requires=zlib/1.3.1 -r conan-proxy
```

- **Conan 2 API.** The proxy serves the read endpoints of [Install and consume](#install-and-consume) from the upstream's `v2` API: `latest`, `revisions`, package `latest` and `revisions`, file lists, files, and the recipe and package searches.
- **Documents.** The server checks that `latest`, `revisions` and file lists are what the API sends. A portal page or a malformed document is `502` and is not cached. Documents are cached for the repository TTL and served stale when the upstream is down. A file list is pinned for good once it lists `conanmanifest.txt`, which is what publishes a revision.
- **Files** of a revision never change once cached. The server refuses an archive that is not gzip. Quarantine and the scanners apply to cached files.
- **Search** passes through uncached. The server forwards only `q`, `ignorecase` and `list_only`. A value longer than 512 bytes is `400`.
- **Session.** This server answers `ping` and the login endpoints. The client logs in to it with an API token, and the proxy uses its own upstream credentials. `ping` advertises `revisions` and not `checksum_deploy`, since nothing can be uploaded.
- **Credentials.** With `upstream_username` and `upstream_password` the proxy logs in at `v2/users/authenticate`, sends the token it receives as a Bearer token, and renews it when it expires. An upstream without that endpoint gets the credentials as Basic. With only `upstream_password` it is sent as a Bearer token, for a token-only remote. The server stores credentials encrypted and never sends them to a host other than the upstream's.
- **Conan 1.x clients** are served from the same upstream documents. A proxy answers `download_urls` and `digest` of the newest recipe revision (and of a package, its newest package revision) with `v1/files/...` URLs of the proxy itself. It serves those URLs as the Conan 2 file requests, so a file is cached once for both kinds of client.
- **Conan 1 pass-through.** `GET /v1/conans/search`, the package search `GET /<ref>/search` and the snapshot `GET /<ref>` are passed to the upstream's own `v1` endpoints. For those the upstream must still serve Conan 1.
- **Conan 1 binaries.** A Conan 1 client can only install what was built for it. Binaries on a Conan 2 only remote such as ConanCenter use Conan 2 package ids, and a Conan 1 client does not find them.

### Minimum age and curation

Rules follow [Package curation](../repositories/hosted-proxy-group.md#package-curation). The format-specific parts:

- **Minimum age.** `min_age_hours` has no effect on a conan proxy. See [Minimum age](../repositories/hosted-proxy-group.md#minimum-age).
- **Names.** Rules name a recipe by its lower-case name and a version as the reference shows it (`zlib`, `1.3.1`).
- **Blocked recipe.** The Conan 2 revision, file-list and file requests answer `403` with the reason, even when cached.
- **Not gated.** Searches are not gated.

> **Warning:** curation on a conan proxy does not stop Conan 1 clients. Conan 1 paths (`v1/...`) are not gated, so a Conan 1 client can still fetch a blocked recipe through the `v1/files/...` URLs.

## Group

A conan group serves several hosted and conan proxy repositories as one remote. Set it up as for a hosted repository: `conan remote add GROUP HOST/repos/GROUP`. A group is read-only, so `conan upload` answers `405`. The group advertises `revisions` and not `checksum_deploy`. See [Group repositories](../repositories/hosted-proxy-group.md#group-repositories) for the general rules.

- **Revision lists and `latest`.** The group merges them for a recipe or a package across the readable members. Revisions are united, newest first, with one entry per revision. `latest` is the newest of all.
- **Package search** of a recipe: the first member that has a package wins it. **Recipe search** unites the members' results.
- **File lists and files** are first-hit in member order. The file of a revision comes from the first member that has it.
- **Conan 1 clients.** The group answers `download_urls` and `digest` from the first member that has the recipe, with file URLs that point at the group.
- **Hosted members win a recipe name.** If a readable hosted member holds a recipe name, revision lists, `latest`, package searches, file lists and files of that name come only from hosted members. The group search leaves out the proxies' results for that name. A Conan 1 or 2 client never sees the proxy's versions. A hosted member that cannot answer the search fails the group search with `502`. Setting `merge_proxy_versions` to `true` turns this off. See [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).
- **Failing members.** A member that fails while another contributes is ignored. With no contributor the group answers `404`, or `502` if a member failed. The server serves a merged answer that left out a failed member with `Cache-Control: no-store` and no `ETag`.
- **Minimum age and curation.** A group serves what its proxy members serve. The Conan 1 warning under Proxy applies to groups too.

## Limits and behaviour

- **Upload size:** 200 MiB per file.
- **Immutable releases:** a different upload to an existing file and every delete are `409` unless an admin passes `?force=true`.
- **Quota:** `507` over quota.
- **Delete:** every delete answers `200`, or `404` when nothing matches. With the trash enabled, deleting a revision moves each of its files to the [trash](../operations/trash.md) as its own item, all with the same name, version and deletion time. Restore every file to bring the revision back.
- **Status codes:** `201` stored, `400` malformed address or checksum, `401` bad token, `404` unknown file or unpublished recipe, `405` write to a proxy or group.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `401` on login | The password is not a valid API token. | Create an API token and use it as the password. |
| `404` when you upload a package | The recipe revision has no `conanmanifest.txt` yet. | Upload the recipe first. |
| `409` on upload or delete | The repository has immutable releases. | Use a new version, or ask an admin to pass `?force=true`. |
| `405` on `conan upload` | The repository is a proxy or group. | Upload to a hosted repository. |
| A Conan 1 client does not find a binary on a proxy | The upstream has only Conan 2 package ids. | Use a Conan 2 client, or an upstream that serves Conan 1 binaries. |
| A Conan 1 client still gets a recipe you blocked | Curation does not gate `v1/...` paths. | Do not rely on curation for Conan 1 clients. |
| `502` from a proxy | The upstream is down, or returned something that is not the Conan API. | Check `upstream_url` and the upstream. |
