<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Cargo

Use a Cargo repository as a private crate registry with a sparse index: `cargo publish` uploads to it and `cargo` resolves and downloads crates from it. A proxy mirrors crates.io (or another sparse registry), and a group merges hosted and proxy repositories.

| Item | Value |
|---|---|
| Registry URL | `sparse+HOST/repos/REPO/index/` |
| Format id | `cargo` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | `https://index.crates.io` |

## Client setup

Create an API token as described in [authentication](../security/authentication.md). The web UI connect panel shows the same commands with your own URL.

```toml
# ~/.cargo/config.toml
[registries.REPO]
index = "sparse+HOST/repos/REPO/index/"
token = "Bearer <TOKEN>"
```

Cargo sends `token` verbatim as the `Authorization` header, so the value carries the `Bearer ` prefix. The server accepts `Authorization: Bearer <TOKEN>`, or Basic with the token as the password. A bare token without a prefix is not accepted.

Cargo reads `config.json` from the root of the index it is given, `HOST/repos/REPO/index/config.json`.

A private repository, proxy or group sets `"auth-required": true` in `config.json`. Cargo then sends the token with every request (index, download, search) instead of only after a `401`. A public repository is read anonymously and the key is absent.

## Publish

```sh
cargo publish --registry REPO
```

The body is the standard Cargo publish payload: a 32-bit little-endian metadata length, the metadata JSON, a 32-bit little-endian crate length, then the `.crate` bytes. The server validates the metadata before it stores anything. An invalid payload answers `400` with `{"errors":[{"detail":"..."}]}`, the shape cargo prints.

- `name` starts with an ASCII letter or `_`, continues with ASCII letters, digits, `-` and `_`, and has at most 64 characters.
- `vers` is a full SemVer 2.0 version (`MAJOR.MINOR.PATCH`, optional `-pre-release` and `+build`) of at most 100 characters.

Success is `200` with `{"warnings": []}`.

A version is published once. A second publish of an existing `name` and `vers` answers `409` with the same JSON error shape. Build metadata is ignored, so `1.0.0+a` equals `1.0.0`. The server decides this from the metadata alone, before it reads the crate part. If two publishes of one version run at the same time, the loser gets `409`, never `500`.

Publishing a name that differs from an existing crate only in case is refused with `409`.

The declared lengths are capped at 1 MiB for the metadata and 128 MiB for the crate; larger answers `413`. A body shorter than declared is `400`. The server does not reserve the declared length up front.

The server stores `description`, `homepage`, `documentation`, `repository`, `license`, `readme`, `keywords`, `categories`, `authors`, and the raw JSON (`deps`, `features`, `links`). The SHA-256 of the crate is the index `cksum`. Each index line carries `name`, `vers`, `deps`, `cksum`, `features`, `yanked`, `links`.

The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) applies on publish (`507` over quota). A yanked crate keeps counting.

<a id="yank"></a>

### Yank and unyank

```sh
cargo yank --registry REPO --version 1.0.0 CRATE
cargo yank --registry REPO --version 1.0.0 --undo CRATE
```

Yank sets `"yanked": true` in the index entry, so new dependency resolution skips the version. The `.crate` stays downloadable, so a `Cargo.lock` that already pins the version keeps building. Unyank clears the flag.

## Install and consume

