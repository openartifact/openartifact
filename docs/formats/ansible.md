<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Ansible collections

Use an Ansible repository as a private Galaxy server: `ansible-galaxy` publishes collections to it and installs them from it through the Galaxy API v3. A proxy mirrors `galaxy.ansible.com` (or any Galaxy NG or automation hub), and a group merges hosted and proxy repositories.

| Item | Value |
|---|---|
| Server URL | `HOST/repos/REPO/api/` (the trailing `/api/` is part of the URL) |
| Format id | `ansible` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | `https://galaxy.ansible.com` |

A repository stores collection tarballs named `NAMESPACE-NAME-VERSION.tar.gz`. A collection is identified by `NAMESPACE.NAME` (for example `community.general`). Permissions, quota, [trash](../operations/trash.md), webhooks and [cleanup](../repositories/cleanup-and-quotas.md) work as for any other format.

Scope: collections only, through Galaxy API v3. Roles, the Galaxy v1 and v2 APIs, collection signature generation and the Galaxy namespace and content-search APIs are not supported. A hosted repository always returns an empty `signatures` list.

## Client setup

Create an API token as described in [authentication](../security/authentication.md). `ansible-galaxy` sends it as `Authorization: Token <TOKEN>`, which OpenArtifact accepts like `Bearer`. Read access installs, write access publishes. A public repository, or a server with `allowAnonymousRead`, is read without a token. The web UI connect panel shows the same commands with your own URL.

`ansible.cfg`:

```ini
[galaxy]
server_list = openartifact

[galaxy_server.openartifact]
url = https://HOST/repos/REPO/api/
token = <TOKEN>
```

`requirements.yml`:

```yaml
collections:
  - name: NAMESPACE.NAME
    version: 1.0.0
    source: https://HOST/repos/REPO/api/
```

Requests under `/repos/REPO/api/` map to `read` (GET and HEAD) and `write` (POST and DELETE) on the repository; see [RBAC](../security/rbac.md).

## Publish

Build the collection, then publish the tarball:

```sh
ansible-galaxy collection build
ansible-galaxy collection publish NAMESPACE-NAME-VERSION.tar.gz --server openartifact
```

Other ways to publish:

- CLI: `oa upload REPO NAMESPACE-NAME-VERSION.tar.gz` ([CLI](../integrations/cli.md)).
- Web UI: the upload zone takes the built `.tar.gz`. The server reads namespace, name and version from `MANIFEST.json`.

The endpoint is `POST /api/v3/artifacts/collections/`: a multipart form with `file` (the tarball) and `sha256`. It answers `202` with `{"task": "<URL of the import task>"}`.

The server checks the tarball before it stores it. A rejected collection answers `400`, a duplicate `409`, and nothing is stored.

- The file name is `NAMESPACE-NAME-VERSION.tar.gz` and agrees with `MANIFEST.json`.
- `MANIFEST.json` is present. `namespace` and `name` match `^[a-z_][a-z0-9_]*$`.
- The version is strict semantic versioning (`1.2.3`, `1.2.3-rc.1`).
- Dependencies are well formed: `NAMESPACE.NAME` mapped to a version requirement. They are stored and returned in the version detail.
- The checksum of `FILES.json` matches `MANIFEST.json`, and every file of the archive matches `FILES.json`. A file that `FILES.json` does not list is refused.
- Absolute or `..` paths, links and other non-regular entries are refused.
- The `sha256` form field equals the digest of the uploaded file.
- `ansible-galaxy` sends the tarball base64-encoded (`Content-Transfer-Encoding: base64`). The server decodes it.

The tarball is limited to 500 MiB. A larger upload is refused.

Versions are immutable. Publishing an existing version answers `409`; delete it first to replace it.

## Install and consume

```sh
ansible-galaxy collection install NAMESPACE.NAME --server openartifact
```

All paths are relative to `HOST/repos/REPO`.

| Method and path | Purpose |
|---|---|
| `GET /api/` | Discovery: `{"available_versions":{"v3":"v3/"}}` |
| `GET /api/v3/collections/` | Collection index |
| `GET /api/v3/collections/NAMESPACE/NAME/` | One collection |
| `GET /api/v3/collections/NAMESPACE/NAME/versions/?limit=&offset=` | Paginated version list (`meta.count`, `links`, `data`) |
| `GET /api/v3/collections/NAMESPACE/NAME/versions/VERSION/` | Version detail: `download_url`, `artifact.filename`, `artifact.sha256`, `artifact.size`, `metadata.dependencies` |
| `GET /api/v3/imports/collections/ID/` | State of a publish import task |
| `POST /api/v3/artifacts/collections/` | Publish |
| `GET /api/v3/artifacts/collections/NAMESPACE-NAME-VERSION.tar.gz` | Download |
| `DELETE /api/v3/collections/NAMESPACE/NAME/versions/VERSION/` | Delete a version (`204`, moved to the trash) |

