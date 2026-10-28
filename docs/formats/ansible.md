<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Ansible collections

Hosted private Galaxy server for Ansible collections: `ansible-galaxy` publishes to and installs from an OpenArtifact repository through the Galaxy API v3.

| | |
|---|---|
| Format id | `ansible` |
| Server URL | `HOST/repos/REPO/api/` (the trailing `/api/` is part of the URL) |
| Repository types | hosted (proxy and group repositories are not available yet) |

## Overview

A repository of format `ansible` stores collection tarballs named `NAMESPACE-NAME-VERSION.tar.gz`. A collection is identified by its FQCN, `NAMESPACE.NAME` (for example `community.general`). Permissions, quota, [trash](../operations/trash.md), webhooks and [cleanup](../repositories/cleanup-and-quotas.md) apply as for any other format. Create the repository in the web UI (format **Ansible**) or with `format: ansible` through the API or CLI, see [repositories](../repositories/hosted-proxy-group.md).

## Supported scope

- **Galaxy API v3, collections only.** Publishing, listing, version detail, import task state, download and delete.
- **Out of scope:** roles and the Galaxy v1/v2 APIs, collection signatures (the `signatures` list is always empty), and the Galaxy namespace and content-search APIs.
- **Proxy and group repositories** (mirroring `galaxy.ansible.com` and merging repositories) are planned as a follow-up; today only hosted repositories exist.

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

All paths are relative to `HOST/repos/REPO`. Every link in the responses (`download_url`, `href`, pagination links and the import task) is an absolute URL built from the server's configured `baseUrl`, which `ansible-core` 2.15 needs: set it to the URL clients reach.

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
