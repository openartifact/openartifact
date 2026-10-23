<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Signing and release verification

Two separate topics: the GPG key the server uses to sign APT and RPM repository metadata, and how to verify the signatures on OpenArtifact's own release binaries and container image.

## Repository metadata signing (GPG)

GPG keys sign the metadata of Debian and RPM repositories and the manifest of sealed [release bundles](../repositories/release-bundles.md). Uploaded packages are stored as-is: OpenArtifact does not sign or re-sign `.deb`/`.rpm` files, only the index files that list them. Other formats are not signed.

There are two ways to provide a key, and they combine:

- **Managed keys** (recommended): generate or import keys with the admin API, the `oa gpg` CLI or the **Admin → GPG keys** page. Private keys are stored in the database, sealed with AES-256-GCM under `OPENARTIFACT_SECRET_KEY`, and are never returned by the API. Managed keys need no restart and can be rotated at runtime.
- **File / environment key**: the ASCII-armored private key from `gpg.keyPath`, loaded at startup. It shows up as a read-only entry (`id: config`) in the key list and is never stored in the database.

### Which key signs a repository

For every hosted Debian or RPM repository, the first match wins:

1. the key **activated for that repository**,
2. the **global active** key (at most one),
3. the file / environment key, when one is configured.

No match means the repository is unsigned. Activating, deactivating or deleting a key re-renders the metadata of every affected repository, so their signatures follow the change without waiting for the next upload. If the signing key cannot be resolved (for example a database error) the metadata is **not** regenerated unsigned; the previous signed metadata stays in place and the regeneration is retried on the next change.

### Configuration

| YAML | Env | Meaning |
|---|---|---|
| `gpg.enabled` | `GPG_ENABLED` | load the file / environment key (default `false`). Managed keys sign whenever one is active, independent of this flag |
| `gpg.keyPath` | `GPG_KEY_PATH` | ASCII-armored **private** key file; only the first key in the file is used |
| `gpg.passphrase` | `GPG_PASSPHRASE` | passphrase if the file key is encrypted |
| `gpg.autoGenerate` | `GPG_AUTO_GENERATE` | at startup, when no key exists at all (no managed key, no file key), generate an RSA-4096 key and make it the global active key (default `false`) |
| (env only) | `OPENARTIFACT_SECRET_KEY` | required to **store** managed keys: importing, generating and `autoGenerate` are refused (or skipped with a warning) without it. Keep it stable: losing it makes the stored private keys unreadable |

`autoGenerate` is safe with several replicas: the "is there a key?" check and the insert run under a PostgreSQL advisory lock, so replicas starting together create exactly one key. The key is named `auto-generated`; export its public key afterwards and distribute it to clients.

If the file key cannot be read or decrypted the server logs `Failed to initialize the configured GPG signer` and keeps running, so check the startup log for `GPG signer initialized` (it prints the fingerprint). Keep the key file readable by the server only. See [../../.env.example](../../.env.example) and [../../openartifact.example.yaml](../../openartifact.example.yaml).

To create a key outside OpenArtifact and import it (or to use `gpg.keyPath`):

```sh
gpg --quick-generate-key 'OpenArtifact Repo <repo@example.com>' rsa4096 sign 0
gpg --armor --export-secret-keys <FINGERPRINT> > private.key
gpg --armor --export <FINGERPRINT> > public.key
```

### What gets signed

| Format | Signed files | Public key |
|---|---|---|
| Debian | `dists/<dist>/InRelease` (clear-signed) and `dists/<dist>/Release.gpg` (detached, armored) | `HOST/repos/REPO/public.key` |
| RPM | `repodata/repomd.xml.asc` (detached, armored) | `HOST/repos/REPO/public.key` |
| RPM package (opt-in) | the OpenPGP header signature inside each uploaded `.rpm`, when the repository's `sign_packages` is on | `HOST/repos/REPO/public.key` |
| Release bundle | the bundle manifest, as a detached armored signature | `GET /api/v1/admin/gpg/keys/{id}/export` |

Debian packages are not signed individually: apt trusts the signed `Release`/`InRelease`, whose checksums cover `Packages` and so every `.deb` (per-package `debsigs` signatures are rarely checked and are not produced). RPM packages are signed per file only when the repository opts in with `sign_packages`; that stores the signed file, not the upload, and does not re-sign existing packages when keys rotate. dnf's default `gpgcheck=1` needs it; see [RPM signing](../formats/rpm.md#signing-packages-sign_packages).

Signatures are SHA-256 OpenPGP signatures created whenever a repository's metadata is generated. `public.key` is generated together with the signature, from the key that signed it, and follows key changes (a repository whose key is removed stops serving it). Without a signer, `InRelease`, `Release.gpg`, `repomd.xml.asc` and `public.key` return 404. Ed25519 keys need a recent verifier (APT with `sqv`/GnuPG ≥ 2.1, DNF5/RPM ≥ 4.14 on current Fedora); pick `rsa4096` for old distributions.

### Client setup

APT (the repository line matches the one the UI produces):

```sh
curl -fsSL HOST/repos/REPO/public.key | sudo gpg --dearmor -o /etc/apt/keyrings/REPO.gpg
echo "deb [signed-by=/etc/apt/keyrings/REPO.gpg] HOST/repos/REPO stable main" | sudo tee /etc/apt/sources.list.d/REPO.list
sudo apt update
```

Credentials for private repositories are covered in [Debian](../formats/debian.md); for them download `public.key` with the same credentials.

DNF / YUM (`/etc/yum.repos.d/REPO.repo`):

