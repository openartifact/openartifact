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
| Repository types | hosted only ([details](../repositories/hosted-proxy-group.md)) |

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
- `dist.tarball` is `BASE_URL/repos/REPO/<pkg>/-/<tarball>`: set `BASE_URL` to the URL clients use, or installs download from the wrong host.
- Tarball names are `<unscoped-name>-<version>.tgz`.
- There is no search or login endpoint; configure the token as shown above rather than `npm login`.

## Signing

npm has no repository signature; integrity is the SHA-512 `dist.integrity` plus SHA-1 `shasum` computed at publish. GPG signing covers only Debian and RPM ([signing](../security/signing.md)).
