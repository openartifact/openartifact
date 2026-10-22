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

Other routes (all relative to `HOST/repos/REPO`):

| Method and path | Purpose |
|---|---|
| `GET /v3/package/<id>/index.json` | version list, `{"versions": ["1.0.0", "1.2.0-beta"]}`, oldest first (what `dotnet restore` requests first); `404` for an unknown id |
| `GET /v3/package/<id>/<version>/<id>.<version>.nupkg` | package download |
| `GET /v3/package/<id>/<version>/<id>.nuspec` | the package's nuspec (`application/xml`) |
| `DELETE /v3/package/<id>/<version>` | delete a version |
| `GET /v3/registration/<id>/index.json` | registration index |
| `GET /v3/registration/<id>/<version>.json` | registration leaf of one version |
| `GET /v3/query` | search (`q`, `skip`, `take`; default 20, clamped to 1–100) |
| `GET /v3/autocomplete` | package id completion |

Ids are matched case-insensitively. Any other file name under `/v3/package/<id>/<version>/` is `404`.

## Versions

Versions are stored, listed and matched in NuGet's normalized form, which is also lower case: leading zeros are dropped, the fourth part is dropped when it is `0`, `+build` metadata is dropped, and release labels are lower-cased. `1.0.0.0`, `1.0` and `01.0.0` are all `1.0.0`; `2.0-Beta+abc` is `2.0.0-beta`. Pushing a version that normalizes to an existing one is a duplicate. URLs accept any spelling of a version and normalize it. A `.nuspec` whose version is not a valid NuGet version is rejected with `400 invalid package`.

Version lists, registration pages and search results order versions by SemVer 2.0 precedence (extended with NuGet's fourth part): `1.0.0-beta` < `1.0.0` < `1.0.0.1` < `1.10.0`.

## Push

`PUT /v3/package` with a multipart form whose `package` field is the `.nupkg`; success is `201`.

- Form limit 100 MiB.
- The `.nupkg` must be a zip with a `.nuspec` (max 4 MiB) carrying `id` and `version`; otherwise `400 invalid package`.
- The package id is lower-cased and the version normalized (see [Versions](#versions)); a SHA-512 of the package is computed.
- Pushing an id and version that already exist answers `409 package version already exists` and changes nothing: the stored package and its blob stay as they were. Delete the version first to publish it again (`dotnet nuget push --skip-duplicate` treats the `409` as a skip).

## Metadata served

The registration index (`@id`, `count`, `items`) holds pages of at most 64 versions in ascending order. Each page has `@id`, `lower`, `upper`, `count`, `parent` and `items`; each item has an `@id` (its leaf URL, `.../v3/registration/<id>/<version>.json`), `packageContent`, `registration` and a `catalogEntry` with `@id`, id, version, description, authors, title, summary, tags, projectUrl, licenseUrl, iconUrl, `listed` (always `true`), `published` (push time), dependency groups (per target framework) and the `packageContent` download URL. The leaf document returned by the leaf URL has `@id`, `catalogEntry` (the entry URL), `listed`, `packageContent`, `published` and `registration`.

Search returns one entry per package: `totalHits` counts packages, and each entry carries the latest version's metadata (the highest stable version, or the highest prerelease when there is no stable one), `totalDownloads` over all versions and a `versions` array of `{"@id", "version", "downloads"}`. `skip` and `take` count packages. Autocomplete lists each package id once.