```ini
[REPO]
name=REPO
baseurl=HOST/repos/REPO
enabled=1
repo_gpgcheck=1
gpgkey=HOST/repos/REPO/public.key
```

`repo_gpgcheck=1` verifies the signed metadata. Package signature checks (`gpgcheck`) use whatever signature each uploaded RPM already carries, since the server does not re-sign packages; add the vendor key to `gpgkey` or set `gpgcheck=0` for unsigned packages. Details: [Debian](../formats/debian.md), [RPM](../formats/rpm.md).

### Managing keys

All routes need the global `manage` permission (admin). Mutations are audited as `gpg_key.created`, `gpg_key.deleted`, `gpg_key.activated`, `gpg_key.deactivated` and `gpg_key.exported`.

| Route | Purpose |
|---|---|
| `GET /api/v1/admin/gpg/keys` | list keys plus the read-only `config` entry; `can_store_keys` is `false` without `OPENARTIFACT_SECRET_KEY` |
| `POST /api/v1/admin/gpg/keys` | import `{name?, private_key, passphrase?, activate?}`; a protected key is unlocked with the passphrase and stored without it (sealed) |
| `POST /api/v1/admin/gpg/keys/generate` | generate `{name?, algorithm?: "ed25519"\|"rsa4096" (default `ed25519`), user_name?, email?, comment?, activate?}` |
| `PUT /api/v1/admin/gpg/keys/{id}/activate` | `{repository?}`: make the key the global signer, or the signer of one hosted deb/rpm repository |
| `DELETE /api/v1/admin/gpg/keys/{id}/activate?repository=NAME` | stop signing globally / for that repository |
| `DELETE /api/v1/admin/gpg/keys/{id}?force=true` | delete; an active key needs `force=true` (otherwise `409 key_in_use`) |
| `GET /api/v1/admin/gpg/keys/{id}/export` | armored public key (`application/pgp-keys`) |

`activate` on import/generate defaults to *true only when nothing signs globally yet*, so adding a second key never silently changes the signer. Import rejects public-only, revoked, expired and non-signing keys. A key id is the UUID from the list, or `config` for the file / environment key (read-only: it can be exported but not deleted or activated per repository).

```sh
oa gpg generate --name release-2026 --algorithm ed25519        # first key becomes the global signer
oa gpg list
oa gpg export release-2026 --output public.key
oa gpg activate release-2026 --repository apt-prod             # per-repository signer
oa gpg delete old-key --force
```

**Rotation.** Generate or import the new key with `--no-activate`, publish its public key to clients next to the old one, then `oa gpg activate NEW`. Repositories are re-signed immediately; keep the old key published until clients have the new one, then delete it.

### Signing release bundles

Sealing a bundle with `{"gpg_key_id": "<key id | fingerprint | config>"}` signs a canonical JSON manifest (`openartifact.release-bundle/v1`: bundle name, version, description and its items ordered by digest). The armored detached signature is stored with the bundle: the bundle's `gpg_key_id` is the signer's fingerprint (it survives deleting the key) and `gpg_signature` the signature. Download both and verify:

```sh
curl -H "Authorization: Bearer $TOKEN" -o manifest.json     HOST/api/v1/bundles/NAME/VERSION/manifest
curl -H "Authorization: Bearer $TOKEN" -o manifest.json.asc HOST/api/v1/bundles/NAME/VERSION/manifest.asc
gpg --import public.key && gpg --verify manifest.json.asc manifest.json
```

See [release bundles](../repositories/release-bundles.md).

### Migrating from Nexus or Artifactory

The importer rebuilds `dists/` and `repodata/` instead of copying them, so they are signed with the key that applies to the repository. Clients must trust it instead of the old repository key: [migration](../migration/nexus-artifactory.md).

## Verifying OpenArtifact releases

Tagged releases (`v*.*.*`, workflow `.github/workflows/release.yml`) publish:

| Artifact | Where | Protection |
|---|---|---|
| `openartifact_<VERSION>_linux_{amd64,arm64}.tar.gz` (server, `resetpw`), `oa_<VERSION>_{linux,darwin,windows}_{amd64,arm64}` (`.tar.gz`, `.zip` on Windows) and `SHA256SUMS` | GitHub release | SHA-256 checksums and a GitHub build-provenance attestation for each archive and for `SHA256SUMS` |
| Container image `ghcr.io/openartifact/openartifact` (multi-arch; tags `X.Y.Z`, `X.Y`, and `latest` for non-prerelease versions) | GHCR | keyless cosign signature, build-provenance attestation, `provenance: mode=max` and an SBOM attached by BuildKit |

The image is scanned with Trivy (fixable HIGH/CRITICAL fail the release) before any tag is pushed.

Archives and checksums:

```sh
sha256sum --ignore-missing -c SHA256SUMS
gh attestation verify openartifact_<VERSION>_linux_amd64.tar.gz --repo openartifact/openartifact
```

Container image (keyless signature tied to the release workflow of this repository):

```sh
cosign verify ghcr.io/openartifact/openartifact:<VERSION> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/openartifact/openartifact/\.github/workflows/release\.yml@'

gh attestation verify oci://ghcr.io/openartifact/openartifact:<VERSION> --repo openartifact/openartifact
```

Pin by digest in production (`ghcr.io/openartifact/openartifact@sha256:…`). Inspect the attached BuildKit SBOM and provenance:

```sh
docker buildx imagetools inspect ghcr.io/openartifact/openartifact:<VERSION> --format '{{ json .SBOM }}'
docker buildx imagetools inspect ghcr.io/openartifact/openartifact:<VERSION> --format '{{ json .Provenance }}'
```

Vulnerability reporting policy: [../../SECURITY.md](../../SECURITY.md).
