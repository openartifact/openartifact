<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Composer (PHP)

Use a Composer repository as a private PHP package registry: Composer installs from it through `packages.json` and the Composer repository v2 metadata (`p2/`), and you publish packages as dist zips. A proxy mirrors `repo.packagist.org` (or any Composer repository v2), and a group merges hosted and proxy repositories.

| Item | Value |
|---|---|
| Repository URL | `HOST/repos/REPO` (Composer reads `HOST/repos/REPO/packages.json`) |
| Format id | `composer` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | `https://repo.packagist.org` |

A repository stores one dist zip per package version. A package is identified by its lower-case `vendor/name`, taken from the `name` field of the `composer.json` inside the zip. Permissions, quota, [trash](../operations/trash.md), webhooks and [cleanup](../repositories/cleanup-and-quotas.md) work as for any other format.

Scope:

- Composer repository v2: `packages.json`, `p2/VENDOR/NAME.json` (stable versions), `p2/VENDOR/NAME~dev.json` (dev versions), `list.json` and `search.json`.
- Dist only. Every version has a `dist` of type `zip`; there is no `source` (VCS) entry.
- Not served: `security-advisories` and `providers-api`. The legacy Composer 1 `provider-includes` layout is not proxied.

## Client setup

Create an API token as described in [authentication](../security/authentication.md). Composer sends credentials as HTTP Basic with the token as the password. The server does not check the user name, so use `token`. Read access installs, write access publishes. A public repository, or a server with `allowAnonymousRead`, is read without credentials. The web UI connect panel shows the same commands with your own URL.

`composer.json`:

```json
{
  "repositories": [
    {"type": "composer", "url": "https://HOST/repos/REPO"}
  ]
}
```

From the command line:

```sh
composer config repositories.openartifact composer https://HOST/repos/REPO
composer config http-basic.HOST token <TOKEN>
composer require VENDOR/NAME
```

`HOST` in `http-basic.HOST` is the host name, followed by `:PORT` when the repository URL names a port. Composer matches credentials on both. The same credentials can live in `auth.json`:

```json
{
  "http-basic": {
    "HOST": {"username": "token", "password": "<TOKEN>"}
  }
}
```

Composer refuses plain `http` repositories unless you run `composer config secure-http false`. Prefer HTTPS. The web UI adds that line for a plain-http server.

Requests under `/repos/REPO/` map to `read` (GET and HEAD) and `write` (PUT and DELETE) on the repository; see [RBAC](../security/rbac.md).

## Publish

Upload the dist zip with a raw `PUT` body. The zip holds the package's `composer.json` at the archive root or in its single top-level directory, the layout that `git archive` and `composer archive` produce.

```sh
curl -T vendor-name-1.0.0.zip -H "Authorization: Bearer <TOKEN>" \
  https://HOST/repos/REPO/packages/upload/VENDOR/NAME/1.0.0
```

`Authorization: Basic` with the token as the password works too. Only the raw `PUT` is supported, not a multipart form.

Other ways to publish:

- CLI: `oa upload REPO vendor-name-1.0.0.zip --version 1.0.0` ([CLI](../integrations/cli.md)). The CLI reads `vendor/name` from the zip's `composer.json`. It reads the version from the `version` field, or from `--version`, which wins. A package versioned by VCS tags has no `version` field.
- Web UI: the upload zone asks for `vendor/name` and the version, then sends the zip with the same raw `PUT`.

Delete a version with `DELETE` on the same path:

```sh
curl -X DELETE -H "Authorization: Bearer <TOKEN>" \
  https://HOST/repos/REPO/packages/upload/VENDOR/NAME/1.0.0
```

A rejected upload answers `400` (or `409` for a duplicate) and nothing is stored.

- `composer.json` is present and valid JSON. Its `name` equals the `VENDOR/NAME` of the URL (case-insensitive, stored lower case).
- The version in the URL is a valid Composer version. A `version` field in `composer.json` normalises to the same version as the URL's.
- The server reads only `composer.json` from the zip, at most 4 MiB, and extracts nothing. An archive with an unreasonable number of entries is refused.
- The `license` of `composer.json` (a string or a list of SPDX ids) is checked against the repository's license policy.
- Versions are immutable. Publishing an existing version answers `409`; delete it first to replace it.

