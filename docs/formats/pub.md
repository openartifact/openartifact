<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Pub (Dart and Flutter)

Use a pub repository as a private package server for Dart and Flutter: `dart pub publish` publishes packages to it and `dart pub get` and `flutter pub get` install them from it. A proxy mirrors `pub.dev` (or any server that speaks the hosted pub repository spec v2), and a group merges hosted and proxy repositories.

| Item | Value |
|---|---|
| Server URL | `HOST/repos/pub/REPO` (`PUB_HOSTED_URL`, or the `hosted` URL of a dependency) |
| Format id | `pub` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | `https://pub.dev` |

A repository stores package archives named `NAME-VERSION.tar.gz`. A package name is 1 to 64 lower-case letters, digits and underscores, does not start with a digit and is not a Dart reserved word. A version is a semantic version (`1.2.3`, `1.2.3-rc.1`, `1.2.3+4`). Permissions, quota, [trash](../operations/trash.md), webhooks and [cleanup](../repositories/cleanup-and-quotas.md) work as for any other format.

The repository is served under the `/repos/pub/` prefix, so a repository named `pub` is not possible (see [URL conventions](README.md#url-conventions)).

Scope: the hosted pub repository spec v2 (package documents, archives, publishing). Retracting and discontinuing packages, advisories, publisher and uploader management, `dart pub deps --advisories` data and package search are not served. A hosted repository answers `404` to the advisories request of a client, which the client treats as "none".

## Client setup

Create an API token as described in [authentication](../security/authentication.md). `dart pub` sends a stored token as `Authorization: Bearer <TOKEN>`. Read access installs, write access publishes. A public repository, or a server with `allowAnonymousRead`, is read without a token. The web UI connect panel shows the same commands with your own URL.

Store the token for the repository once. The command asks for it:

```sh
dart pub token add https://HOST/repos/pub/REPO
```

In a CI job without a terminal, read it from the environment instead: `dart pub token add https://HOST/repos/pub/REPO --env-var OA_TOKEN`.

Resolve every package from the repository:

```sh
export PUB_HOSTED_URL=https://HOST/repos/pub/REPO
dart pub get
```

Or name the repository per dependency in `pubspec.yaml`:

```yaml
dependencies:
  NAME:
    hosted: https://HOST/repos/pub/REPO
    version: ^1.0.0
```

Add one dependency from the command line: `dart pub add NAME:1.0.0 --hosted-url https://HOST/repos/pub/REPO`.

Requests under `/repos/pub/REPO` map to `read` (GET and HEAD) and `write` (POST). The two GET requests of a publish (the upload URL and the finishing request) need `write` too, so a caller that may only read learns that before the archive is sent. See [RBAC](../security/rbac.md).

## Publish

Point `publish_to` at the repository or pass the server on the command line:

```sh
dart pub publish --server https://HOST/repos/pub/REPO
```

```yaml
# pubspec.yaml
publish_to: https://HOST/repos/pub/REPO
```

`dart pub publish` makes three requests, all relative to `HOST/repos/pub/REPO`:

| Method and path | Purpose |
|---|---|
| `GET /api/packages/versions/new` | Answers the upload URL and the (empty) form fields |
| `POST /api/packages/versions/newUpload` | Multipart upload with the archive in the `file` part; answers `204` and a `Location` header |
| `GET /api/packages/versions/newUploadFinish?package=NAME&version=VERSION` | Answers `{"success": {"message": "..."}}` |

The server checks the archive before it stores it. A rejected archive answers `400` with `{"error": {"code": "PackageRejected", "message": "..."}}`, and nothing is stored.

- The archive is a gzipped tar and has a `pubspec.yaml` at its root.
- `name` is a valid package name and not a Dart reserved word. `version` is a valid version.
- `pubspec.yaml` is at most 128 KiB and has a bounded nesting depth.
- Entries are regular files and directories; paths are not absolute and hold no `..`; the archive stays under its size limits.

The archive is limited to 100 MiB (the limit of pub.dev); the unpacked files to 256 MiB. A larger upload answers `413` (`RequestTooLarge`). A repository over its quota answers `507` (`RequestRejected`).

Versions are immutable. Publishing an existing version answers `409`; delete it first to replace it.

Other ways to publish: `oa upload` and the web upload zone are not available for this format, because the publish is a three-step exchange; use `dart pub publish`.

## Install and consume

All paths are relative to `HOST/repos/pub/REPO`.

| Method and path | Purpose |
|---|---|
| `GET /api/packages/NAME` | Package document: `name`, `latest` and every `versions` entry with its `pubspec`, `archive_url` and `archive_sha256`. Media type `application/vnd.pub.v2+json` |
| `GET /api/packages/NAME/versions/VERSION` | The entry of one version |
| `GET /api/archives/NAME-VERSION.tar.gz` | Download |

`latest` is the highest stable version; when a package only has pre-releases it is the highest of them. Every `archive_url` is an absolute URL that points at the repository, because the server builds the origin from the request scheme and `Host`. It trusts `X-Forwarded-Host` and `X-Forwarded-Proto` only from a peer listed in `OPENARTIFACT_TRUSTED_PROXIES`. An explicit `BASE_URL` always wins.

Package documents carry an `ETag`; a client or cache that revalidates gets `304` while nothing changed.

<a id="proxy-repositories"></a>

## Proxy

Create a proxy with `repo_type: "proxy"`. Without `upstream_url` the default is `https://pub.dev`. Clients use the same `HOST/repos/pub/REPO` URL as for a hosted repository. A proxy is read-only, so there is no publish.

- **Package documents.** The server fetches the upstream document and caches it for the repository TTL. It serves a stale copy when the upstream is down. Each request renders the cached document with every `archive_url` pointing at OpenArtifact, never at the upstream.
- **Archives.** The server fetches the archive from the `archive_url` of the upstream document. It caches the archive only when its SHA-256 equals `archive_sha256` of that document; a mismatch is refused and nothing is stored. An upstream that publishes no digest is served unverified. Cached archives never change.
- **Unknown versions.** A version the upstream does not list answers `404`. Quarantine applies to cached archives.
- **Not proxied.** Advisories and search.

### Minimum age and curation

Rules and `min_age_hours` follow [Minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [Package curation](../repositories/hosted-proxy-group.md#package-curation). The format-specific parts:

- **Names.** Rules name a package by its pub name and a version as pub.dev shows it.
- **Minimum age date.** The server uses the `published` date of the version. A version without a readable date is not held back.
- **Hidden version.** A version the rules or the minimum age hide leaves the package document, and `latest` moves to the newest remaining version. A package with no version left answers `404`.
- **Hidden version, requests.** Its version document and archive answer `403`.
- **Freshness.** The server builds the document per request, so a version that has aged enough appears at once. Its `ETag` follows what it lists.

## Group

A group merges hosted and proxy `pub` repositories. It is served at `HOST/repos/pub/GROUP` with links to itself.

- **Versions** of a package are merged across the members in order, and `latest` is recomputed. An earlier member wins a duplicate version.
- **Archives** come from the first member that has them.
- **Hosted members win a package name.** If a readable hosted member holds a package name, only hosted members serve it. An upstream cannot shadow a private package. Setting `merge_proxy_versions` to `true` turns this off. See [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).
- **Minimum age and curation.** A proxy member's hidden versions stay hidden in the group.
- A group is read-only: a publish answers `404`.

## Scanning

A hosted pub repository is scanned for vulnerabilities from the `pubspec` of each version. The scanners read the OSV `Pub` ecosystem and the package URL type `pub`. Dependency lists in the artifact detail come from the `dependencies` and `dev_dependencies` of the pubspec. License detection and SBOM extraction are not provided for this format.

## Limits and behaviour

- **Archive size:** 100 MiB.
- **Delete:** deleting a version through the API or the web UI is a soft delete. The version moves to the [trash](../operations/trash.md) (kind `pub`) and can be restored until it is purged. A repository with immutable releases refuses to delete released versions; see [release rules](../operations/trash.md#immutable-releases). `dart pub` has no delete command.
- **Quota:** the [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is reserved before an archive is stored and released on every failure. Each package version is one artifact.
- **Status codes:** `204` upload accepted, `400` invalid archive, `409` duplicate version, `413` archive too large, `507` over quota.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `409` on publish | The version exists. | Delete it first, or publish a new version. |
| `400` `PackageRejected` | The archive failed validation. | Check `pubspec.yaml` at the archive root, the package name and the version. |
| `401` or `403` on publish | The token is missing or lacks write access. | Run `dart pub token add https://HOST/repos/pub/REPO` with a token that may write. |
| `403` for a version on a proxy | Minimum age or curation hides it. | Wait until it is old enough, or change the rules. |
| `dart pub get` cannot download | `archive_url` is not reachable at the origin the server builds. | Set `BASE_URL`, or list the reverse proxy in `OPENARTIFACT_TRUSTED_PROXIES`. |
| `404` for the repository | A repository named `pub` is not reachable, or the repository is not of format `pub`. | Rename the repository and check its format. |
