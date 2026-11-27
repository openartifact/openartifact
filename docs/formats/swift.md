<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Swift

Use a Swift repository as a private Swift Package Registry (SE-0292 and SE-0391): `swift package-registry publish` uploads a package to it, and SwiftPM resolves `.package(id:)` dependencies from it. A proxy mirrors another Swift registry, and a group merges hosted and proxy repositories.

| Item | Value |
|---|---|
| Registry URL | `HOST/repos/swift/REPO` (no trailing slash) |
| Format id | `swift` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | none: you must set `upstream_url` |

## Client setup

Create an API token as described in [authentication](../security/authentication.md). The web UI connect panel shows the same commands with your own URL.

Create a repository with format `swift`, then point SwiftPM at it:

```sh
swift package-registry set HOST/repos/swift/REPO
swift package-registry login HOST/repos/swift/REPO/login --token <TOKEN>
```

- `set` writes `.swiftpm/configuration/registries.json` of the current package. Add `--global` to write it for your user, or `--scope mona` to use the registry only for packages of the scope `mona`.
- `login` takes the **login URL**: the registry URL with `/login` appended. It stores the token in the macOS keychain or in `~/.netrc`; on Linux it asks before writing the netrc file (`--no-confirm` skips the question). Use `--username USER --password <TOKEN>` if you prefer. The server accepts the token as a Bearer token, or as the Basic password, and answers `POST /login` with `200` when the credentials are valid. `swift package-registry logout HOST/repos/swift/REPO/login` removes it.
- SwiftPM sends credentials **only over HTTPS**. Over plain http (a local test server) `login` fails with `invalid URL`, and a `~/.netrc` entry is not used for requests either: reading a public repository works with `--allow-insecure-http` on `set`, but publishing to, or reading from, a private repository needs HTTPS in front of the server.

A public repository can be read without a token. Publishing always needs one.

## Publish

A package is `scope.name`, for example `mona.LinkedList`. Run this inside the package directory:

```sh
swift package-registry publish mona.LinkedList 1.0.0 --url HOST/repos/swift/REPO
```

`publish` builds the source archive itself (`swift package archive-source`), reads the manifests, and sends a multipart request:

```
PUT /repos/swift/REPO/<scope>/<name>/<version>
```

Pass `--metadata-path FILE` when the metadata is not in `package-metadata.json`. The file may carry:

- `author` (with `name`, `email`, `description`, `organization`, `url`)
- `description`
- `licenseURL`
- `originalPublicationTime`
- `readmeURL`
- `repositoryURLs`: the source locations of the package, used by the lookup in [Consume](#install-and-consume)

A version is published once. A second publish of the same scope, name and version answers `409`. Publishing to a proxy or group answers `405`; publish to a hosted repository. The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) applies (`507` over quota).

### Limits

- The publish body is at most 256 MiB.
- The version is a strict SemVer 2.0 version. A version ending in `.zip` or `.json` is refused, because it would clash with the download and metadata URLs.
- The scope matches `[A-Za-z0-9](-?[A-Za-z0-9])*` and has at most 39 characters. The name matches `[A-Za-z0-9](?:[-_]?[A-Za-z0-9])*`.
- Scope and name are case-insensitive: `mona.LinkedList` and `Mona.linkedlist` are the same package. The case of the first publish is kept for display.

### Signing

SwiftPM can sign the source archive (`--signing-identity`, or `--private-key-path` with `--cert-chain-paths`). The server stores the signatures and serves them back to clients, but it does **not** verify them. Clients decide whether to trust a signature.

## Install and consume

Depend on a package by id in `Package.swift`:

```swift
dependencies: [
    .package(id: "mona.LinkedList", from: "1.0.0"),
],
targets: [
    .target(name: "App", dependencies: [
        .product(name: "LinkedList", package: "mona.LinkedList"),
    ]),
]
```

Then run:

```sh
swift package resolve
```

SwiftPM fetches the release list, the release metadata (with the checksum), the manifest and the source archive. Paths are relative to `HOST/repos/swift/REPO`:

