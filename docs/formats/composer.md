<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Composer (PHP)

Hosted private Composer repository: Composer installs from an OpenArtifact repository through `packages.json` and the Composer repository v2 metadata (`p2/`), and packages are published as dist zips.

| | |
|---|---|
| Format id | `composer` |
| Repository URL | `HOST/repos/REPO` (Composer reads `HOST/repos/REPO/packages.json`) |
| Repository types | hosted (proxy and group repositories are not available yet) |

## Overview

A repository of format `composer` stores one dist zip per package version. A package is identified by its lowercase `vendor/name`, taken from the `name` field of the `composer.json` inside the zip. Permissions, quota, [trash](../operations/trash.md), webhooks and [cleanup](../repositories/cleanup-and-quotas.md) apply as for any other format. Create the repository in the web UI (format **Composer**) or with `format: composer` through the API or CLI, see [repositories](../repositories/hosted-proxy-group.md).

## Supported scope

- **Composer repository v2:** `packages.json`, `p2/VENDOR/NAME.json` (stable versions) and `p2/VENDOR/NAME~dev.json` (dev versions), `list.json` and `search.json`.
- **Dist only.** Every version has a `dist` of type `zip`; there is no `source` (VCS) entry.
- **Out of scope for now:** proxy repositories (mirroring Packagist) and group repositories come in a follow-up; `security-advisories` and `providers-api` are not served.

## Client setup

`internal/clientsetup` produces the same snippets the web UI shows. Create an API token as described in [authentication](../security/authentication.md). Composer sends credentials as HTTP Basic, with the token as the password; the user name is not checked, so use `token`. Read access installs, write access publishes. A public repository (or `allowAnonymousRead`) is read without credentials.

`composer.json`:

```json
{
  "repositories": [
    {"type": "composer", "url": "https://HOST/repos/REPO"}
  ]
}
```

or from the command line:

```sh
composer config repositories.openartifact composer https://HOST/repos/REPO
composer config http-basic.HOST token TOKEN
composer require VENDOR/NAME
```

`HOST` in `http-basic.HOST` is the host name, followed by `:PORT` when the repository URL names a port (Composer matches credentials on both). The same credentials can live in `auth.json`:

```json
{
  "http-basic": {
    "HOST": {"username": "token", "password": "TOKEN"}
  }
}
```

Composer refuses plain `http` repositories unless you run `composer config secure-http false`; prefer HTTPS. The web UI adds that line for a plain-http server.

## Publish and delete

Upload the dist zip, the package's `composer.json` at the archive root or in its single top-level directory (the layout `git archive` and `composer archive` produce), with a raw `PUT` body:

```sh
curl -T vendor-name-1.0.0.zip -H "Authorization: Bearer TOKEN" \
  https://HOST/repos/REPO/packages/upload/VENDOR/NAME/1.0.0
```

`Authorization: Basic` with the token as the password works too. Delete a version with `DELETE` on the same path:

```sh
curl -X DELETE -H "Authorization: Bearer TOKEN" \
  https://HOST/repos/REPO/packages/upload/VENDOR/NAME/1.0.0
```

The CLI publishes through the same endpoint: `oa upload REPO vendor-name-1.0.0.zip --version 1.0.0` ([CLI](../integrations/cli.md)). `vendor/name` is read from the zip's `composer.json`; the version from its `version` field, or from `--version` (which wins) when there is none, as is usual for a package that is versioned by its VCS tags. The web UI's upload zone shows the publish command instead of a file picker. Only the raw `PUT` is supported, not a multipart form.

## Tokens and RBAC

Requests under `/repos/REPO/` map to `read` (GET and HEAD) and `write` (PUT and DELETE) on the repository; see [RBAC](../security/rbac.md).

## API endpoints

All paths are relative to `HOST/repos/REPO`.

| Method and path | Purpose |
|---|---|
| `GET /packages.json` | Repository root: `metadata-url`, `available-packages` (every package of the repository), `list` and `search` |
| `GET /p2/VENDOR/NAME.json` | Stable versions of a package |
| `GET /p2/VENDOR/NAME~dev.json` | Dev versions of a package (`dev-*` and `*-dev`) |
| `GET /list.json?filter=` | Package names, optionally filtered by a `*` pattern |
| `GET /search.json?q=` | Name and description search |
| `PUT /packages/upload/VENDOR/NAME/VERSION` | Publish a version (raw zip body) |
| `DELETE /packages/upload/VENDOR/NAME/VERSION` | Delete a version (moved to the trash) |
| `GET /dists/VENDOR/NAME/VERSION/REF.zip` | Download; `REF` is the SHA-256 of the zip and VERSION is path-escaped |

## Versions

Versions are normalised like Composer's `VersionParser`: `v1.2.3` becomes `1.2.3.0`, a branch such as `dev-main` stays as it is and `1.x-dev` becomes `1.9999999.9999999.9999999-dev`. The version as published is kept for display and for the `version` field of the metadata; the normalised form is the identity, so `1.0.0` and `v1.0` are the same version. The metadata is split by stability: `p2/VENDOR/NAME.json` lists the stable and pre-release versions, `p2/VENDOR/NAME~dev.json` the dev versions, as Composer expects.

## Validation on publish

A rejected upload answers `400` (or `409` for a duplicate) and nothing is stored.

- **`composer.json`** must be present, valid JSON, and its `name` must equal the `VENDOR/NAME` of the URL (case-insensitive; stored lowercase).
- **Version** in the URL must be a valid Composer version. A `version` field in `composer.json` must normalise to the same version as the URL's.
- **Archive limits:** only `composer.json` is read from the zip (at most 4 MiB, nothing is extracted), and an archive with an unreasonable number of entries is refused.
- **Size limit** is 500 MiB; larger uploads answer `413`.
- **License policy and quota** apply: the `license` of `composer.json` (a string or a list of SPDX ids) is checked against the repository's license policy.
- **Versions are immutable.** Publishing a version that exists answers `409`; delete it first to replace it.

## Dist URL layout

Metadata points each version at `HOST/repos/REPO/dists/VENDOR/NAME/VERSION/REF.zip` with `type: zip` and `shasum` (SHA-1) set, so Composer verifies the download. Every link is an absolute URL built from the server's configured `baseUrl`: set it to the URL clients reach.

## Deletion and trash

`DELETE` on a version is a soft delete: the version moves to the [trash](../operations/trash.md) (kind `composer`) and can be restored until it is purged.

## Quotas

The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is reserved before a package is stored and released on every failure. Each package version is one artifact.

## Not supported yet

Proxy repositories (mirroring Packagist or another Composer repository) and group repositories are planned as a follow-up; today only hosted repositories exist. Creating a proxy or group repository of this format answers `400 validation_failed`.
