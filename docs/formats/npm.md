<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# npm

Use an npm registry to publish, install, tag and unpublish packages with `npm`, `pnpm` and `yarn`, including scoped packages. It can be hosted, a proxy of an upstream registry, or a group of both.

| Item | Value |
|---|---|
| Registry URL | `HOST/repos/REPO/` |
| Format id | `npm` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | none: set `upstream_url`, for example `https://registry.npmjs.org` |

## Client setup

The web UI connect panel shows the same commands with your own URL. `HOSTPORT` is the host with port of `HOST`, for example `localhost:8080`.

```sh
# npm
npm config set registry HOST/repos/REPO/
npm config set //HOSTPORT/repos/REPO/:_authToken <TOKEN>
npm publish --registry HOST/repos/REPO/

# pnpm
pnpm config set registry HOST/repos/REPO/
pnpm config set //HOSTPORT/repos/REPO/:_authToken <TOKEN>
pnpm publish --registry HOST/repos/REPO/

# yarn (berry)
yarn config set npmRegistryServer HOST/repos/REPO/
yarn config set npmAuthToken <TOKEN>
yarn npm publish
```

To send only one namespace to this registry, keep the default registry and scope it:

```sh
npm config set @myscope:registry HOST/repos/REPO/
```

There is no login endpoint. Configure the token as shown instead of running `npm login`.

## Publish

Publishing uses the npm wire protocol: `PUT /repos/REPO/<package>` with the package metadata JSON and a base64 `_attachments` tarball. Scoped names may be sent as `@scope%2Fname`. The metadata JSON is called a packument.

- Publishing needs an authenticated principal. Anonymous callers get `401`, even on a public repository.
- A version that already exists is `409 {"error": "version already exists"}`. Versions are immutable.
- A failed publish rolls back the versions and package it created.

Checks before anything is stored (failures are `400`):

- The package name is valid and at most 214 characters.
- `name` in the body equals the URL.
- Each version is valid semver, and the manifest `name` and `version` match.
- The tarball attachment is present, keyed `<name>-<version>.tgz` or by the bare file name.
- `dist.integrity`, when sent, matches the tarball.
- Every `dist-tags` entry points at a version in the request or an existing one.
- Tag names are not valid semver versions.

Limits: the request body is at most 200 MiB (`413 payload too large`) and is held in memory. The [storage quota](../repositories/cleanup-and-quotas.md) is enforced.

### Unpublish and deprecate

- `npm unpublish pkg@ver` removes that version. The package is deleted if no version remains.
- `npm unpublish pkg` removes every version and the package.
- Versions older than 72 hours cannot be unpublished: `403 cannot unpublish after 72h, use npm deprecate instead`. An operator can lift this per repository, but no API or UI setting for it exists yet.
- `npm deprecate` works. A `PUT` that carries existing versions without `_attachments` only updates their `deprecated` field. An empty message removes the deprecation.

### Dist-tags

| Request | Behaviour |
|---|---|
| `GET /-/package/<pkg>/dist-tags` | returns `{tag: version}` |
| `PUT /-/package/<pkg>/dist-tags/<tag>` | body is the version as a JSON string (max 1 KiB); `404` if the version does not exist |
| `DELETE /-/package/<pkg>/dist-tags/<tag>` | removes the tag; `latest` cannot be deleted (`400`) |

## Install and consume

| Request | Response |
|---|---|
| `GET /<pkg>` | the packument, built from the stored versions and dist-tags |
| `GET /<pkg>` with `Accept: application/vnd.npm.install-v1+json` | the abbreviated packument |
| `GET /<pkg>/<version-or-tag>` | the stored manifest of one version |
| `GET /<pkg>/-/<file>.tgz`, `GET /@scope/<pkg>/-/<file>.tgz` | the tarball |

- Tarball names are `<unscoped-name>-<version>.tgz`.
- `dist.tarball` is an absolute URL `ORIGIN/repos/REPO/<pkg>/-/<tarball>`, because npm stores it as `resolved`.
- `ORIGIN` comes from the request. `X-Forwarded-Host` and `X-Forwarded-Proto` are trusted only from a peer in `OPENARTIFACT_TRUSTED_PROXIES`. A reverse proxy must forward a non-default port in the host.
- An explicit `BASE_URL` always wins over the request origin.
- Package names that collide with other formats' routes (`charts`, `index.yaml`, `config.json`, `public.key`) are served as npm packages in an npm repository.
- There is no search endpoint.

## Proxy

Create a proxy with `repo_type: "proxy"` and the upstream registry as `upstream_url`:

```sh
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' \
  -d '{"name":"npmjs","format":"npm","repo_type":"proxy","upstream_url":"https://registry.npmjs.org"}'
```

