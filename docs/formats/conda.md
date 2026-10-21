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
- Name must be `<name>-<version>-<build>` with optional `.tar.bz2` or `.conda` suffix: split on `-`, first segment = name, second = version, the rest = build; fewer than three segments is `400 invalid filename format`. Package names containing `-` therefore cannot be uploaded correctly.
- Body limit 500 MiB. sha256 and md5 are computed; success is `201` with `{id, name, version, sha256}`.

## repodata.json

`info.subdir` is the platform. Every package in the platform appears under `packages` as `<name>-<version>-<build>.tar.bz2` with `name`, `version`, `build`, `depends`, `sha256`, `size`. `packages.conda` is always empty, and `depends` is empty because the package is not inspected on upload.
