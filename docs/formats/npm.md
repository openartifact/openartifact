<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# npm

Hosted npm registry for `npm`, `pnpm` and `yarn`: publish, install, dist-tags, scoped packages and unpublish.

| | |
|---|---|
| Format id | `npm` |
| Registry URL | `HOST/repos/REPO/` |
| Repository types | hosted, proxy, group ([details](../repositories/hosted-proxy-group.md)) |

## Client setup

`internal/clientsetup` produces these commands. `HOSTPORT` is the host (with port) of `HOST`, for example `localhost:8080`.

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

Scope a single namespace to this registry instead of replacing the default: `npm config set @myscope:registry HOST/repos/REPO/`.

## Publish and unpublish

Publishing uses the npm wire protocol: `PUT /repos/REPO/<package>` with the packument JSON and a base64 `_attachments` tarball. Scoped names may be sent as `@scope%2Fname`.

- Publishing requires an authenticated principal; anonymous callers get `401` even on a public repository.
- Checked before anything is stored: valid package name (max 214 characters), `name` in the body equals the URL, each version is valid semver, manifest `name` and `version` match, the tarball attachment is present (key `<name>-<version>.tgz` or the bare file name), the optional `dist.integrity` matches the tarball, every `dist-tags` entry points at a version in the request or an existing one, and tag names are not valid semver versions. Failures are `400`.
- A version that already exists is `409 {"error": "version already exists"}`. Versions are immutable.
- Request body limit 200 MiB (`413 payload too large`); the whole body is held in memory. Quota is enforced ([quotas](../repositories/cleanup-and-quotas.md)).
- A failed publish rolls back the versions and package it created.

Unpublish:

- `npm unpublish pkg@ver` sends `PUT /<package>/-rev/<rev>` with the reduced packument (versions missing from it are removed; the package is deleted if none remain), then `DELETE /<package>/-/<file>.tgz/-rev/<rev>`, which is a no-op once the version is gone.
- `npm unpublish pkg` sends `DELETE /<package>/-rev/<rev>`, removing every version and the package.
- Versions older than 72 hours cannot be unpublished: `403 cannot unpublish after 72h, use npm deprecate instead`. The per-repository override is the `repositories.allow_full_unpublish` database column; no API or UI field sets it.
- `npm deprecate` works: a `PUT` that carries existing versions without `_attachments` updates only their `deprecated` field (an empty message un-deprecates). New versions still need their attachment.
- Package names that collide with other formats' routes (`charts`, `index.yaml`, `config.json`, `public.key`) are served as npm packages in an npm repository.

Dist-tags (`npm dist-tag`):

| Request | Behaviour |
|---|---|
| `GET /-/package/<pkg>/dist-tags` | `{tag: version}` |
| `PUT /-/package/<pkg>/dist-tags/<tag>` | body is the version as a JSON string (max 1 KiB); `404` if the version does not exist; the packument ETag changes immediately |
| `DELETE /-/package/<pkg>/dist-tags/<tag>` | removes the tag; `latest` cannot be deleted (`400`) |

## Read and generated metadata

| Request | Response |
|---|---|
| `GET /<pkg>` | packument, built from the stored versions and dist-tags |
| `GET /<pkg>` with `Accept: application/vnd.npm.install-v1+json` | abbreviated packument |
| `GET /<pkg>/<version-or-tag>` | the stored manifest of one version |
| `GET /<pkg>/-/<file>.tgz`, `GET /@scope/<pkg>/-/<file>.tgz` | tarball |

- The packument carries `ETag` (`If-None-Match` answers `304`), `Vary: Accept` and `Cache-Control: public, max-age=60`.
- `dist.tarball` is an absolute URL `ORIGIN/repos/REPO/<pkg>/-/<tarball>`, because npm stores it verbatim as `resolved`. `ORIGIN` is derived from the request (`X-Forwarded-Host` and `X-Forwarded-Proto` are believed only from a peer in `OPENARTIFACT_TRUSTED_PROXIES`, and the proxy must forward a non-default port in the host); an explicit `BASE_URL` always wins. The packument sends `Vary: Host, X-Forwarded-Host, X-Forwarded-Proto` and its `ETag` carries an origin tag (`"<nanos>-<origin tag>"`, abbreviated `"abbr-<nanos>-<origin tag>"`), so a `304` for one host never serves another.
- Tarball names are `<unscoped-name>-<version>.tgz`.
- There is no search or login endpoint; configure the token as shown above rather than `npm login`.

## Proxy and group repositories

- **Proxy:** mirrors an upstream registry (for example `https://registry.npmjs.org`): `npm install` against `HOST/repos/PROXY/` fetches and caches packuments (with the repository TTL, served stale when the upstream is down) and immutable, integrity-verified tarballs; `dist.tarball` is rewritten to the proxy. Publishing to a proxy is `405`. See [npm proxies](../repositories/hosted-proxy-group.md#npm-proxies).
- **Minimum age:** with `min_age_hours` set on a proxy, a version whose `time[version]` in the upstream packument is younger than that is left out of what the proxy serves: its `versions` and `time` entries are removed (`created` and `modified` stay), a `latest` dist-tag that pointed at it moves to the highest remaining version that is not a prerelease (the highest remaining one when only prereleases are left), any other dist-tag that pointed at it is dropped, and `GET /<pkg>/<version>` for it is `404`. A package whose versions are all too new is `404`. Its tarball is refused with `403` unless it is already cached. A version the packument gives no `time` for is not held back. The abbreviated packument (`application/vnd.npm.install-v1+json`) carries no publish times, so while the gate is on an abbreviated request is answered from the full packument (`application/json`) and the abbreviated document is not used. A group member that is a proxy contributes the already filtered packument.
- **Curation:** a proxy repository can also hold back packages and versions by administrator rules, see [package curation](../repositories/hosted-proxy-group.md#package-curation). Rules name the package as the registry does, lower-cased with its scope (`@scope/name`), and the version as the packument shows it. A matching version is left out of the packument like a too-young one (with no `time` needed; dist-tags move the same way), and its tarball answers `403` with the reason even when it is already cached. A matching allow rule also exempts a version from the minimum age. The abbreviated packument is answered from the full one while curation is on.
- **Group:** an ordered list of hosted and proxy npm members behind one registry URL. Packuments are merged (versions united, first member wins on a version conflict, each dist-tag from the first member that has it), tarball URLs point at the group, tarballs come from the first member that has them. Publishing to a group is `405`: publish to a hosted member and read through the group. See [group repositories](../repositories/hosted-proxy-group.md#group-repositories).

```sh
npm config set registry HOST/repos/PROXY_OR_GROUP/
npm config set //HOSTPORT/repos/PROXY_OR_GROUP/:_authToken <TOKEN>
npm install lodash @babel/core
```

## Signing

npm has no repository signature; integrity is the SHA-512 `dist.integrity` plus SHA-1 `shasum` computed at publish. GPG signing covers only Debian and RPM ([signing](../security/signing.md)).
