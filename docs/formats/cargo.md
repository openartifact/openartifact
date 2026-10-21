<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Cargo

Hosted Cargo registry with a sparse index, `config.json`, crate download, publish, yank/unyank and search.

| | |
|---|---|
| Repo URL | `HOST/repos/REPO/` (sparse index at `HOST/repos/REPO/index/`) |
| Repository types | hosted only. See [hosted, proxy, group](../repositories/hosted-proxy-group.md) |

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

## Endpoints

Relative to `HOST/repos/REPO`:

| Method and path | Purpose |
|---|---|
| `GET /config.json` | `{"dl": ".../repos/REPO/api/v1/crates", "api": ".../repos/REPO"}`, built from `base_url` |
| `GET /index/<crate>`, `/index/<p1>/<crate>`, `/index/<p1>/<p2>/<crate>` | sparse index entry, newline-delimited JSON |
| `GET /api/v1/crates/<crate>/<version>/download` | `.crate` file (`410` if yanked) |
| `PUT /api/v1/crates/new` | publish |
| `DELETE /api/v1/crates/<crate>/<version>/yank` | yank |
| `PUT /api/v1/crates/<crate>/<version>/unyank` | unyank |
| `GET /api/v1/crates?q=&per_page=` | search |

Known gap: cargo reads `config.json` from the index root (`HOST/repos/REPO/index/config.json`), but the server routes it only at `HOST/repos/REPO/config.json`; under `/index/` the name is treated as a crate and answers `404`.

## Publish and metadata

The body is the standard Cargo publish payload: u32-LE metadata length, metadata JSON, u32-LE crate length, `.crate` bytes. The JSON needs string `name` and `vers` (else `400`). Stored: `description`, `homepage`, `documentation`, `repository`, `license`, `readme`, `keywords`, `categories`, `authors`, plus the raw JSON (`deps`, `features`, `links`). The sha256 of the crate is the index `cksum`. Each index line carries `name`, `vers`, `deps`, `cksum`, `features`, `yanked`, `links`. Success is `200` with `{"warnings": []}`.
