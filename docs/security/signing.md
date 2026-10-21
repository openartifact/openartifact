<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Signing and release verification

Two separate topics: the GPG key the server uses to sign APT and RPM repository metadata, and how to verify the signatures on OpenArtifact's own release binaries and container image.

## Repository metadata signing (GPG)

One server-wide GPG key signs the metadata of **all** Debian and RPM repositories. Uploaded packages are stored as-is: OpenArtifact does not sign or re-sign `.deb`/`.rpm` files, only the index files that list them. Other formats are not signed.

### Configuration

| YAML | Env | Meaning |
|---|---|---|
| `gpg.enabled` | `GPG_ENABLED` | turn signing on (default `false`) |
| `gpg.keyPath` | `GPG_KEY_PATH` | ASCII-armored **private** key file; only the first key in the file is used |
| `gpg.passphrase` | `GPG_PASSPHRASE` | passphrase if the key is encrypted |
| `gpg.autoGenerate` | `GPG_AUTO_GENERATE` | parsed but **has no effect**: the server never generates a key |

Signing is active only when `enabled` is true **and** `keyPath` is set. If the key cannot be read or decrypted the server logs `Failed to initialize GPG signer, signing disabled` and keeps running unsigned, so check the startup log for `GPG signer initialized` (it prints the fingerprint). Keep the key file readable by the server only. See [../../.env.example](../../.env.example) and [../../openartifact.example.yaml](../../openartifact.example.yaml).

Create a signing key and export it:

```sh
gpg --quick-generate-key 'OpenArtifact Repo <repo@example.com>' rsa4096 sign 0
gpg --armor --export-secret-keys <FINGERPRINT> > private.key
gpg --armor --export <FINGERPRINT> > public.key
```

```sh
GPG_ENABLED=true GPG_KEY_PATH=/etc/openartifact/gpg/private.key GPG_PASSPHRASE='<passphrase>'
```

### What gets signed

| Format | Signed files | Public key |
|---|---|---|
| Debian | `dists/<dist>/InRelease` (clear-signed) and `dists/<dist>/Release.gpg` (detached, armored) | see the note below |
| RPM | `repodata/repomd.xml.asc` (detached, armored) | served at `HOST/repos/REPO/public.key` |

Signatures are SHA-256 OpenPGP signatures created whenever a repository's metadata is generated. Enabling or replacing the key does not re-sign what is already stored: existing signatures stay until that repository's metadata is regenerated (a later upload or delete). Without a signer, `InRelease`, `Release.gpg`, `repomd.xml.asc` and `public.key` return 404.

**APT public key.** The server writes `public.key` only for RPM repositories. `HOST/repos/REPO/public.key` on an APT repository returns 404, including the URL used by the web UI's apt snippet. Distribute the key you exported above yourself (config management, an image, or a static URL).

### Client setup

APT (use the exported `public.key`; the repository line matches the one the UI produces):

```sh
sudo gpg --dearmor -o /etc/apt/keyrings/REPO.gpg public.key
echo "deb [signed-by=/etc/apt/keyrings/REPO.gpg] HOST/repos/REPO stable main" | sudo tee /etc/apt/sources.list.d/REPO.list
sudo apt update
```

Credentials for private repositories are covered in [Debian](../formats/debian.md).

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

### Migrating from Nexus or Artifactory

The importer rebuilds `dists/` and `repodata/` instead of copying them, so they are signed with this key. Clients must trust it instead of the old repository key: [migration](../migration/nexus-artifactory.md).

### Admin GPG endpoints

`GET /api/v1/admin/gpg/keys` always returns an empty list, and `POST /api/v1/admin/gpg/keys`, `POST /api/v1/admin/gpg/keys/generate` and `DELETE /api/v1/admin/gpg/keys/{id}` answer 501 `not_implemented`. Key management is file and environment based only.

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