The zip is limited to 500 MiB; a larger upload answers `413`.

### Versions

Versions are normalised like Composer's `VersionParser`. `v1.2.3` becomes `1.2.3.0`. A branch such as `dev-main` stays as it is. `1.x-dev` becomes `1.9999999.9999999.9999999-dev`. The server keeps the version as published for display and for the `version` field of the metadata. The normalised form is the identity, so `1.0.0` and `v1.0` are the same version.

The metadata is split by stability, as Composer expects. `p2/VENDOR/NAME.json` lists stable and pre-release versions. `p2/VENDOR/NAME~dev.json` lists dev versions.

## Install and consume

```sh
composer require VENDOR/NAME
```

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

Metadata points each version at `HOST/repos/REPO/dists/VENDOR/NAME/VERSION/REF.zip` with `type: zip` and the SHA-1 `shasum` set, so Composer verifies the download.

The version `dist.url`, the publish response URL and the search hit URL are absolute URLs on the request origin, because Composer does not resolve a relative `dist.url`. An explicit `BASE_URL` always wins. The root fields of `packages.json` (`metadata-url`, `list`, `search`) are path-absolute (`/repos/REPO/...`), which Composer resolves against the repository origin. Under a path prefix, set `BASE_URL`: the server uses only its path for those fields.

<a id="proxy-repositories"></a>

## Proxy

Create a proxy with `repo_type: "proxy"`. Without `upstream_url` the default is `https://repo.packagist.org`. For a private Packagist, Satis or Private Packagist, set `upstream_url` to an instance that serves `p2/`. Clients use the same `HOST/repos/REPO` URL and `composer.json` as for a hosted repository. A proxy is read-only, so there is no publish. To use it instead of Packagist, add `"packagist.org": false` to the project's `repositories`.

A proxy and a group serve `packages.json` and `p2/` only. `list.json`, `search.json` and the upload routes answer `404`.

