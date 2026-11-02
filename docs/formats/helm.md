<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Helm

Host Helm charts in a classic chart repository with a generated `index.yaml`, and upload charts with a plain HTTP `POST`. Use a hosted repository for your own charts, a proxy to cache an upstream chart repository, and a group to put several of them behind one URL.

| Item | Value |
|---|---|
| Repo URL | `HOST/repos/REPO` |
| Index | `HOST/repos/REPO/index.yaml` |
| Format id | `helm` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | none |

See [hosted, proxy, group](../repositories/hosted-proxy-group.md) for the repository types.

## Client setup

The web UI connect panel of a repository shows the same commands with your own URL.

```sh
helm repo add REPO HOST/repos/REPO --username <USERNAME> --password <TOKEN>
helm repo update
helm install my-release REPO/my-chart
```

The username is ignored. The token is the password. For a proxy or group, use `HOST/repos/PROXY_OR_GROUP` as the URL.

## Publish

Upload the raw chart archive as the request body:

```sh
curl -u <USERNAME>:<TOKEN> --data-binary @my-chart-0.1.0.tgz HOST/repos/REPO/charts
```

Do not use `helm push` or `-F` multipart: the server expects the raw `.tgz` as the body. The answer is `201` with `{"id","name","version"}`.

- The body is at most 100 MiB (`413 chart too large`). An archive that is not a valid chart is `400`.
- `name` and `version` come from `Chart.yaml` and are required. They must not be empty, `.` or `..`, must not contain `/`, `\`, whitespace or control characters, and are limited to 255 characters.
- A missing `apiVersion` defaults to `v2` and a missing `type` to `application`.
- An existing name and version is `409 chart version already exists`. Chart versions are immutable, so bump `version`.
- The [storage quota](../repositories/cleanup-and-quotas.md) is enforced on upload and released on delete.

## Install and consume

Relative to `HOST/repos/REPO`:

| Request | Behaviour |
|---|---|
| `GET /index.yaml` | repository index |
| `GET /charts/<name>-<version>.tgz` | download; `ETag` is the chart digest |
| `POST /charts` | upload |
| `GET /charts` | JSON list: id, name, version, appVersion, description, digest, size, createdAt, updatedAt |
| `DELETE /charts/<name>-<version>.tgz` | delete one chart version, `200` |

A download resolves `<name>-<version>.tgz` by trying each `-` followed by a digit (or `v` and a digit) as the name and version split, so names with hyphens work.

### Index

`index.yaml` lists the stored charts (`apiVersion: v1`, `generated`, `entries`). Each entry has `name`, `version`, `appVersion`, `description`, `digest`, `created`, `home`, `sources`, `maintainers`, `icon`, `apiVersion`, `type`, `keywords` and `urls: [charts/<name>-<version>.tgz]`.

The URL is relative. Helm resolves it against the repository URL, so `BASE_URL` plays no part.

The index is rendered again only when the chart set changes. Its `ETag` follows the chart set, so a client revalidating with `If-None-Match` gets `304` while nothing changed.

## Proxy

A Helm proxy mirrors a classic chart repository. Set `upstream_url` to the directory that holds `index.yaml`, for example `https://charts.jetstack.io` or `https://prometheus-community.github.io/helm-charts`. There is no default.

```sh
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" -H 'Content-Type: application/json' \
  -d '{"name":"jetstack","format":"helm","repo_type":"proxy","upstream_url":"https://charts.jetstack.io"}'
helm repo add jetstack HOST/repos/jetstack --username <USERNAME> --password <TOKEN>
helm pull jetstack/cert-manager --version 1.15.0
```

Uploading to a proxy (`POST /charts`) and every other write answer `405`.