Add a dependency with `registry = "REPO"`, or use the repository in place of crates.io (see [Proxy](#proxy)). Paths are relative to `HOST/repos/REPO`.

| Method and path | Purpose |
|---|---|
| `GET /index/config.json` (also `GET /config.json`) | Registry configuration with `dl` and `api` |
| `GET /index/<crate>`, `/index/<p1>/<crate>`, `/index/<p1>/<p2>/<crate>` | Sparse index entry, newline-delimited JSON |
| `GET /api/v1/crates/<crate>/<version>/download` | `.crate` file; yanked versions are served too |
| `PUT /api/v1/crates/new` | publish |
| `DELETE /api/v1/crates/<crate>/<version>/yank` | Yank |
| `PUT /api/v1/crates/<crate>/<version>/unyank` | Unyank |
| `GET /api/v1/crates?q=&per_page=` | Search, one entry per crate |

`config.json` has the form `{"dl": ".../repos/REPO/api/v1/crates", "api": ".../repos/REPO"}`. Both URLs are absolute, on the request origin or on `BASE_URL` when it is set, because `dl` is a URL template that cargo does not resolve against anything.

### Crate names

Crate names are case-insensitive, as on crates.io. Cargo lower-cases them in index paths and download URLs, and lookups compare them case-insensitively. A crate published as `Demo-Crate` is found as `demo-crate`. The index entry keeps the name as published.

### Search

`GET /api/v1/crates?q=<text>&per_page=<1..100, default 10>` matches the crate name or description (substring, case-insensitive). It returns one entry per crate, ordered by name:

- `name`
- `max_version`: the highest non-yanked stable version, else the highest non-yanked pre-release, else (everything yanked) the highest version, by SemVer precedence
- `description` of the newest version
- `downloads`, summed over all versions (counts are written in batches, so a download shows up within `DB_DOWNLOAD_COUNT_FLUSH_INTERVAL`, 5 seconds by default)

`meta.total` is the number of crates that match the query, not the number in the page.

## Proxy

Create a proxy with `repo_type: "proxy"`. Without `upstream_url` the default is `https://index.crates.io`. A cargo proxy is a sparse registry, not a path mirror. Point cargo at it with `sparse+HOST/repos/REPO/index/`, or replace the crates.io source:

```toml
# ~/.cargo/config.toml
[source.crates-io]
replace-with = "oa"

[source.oa]
registry = "sparse+HOST/repos/REPO/index/"
```

- **`config.json`** is generated and never fetched from the upstream. `dl` and `api` point at the proxy.
- **Index files** use the usual `1/`, `2/`, `3/<c>/` and `<ab>/<cd>/` lower-case layout. The server fetches them from the upstream and checks that they are well-formed newline-delimited JSON. An HTML error page served with `200` is `502` and is not cached.
- **Index caching.** Index files are cached with the repository TTL and served stale when the upstream is down. The server answers `If-None-Match` with `304`. A path that does not follow the layout is `404` without an upstream request.
- **Crate files** are fetched from the URL the upstream's own `config.json` `dl` template yields. The server first checks that the version is listed in the crate's index file. A version it does not list is `404` and is never fetched.
- **Crate check.** The bytes must match the SHA-256 `cksum` of the index entry, or the server refuses them with `502` and does not cache them. Cached crate files never change. Yanked versions stay downloadable, as upstream.
- **Credentials.** Upstream Basic credentials go only to the configured upstream host, never to the host a `dl` template points at. Registries that need cargo's bare-token `Authorization` header are not supported as upstreams.
- **Writes.** `cargo publish`, `yank` and `unyank` answer `405` (`Allow: GET, HEAD`). `cargo search` is not proxied (`404`).
- **Upstream down.** Builds keep working from the cache. Cached crates are always served. An index file is served stale (`Warning: 110`) for up to `max_stale_seconds` past its expiry. A crate never requested before answers `502`.

### Minimum age and curation

Rules and `min_age_hours` follow [Minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [Package curation](../repositories/hosted-proxy-group.md#package-curation). The format-specific parts:

- **Names.** Rules name a crate lower-cased with `_` as `-` (`serde-json`) and a version as the index shows it.
- **Minimum age date.** The server uses `pubtime` of the index entry (RFC 3339). An entry without a `pubtime` is not held back.
- **Hidden version.** The entry leaves the index file. Its `.crate` answers `403`. A cached crate is still served when only the minimum age hides it. Curation refuses it even when cached.
- **Hidden crate.** A crate none of whose versions is old enough, or allowed, answers `404`.
- **Resolution.** The index has no "latest" pointer, so cargo resolves to the newest remaining version that satisfies the requirement. A version pinned in a `Cargo.lock` cannot be fetched while it is hidden.
- **Freshness.** The `ETag` of the index changes with the threshold, and with the clock every 5 minutes. A version that has aged enough appears without a stale `304`.

## Group

A group puts hosted and cargo proxy repositories behind one index URL. Clients use `sparse+HOST/repos/GROUP/index/`.

- **`config.json`** is the group's own. `dl` and `api` point at the group.
- **Index files** are merged across the readable hosted and proxy members. The union of versions is listed, ignoring build metadata when comparing. The earliest member wins a version that several members list. Every other line stays untouched.
- **Crate downloads** come from the member whose index entry won the merge.
- **Hosted members win a crate name.** If a readable hosted member holds a crate, its index file lists only hosted versions and `.crate` downloads come only from hosted members. Setting `merge_proxy_versions` to `true` turns this off. See [hosted members win a package name](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).
- **Failing members.** A member that fails while another contributes is ignored. With no contributor the group answers `404`, or `502` if a member failed or served something that is not an index file.
- **Minimum age and curation.** A group lists and serves what its proxy members serve, so hidden versions stay hidden.
- **Writes.** Publishing to a group is `405`. Publish to a hosted member and read through the group.

## Limits and behaviour

- **Publish limits:** 1 MiB metadata, 128 MiB crate (`413` above).
- **Caching:** the sparse index is served with `Cache-Control: public, max-age=60` for anonymous reads of a public repository, `private, no-cache` otherwise. `config.json` follows the same rule with `max-age=300`. Both carry an `ETag`.
- **Status codes:** `400` invalid publish payload, `409` existing version or case-only name clash, `413` too large, `507` over quota.
- **Yank:** the `.crate` stays downloadable.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `401` on publish or index fetch | The token is missing or has no `Bearer ` prefix. | Set `token = "Bearer <TOKEN>"` in `~/.cargo/config.toml`. |
| `409` on publish | The version exists, or the name differs from an existing crate only in case. | Publish a new version. |
| `502` from a proxy for an index file | The upstream returned something that is not an index. | Check `upstream_url`. |
| `502` from a proxy for a crate | The download did not match the index `cksum`. | Retry; if it persists the upstream is serving a different file. |
| `403` for a crate on a proxy | Minimum age or curation hides the version. | Wait until it is old enough, or change the rules. |
| `405` on publish | The repository is a proxy or group. | Publish to a hosted repository. |