- **`packages.json`** is generated by OpenArtifact. It names the proxy's own `metadata-url` and nothing else. It has no `available-packages` because an upstream cannot be enumerated. Packagist serves `list.json` and `search.json` from another host than its metadata, so those answer `404` here.
- **Metadata** is fetched from the upstream and cached unchanged for the repository TTL. The server serves stale entries when the upstream is down. On each request it rewrites the `dist.url` of every version to `HOST/repos/REPO/dists/VENDOR/NAME/VERSION/REF.zip`. `source`, `dist.reference`, `dist.shasum` and everything else stay as the upstream published them, so Composer still verifies `shasum` end to end.
- **Metadata caching.** The server serves metadata with an `ETag` and answers `304` to `If-None-Match`. The server builds the rewritten `dist.url` from the request origin (or `BASE_URL`) when it answers and never stores it. The `ETag` of the `packages.json` root depends only on the path prefix and the cached document.
- **Dists.** The server fetches a dist from the URL the cached metadata publishes for that version, never from a URL the client sent. `REF` in the path identifies that URL and the dist reference. A path the metadata does not publish answers `404`.
- **Dist URLs.** A dist URL must pass the same outbound check as webhooks: no loopback, private or link-local addresses. The server follows redirects (GitHub's `zipball` redirects to `codeload.github.com`) and checks every hop.
- **Dist cache.** The zip is cached once, however many versions or repositories point at it.
- **Dist check.** When the upstream publishes a non-empty `shasum`, the download must match that SHA-1, or the server refuses it with `502` and does not cache it. Packagist publishes none for most packages; the commit `reference` vouches for the dist there.
- **Dist freshness.** Dists of released versions never change. Dists of branches (`dev-main`, `1.x-dev`) follow the repository TTL because a branch moves.
- **Quarantine** applies to cached dists.
- **Credentials.** `upstreamUsername` and `upstreamPassword` go to the origin of `upstreamUrl` only. They never go to the host a dist is downloaded from (`api.github.com`, `codeload.github.com`, GitLab, Bitbucket). A private Packagist keeps its dists on its own origin and works. Dists of private GitHub repositories are not reachable through a proxy.
- **Dists that are not zips** (`tar`, `rar`, `gzip`) lose their `dist`, so Composer installs that version from its `source` (`git clone`).
- **Unknown packages.** A package or version the upstream does not list answers `404`. The server refuses a single document larger than 64 MiB.

After the first install fills the cache, later installs are served from it, including with the upstream unreachable. Expired metadata is served stale for `maxStaleSeconds`; see [cache TTLs](../repositories/hosted-proxy-group.md#cache-ttls).

### Minimum age and curation

Rules and `min_age_hours` follow [Minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [Package curation](../repositories/hosted-proxy-group.md#package-curation). The format-specific parts:

- **Names.** Rules name a package as lower-case `vendor/name` and a version as the p2 document shows it (`1.2.3`, `v1.2.3`, `dev-main`).
- **Minimum age date.** The server uses `time` of the version in the p2 document. A version that only inherits its `time` from the previous entry of the upstream's compact document is dated by the inherited value. A version without a `time` is not held back.
- **Dev versions.** They are dated by `time` too. A branch with a recent commit stays hidden until that commit is old enough.
- **Hidden version.** It leaves the p2 document. Its dist answers `403` with the reason. A cached dist is still served when only the minimum age hides it. Curation refuses it even when cached.
- **Hidden package.** A package whose versions are all hidden answers `404`, as a package the upstream does not list.
- **Allow rules.** A matching allow rule exempts a version from the minimum age.
- **Freshness.** The `ETag` of a rendered document changes with the threshold, and with the clock every 5 minutes. A version that has aged enough appears without a stale `304`.

<a id="group-repositories"></a>

## Group

A group merges hosted and proxy `composer` repositories. It is served at `HOST/repos/GROUP`, with links to itself.

- **`packages.json`** is the group's own, with its `metadata-url`. It lists `available-packages` (the union of the members' lists, sorted) only when every member is hosted. Composer treats that list as complete, which a proxy cannot guarantee.
- **Versions** of a package are merged across the members in order, keyed by normalised version. An earlier member wins a duplicate version, so a private `1.0.0` is never replaced by a public one.
- **Stable and dev documents** (`NAME.json` and `NAME~dev.json`) are merged separately, because Composer asks for them separately.
- **Dists** are linked to the group. A download is asked of the members in order. Only the member that published that version has a dist with that reference, so the member whose version was listed serves it. `source` blocks stay as the member published them.
- **Hosted members win a package name.** If a readable hosted member holds a package (stable or dev versions), only hosted members serve it, so a public package cannot shadow a private one. A hosted package with only dev versions hides the proxy's stable versions too. Setting `merge_proxy_versions` to `true` turns this off. See [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).
- **Failing members.** A member that fails while another contributes is ignored. The server then serves the partial document with `Cache-Control: no-store` and no `ETag`. When only failing members could have the package, the group answers `502`, not `404`.
- **Minimum age and curation.** A group reads the proxy's rendering, so it does not list or serve hidden versions.
- **Not merged.** `list.json` and `search.json` answer `404`.
- A group is read-only. Quota, publish and delete apply to hosted repositories only.

## Limits and behaviour

- **Zip size:** 500 MiB (`413` above).
- **`composer.json` inside the zip:** at most 4 MiB.
- **Caching:** metadata is served with `Cache-Control: public, max-age=60` for anonymous reads of a public repository, `private, no-cache` otherwise. Dists use the same rule with `max-age=3600`.
- **Delete:** `DELETE` on a version is a soft delete. The version moves to the [trash](../operations/trash.md) (kind `composer`) and can be restored until it is purged.
- **Quota:** the [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is reserved before a package is stored and released on every failure. Each package version is one artifact.
- **Status codes:** `400` invalid upload, `409` existing version, `413` too large.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `409` on publish | The version exists. | Delete it first, or publish a new version. |
| `400` on publish | `composer.json` is missing, its `name` differs from the URL, or the version is invalid. | Check the zip layout and the URL. |
| Composer refuses the repository | The URL is plain `http`. | Use HTTPS or run `composer config secure-http false`. |
| `401` | The token is missing. | Set `http-basic` with user `token` and your token. |
| `404` for `list.json` or `search.json` | The repository is a proxy or group. | Use `composer require` or `composer show` instead. |
| `403` for a dist on a proxy | Minimum age or curation hides the version. | Wait until it is old enough, or change the rules. |
| `502` for a dist on a proxy | The download did not match the upstream `shasum`. | Retry; if it persists the upstream serves a different file. |