Point clients at `HOST/repos/PROXY/` as shown in [Client setup](#client-setup). A proxy is a registry, not a path mirror.

- **Packuments** are fetched from the upstream and cached with `cache_ttl_seconds`. They are served stale when the upstream is down, for up to `max_stale_seconds` past expiry (`Warning: 110`).
- The abbreviated and the full packument are cached separately. Every `dist.tarball` is rewritten at serve time to the proxy. All other fields, including `dist.integrity` and `dist.shasum`, are untouched.
- **Tarballs** are fetched from the URL the packument names for that file. A file no version lists is `404` and never fetched.
- A tarball is checked against `dist.integrity` (the strongest algorithm present) or else `dist.shasum`, then cached. A mismatch is `502` and nothing is stored.
- A verified tarball is immutable. A version that advertises no digest is accepted but cached for the repository TTL only.
- Upstream credentials go only to the configured upstream host, never to another host a packument points at.
- With the upstream down, cached tarballs are still served. A package never requested before answers `502`.
- `npm publish`, `unpublish`, `deprecate` and `dist-tag` against a proxy answer `405` (`Allow: GET, HEAD`). Search, audit and login are not proxied.

### Minimum age and curation

The general rules are in [minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [package curation](../repositories/hosted-proxy-group.md#package-curation). The npm specifics:

- **Spelling:** rules name the package lower-cased with its scope (`@scope/name`) and the version as the packument shows it.
- **Age source:** the version's `time[version]` in the upstream packument. A version with no `time` is not held back.
- **Hidden version:** it is removed from `versions` and `time` (`created` and `modified` stay), and `GET /<pkg>/<version>` is `404`. Its tarball answers `403` with the reason, unless it is already cached and only the minimum age holds it back. Curation blocks cached tarballs too.
- **Dist-tags:** a `latest` tag that pointed at it moves to the highest remaining non-prerelease version, or the highest remaining version if only prereleases are left. Other tags that pointed at it are dropped.
- **No version left:** the package is `404`.
- **Abbreviated requests:** the abbreviated packument has no publish times. While minimum age or curation is active, the server answers an abbreviated request from the full packument. Clients read it the same way.

## Group

A group is an ordered list of hosted and proxy npm members behind one registry URL.

- Hosted members win a package name first. Among the remaining members the first one wins a version. See [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).
- With `merge_proxy_versions` set to `true`, versions of a name held by a hosted member are also merged in from proxy members.
- Packuments are merged: versions are united, and each dist-tag comes from the first member that has it.
- Tarball URLs point at the group. Tarballs come from the first member that has them.
- A proxy member contributes its already filtered packument, so minimum age and curation apply through the group.
- Publishing to a group is `405`. Publish to a hosted member and read through the group.

Clients use `HOST/repos/GROUP/` like any other npm registry:

```sh
npm config set registry HOST/repos/GROUP/
npm config set //HOSTPORT/repos/GROUP/:_authToken <TOKEN>
npm install lodash @babel/core
```

A merge that left out a failed member is served with `Cache-Control: no-store` and no `ETag`.

## Limits and behaviour

- The packument carries an `ETag`; `If-None-Match` answers `304`. It also sends `Vary: Accept`, and `Vary: Host, X-Forwarded-Host, X-Forwarded-Proto` because tarball URLs depend on the origin. A `304` for one host never serves another.
- `Cache-Control` on a packument is `public, max-age=60` for anonymous reads of a public repository and `private, no-cache` otherwise.
- npm has no repository signature. Integrity is the SHA-512 `dist.integrity` plus the SHA-1 `shasum` computed at publish. GPG signing covers only Debian and RPM ([signing](../security/signing.md)).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `401` on publish | anonymous call, or no token configured | set `_authToken` for the registry host and path |
| `409 version already exists` | versions are immutable | bump the version |
| `400` on publish | name, version, tag or integrity check failed | compare the body with the checks under [Publish](#publish) |
| `413 payload too large` | body over 200 MiB | publish a smaller package |
| `403 cannot unpublish after 72h` | the version is older than 72 hours | use `npm deprecate` |
| `405` on publish | the repository is a proxy or a group | publish to a hosted repository |
| `502` on a tarball through a proxy | the download did not match its digest | retry, then check the upstream |
| `403` on a tarball | a minimum age or curation rule holds it back | see [Minimum age and curation](#minimum-age-and-curation) |
| `dist.tarball` points at the wrong host | the reverse proxy headers are not trusted | add the proxy to `OPENARTIFACT_TRUSTED_PROXIES` or set `BASE_URL` |
