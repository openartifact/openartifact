<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Nix

Hosted Nix binary cache: `nix copy --to` uploads store paths and Nix substitutes from the repository. Every `.narinfo` is signed with the repository's own ed25519 key.

| | |
|---|---|
| Format id | `nix` |
| Repo URL (substituter) | `HOST/repos/REPO` (`/nix-cache-info`, `/<hash>.narinfo`, `/nar/<file>`) |
| Repository types | hosted |
| Public key | `HOST/repos/REPO/public.key` (plain text, `name:base64`) |

Proxy and group repositories are not available for this format yet.

A component is one store path: its name is the store path name without the hash (`hello-2.12`) and its version is the 32 character store hash.

## Create a repository

Create a repository with format `nix` (web UI: format **Nix**; API or CLI: `format: nix`, see [repositories](../repositories/hosted-proxy-group.md)). The signing key is generated on first use; set `OPENARTIFACT_SECRET_KEY` first, see [Signing](#signing).

## Key management

The repository has one ed25519 signing key. Its public half is what clients put into `trusted-public-keys`:

```sh
curl -fsS HOST/repos/REPO/public.key
# openartifact-REPO-1:AbCd...=
```

Administrators manage the key on the repository **Settings** page or through the API (global manage permission, hosted nix repositories only):

| Call | Effect |
|---|---|
| `GET /api/v1/repositories/REPO/nix-key` | Returns `{key_name, public_key, created_at, updated_at}`; generates the key on first use. `public_key` is the `name:base64` string; the private half is never returned. |
| `POST /api/v1/repositories/REPO/nix-key` with an empty body | Generates a new key (rotation). |
| `POST /api/v1/repositories/REPO/nix-key` with `{"private_key": "name:base64", "key_name": "..."}` | Imports a key made with `nix-store --generate-binary-cache-key NAME secret.key public.key` (`private_key` is the content of `secret.key`, 64 bytes encoded as base64; `key_name` is optional). |

Without `OPENARTIFACT_SECRET_KEY` both calls answer `409 secret_key_required`. Rotating or importing a key re-signs every `.narinfo` immediately, because they are signed when served; every client has to add the new public key to `trusted-public-keys`.

## Client setup

`internal/clientsetup` produces these snippets (the web UI shows them on the repository page):

```ini
# /etc/nix/nix.conf
extra-substituters = HOST/repos/REPO
extra-trusted-public-keys = openartifact-REPO-1:AbCd...=   # output of: curl -fsS HOST/repos/REPO/public.key
```

For a private repository Nix reads the token from a netrc file, for downloads and for uploads (the user name is ignored, the token is the password; `machine` is the host name without the port):

```sh
# /etc/nix/netrc
machine HOST login oa password TOKEN
```

```ini
# /etc/nix/nix.conf
netrc-file = /etc/nix/netrc
```

Without editing `nix.conf`, for a single command:

```sh
nix build --substituters HOST/repos/REPO --option require-sigs true \
  --option extra-trusted-public-keys "$(curl -fsS HOST/repos/REPO/public.key)" /nix/store/HASH-NAME
```

`require-sigs true` makes Nix refuse any path whose `.narinfo` is not signed by a trusted key.

## Publish

```sh
nix copy --to 'https://HOST/repos/REPO' /nix/store/HASH-NAME
```

Needs write permission on `REPO` ([RBAC](../security/rbac.md)); the token comes from the netrc file above. Nix first reads `/nix-cache-info`, asks for `/<hash>.narinfo` (404 means missing), uploads the NAR `PUT /nar/<hash>.nar.xz` and finally `PUT /<hash>.narinfo`. A NAR is kept in a staging area until its `.narinfo` arrives; staged NARs without a narinfo are removed by the cleanup worker.

### Accepted options

| Option | Accepted values |
|---|---|
| `compression` | `none`, `xz` (default), `zstd`, `bzip2`, `gzip`, `br`; the NAR is stored and served as uploaded, the `.narinfo` names the extension |
| `write-nar-listing` | accepted; the `.ls` listing is discarded |
| `index-debug-info` | accepted; the debug files are discarded |

## Signing

Each repository has its own ed25519 key (default name `openartifact-REPO-1`). The private key is sealed with AES-256-GCM under `OPENARTIFACT_SECRET_KEY` and never leaves the database. `.narinfo` files are signed when served, with the key that is current at that moment: rotation therefore takes effect for every path at once, no regeneration needed. The `Sig:` lines an uploader supplied (for example from a build host key) are preserved next to the repository's signature. Without `OPENARTIFACT_SECRET_KEY` the `.narinfo` files are served with the uploader's signatures only (unsigned by the repository) and the key API answers `409`. See [signing](../security/signing.md#nix-narinfo-signing-key).

## Immutability and conflicts

A store path is identified by its store hash. Uploading a path that already exists with the same `NarHash` succeeds and changes nothing (idempotent, so repeated `nix copy` runs are safe). The same store hash with a different `NarHash` answers `409 Conflict`: delete the path first (it goes to the [trash](../operations/trash.md), kind `nix_path`) if it really has to be replaced.

## Limits and notes

- A NAR file may be up to 16 GiB (it is streamed to a temporary file while its hash is checked, never held in memory) and counts against the repository [quota](../repositories/cleanup-and-quotas.md); a NAR is checked against the SHA-256 in its file name, and a narinfo against the NAR it names (`FileHash`, `FileSize`, `URL`). A narinfo may be sent with `Content-Encoding` gzip, xz, zstd or bzip2 (`narinfo-compression`).
- A NAR is hosted once per repository however many paths share it. Range requests are not supported.
- The repository is not served under an isolated prefix: always use `HOST/repos/REPO` as the substituter URL, without a trailing slash.
- Package downloads are counted per store path.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `error: ... is not signed by any of the keys in trusted-public-keys` | The client lacks the repository key (or the key was rotated): re-run `curl -fsS HOST/repos/REPO/public.key` and add it. |
| `HTTP error 401` on `nix copy --to` or on a private substituter | Missing or wrong netrc: `machine` must be the host name without the port, the password the API token, and `netrc-file` set in `nix.conf`. |
| `HTTP error 403` on upload | The token has no write permission on `REPO`. |
| `409 Conflict` on upload | The store hash exists with a different `NarHash` (see above). |
| `409 secret_key_required` from `nix-key` | Set `OPENARTIFACT_SECRET_KEY` on the server. |
| Substitution silently falls back to building | The repository is not in `substituters`, or the path is absent: check `curl -I HOST/repos/REPO/HASH.narinfo`. |
