<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Cargo

Cargo registry (hosted, proxy or group) with a sparse index, `config.json`, crate download, publish, yank/unyank and search.

| | |
|---|---|
| Repo URL | `HOST/repos/REPO/` (sparse index at `HOST/repos/REPO/index/`) |
| Repository types | hosted, proxy, group. See [hosted, proxy, group](../repositories/hosted-proxy-group.md) |

## Client setup

`internal/clientsetup` produces:

```toml
# ~/.cargo/config.toml
[registries.REPO]
index = "sparse+HOST/repos/REPO/index/"
token = "<TOKEN>"
```

```sh
cargo publish --registry REPO
```

The server accepts `Authorization: Bearer <TOKEN>` or Basic with the token as password; there is no bare-token form. Cargo sends `token` verbatim as the header value, so if publish answers `401` set `token = "Bearer <TOKEN>"`. See [authentication](../security/authentication.md).

## Proxy and group repositories

- **Proxy:** mirrors a sparse registry (default `https://index.crates.io`). Replace crates.io with it in `~/.cargo/config.toml`, or use it as an alternative registry:

  ```toml
  [source.crates-io]
  replace-with = "oa"

  [source.oa]
  registry = "sparse+HOST/repos/PROXY/index/"
  ```

  `config.json` is generated (`dl`/`api` point at the proxy); index files are cached with the repository TTL and served stale when the upstream is down; crate files are fetched from the upstream's `dl` template, verified against the index `cksum` and cached immutably. Publishing to a proxy is `405`. See [cargo proxies](../repositories/hosted-proxy-group.md#cargo-proxies).
- **Group:** an ordered list of hosted and proxy cargo members behind one index URL. Index files are merged (union by version, first member wins), crates come from the first member that has them. Publishing to a group is `405`: publish to a hosted member and read through the group. See [group repositories](../repositories/hosted-proxy-group.md#group-repositories).

Private proxies and groups work like private hosted repositories: `config.json` carries `"auth-required": true` and cargo needs the token (`token = "Bearer <TOKEN>"`, see above).

## Endpoints

Relative to `HOST/repos/REPO`:

| Method and path | Purpose |
|---|---|
| `GET /index/config.json` (also `GET /config.json`) | `{"dl": ".../repos/REPO/api/v1/crates", "api": ".../repos/REPO"}`, built from `base_url`; a private repository adds `"auth-required": true` |
| `GET /index/<crate>`, `/index/<p1>/<crate>`, `/index/<p1>/<p2>/<crate>` | sparse index entry, newline-delimited JSON |
| `GET /api/v1/crates/<crate>/<version>/download` | `.crate` file; yanked versions are served too |
| `PUT /api/v1/crates/new` | publish |
| `DELETE /api/v1/crates/<crate>/<version>/yank` | yank |
| `PUT /api/v1/crates/<crate>/<version>/unyank` | unyank |
| `GET /api/v1/crates?q=&per_page=` | search, one entry per crate |

Cargo reads `config.json` from the root of the index it was given (`HOST/repos/REPO/index/config.json`).

## Private repositories

`config.json` of a private repository carries `"auth-required": true`, which makes cargo send the registry token with every request (index, download, search) instead of only after a `401`. A public repository is read anonymously and the key is absent.

## Crate names

Crate names are case-insensitive, as on crates.io: cargo lowercases them in index paths and download URLs, and lookups here compare them case-insensitively, so `Demo-Crate` published once is found as `demo-crate`. The index entry keeps the name as published. Publishing a name that differs from an existing crate only in case is refused (`409`).

## Yank

Yank sets `"yanked": true` in the index entry so that new dependency resolution skips the version; the `.crate` stays downloadable, so a `Cargo.lock` that already pins it keeps building. Unyank clears the flag.

## Search

`GET /api/v1/crates?q=<text>&per_page=<1..100, default 10>` matches crate name or description (substring, case-insensitive) and returns one entry per crate, ordered by name: `name`, `max_version`, `description` (of the newest version) and `downloads` (summed over all versions). `max_version` is the highest non-yanked stable version, else the highest non-yanked pre-release, else (everything yanked) the highest version, by SemVer precedence. `meta.total` is the number of crates matching the query, not the number returned in the page.

## Publish and metadata

The body is the standard Cargo publish payload: u32-LE metadata length, metadata JSON, u32-LE crate length, `.crate` bytes. The JSON needs string `name` and `vers`, validated before anything is stored (else `400` with `{"errors":[{"detail":"..."}]}`, the shape cargo prints):

- `name` starts with an ASCII letter or `_`, continues with ASCII letters, digits, `-` and `_`, and is at most 64 characters.
- `vers` is a full SemVer 2.0 version (`MAJOR.MINOR.PATCH`, optional `-pre-release` and `+build`), at most 100 characters.

A version is published once: a second publish of an existing `name`/`vers` (build metadata ignored, so `1.0.0+a` equals `1.0.0`) answers `409` with the same JSON error shape. This is decided from the metadata alone, before the crate part is read. Two concurrent publishes of one version also end in `409` for the loser, never `500`.

The declared lengths are capped at 1 MiB for the metadata and 128 MiB for the crate (`413` above). The server does not allocate the declared length up front: buffers grow as bytes arrive, and a body shorter than declared is a `400`.

Stored: `description`, `homepage`, `documentation`, `repository`, `license`, `readme`, `keywords`, `categories`, `authors`, plus the raw JSON (`deps`, `features`, `links`). The sha256 of the crate is the index `cksum`. Each index line carries `name`, `vers`, `deps`, `cksum`, `features`, `yanked`, `links`. Success is `200` with `{"warnings": []}`.
