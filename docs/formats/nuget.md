<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# NuGet

Hosted NuGet v3 feed: service index, registration, package download, search, autocomplete, push and delete.

| | |
|---|---|
| Repo URL | `HOST/repos/REPO/v3/index.json` |
| Repository types | hosted only. See [hosted, proxy, group](../repositories/hosted-proxy-group.md) |

## Client setup

`internal/clientsetup` produces:

```sh
dotnet nuget add source HOST/repos/REPO/v3/index.json --name REPO --username <USERNAME> --password <TOKEN> --store-password-in-clear-text
dotnet nuget push my-package.1.0.0.nupkg --source REPO --api-key <TOKEN>
```

Source credentials carry the token as the Basic password. The server does not read `X-NuGet-ApiKey` (the `--api-key` value); the source credentials authenticate the push. See [authentication](../security/authentication.md).

## Service index

`GET /v3/index.json` returns version `3.0.0` with these resources (under `HOST/repos/REPO`):

| `@type` | Path |
|---|---|
| `RegistrationsBaseUrl` | `/v3/registration/` |
| `PackageBaseAddress/3.0.0` | `/v3/package/` |
| `SearchQueryService` | `/v3/query` |
| `SearchAutocompleteService` | `/v3/autocomplete` |
| `PackagePublish/2.0.0` | `/v3/package` |

Other routes: `GET /v3/registration/<id>/index.json`, `GET /v3/package/<id>/<version>/<filename>` (download), `DELETE /v3/package/<id>/<version>`. `/v3/query` takes `q`, `skip` and `take` (default 20, clamped to 1–100).

## Push

`PUT /v3/package` with a multipart form whose `package` field is the `.nupkg`; success is `201`.

- Form limit 100 MiB.
- The `.nupkg` must be a zip with a `.nuspec` (max 4 MiB) carrying `id` and `version`; otherwise `400 invalid package`.
- The package id is lower-cased; a SHA-512 of the package is computed.

## Metadata served

The registration index lists each version with id, version, description, authors, title, summary, tags, projectUrl, licenseUrl, iconUrl, dependency groups (per target framework) and the `packageContent` download URL.
