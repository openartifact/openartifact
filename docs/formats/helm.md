<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Helm

Hosted Helm chart repository with a generated `index.yaml` and chart upload by plain HTTP `POST`.

| | |
|---|---|
| Format id | `helm` |
| Repo URL | `HOST/repos/REPO` (index at `HOST/repos/REPO/index.yaml`) |
| Repository types | hosted only ([details](../repositories/hosted-proxy-group.md)) |

## Client setup

`internal/clientsetup` produces:

```sh
helm repo add REPO HOST/repos/REPO --username USERNAME --password <TOKEN>
helm repo update
helm install my-release REPO/my-chart

# publish: raw chart archive as the request body
curl -u USERNAME:<TOKEN> --data-binary @my-chart-0.1.0.tgz HOST/repos/REPO/charts
```

The username is ignored; the token is the password. Do not use `helm push` or `-F` multipart: the server expects the raw `.tgz` as the body.

## Endpoints

Relative to `HOST/repos/REPO`:

| Request | Behaviour |
|---|---|
| `GET /index.yaml` | repository index, `Cache-Control: public, max-age=60` |
| `GET /charts/<name>-<version>.tgz` | download; `ETag` is the chart digest |
| `POST /charts` | upload; `201` with `{"id","name","version"}` |
| `GET /charts` | JSON list: id, name, version, appVersion, description, digest, size, createdAt, updatedAt |
| `DELETE /charts/<name>-<version>.tgz` | delete one chart version, `200` |

## Index

`index.yaml` is built on every request from the stored charts (`apiVersion: v1`, `generated`, `entries`). Each entry has `name`, `version`, `appVersion`, `description`, `digest`, `created`, `home`, `sources`, `maintainers`, `icon`, `apiVersion`, `type`, `keywords` and `urls: [BASE_URL/repos/REPO/charts/<name>-<version>.tgz]`. `BASE_URL` must be the URL clients use or `helm pull` follows links to the wrong host.

## Upload

- The body is the raw chart archive, at most 100 MiB (`413 chart too large`); an archive that is not a valid chart is `400`.
- `name` and `version` come from `Chart.yaml` and are required. They must not contain `/`, `\`, whitespace or control characters, must not be empty, `.` or `..`, and are limited to 255 characters.
- Missing `apiVersion` defaults to `v2` and missing `type` to `application`.
- An existing name and version is `409 chart version already exists`: chart versions are immutable, bump `version`.
- Storage quota is enforced on upload and released on delete ([quotas](../repositories/cleanup-and-quotas.md)).
- A chart download resolves `<name>-<version>.tgz` by trying each `-` followed by a digit (or `v` and a digit) as the name/version split, so names with hyphens work.
- Delete moves the chart to the [trash](../operations/trash.md) by default (it disappears from `index.yaml` and downloads; quota is released at purge). With retention `0` it removes the record, releases the quota and frees the blob when nothing else references it. In a repository with `immutable_releases`, deleting a release chart (no semver prerelease) is `409` unless an admin passes `?force=true`. Downloads of quarantined charts are refused.

## Signing

There is no `.prov` provenance file support and no repository signature; `helm install --verify` is not served. GPG signing covers only Debian and RPM ([signing](../security/signing.md)).