Every link in a response (`download_url`, `href`, pagination links, the import task) is an absolute URL, because `ansible-core` 2.15 cannot resolve a root-relative `download_url`. The server builds the origin from the request scheme and `Host`. It trusts `X-Forwarded-Host` and `X-Forwarded-Proto` only from a peer listed in `OPENARTIFACT_TRUSTED_PROXIES`. An explicit `BASE_URL` always wins. The discovery document keeps `"v3": "v3/"`.

<a id="proxy-repositories"></a>

## Proxy

Create a proxy with `repo_type: "proxy"`. Without `upstream_url` the default is `https://galaxy.ansible.com`; set it for a Galaxy NG or automation hub. Clients use the same `HOST/repos/REPO/api/` URL and `ansible.cfg` as for a hosted repository. A proxy is read-only, so there is no publish.

- **Version lists.** The server fetches the upstream's full version list and caches it for the repository TTL. It serves a stale copy when the upstream is down. It paginates the list itself with `limit` and `offset`.
- **Version details.** The upstream detail document is cached for the repository TTL. `signatures` are passed through. Every link points at OpenArtifact, never at the upstream.
- **Tarballs.** The server fetches the tarball from the `download_url` of the version detail. It caches the tarball only when its SHA-256 equals `artifact.sha256` of that detail. A mismatch is refused and nothing is stored. Cached tarballs never change.
- **Collection index.** `GET /api/v3/collections/` answers `404` because an upstream cannot be enumerated.
- **Unknown versions.** A version the upstream does not list answers `404`. Quarantine applies to cached tarballs.
- **Not proxied.** Roles, Galaxy v1 and the legacy v2 API.

### Minimum age and curation

Rules and `min_age_hours` follow [Minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [Package curation](../repositories/hosted-proxy-group.md#package-curation). The format-specific parts:

- **Names.** Rules name a collection as `namespace.name`, lower case as Galaxy writes it, and a version as Galaxy shows it.
- **Minimum age date.** The server uses `created_at` of the version. It ignores `updated_at`, which moves later. A version without a readable `created_at` is not held back.
- **Hidden version.** A version the rules or the minimum age hide leaves the version list. `highest_version` moves to the newest remaining version. A collection with no version left answers `404`.
- **Hidden version, requests.** Its version detail and tarball answer `403`. A cached tarball is still served when only the minimum age hides it. Curation refuses it even when cached.
- **Freshness.** The server builds these documents per request, so a version that has aged enough appears at once. Their `ETag` follows what they list.

## Group

A group merges hosted and proxy `ansible` repositories. It is served at `HOST/repos/GROUP/api/` with links to itself.

- **Versions** of a collection are merged across the members in order and listed newest first. An earlier member wins a duplicate version.
- **Details and downloads** come from the first member that has them.
- **Hosted members win a collection name.** If a readable hosted member holds a `NAMESPACE.NAME`, only hosted members serve it. An upstream cannot shadow a private collection. Other collections come from the proxies. Setting `merge_proxy_versions` to `true` turns this off. See [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).
- **Collection index** lists the collections of the hosted members only.
- **Minimum age and curation.** A proxy member's hidden versions stay hidden in the group.
- A group is read-only.

## Limits and behaviour

- **Tarball size:** 500 MiB.
- **Delete:** `DELETE` on a version is a soft delete. The version moves to the [trash](../operations/trash.md) (kind `ansible`) and can be restored until it is purged. A repository with immutable releases refuses to delete released versions; see [release rules](../operations/trash.md#immutable-releases).
- **Quota:** the [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is reserved before a collection is stored and released on every failure. Each collection version is one artifact.
- **Status codes:** `202` accepted, `400` invalid collection, `409` duplicate version, `204` deleted.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `409` on publish | The version exists. | Delete it first, or publish a new version. |
| `400` on publish | The collection failed validation. | Check the file name, `MANIFEST.json`, `FILES.json` checksums and the `sha256` field. |
| `404` for `GET /api/v3/collections/` on a proxy | A proxy cannot list its upstream. | Ask for a named collection instead. |
| `403` for a version on a proxy | Minimum age or curation hides it. | Wait until it is old enough, or change the rules. |
| `ansible-core` cannot download | `download_url` is not reachable at the origin the server builds. | Set `BASE_URL`, or list the reverse proxy in `OPENARTIFACT_TRUSTED_PROXIES`. |
| `401` | The token is missing or lacks read or write access. | Set `token` in `ansible.cfg`. |
