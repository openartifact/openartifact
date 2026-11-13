<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# NuGet

Use a NuGet v3 feed to push, restore, search and delete packages with `dotnet` and `nuget`. It can be hosted, a proxy of an upstream feed such as nuget.org, or a group of both.

| Item | Value |
|---|---|
| Repo URL | `HOST/repos/REPO/v3/index.json` |
| Format id | `nuget` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | `https://api.nuget.org/v3/index.json` |

## Client setup

The web UI connect panel shows the same commands with your own URL.

```sh
dotnet nuget add source HOST/repos/REPO/v3/index.json --name REPO --username <USERNAME> --password <TOKEN> --store-password-in-clear-text
dotnet nuget push my-package.1.0.0.nupkg --source REPO --api-key <TOKEN>
```

Source credentials carry the token as the Basic password. The server does not read `X-NuGet-ApiKey` (the `--api-key` value). The source credentials authenticate the push. See [authentication](../security/authentication.md).

`--store-password-in-clear-text` writes the token unencrypted into `NuGet.Config`. NuGet encrypts stored passwords only on Windows, so on Linux and macOS the flag is required. Keep that `NuGet.Config` out of version control, or keep the token out of the file: NuGet expands environment variables written as `%NAME%` in `NuGet.Config` values, so `--password '%OA_TOKEN%'` stores only the variable name.

Over plain `http://`, NuGet needs `allowInsecureConnections="true"` on the source, or `--allow-insecure-connections`.

## Publish

Push with `dotnet nuget push`, or send `PUT /v3/package` with a multipart form whose `package` field is the `.nupkg`. Success is `201`.