- **`index.yaml`.** The proxy fetches the upstream index, caches it as received for `cache_ttl_seconds` and serves it stale when the upstream fails. A body that does not parse as an index (an HTML error page, a truncated document, a document using YAML anchors) is `502` and not cached.
- **Chart URLs.** Every chart `urls` entry, absolute or relative to the upstream, is rewritten to the relative reference `charts/<file>`, where `<file>` is the last segment of the chart's URL path. Helm resolves it against the repository URL, so `BASE_URL` plays no part. All other fields (`digest`, `created`, `dependencies`, `annotations`) are untouched.
- **Non-HTTP charts.** A chart version that only lists non-HTTP URLs (`oci://`, as the current Bitnami index does) is left out of the served index, because this server cannot serve it. Use a [docker](docker.md) proxy for those. Helm OCI registries (`helm push` and `oci://`) are not classic repositories.
- **Charts.** `GET /charts/<file>` fetches the chart from the URL the cached index lists for that file name, on first pull. A file no version lists is `404` and never fetched. If two entries list different URLs that end in the same file name, the first keeps the plain name and the later one is listed as `<name>-<8 hex of a hash of its URL>.<ext>`.
- **Digest check.** The chart is hashed and compared with the entry's `digest` (SHA-256 hex, an `sha256:` prefix is accepted). A mismatch, or a `digest` that is not SHA-256 hex, is `502` and nothing is stored. Verified charts are immutable, so a second pull works with the upstream down. An entry without a `digest` is cached unverified, for the repository TTL only.
- **Provenance.** `GET /charts/<file>.prov` (`helm pull --verify`) is fetched from next to the chart (the chart URL plus `.prov`) and cached for the repository TTL. It is not digest-checked.
- **Credentials.** Upstream Basic credentials go only to the upstream's own origin. Charts on another host (a CDN, GitHub release assets) are fetched without them.
- **Upstream down.** `helm repo add` and `helm repo update` are answered from the cached index (stale, `Warning: 110`, for up to `max_stale_seconds`). Cached charts are always served. A chart never pulled before answers `502`.

### Minimum age and curation

The general rules are in [minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [package curation](../repositories/hosted-proxy-group.md#package-curation). The Helm specifics are:

- **Minimum age.** A chart version whose `created` timestamp in the upstream `index.yaml` is younger than `min_age_hours` is left out of the served index. A chart left without versions disappears. A version without a parsable `created` is not held back.
- **Curation.** Rules name the chart by its name in the index (the key under `entries`) and the version as the index shows it. A matching version is left out of the index like a too-young one, with no `created` needed. A matching allow rule also exempts a version from the minimum age.
- **Hidden version.** `GET /charts/<file>` (and its `.prov`) of a file that only hidden versions list answers `403` with the reason, even when cached for curation. For minimum age it answers `403` unless the file is already cached.
- Helm picks the newest version itself, so no pointer moves.
- The `ETag` of the index changes with the threshold and, every 5 minutes, with the clock. A chart that has aged enough appears without a stale `304`.

## Group

A Helm group is an ordered list of hosted and proxy Helm members behind one repository URL.

- `index.yaml` is the union of the members' charts. A name and version is listed once, from the first member that has it, digest included. Chart URLs point at the group.
- Chart downloads and `.prov` files come from the first member that has them.
- Hosted members win a package name: when a readable hosted member holds a chart name, the index lists only its hosted versions and downloads of that name come only from hosted members. See [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).
- A member that fails or serves an unreadable index while another contributes is ignored. With no contributor the group answers `404`, or `502` if a member failed. An index that left out a failed member is served with `Cache-Control: no-store` and without an `ETag`.
- A group with a proxy member lists only what the proxy lists.
- Only `index.yaml` and `charts/<file>` are served. A hosted member's chart listing (`GET /charts`) is not exposed.
- Uploading to a group is `405`. Upload to a hosted member and read through the group.

## Limits and behaviour

- **Caching.** `index.yaml` is sent with `Cache-Control: public, max-age=60` for anonymous reads of a public repository, and `private, no-cache` otherwise. Chart downloads carry the chart digest as `ETag`.
- **Delete.** Delete moves the chart to the [trash](../operations/trash.md) by default. It disappears from `index.yaml` and downloads, and the quota is released at purge. With retention `0` it removes the record, releases the quota and frees the blob when nothing else references it.
- **Immutable releases.** In a repository with `immutable_releases`, deleting a release chart (no semver prerelease) is `409` unless an admin passes `?force=true`.
- **Quarantine.** Downloads of quarantined charts are refused.
- **Signing.** Hosted repositories have no `.prov` provenance file support and no repository signature, so `helm install --verify` is not served by them. A proxy passes through the `.prov` file the upstream publishes next to a chart. You verify it against the upstream's key. GPG signing covers only Debian and RPM ([signing](../security/signing.md)).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| upload fails with `400` | the body is not a valid chart archive, or `name` or `version` is missing | send the raw `.tgz` produced by `helm package` |
| `helm push` or multipart upload fails | the server expects the raw `.tgz` as the body | use the `curl --data-binary` command above |
| `409 chart version already exists` | chart versions are immutable | bump `version` |
| `413 chart too large` | archive above 100 MiB | shrink the chart |
| `405` on upload | target is a proxy or group | upload to a hosted repository |
| chart missing from a proxy index | too young for `min_age_hours`, blocked by a curation rule, or listed only with `oci://` URLs | check the rules and the upstream index |
| `403` on a chart download through a proxy | minimum age or a curation rule hides that version | wait or ask an admin to change the rule |
| `502` on a chart download through a proxy | digest mismatch, or the upstream failed and the chart is not cached | retry; check the upstream |