| Method and path | Purpose |
|---|---|
| `GET /<scope>/<name>` | Release list |
| `GET /<scope>/<name>/<version>` | Release metadata: resources with the SHA-256 checksum, and the package metadata |
| `GET /<scope>/<name>/<version>/Package.swift` | Manifest (`?swift-version=` selects a version-specific manifest) |
| `GET /<scope>/<name>/<version>.zip` | Source archive |
| `GET /identifiers?url=` | Package ids for a repository URL |
| `PUT /<scope>/<name>/<version>` | Publish |
| `DELETE /<scope>/<name>/<version>` | Delete a release |
| `POST /login` | Credential check |

The registry configured with `set` without `--scope` is the default for every scope. With `--scope mona` only `mona.*` packages come from it, so you can mix a public registry and a private one.

### Replace source control dependencies

`GET /identifiers?url=` answers with the ids of the packages whose `repositoryURLs` metadata contains the URL. SwiftPM uses it with `--replace-scm-with-registry` (or `swift package resolve --replace-scm-with-registry`) to fetch `.package(url: "https://github.com/mona/LinkedList", ...)` dependencies from the registry instead of from git.

## Proxy

Create a proxy with `repo_type: "proxy"` and an `upstream_url`. There is no default upstream: no public Swift registry is a drop-in mirror target, so the upstream must be a registry that speaks the Swift Package Registry protocol. For example, a Swift repository on another OpenArtifact instance:

```
upstream_url: https://other.example.com/repos/swift/libs
```

Set the upstream credentials of the repository if the upstream needs them; they go only to the configured upstream host. Clients use the proxy like a hosted repository, at `HOST/repos/swift/PROXY`. Release lists, metadata, manifests and archives are cached on first request and served from the cache while the upstream is down. Writes (`publish`, `DELETE`) answer `405`.

Rules and `min_age_hours` follow [Minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [Package curation](../repositories/hosted-proxy-group.md#package-curation).

## Group

A group puts hosted and Swift proxy repositories behind one registry URL, `HOST/repos/swift/GROUP`.

- Release lists are merged across the readable members.
- Hosted members win a package name: if a readable hosted member holds `scope.name`, its releases are the only ones listed. Setting `merge_proxy_versions` to `true` turns this off. See [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).
- Downloads come from the member that supplied the release.
- Publishing to a group is `405`. Publish to a hosted member and read through the group.

## What is stored

Per release the server keeps the source archive (`scope/name/version.zip`) and the manifests extracted from it at publish time: `Package.swift` and every `Package@swift-X.Y.swift`. The checksum in the release metadata is the SHA-256 of the archive. SwiftPM compares it with the archive it downloads and with `Package.resolved`.

## Delete

Delete a release from the web UI or through the REST API; it goes to the [trash](../repositories/cleanup-and-quotas.md) like any other package. The registry route `DELETE /<scope>/<name>/<version>` does the same for a client with delete permission.

## Security scanning

Swift is mapped to the OSV ecosystem `SwiftURL` and to the Package URL type `swift`: SBOMs list a release as `pkg:swift/scope.name@version`. OSV names Swift packages by their source repository URL, while a registry identifies them as `scope.name`, so the name-based [vulnerability scan](../security/scanning.md) does not match advisories to Swift releases. Do not rely on it for Swift packages.

## Not supported

- Asynchronous publish: a request with `Prefer: respond-async` is answered synchronously, once the release is stored.
- Search or collections: the registry protocol has no such endpoint.
- Signature verification (see [Signing](#signing)).
- Publishing from the web UI: SwiftPM builds and signs the archive, so there is no upload form. Use `swift package-registry publish`.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `invalid URL` on `login` | The URL is plain http. | Use HTTPS. |
| `405` on `login` | The URL is the registry URL, not the login URL. | Add `/login` to the URL. |
| `insecure registry URL` on `set`, `publish` or `resolve` | The URL is plain http. | Add `--allow-insecure-http`, or use HTTPS. |
| `401` on publish | The token is missing or wrong, or the URL is plain http (SwiftPM sends no credentials). | Run `login` again, and use HTTPS. |
| `409` on publish | The release exists. | Publish a new version. |
| `413` on publish | The archive is above 256 MiB. | Trim the package. |
| `405` on publish | The repository is a proxy or group. | Publish to a hosted repository. |
| Checksum mismatch on `resolve` | `Package.resolved` holds the checksum of an earlier archive. | Remove the entry from `Package.resolved` and resolve again. |
