<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Git LFS

Use a hosted Git LFS repository as the large-file store of your Git repositories. `git lfs` sends the big files to OpenArtifact; the commits, with small pointer files in place of the big ones, stay on your Git server. A Git LFS repository is hosted only: there is no proxy or group.

| Item | Value |
|---|---|
| Repo URL | `HOST/repos/gitlfs/REPO` (this is `lfs.url`) |
| Format id | `gitlfs` |
| Repository types | hosted only; see [hosted, proxy, group](../repositories/hosted-proxy-group.md) |
| Protocol | Git LFS batch API, `basic` transfer adapter |

## Client setup

The web UI connect panel shows the same commands with your own URL.

```sh
git config --global credential.helper store
git credential approve <<EOF
url=HOST/repos/gitlfs/REPO
username=<USERNAME>
password=<TOKEN>

EOF

git lfs install
git config lfs.url HOST/repos/gitlfs/REPO
git lfs track "*.bin"
git add .gitattributes big.bin && git commit -m "add big.bin" && git push origin main
```

The token goes in the password. The user name can be anything. A client that has no credentials gets `401` with an `LFS-Authenticate: Basic` header and asks its credential helper, so an interactive `git push` prompts once. See [authentication](../security/authentication.md).

To give everyone who clones the repository the same server, commit the URL instead of setting it per clone:

```sh
git config -f .lfsconfig lfs.url HOST/repos/gitlfs/REPO
git add .lfsconfig
```

`lfs.url` has no `.git` suffix and no `/info/lfs`: the client adds `/objects/batch` to it.

## Publish

`git push` uploads the objects that the server does not have yet, then pushes the commits. The client asks `POST /objects/batch` which objects are missing and uploads each one with a `PUT` to the address the answer names. An object that is already stored is not sent again, also not from another clone.

- A write token is needed. A read-only token can download but gets `403` on an upload batch and on `PUT`.
- The server hashes the body while it receives it. A body that does not hash to the object id, or whose length is not the declared size, is `422` and nothing is stored.
- The limit is 5 GiB per object; larger bodies are `413`.
- The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is enforced; over quota is `507`.
- Uploading an object that exists is a success and changes nothing.

## Install and consume

`git clone`, `git checkout` and `git lfs pull` download the objects of the files you check out. Reading needs a read token for a private repository.

```sh
git clone GIT_URL project
cd project
git lfs pull
```

A clone of a repository that does not carry `.lfsconfig` needs `git config lfs.url HOST/repos/gitlfs/REPO` first.

Paths are relative to `HOST/repos/gitlfs/REPO`. `OID` is the lower-case hex SHA-256 of the file.

| Method and path | Result |
|---|---|
| `POST /objects/batch` | the Git LFS batch API: `download` and `upload` operations |
| `PUT /objects/OID` | upload an object, `200` |
| `GET /objects/OID` | the object, with `Content-Length`, `ETag`, `Last-Modified` and `X-Checksum-Sha256`; `Range` and conditional requests are honoured |
| `HEAD /objects/OID` | the same headers only |
| `POST /verify` | the check the client runs after an upload: `200` when the object is stored with that size |

The batch API follows the [Git LFS specification](https://github.com/git-lfs/git-lfs/blob/main/docs/api/batch.md): only the `basic` transfer adapter and only `sha256` are supported. The `download` and `upload` actions it hands out are plain URLs without a token in them; a Git LFS client sends its credentials to them, and so does `curl -u`.

An object is listed in the web UI and the REST API as a package named by its object id. Deleting it moves it to the [trash](../operations/trash.md) (kind `gitlfs_object`), after which `GET` is `404`. Cleanup policies see every object as one item, so `keep_last_n` keeps the newest N objects of the repository and an age limit removes the old ones.

## Limits and behaviour

- **Object size.** 5 GiB per upload; see [Publish](#publish). A batch request is at most 1 MiB.
- **Quota.** Over quota is `507`. Deleting an object releases it.
- **Quarantine.** A [quarantined](../security/scanning.md#quarantine-after-upload) object is refused on download, in the batch answer (`403` for that object) and on `GET`.
- **Presigned downloads.** `GET` follows the repository's [presigned download](../operations/storage.md#presigned-urls) setting like other formats.
- **File locking is not supported.** The locks endpoints (`/locks`, `/locks/verify`, `/locks/ID/unlock`) answer `501`. The client treats that as "this server has no locks": `git push` still works, and the client records `lfs.URL.locksverify=false` for the server by itself. `git lfs lock` fails.
- **Only HTTP.** SSH transfer (`git-lfs-authenticate`) is not offered.
- **No Git hosting.** OpenArtifact stores the LFS objects only; your commits stay on your Git server.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `could not read Username` or `Git credentials ... not found` | The client has no credentials and cannot prompt, as in CI. Add the token with `git credential approve`, see [Client setup](#client-setup). |
| `Authorization error` on `git push` | The token is read-only or belongs to another project. Use a token with write access to the repository. |
| `404` on `git lfs pull` | The object was never pushed to this repository, or it is in the [trash](../operations/trash.md). Push it again with `git lfs push --all origin`. |
| `403` for one object on download | The object is [quarantined](../security/scanning.md#quarantine-after-upload). |
| `422` on upload | The body is not the object, or its length differs from the declared size. Run `git lfs fsck`. |
| `507` on upload | The repository is over its quota. Free space or raise the quota. |
| `501` from `git lfs lock` | The server has no file locking. |
| `400` creating a proxy or group | Git LFS exists as a hosted repository only. |
