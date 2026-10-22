<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# NuGet

NuGet v3 feed: service index, registration, package download, search, autocomplete, push and delete; also as a proxy of an upstream feed (nuget.org) and as a group.

| | |
|---|---|
| Repo URL | `HOST/repos/REPO/v3/index.json` |
| Repository types | hosted, proxy, group. See [hosted, proxy, group](../repositories/hosted-proxy-group.md) |

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

- The package is limited to 100 MiB (`413` above it); the whole request body is capped at that plus 1 MiB of form overhead.
- The `.nupkg` must be a zip with exactly one root-level `.nuspec` (max 4 MiB; nested `.nuspec` files are package content and ignored) carrying an `id` of 1–100 characters from `A-Z a-z 0-9 . _ -` and a valid `version`; otherwise `400 invalid package`.
- The package id is lower-cased and the version normalized (see [Versions](#versions)); a SHA-512 of the package is computed.
- Pushing an id and version that already exist answers `409 package version already exists` and changes nothing: the stored package and its blob stay as they were. Delete the version first to publish it again (`dotnet nuget push --skip-duplicate` treats the `409` as a skip).
- The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is enforced on push (`507` over quota) and released when the version is deleted for good.

## Metadata served

The registration index (`@id`, `count`, `items`) holds pages of at most 64 versions in ascending order. Each page has `@id`, `lower`, `upper`, `count`, `parent` and `items`; each item has an `@id` (its leaf URL, `.../v3/registration/<id>/<version>.json`), `packageContent`, `registration` and a `catalogEntry` with `@id`, id, version, description, authors, title, summary, tags, projectUrl, licenseUrl, iconUrl, `listed` (always `true`), `published` (push time), dependency groups (per target framework, read from the stored package on first use and then cached in memory by package digest) and the `packageContent` download URL. The leaf document returned by the leaf URL has `@id`, `catalogEntry` (the entry URL), `listed`, `packageContent`, `published` and `registration`.

Search returns one entry per package: `totalHits` counts packages, and each entry carries the latest version's metadata (the highest stable version, or the highest prerelease when there is no stable one), `totalDownloads` over all versions and a `versions` array of `{"@id", "version", "downloads"}`. `skip` and `take` count packages. Autocomplete lists each package id once.

## Proxy and group repositories

A proxy (`repo_type: proxy`, `upstream_url` the upstream **service index**, default `https://api.nuget.org/v3/index.json`) and a group (`repo_type: group`) use the same source URL as a hosted repository, `HOST/repos/REPO/v3/index.json`, and the same `/v3/...` routes (read-only; `dotnet nuget push` is `405`):

```sh
dotnet nuget add source HOST/repos/REPO/v3/index.json --name REPO
dotnet restore
```

Over plain `http://` NuGet needs `allowInsecureConnections="true"` on the source (or `--allow-insecure-connections`).

- **Proxy.** The service index, flat container (version list, `.nupkg`, `.nuspec`) and registrations (inline or paged, gzip decoded) are fetched from the upstream's own service index and rewritten so that every resource and `packageContent` URL points at the proxy (`BASE_URL` must be the externally visible URL). `.nupkg` files are verified against the catalog `packageHash`/`packageSize` when the upstream publishes them, else against being a zip with a root `.nuspec`, then cached forever; metadata follows `cache_ttl_seconds` and is served stale when the upstream is down, so a restore of anything already fetched works offline. Search and autocomplete pass through uncached.
- **Group.** The service index is the group's (registrations and flat container, no search). Version lists are united, registrations merged per version (earliest member wins) into inline pages, and package files come from the first member that has them. Members are hosted nuget and nuget proxy repositories.

Details and limits: [NuGet proxies and groups](../repositories/hosted-proxy-group.md#nuget-proxies).

Limits: the catalog (`Catalog/3.0.0`) and publishing resources are not proxied. Credentials are sent only to the origin of `upstream_url`, so a feed whose resources are on another origin than its service index is read anonymously there. Search on a group is not offered.