- The package is at most 100 MiB (`413` above it). The whole request is capped at that plus 1 MiB of form overhead.
- The `.nupkg` must be a zip with exactly one root-level `.nuspec` (at most 4 MiB). Nested `.nuspec` files are package content and are ignored.
- The `.nuspec` needs an `id` of 1 to 100 characters from `A-Z a-z 0-9 . _ -` and a valid `version`. Otherwise `400 invalid package`.
- The package id is lower-cased and the version normalized, see [Versions](#versions).
- A version that already exists is `409 package version already exists` and nothing changes. Delete the version first to publish it again. `dotnet nuget push --skip-duplicate` treats the `409` as a skip.
- The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is enforced (`507` over quota) and released when the version is deleted for good.

Delete a version with `DELETE /v3/package/<id>/<version>`.

## Versions

Versions are stored, listed and matched in NuGet's normalized lower-case form:

- Leading zeros are dropped.
- The fourth part is dropped when it is `0`.
- `+build` metadata is dropped.
- Release labels are lower-cased.

```text
1.0.0.0, 1.0, 01.0.0  ->  1.0.0
2.0-Beta+abc          ->  2.0.0-beta
```

Pushing a version that normalizes to an existing one is a duplicate. URLs accept any spelling of a version. A `.nuspec` with an invalid version is `400 invalid package`.

Version lists, registration pages and search results use SemVer 2.0 precedence, extended with NuGet's fourth part: `1.0.0-beta` < `1.0.0` < `1.0.0.1` < `1.10.0`.

## Install and consume

### Service index

`GET /v3/index.json` returns the service index (version `3.0.0`). Paths are relative to `HOST/repos/REPO`:

| `@type` | Path |
|---|---|
| `RegistrationsBaseUrl` | `/v3/registration/` |
| `PackageBaseAddress/3.0.0` | `/v3/package/` |
| `SearchQueryService` | `/v3/query` |
| `SearchAutocompleteService` | `/v3/autocomplete` |
| `PackagePublish/2.0.0` | `/v3/package` |

| Method and path | Purpose |
|---|---|
| `GET /v3/package/<id>/index.json` | version list `{"versions": [...]}`, oldest first; `404` for an unknown id |
| `GET /v3/package/<id>/<version>/<id>.<version>.nupkg` | package download |
| `GET /v3/package/<id>/<version>/<id>.nuspec` | the nuspec (`application/xml`) |
| `GET /v3/registration/<id>/index.json` | registration index |
| `GET /v3/registration/<id>/<version>.json` | registration leaf of one version |
| `GET /v3/query` | search: `q`, `skip`, `take` (default 20, clamped to 1 to 100) |
| `GET /v3/autocomplete` | package id completion |

Ids are matched case-insensitively. Any other file name under `/v3/package/<id>/<version>/` is `404`.

### Registration documents

The registration index lists versions in ascending order, in pages of at most 64.

- Each page has `@id`, `lower`, `upper`, `count`, `parent` and `items`.
- Each item has its leaf URL as `@id`, `packageContent`, `registration` and a `catalogEntry`.
- The `catalogEntry` holds id, version, description, authors, title, summary, tags, `projectUrl`, `licenseUrl`, `iconUrl`, `listed` (always `true`), `published` (push time) and dependency groups per target framework.
- Dependency groups are read from the stored package on first use and then kept in memory.
- The leaf document has `@id`, `catalogEntry`, `listed`, `packageContent`, `published` and `registration`.

### Search

- Search returns one entry per package. `totalHits`, `skip` and `take` count packages.
- Each entry carries the metadata of the latest version: the highest stable version, or the highest prerelease when there is no stable one.
- Each entry also has `totalDownloads` over all versions and a `versions` array of `{"@id", "version", "downloads"}`. Download counts are written in batches and lag by up to `DB_DOWNLOAD_COUNT_FLUSH_INTERVAL` (5 seconds by default).
- Autocomplete lists each package id once.

## Proxy

Create a proxy with `repo_type: "proxy"` and the upstream **service index** as `upstream_url`. If you omit it, the default is `https://api.nuget.org/v3/index.json`.

```sh
curl -X POST HOST/api/v1/repositories -H "Authorization: Bearer <TOKEN>" \
  -H 'Content-Type: application/json' \
  -d '{"name":"nuget-org","format":"nuget","repo_type":"proxy"}'
```

Clients use `HOST/repos/REPO/v3/index.json` as the source, like a hosted feed. A proxy is read-only: `dotnet nuget push` and delete answer `405` (`Allow: GET, HEAD`).

```sh
dotnet nuget add source HOST/repos/REPO/v3/index.json --name REPO
dotnet restore
```

- **Service index:** fetched from `upstream_url`, cached for the repository TTL and rewritten. It advertises only what the proxy serves: registrations, the flat container (`PackageBaseAddress`) and, when the upstream has them, search and autocomplete. Every URL points at the proxy, on the request origin or on `BASE_URL` when set.
- **Registrations:** inline or paged, gzip decoded, and rewritten so `@id`, `parent`, `registration` and `packageContent` point at the proxy. Paged registrations are served under `/v3/registration/<id>/page/<lower>/<upper>.json`. A page the index does not list is `404` and never fetched. Catalog URLs are left unchanged.
- **Flat container:** version lists, `.nupkg` and `.nuspec` come from the upstream's `PackageBaseAddress`. Ids and versions are matched in normalized form, so all spellings share one cache entry.
- **Packages:** a `.nupkg` is checked before it is cached. With a catalog entry, it must match `packageHash` (SHA-512 or SHA-256) and `packageSize`. Without one, it must be a zip with a root `.nuspec`. A mismatch is `502` and nothing is stored.
- **Immutable once cached:** `.nupkg` and `.nuspec` files. A `.nupkg` with no `packageHash` is cached for the repository TTL only.
- **Search and autocomplete:** passed through uncached with rewritten URLs. `404` when the upstream has none.
- **Credentials:** sent only to the origin of `upstream_url`. Resources on another origin are reachable, but those requests are anonymous.
- **Upstream down:** cached packages are always served. The service index, version lists and registrations are served stale for up to `max_stale_seconds` past expiry (`Warning: 110`). A package never requested before answers `502`.
- The upstream's Catalog resource and publishing resources are not proxied.

### Minimum age and curation

The general rules are in [minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [package curation](../repositories/hosted-proxy-group.md#package-curation). The NuGet specifics:

- **Spelling:** rules name the package id (matched lower-cased) and the normalized version (`1.0` is `1.0.0`).
- **Age source:** the `published` time of the registration entry. Unlisted packages state the year 1900, which is no date, so they are not held back.
- **Hidden version:** it is removed from the registration index, its pages and the flat-container version list. Its registration leaf is `404`.
- **Files:** its `.nupkg` and `.nuspec` answer `403`. Under minimum age they are served if already cached. Curation blocks cached files too.
- **No version left:** the package is `404`.
- **Pages the upstream does not inline** are served as they are and gated when fetched.
- **Flat-container version list:** it has no times, so it is filtered with the dates from the package's registration. A version the registration does not list yet is treated as too new. If the feed offers only semver 1 registrations, or none, the list is not filtered.
- **Search and autocomplete** are not filtered and may still name a hidden version. Its files are refused.
- **Freshness:** the `ETag` of these documents changes with the threshold and every 5 minutes, so a version that has aged enough appears without a stale `304`.

## Group

A group has hosted and NuGet proxy members. It uses the same source URL as a hosted feed and is read-only.

- The service index is the group's own and has registrations and the flat container, but no search. `/v3/query` and `/v3/autocomplete` are `404`.
- Version lists are united across members.
- Registrations are merged per version into inline pages of 64. For a version held by several members, the first member in member order wins.
- Pages a member does not inline are fetched from it. A member that cannot deliver its registration whole is left out.
- Registration leaves, pages, `.nupkg` and `.nuspec` come from the first member that has them. Every URL points at the group.
- Hosted members win a package id first, see [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).
- A proxy member's gate applies through the group. A hidden file is `403` and the group does not try the next member.
- A failing member is ignored while another contributes. With no contributor the group answers `404`, or `502` if a member failed. A merge that left out a failed member is served with `Cache-Control: no-store` and no `ETag`.

## Limits and behaviour

- Package size is at most 100 MiB. Quota over the limit is `507`.
- Registration documents carry an `ETag` and revalidate with `If-None-Match`.
- NuGet has no repository signature. GPG signing covers only Debian and RPM ([signing](../security/signing.md)).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `400 invalid package` on push | no single root `.nuspec`, bad `id`, or bad `version` | fix the nuspec |
| `409 package version already exists` | the id and normalized version exist | bump the version, or delete the old one |
| `405` on push | the repository is a proxy or a group | push to a hosted repository |
| `401` on push | the source has no credentials | add the source with `--username` and `--password <TOKEN>` |
| NuGet refuses an `http://` source | insecure sources are off | set `allowInsecureConnections="true"` |
| `502` for a package through a proxy | the download did not match its hash | retry, then check the upstream |
| `403` on a `.nupkg` | minimum age or curation holds it back | see [Minimum age and curation](#minimum-age-and-curation) |
| a restore from a group finds no search | groups have no search | search a member repository |
