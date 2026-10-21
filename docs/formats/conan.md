<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Conan

Hosted Conan v2 remote with a minimal revision model.

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

The login password is an API token; see [authentication](../security/authentication.md).

## Endpoints

Relative to `HOST/repos/REPO/v2/conans`:

| Method and path | Purpose |
|---|---|
| `GET /<pkg>/<version>/<user>/<channel>/revisions` | revision list |
| `GET /<pkg>/<version>/<user>/<channel>/revisions/<rev>/files` | file list |
| `GET /<pkg>/<version>/<user>/<channel>/revisions/<rev>/files/<filename>` | download |
| `PUT /<pkg>/<version>/<user>/<channel>/revisions/<rev>/files/<filename>` | upload (raw body) |
| `DELETE /<pkg>/<version>/<user>/<channel>` | delete the package record |
| `GET /search?q=` | search |

## Behavior and limits

- Upload body limit 200 MiB; success is `201` with `{id, name, sha256}`.
- The `<rev>` path element is ignored: every package is stored and served as recipe revision `0`, keyed by name, version and `user/channel`. The revisions endpoint returns only revision `0` with the creation timestamp.
- One blob is stored per name/version/user/channel. A second file upload for the same package hits the unique constraint and fails with `500`.
- The file list is static (`conanfile.py`, `conaninfo.txt`, size `0`); the download route returns the package's single stored blob whatever `<filename>` is.
- No binary-package (`packages/...`) routes and no `/v2/ping` route are registered.
