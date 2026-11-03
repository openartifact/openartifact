<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Debian (APT)

Use a Debian repository to publish `.deb` packages for apt, mirror an upstream archive with a proxy, or combine repositories in a group. The server generates `dists/` indexes and `Release` files and can sign them with GPG.

| Item | Value |
|---|---|
| Repo URL | `HOST/repos/REPO` (suites at `dists/<distribution>/`, packages under `pool/`) |
| Format id | `deb` |
| Repository types | hosted, proxy, group |
| Default upstream (proxy) | none; `upstream_url` is required |

## Client setup

Fetch the repository key first, then add the source and update. The web UI connect panel shows the same commands with your own URL.

```sh
curl -fsSL HOST/repos/REPO/public.key | sudo gpg --dearmor -o /etc/apt/keyrings/REPO.gpg
echo "deb [signed-by=/etc/apt/keyrings/REPO.gpg] HOST/repos/REPO stable main" | sudo tee /etc/apt/sources.list.d/REPO.list
sudo apt update
```

A private repository needs credentials. Put them in `/etc/apt/auth.conf.d/` for the host, with login `<USERNAME>` and the token as password:

```
machine HOST login <USERNAME> password <TOKEN>
```

`public.key`, `InRelease` and `Release.gpg` exist only when a signing key applies to the repository. Without one they answer `404`. Mark the source `[trusted=yes]` only on networks you control. See [Signing](#signing).

## Publish

<a id="upload-api"></a>

Upload a `.deb` with `POST /api/v1/repositories/REPO/debs`. You need write permission on `REPO` ([RBAC](../security/rbac.md)).

```sh
curl -u <USERNAME>:<TOKEN> -F file=@my-package_1.0_amd64.deb -F distribution=stable -F component=main \
  HOST/api/v1/repositories/REPO/debs
```

`/packages` is accepted too and dispatches to the Debian handler for `deb` repositories.

| Form field | Default | Notes |
|---|---|---|
| `file` | required | the `.deb` |
| `distribution` | `stable` | `^[A-Za-z0-9][A-Za-z0-9._\-]*$` |
| `component` | `main` | same pattern |

- Success returns `{"success": true, "data": {id, name, version, architecture, sha256, blob_id, size, distribution, component}}`.
- Name, version and architecture come from the control file of the `.deb`. They must match `^[a-z0-9][a-z0-9+.\-]+$`, `^[A-Za-z0-9][A-Za-z0-9.+~:\-]*$` and `^[a-z0-9][a-z0-9\-]*$`.
- An unreadable `.deb` is `400 invalid deb package`.
- The limit is 500 MiB (`413 file exceeds maximum size limit`). The [quota](../repositories/cleanup-and-quotas.md) is enforced.
- The same SHA-256 in the repository, or an existing `name_version_arch.deb` (epoch ignored), is `409 CONFLICT` with `data.duplicate: true`. Packages are immutable.
- A non-`deb` repository is `400`. A proxy repository is `405`.
- `DELETE /api/v1/repositories/REPO/debs/<id>` deletes one package by id, only if it belongs to `REPO`.
- There is no `/repos/REPO/upload` route. `POST` or `PUT` there answers `404` with a message naming the correct endpoint.

## Install and consume

Clients read the generated layout, relative to `HOST/repos/REPO`:

| Path | Notes |
|---|---|
| `dists/<dist>/Release` | `Origin` and `Label` are `openartifact`; `Suite` and `Codename` are the distribution; MD5Sum, SHA1, SHA256 |
| `dists/<dist>/InRelease`, `Release.gpg` | only when signing is configured, otherwise `404` |
| `dists/<dist>/<comp>/binary-<arch>/Packages`, `.gz` | one per component and architecture; no `.xz` is built, apt uses the gzip index |
| `pool/<comp>/<prefix>/<package>/<file>.deb` | package download |

- A background worker regenerates the indexes 500 ms after an upload or delete. Until it has run, `Release` and `Packages*` are served straight from the database.
- Architecture `all` packages are listed in every real architecture index. A distribution with only `all` packages gets `binary-all`.
- Package files are found by file name, honour quarantine and are served with `Content-Disposition`.

## Signing

Set `GPG_ENABLED=true`, `GPG_KEY_PATH` and `GPG_PASSPHRASE` to sign `InRelease` and `Release.gpg`; see [signing](../security/signing.md). A key managed per repository or globally works too, and the server writes `public.key` whenever a key applies. A key that is activated or removed after uploads re-signs the repository straight away.

- Without a key apt refuses the repository unless the source line is marked `[trusted=yes]`.
- `Release.gpg` is a standard ASCII-armored detached signature. Check it with `gpgv --keyring KEYRING Release.gpg Release`.
- Individual `.deb` files are not signed. apt verifies them through the checksums in the signed `Release`.

If an older `Release.gpg` fails to verify, see [upgrading](../getting-started/upgrading.md).

## Proxy

Create a proxy with `repo_type: "proxy"` and an `upstream_url`. It mirrors `dists/` and `pool/` from the upstream. Point the source line at `HOST/repos/REPO`.

- **Immutable files.** `.deb` and `.udeb` files below a `pool/` directory are cached as immutable. In a flat layout they are treated like indexes.
- **Other files.** Indexes and `Release` are served from cache until `cache_ttl_seconds` (default 1 hour), then revalidated.
- **Signatures.** The upstream's signed `InRelease` passes through unchanged, so clients must trust the upstream's key.
- **Failures.** Negative caching and serve-stale are described in [hosted, proxy, group](../repositories/hosted-proxy-group.md).

### Minimum age and curation

Curation applies to downloads. Minimum age has no effect, because Debian indexes carry no publish date; see [minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [package curation](../repositories/hosted-proxy-group.md#package-curation).

- **Names.** Rules match the package name and version as shown in the file name `<name>_<version>_<arch>.deb`, without an epoch.
- **Blocked download.** A blocked `.deb` or `.udeb` answers `403` with the reason, even when cached.
- **Not gated.** `dists/` indexes are served as the upstream has them, so apt still lists a blocked package. Source files are not gated either.

## Group

A Debian group is read-only. It publishes merged `Release`, `InRelease`, `Release.gpg` and `Packages.gz` for each distribution at least one member has, signed with the server key. It serves `pool/` files from the member that owns them.

Use it like a hosted repository, with the key from the group's `public.key`:

```sh
curl -fsSL HOST/repos/GROUP/public.key | sudo gpg --dearmor -o /etc/apt/keyrings/GROUP.gpg
echo "deb [signed-by=/etc/apt/keyrings/GROUP.gpg] HOST/repos/GROUP bookworm main" | sudo tee /etc/apt/sources.list.d/GROUP.list
```

- A package version offered by several members is listed once, from the earliest member.
- A package name held by a hosted member is not offered from proxy members, unless the group has `merge_proxy_versions`; see [hosted members win](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name).
- Asking for a distribution that no member has is `404`.
- Debian `by-hash`, `Contents`, `Sources` and translations are not merged or served.

Merging rules, regeneration and access control: [Deb, RPM and pacman groups](../repositories/hosted-proxy-group.md#deb-rpm-and-pacman-groups).

## Limits and behaviour

- **Upload size.** 500 MiB per `.deb`.
- **Index files.** Generated files with identical content share one stored blob. A replaced blob is deleted only once no repository or package references it.
- **Caching.** Cache-Control is `public, max-age=N` for anonymous reads of a public repository and `private, no-cache` otherwise.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `NO_PUBKEY` or "not signed" on `apt update` | The key file is missing or the repository has no signing key. Fetch `public.key` into the `signed-by` path, or configure signing. |
| `404` on `public.key`, `InRelease` or `Release.gpg` | No signing key applies. Add one or use `[trusted=yes]` on a trusted network. |
| `401` on `apt update` | Private repository. Add the host to `/etc/apt/auth.conf.d/` with the token as password. |
| `400 invalid deb package` | The `.deb` control file is unreadable or has an invalid name, version or architecture. |
| `409 CONFLICT` on upload | The same SHA-256 or `name_version_arch` exists. Delete it first, or bump the version. |
| `405` on upload | The repository is a proxy or group. Upload to a hosted repository. |
| A new upload is not listed yet | Indexes regenerate about 500 ms after the change. Run `apt update` again. |
| A group does not offer a public package | A hosted member holds that name. Set `merge_proxy_versions` to `true` to merge. |
