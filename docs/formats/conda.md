<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Conda

Hosted conda channel: per-platform `repodata.json`, package download, upload and delete.

| | |
|---|---|
| Repo URL | `HOST/repos/REPO/<platform>/repodata.json` (channel `HOST/repos/REPO`) |
| Repository types | hosted only. See [hosted, proxy, group](../repositories/hosted-proxy-group.md) |

## Client setup

`internal/clientsetup` produces:

```sh
conda config --add channels HOST/repos/REPO
curl -u <USERNAME>:<TOKEN> -H "X-Filename: my-package-1.0-0.conda" --data-binary @my-package-1.0-0.conda HOST/repos/REPO/linux-64
```

Credentials are Basic with the token as password; see [authentication](../security/authentication.md).

## Endpoints

Relative to `HOST/repos/REPO`; `<platform>` is any path segment such as `linux-64`, `osx-arm64`, `noarch`.

| Method and path | Purpose |
|---|---|
| `GET /<platform>/repodata.json` | index for that platform |
| `GET /<platform>/<filename>` | package download |
| `POST /<platform>` | upload (body = package) |
| `DELETE /<platform>/<filename>` | delete |

## Upload

- Filename comes from the `filename` query parameter or the `X-Filename` header (`400 filename required` if neither).
- The file name must be `<name>-<version>-<build>.tar.bz2` or `<name>-<version>-<build>.conda`. It is split from the right: the build is the part after the last `-`, the version the part before that, and the name everything before, so names containing `-` (`scikit-learn-1.0-py39_0.tar.bz2`) work. Earlier releases split at the first two hyphens and stored such a package as name `scikit`, version `learn`; migration `019` re-splits those rows. Any other extension, fewer than three parts, an empty part, a path separator or a part longer than its column (name 255, version 100, build 255) is `400 invalid filename format`.
- The `.tar.bz2` and the `.conda` of one name, version, build and platform are separate files and can both be uploaded. Uploading a file that already exists is `409 package file already exists` and changes nothing; delete it first to replace it.
- Body limit 500 MiB. sha256 and md5 are computed; success is `201` with `{id, name, version, sha256}`.
- `info/index.json` is read from the package (from `info-*.tar.zst` inside a `.conda`) for the license policy and for the repodata fields below. Reading is bounded: at most 256 MiB of the decompressed archive and 100000 archive entries are scanned, and a package whose `info/index.json` cannot be found or parsed within that is accepted with empty `depends`, build number `0` and no timestamp.

## repodata.json

`info.subdir` is the platform. `.tar.bz2` files are listed under `packages` and `.conda` files under `packages.conda`, keyed by file name (`<name>-<version>-<build>.tar.bz2` or `.conda`). Each entry has:

| Field | Source |
|---|---|
| `name`, `version`, `build` | the file name |
| `build_number` | `info/index.json` (`0` when absent) |
| `depends` | `info/index.json` (`[]` when absent) |
| `subdir` | the platform the file was uploaded to |
| `sha256`, `md5`, `size` | computed on upload |
| `timestamp` | `info/index.json`, in milliseconds; omitted when the package declares none |
| `constrains`, `license`, `license_family`, `noarch`, `arch`, `platform`, `features`, `track_features` | `info/index.json`, copied as they are when present |

Packages stored before these fields were kept have no `info/index.json` data (empty `depends`, build number `0`, no `md5` or `timestamp`). They are all listed as `.tar.bz2`, including files that were uploaded with a `.conda` name (the extension was not recorded then), until they are deleted and uploaded again.
