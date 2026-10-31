<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Ansible collections

Private Galaxy server for Ansible collections: `ansible-galaxy` publishes to and installs from an OpenArtifact repository through the Galaxy API v3. Proxy repositories mirror `galaxy.ansible.com` (or any Galaxy NG / automation hub), and group repositories merge hosted and proxy repositories.

| | |
|---|---|
| Format id | `ansible` |
| Server URL | `HOST/repos/REPO/api/` (the trailing `/api/` is part of the URL) |
| Repository types | hosted, proxy (default upstream `https://galaxy.ansible.com`) and group |

## Overview

A repository of format `ansible` stores collection tarballs named `NAMESPACE-NAME-VERSION.tar.gz`. A collection is identified by its FQCN, `NAMESPACE.NAME` (for example `community.general`). Permissions, quota, [trash](../operations/trash.md), webhooks and [cleanup](../repositories/cleanup-and-quotas.md) apply as for any other format. Create the repository in the web UI (format **Ansible**) or with `format: ansible` through the API or CLI, see [repositories](../repositories/hosted-proxy-group.md).

## Supported scope

- **Galaxy API v3, collections only.** Publishing, listing, version detail, import task state, download and delete.
- **Out of scope:** roles and the Galaxy v1/v2 APIs, collection signature generation (a hosted repository's `signatures` list is always empty), and the Galaxy namespace and content-search APIs.
- **Proxy and group repositories** are described [below](#proxy-repositories). Roles, Galaxy v1 and the legacy v2 API are not proxied.

## Client setup

`internal/clientsetup` produces the same snippets the web UI shows. Create an API token as described in [authentication](../security/authentication.md); `ansible-galaxy` sends it as `Authorization: Token <TOKEN>`, which OpenArtifact accepts exactly like `Bearer`. Read access installs, write access publishes. A public repository (or `allowAnonymousRead`) is read without a token.

`ansible.cfg`:

```ini
[galaxy]
server_list = openartifact

[galaxy_server.openartifact]
url = https://HOST/repos/REPO/api/
token = TOKEN
```

Publish a built collection (`ansible-galaxy collection build` produces the tarball):

```sh
ansible-galaxy collection publish NAMESPACE-NAME-VERSION.tar.gz --server openartifact
```

Install:

```sh
ansible-galaxy collection install NAMESPACE.NAME --server openartifact
```

`requirements.yml`:

```yaml
collections:
  - name: NAMESPACE.NAME
    version: 1.0.0
    source: https://HOST/repos/REPO/api/
```

The CLI publishes through the same endpoint: `oa upload REPO NAMESPACE-NAME-VERSION.tar.gz` ([CLI](../integrations/cli.md)). The web UI's upload zone shows the publish command instead of a file picker, because publishing is a token-authenticated multipart request driven by `ansible-galaxy`.

## Tokens and RBAC

Requests under `/repos/REPO/api/` map to `read` (GET and HEAD) and `write` (POST and DELETE) on the repository; see [RBAC](../security/rbac.md). Both `Authorization: Token TOKEN` and `Authorization: Bearer TOKEN` carry an API token.

## API endpoints

All paths are relative to `HOST/repos/REPO`. Every link in the responses (`download_url`, `href`, pagination links and the import task) is an absolute URL, which `ansible-core` 2.15 needs (it cannot resolve a root-relative `download_url`). The origin is derived from the request (scheme and `Host`; `X-Forwarded-Host` and `X-Forwarded-Proto` only from a peer in `OPENARTIFACT_TRUSTED_PROXIES`). An explicit `BASE_URL` always wins. The `/api/` discovery document keeps `"v3": "v3/"`.

| Method and path | Purpose |
|---|---|
| `GET /api/` | Discovery: `{"available_versions":{"v3":"v3/"}}` |
| `GET /api/v3/collections/` | Collection index |
| `GET /api/v3/collections/NAMESPACE/NAME/` | One collection |
| `GET /api/v3/collections/NAMESPACE/NAME/versions/?limit=&offset=` | Paginated version list (`meta.count`, `links`, `data`) |
| `GET /api/v3/collections/NAMESPACE/NAME/versions/VERSION/` | Version detail: `download_url`, `artifact.filename`, `artifact.sha256`, `artifact.size`, `metadata.dependencies` |
| `GET /api/v3/imports/collections/ID/` | State of a publish import task |
| `POST /api/v3/artifacts/collections/` | Publish: multipart form with `file` (the tarball) and `sha256`; answers `202` with `{"task": "<URL of the import task>"}` |
| `GET /api/v3/artifacts/collections/NAMESPACE-NAME-VERSION.tar.gz` | Download |
| `DELETE /api/v3/collections/NAMESPACE/NAME/versions/VERSION/` | Delete a version (`204`, moved to the trash) |

## Validation on publish

The tarball is checked before it is stored; a rejected collection answers `400` (or `409` for a duplicate) and nothing is stored.

- **File name** must be `NAMESPACE-NAME-VERSION.tar.gz` and agree with `MANIFEST.json`.
- **`MANIFEST.json`** must be present; `namespace` and `name` must match `^[a-z_][a-z0-9_]*$`.
- **Version** must be strict semantic versioning (`1.2.3`, `1.2.3-rc.1`).
- **Dependencies** in `MANIFEST.json` must be well formed (`NAMESPACE.NAME` mapped to a version requirement); they are stored and returned in the version detail.
- **`FILES.json`** checksum is verified against `MANIFEST.json`, and every file of the archive against `FILES.json`; a file that `FILES.json` does not list is refused.
- **Archive safety:** absolute or `..` paths, links and other non-regular entries are refused.
- **File part encoding:** `ansible-galaxy` sends the tarball base64-encoded (`Content-Transfer-Encoding: base64`); the server decodes it.
- **`sha256`** form field must equal the digest of the uploaded file.
- **Size limit** is 500 MiB; larger uploads are refused.
- **Versions are immutable.** Publishing a version that exists answers `409`; delete it first to replace it.

## Deletion and trash

`DELETE` on a version is a soft delete: the version moves to the [trash](../operations/trash.md) (kind `ansible`) and can be restored until it is purged. A repository with immutable releases refuses to delete released versions, see [release rules](../operations/trash.md#immutable-releases).

## Quotas

The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is reserved before a collection is stored and released on every failure. Each collection version is one artifact.

## Proxy repositories

A proxy repository serves the Galaxy API v3 from an upstream (default `https://galaxy.ansible.com`; set `upstreamUrl` for a Galaxy NG or automation hub). Clients use the same `HOST/repos/REPO/api/` URL and `ansible.cfg` as for a hosted repository; a proxy is read-only, so there is no publish.

- **Version lists.** The upstream's paginated version list is fetched completely, normalized and cached as one entry with the repository's TTL (stale entries are served when the upstream is down, as for other proxies). OpenArtifact then paginates it itself with `limit` and `offset`.
- **Version details.** The upstream detail document is cached with the repository TTL; `signatures` are passed through from it. Every link (`download_url`, `href`, pagination) is regenerated to point at OpenArtifact, never at the upstream.
- **Tarballs.** The tarball is fetched from the `download_url` of the version detail and cached only when its SHA-256 equals the `artifact.sha256` of that detail; a mismatch is refused and nothing is stored. Cached tarballs are immutable.
- **Collection index.** `GET /api/v3/collections/` answers `404`: an upstream cannot be enumerated.
- A version the upstream does not list answers `404`. Quarantine applies to cached tarballs.
- **Minimum age.** With `min_age_hours` set on the proxy, a version whose `created_at` (the time Galaxy created the version; `updated_at` moves later and is not used) is younger than that is left out of the version list and of the collection's `highest_version` (which moves to the newest remaining version; a collection with none left is `404`), and its version detail and tarball answer `403` unless the tarball is already cached. A version without a readable `created_at` is not held back. The documents are generated per request, so a version that has aged enough appears at once and their `ETag` follows what they list. A group with the proxy as a member serves only what the proxy does. See [Minimum age](../repositories/hosted-proxy-group.md#minimum-age).
- **Curation.** [Package curation](../repositories/hosted-proxy-group.md#package-curation) rules name a collection `namespace.name` (lower-case, as Galaxy writes it) and a version as Galaxy shows it; a version the rules make unavailable is left out of the version list and of `highest_version`, and its version detail and tarball answer `403` even when the tarball is already cached. No `created_at` is needed.

## Group repositories

A group merges hosted and proxy repositories of format `ansible`, served at its own `HOST/repos/GROUP/api/` with links to itself.

- **Versions** of a collection are merged across the members in order (an earlier member wins on a duplicate version) and listed newest first; version details and downloads come from the first member that has them.
- **Hosted wins.** Unless the group enables merging proxy versions, a collection (`NAMESPACE.NAME`) that a readable hosted member holds is served from the hosted members only, so an upstream cannot shadow a private collection (dependency confusion). Other collections come from the proxies. See [proxy and group rules](../repositories/hosted-proxy-group.md).
- **Collection index** lists the collections of the hosted members only; proxy members do not contribute.
