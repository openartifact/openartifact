<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Composer (PHP)

Private Composer repository: Composer installs from an OpenArtifact repository through `packages.json` and the Composer repository v2 metadata (`p2/`), and packages are published as dist zips. Proxy repositories mirror `repo.packagist.org` (or any Composer repository v2), and group repositories merge hosted and proxy repositories.

| | |
|---|---|
| Format id | `composer` |
| Repository URL | `HOST/repos/REPO` (Composer reads `HOST/repos/REPO/packages.json`) |
| Repository types | hosted, proxy (default upstream `https://repo.packagist.org`) and group |

## Overview

A repository of format `composer` stores one dist zip per package version. A package is identified by its lowercase `vendor/name`, taken from the `name` field of the `composer.json` inside the zip. Permissions, quota, [trash](../operations/trash.md), webhooks and [cleanup](../repositories/cleanup-and-quotas.md) apply as for any other format. Create the repository in the web UI (format **Composer**) or with `format: composer` through the API or CLI, see [repositories](../repositories/hosted-proxy-group.md).

## Supported scope

- **Composer repository v2:** `packages.json`, `p2/VENDOR/NAME.json` (stable versions) and `p2/VENDOR/NAME~dev.json` (dev versions), `list.json` and `search.json`.
- **Dist only.** Every version has a `dist` of type `zip`; there is no `source` (VCS) entry.
- **Proxy and group repositories** are described [below](#proxy-repositories). A proxy and a group serve `packages.json` and `p2/` only: `list.json`, `search.json` and the upload routes answer `404`.
- **Out of scope:** `security-advisories` and `providers-api` are not served, and the legacy Composer 1 `provider-includes` layout is not proxied.

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

Metadata points each version at `HOST/repos/REPO/dists/VENDOR/NAME/VERSION/REF.zip` with `type: zip` and `shasum` (SHA-1) set, so Composer verifies the download. The version `dist.url`, the publish response url and the search hit url are absolute URLs on the request origin (Composer does not resolve a relative `dist.url`); an explicit `BASE_URL` always wins. The root fields of `packages.json` (`metadata-url`, `list`, `search`) are path-absolute (`/repos/REPO/...`), which Composer resolves against the repository origin. Under a path prefix, set `BASE_URL`: only its path is used for those fields.

## Deletion and trash

`DELETE` on a version is a soft delete: the version moves to the [trash](../operations/trash.md) (kind `composer`) and can be restored until it is purged.

## Quotas

The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is reserved before a package is stored and released on every failure. Each package version is one artifact.

## Proxy repositories

A proxy repository mirrors a Composer repository v2 (default `https://repo.packagist.org`; set `upstreamUrl` for a private Packagist, Satis or Private Packagist instance that serves `p2/`). Clients use the same `HOST/repos/REPO` URL and `composer.json` as for a hosted repository; a proxy is read-only, so there is no publish. To use it instead of Packagist, add `"packagist.org": false` to the project's `repositories`.

- **`packages.json`** is generated by OpenArtifact: it names the proxy's own `metadata-url` and nothing else. It has no `available-packages` (an upstream cannot be enumerated), and `list.json` and `search.json` answer `404`: Packagist serves them from another host than its metadata.
- **Metadata** (`p2/VENDOR/NAME.json`, `p2/VENDOR/NAME~dev.json`) is fetched from the upstream and cached verbatim with the repository's TTL; stale entries are served when the upstream is down, as for other proxies. Each request renders the cached document: it is expanded from Composer's minified form, the `dist.url` of every version is rewritten to `HOST/repos/REPO/dists/VENDOR/NAME/VERSION/REF.zip` and it is minified again. `source`, `dist.reference`, `dist.shasum` and everything else stay as the upstream published them, so Composer still verifies `shasum` end to end. Metadata is served with an `ETag` and answers `304` to `If-None-Match`; the `ETag` of the `packages.json` root depends only on the blob key and the path prefix. The rewritten `dist.url` is built at serve time from the request origin (or `BASE_URL`), never stored in the cache. Object keys inside a version are emitted in sorted order; arrays keep theirs.
- **Dists.** A dist is fetched from the URL the cached metadata publishes for that version, never from a URL the client sent: `REF` in the path is a digest of that URL and the dist reference, so a path the metadata does not publish answers `404`. The URL passes the same outbound validation as webhooks (no loopback, private or link-local addresses), and redirects (GitHub's `zipball` redirects to `codeload.github.com`) are followed only through the SSRF-safe client and are validated at every hop. The zip is cached content-addressed, once however many versions or repositories point at it. When the upstream publishes a non-empty `shasum`, the download must match that SHA-1 or it is refused with `502` and not cached (Packagist publishes none for most packages, where the commit `reference` vouches for the dist). Dists of released versions are immutable; dists of branches (`dev-main`, `1.x-dev`) follow the repository TTL because a branch moves. Quarantine applies to cached dists.
- **Credentials** (`upstreamUsername`/`upstreamPassword`) are sent to the origin of `upstreamUrl` only, **never** to the host a dist is downloaded from (`api.github.com`, `codeload.github.com`, GitLab, Bitbucket and so on). A private Packagist keeps its dists on its own origin and is authenticated; dists of private GitHub repositories are not reachable through a proxy, since no token is sent to GitHub.
- **Dists that are not zips** (`tar`, `rar`, `gzip`) lose their `dist`, so Composer installs that version from its `source` (`git clone`).
- A package or version the upstream does not list answers `404`. Responses larger than 64 MiB for one document are refused.

`composer require` through a proxy: the first install fills the cache; later installs, including with the upstream unreachable, are served from it (expired metadata is served stale for `maxStaleSeconds`, see [cache TTLs](../repositories/hosted-proxy-group.md#cache-ttls)).

## Group repositories

A group merges hosted and proxy repositories of format `composer`, served at its own `HOST/repos/GROUP` with links to itself.

- **`packages.json`** is the group's own, with its `metadata-url`. It lists `available-packages` (the union of the members' lists, sorted) only when every member is hosted: Composer treats that list as complete, which a proxy cannot guarantee.
- **Versions** of a package are merged across the members in order, keyed by normalized version: an earlier member wins a duplicate version, so a private `1.0.0` is never replaced by a public one. Stable and `~dev` documents are merged separately, as Composer asks for them separately.
- **Dists** are linked to the group. A download is asked of the members in order and only the member that published that version has a dist with that reference, so it is served by the member whose version was listed. `source` blocks are kept as the member published them.
- **Hosted wins.** Unless the group enables merging proxy versions, a package that a readable hosted member holds (stable or dev versions) is served from the hosted members only, so a public package cannot shadow a private one (dependency confusion). See [proxy and group rules](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).
- A member failing while another contributes is ignored, but the partial document is served with `Cache-Control: no-store` and no `ETag`; when only failing members could have the package the group answers `502` (not `404`). `list.json` and `search.json` are not merged and answer `404`.
- Quota, publish and delete apply to hosted repositories only; a group is read-only.
