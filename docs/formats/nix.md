<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Nix

Use a Nix repository as a binary cache. A hosted repository takes `nix copy --to` uploads and Nix substitutes from it, with every `.narinfo` signed by the repository's own ed25519 key. A proxy mirrors an upstream cache, and a group combines both.

| Item | Value |
|---|---|
| Repo URL (substituter) | `HOST/repos/REPO` (`/nix-cache-info`, `/<hash>.narinfo`, `/nar/<file>`) |
| Format id | `nix` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | `https://cache.nixos.org` |
| Public key | `HOST/repos/REPO/public.key` (plain text, `name:base64`; hosted and group repositories) |

A component is one store path. Its name is the store path name without the hash (`hello-2.12`) and its version is the 32 character store hash.

## Create a repository

Create a repository with format `nix`. In the web UI choose **Nix**. In the API or CLI use `format: nix`; see [repositories](../repositories/hosted-proxy-group.md). The signing key is generated on first use. Set `OPENARTIFACT_SECRET_KEY` first; see [Signing](#signing).

## Client setup

Add the substituter and the repository's public key to `/etc/nix/nix.conf`. The web UI shows the same snippets on the repository page.

```ini
# /etc/nix/nix.conf
extra-substituters = HOST/repos/REPO
extra-trusted-public-keys = openartifact-REPO-1:AbCd...=   # output of: curl -fsS HOST/repos/REPO/public.key
```

Use the repository URL without a trailing slash.

For a private repository Nix reads the token from a netrc file, for downloads and uploads. The user name is ignored and the token is the password. `machine` is the host name without the port.

```sh
# /etc/nix/netrc
machine HOST login <USERNAME> password <TOKEN>
```

```ini
# /etc/nix/nix.conf
netrc-file = /etc/nix/netrc
```

For a single command, without editing `nix.conf`:

```sh
nix build --substituters HOST/repos/REPO --option require-sigs true \
  --option extra-trusted-public-keys "$(curl -fsS HOST/repos/REPO/public.key)" /nix/store/HASH-NAME
```

`require-sigs true` makes Nix refuse any path whose `.narinfo` is not signed by a trusted key.

## Publish

Upload a store path to a hosted repository:

```sh
nix copy --to 'https://HOST/repos/REPO' /nix/store/HASH-NAME
```

You need write permission on `REPO` ([RBAC](../security/rbac.md)). The token comes from the netrc file above.

Nix reads `/nix-cache-info`, asks for `/<hash>.narinfo` (`404` means missing), uploads the NAR with `PUT /nar/<hash>.nar.xz`, and finally uploads `PUT /<hash>.narinfo`.

- **Order matters.** The NAR must be uploaded before its narinfo. A narinfo sent first is `409`.
- **Staging.** A NAR waits in a staging area until its narinfo arrives. The cleanup worker removes staged NARs that never get one.
- **Immutable.** A store path is identified by its store hash. Uploading a path that exists with the same `NarHash` succeeds and changes nothing, so repeated `nix copy` runs are safe.
- **Conflict.** The same store hash with a different `NarHash` is `409 Conflict`. Delete the path first, which moves it to the [trash](../operations/trash.md) (kind `nix_path`), if it must be replaced.

Accepted `nix copy` options:

| Option | Accepted values |
|---|---|
| `compression` | `none`, `xz` (default), `zstd`, `bzip2`, `gzip`, `br` |
| `write-nar-listing` | accepted; the `.ls` listing is discarded |
| `index-debug-info` | accepted; the debug files are discarded |

The NAR is stored and served as uploaded, and the `.narinfo` names the extension.

## Install and consume

Substitution needs only the client setup above. Nix reads `/nix-cache-info`, the `.narinfo` of a store path and then its NAR.

- **Check a path.** `curl -I HOST/repos/REPO/HASH.narinfo` answers `200` when the path exists.
- **Downloads are counted per store path.**
- **No ranges.** Range requests are not supported.
- **Shared NARs.** A NAR is hosted once per repository, however many paths share it.

## Signing

<a id="key-management"></a>

Each repository has its own ed25519 key (default name `openartifact-REPO-1`). Its public half is what clients put into `trusted-public-keys`:

```sh
curl -fsS HOST/repos/REPO/public.key
# openartifact-REPO-1:AbCd...=
```

The private key is encrypted under `OPENARTIFACT_SECRET_KEY` and never leaves the database. Without `OPENARTIFACT_SECRET_KEY` there is no repository key: `public.key` answers `404`, the key API answers `409 secret_key_required`, and `.narinfo` files are served with the uploader's `Sig:` lines only.

`.narinfo` files are signed when served, with the key that is current at that moment. Rotation or import therefore re-signs every path at once, and every client must add the new public key to `trusted-public-keys`. The `Sig:` lines an uploader supplied, for example from a build host key, are kept next to the repository's signature. See [signing](../security/signing.md#nix-narinfo-signing-key).

Administrators manage the key on the repository **Settings** page or through the API. It needs the global manage permission and works for hosted and group nix repositories. A proxy has no key.

| Call | Effect |
|---|---|
| `GET /api/v1/repositories/REPO/nix-key` | Returns `{key_name, public_key, created_at, updated_at}` and generates the key on first use. The private half is never returned. |
| `POST /api/v1/repositories/REPO/nix-key` with an empty body | Generates a new key (rotation). |
| `POST /api/v1/repositories/REPO/nix-key` with `{"private_key": "<name>:<base64>", "key_name": "..."}` | Imports a key. `key_name` is optional and renames the key. |

To import, generate a key pair with `nix-store --generate-binary-cache-key NAME secret.key public.key` and send the content of `secret.key` as `private_key`. It has the form `<name>:<base64>`.

## Proxy

Create a proxy with `repo_type: "proxy"`. The default `upstream_url` is `https://cache.nixos.org`. It is a read-only mirror of an upstream binary cache. Point `extra-substituters` at `HOST/repos/REPO`.

- **narinfo.** `/<hash>.narinfo` is cached for the repository TTL and served byte for byte. The upstream's `Sig:` lines pass through, so clients trust the upstream's key, not a key of this server. For the default upstream that is `cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=`.
- **What is cached.** A narinfo is cached only if it parses, its `StorePath` hash equals the requested hash and its `URL` is a clean relative path. Missing paths (`404`) are cached briefly. Upstream errors are not cached.
- **`/nix-cache-info`.** Passed through the cache like other metadata.
- **NARs.** `/nar/...` files are cached as immutable. The proxy records the `FileHash` and `FileSize` from the narinfo and stores a NAR only when its body matches both. A mismatch is refused and not cached.
- **NAR without narinfo.** A NAR whose narinfo was never fetched through the proxy is refused unless its file name is the nix32 SHA-256 of its own content.
- **NAR file names.** The name need not be the file hash. `cache.nixos.org` names zstd NARs after the `NarHash`, and the narinfo is authoritative.
- **Size limits.** Metadata (narinfo, `nix-cache-info`, `.ls`) is capped at the proxy metadata size limit. A NAR larger than the proxy body limit is refused, so very large store paths cannot be substituted through a proxy. Fetch them from the upstream directly.
- **Read-only.** Uploads answer `405`.

### Minimum age and curation

Neither applies to Nix. The API accepts `curation_mode` and `min_age_hours` on a nix proxy, but they have no effect: the proxy serves everything the upstream has. See [minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [package curation](../repositories/hosted-proxy-group.md#package-curation) for formats where they work.

## Group

A group repository (`repo_type: group`) serves a read-only binary cache over ordered members, which are hosted and proxy nix repositories. Writes answer `405`.

- **Member order.** Members are asked hosted first, in a stable order within each kind. The first member that has the file answers. A member's `404` is passed over.
- **Member failures.** If a member failed and no other member has the file, the group answers `502 member repository error`. If no member failed and none has the file, it answers `404`.
- **Group endpoints.** `/nix-cache-info` and `/public.key` are answered by the group itself (`Priority: 30`, like a hosted repository). `/public.key` is the group's own key, managed with `GET` and `POST /api/v1/repositories/GROUP/nix-key`, the same calls as for a hosted repository. Without a key it answers `404`.
- **Signing hosted paths.** A narinfo from a hosted member gets the group's own signature added next to the member's signatures. Without `OPENARTIFACT_SECRET_KEY` it is served as is.
- **Proxy paths.** A narinfo from a proxy member passes through unchanged. The group does not sign what it did not vouch for, so clients must trust the upstream's key for those paths.
- **Other paths.** NARs and other paths come from the first member that has them. `HEAD` on a narinfo works.

Clients trust the group's public key and, when the group has proxy members, the upstream's key too:

```ini
extra-substituters = HOST/repos/GROUP
extra-trusted-public-keys = openartifact-GROUP-1:AbCd...= cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=
```

## Limits and behaviour

- **NAR size.** A NAR may be up to 16 GiB. It is streamed to a temporary file while its hash is checked, and counts against the repository [quota](../repositories/cleanup-and-quotas.md).
- **Checks.** A NAR is checked against the SHA-256 in its file name. A narinfo is checked against the NAR it names (`FileHash`, `FileSize`, `URL`).
- **Compressed narinfo.** A narinfo may be sent with `Content-Encoding` gzip, xz, zstd or bzip2.
- **Status codes.** `409` for a narinfo before its NAR or a different `NarHash`, `409 secret_key_required` from the key API, `404` for `public.key` without a signing key, `405` for writes to proxy or group, `502` for a group with a failed member.
- **Delete.** A deleted path goes to the [trash](../operations/trash.md).

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `error: ... is not signed by any of the keys in trusted-public-keys` | The client lacks the repository key, or it was rotated. Run `curl -fsS HOST/repos/REPO/public.key` again and add it. |
| `HTTP error 401` on `nix copy --to` or a private substituter | Wrong netrc. `machine` must be the host name without the port, the password the API token, and `netrc-file` set in `nix.conf`. |
| `HTTP error 403` on upload | The token has no write permission on `REPO`. |
| `409 Conflict` on upload | The store hash exists with a different `NarHash`, or the narinfo arrived before its NAR. Delete the path, or upload the NAR first. |
| `409 secret_key_required` from `nix-key` | Set `OPENARTIFACT_SECRET_KEY` on the server. |
| `404` on `public.key` | The repository has no signing key. Set `OPENARTIFACT_SECRET_KEY` and call `GET .../nix-key`. |
| Group answers `502 member repository error` | A member failed and no other has the file. Check the failing member's upstream. |
| Substitution silently falls back to building | The repository is not in `substituters`, or the path is absent. Check `curl -I HOST/repos/REPO/HASH.narinfo`. |
