<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Terraform and OpenTofu

Hosted private registry for Terraform and OpenTofu: modules (module registry protocol) and providers (provider registry protocol) with server-side signing of provider checksums. One repository holds both modules and providers.

| | |
|---|---|
| Registry host | the OpenArtifact host, `HOST` below (no `https://`, no path) |
| Namespace | the repository name: module source `HOST/REPO/MODULE/SYSTEM`, provider source `HOST/REPO/TYPE` |
| Repository types | hosted, [proxy](#proxy-repositories) (`registry.terraform.io`, `registry.opentofu.org`) and [group](#group-repositories). See [hosted, proxy, group](../repositories/hosted-proxy-group.md) |

Terraform and OpenTofu talk to a registry through [service discovery](#service-discovery) and a fixed API; there is no upload protocol in either client, so publishing uses a small [publish API](#publish-api) of its own (plain `PUT` with `curl`).

## Repository model

- The **namespace is the repository**. Create a repository of format `terraform` named `acme` and the namespace `acme` exists on the registry host: `HOST/acme/vpc/aws` is the module `vpc` for system `aws`, `HOST/acme/random` is the provider `random`.
- Modules and providers share the repository. Permissions, quota, [trash](../operations/trash.md), webhooks and [cleanup](../repositories/cleanup-and-quotas.md) apply to the repository as for any other format.
- A repository is hosted (it stores what you publish), a [proxy](#proxy-repositories) of a public registry, or a [group](#group-repositories) that merges hosted and proxy repositories. Publishing and deleting need a hosted repository; proxy and group repositories are read-only.
- The names `ivy`, `maven`, `raw` and `sbt` cannot be a namespace: those repository names are served under their own isolated prefix (`/repos/ivy/REPO`, ...), not under `/repos/REPO`, so the registry API would never reach them. See [URL conventions](README.md#url-conventions).

## Requirements

Both clients impose these on the registry, not on OpenArtifact; they were verified against Terraform 1.17 and OpenTofu 1.13.

| Requirement | Why |
|---|---|
| **HTTPS** with a certificate the client trusts | Service discovery and every registry call use `https://`. A plain `http://` registry is not usable. |
| **A hostname with a dot** (`artifacts.example.com`, not `localhost` or `oa`) | The first segment of a module or provider source is read as a registry host only if it contains a dot (otherwise it is a namespace on the default public registry). `localhost` and single-label names such as `oa` therefore fail with `must contain at least one dot`. A port is allowed (`artifacts.example.com:8443`). |
| **The discovery document** at `https://HOST/.well-known/terraform.json`, served as `application/json` | OpenArtifact serves it itself, without authentication. A reverse proxy that rewrites the content type or routes `/.well-known/` elsewhere breaks the registry. |
| **A `credentials` block with an API token** for private repositories | Both clients send it as `Authorization: Bearer <TOKEN>` to discovery and the registry API. See [authentication](../security/authentication.md). |
| **`BASE_URL` set to the public HTTPS URL** | Download URLs are absolute and built from it, see [download tickets](#download-tickets). With a wrong `BASE_URL` (for example the internal `http://` address) the registry answers fine and `terraform init` then fails on the download step. |

Reverse proxy and TLS:

- Terminate TLS in front of OpenArtifact (or give the server its own certificate) and forward `/` unchanged, including `/.well-known/terraform.json` and `/terraform/dl/`. If `BASE_URL` has a path prefix, the discovery document still lives at the host root: route `/.well-known/terraform.json` on the public host to the server as well as the prefix.
- Allow request bodies as large as the files you publish (provider zips up to 1 GiB, module archives up to 256 MiB, see [limits](#behavior-and-limits)); proxy buffering and `client_max_body_size`-style limits must not cut them.
- A private CA is trusted by the client through the operating system store or `SSL_CERT_FILE` (Go reads it on Unix systems); there is no flag to skip verification in either client.

## Client setup

Terraform reads `~/.terraformrc` (or the file named by `TF_CLI_CONFIG_FILE`), OpenTofu reads `~/.tofurc` (or `TF_CLI_CONFIG_FILE`); the content is the same. Create an API token as described in [authentication](../security/authentication.md); any user with read access to the repository can install, write access can publish.

```hcl
credentials "HOST" {
  token = "<TOKEN>"
}
```

The host is spelled exactly as in the source address (with the port, if any). Alternatively export the token without a file, replacing dots with underscores in the host name (`artifacts.example.com` becomes `artifacts_example_com`; a hyphen becomes a double underscore):

```sh
export TF_TOKEN_artifacts_example_com=<TOKEN>
```

`terraform login` and `tofu login` are not supported: they use an OAuth flow OpenArtifact does not offer. A public repository (or `allowAnonymousRead`) is read without credentials, writes always need a token.

### Install a module

```hcl
module "vpc" {
  source  = "HOST/REPO/vpc/aws"
  version = "1.0.0"
}
```

```sh
terraform init        # or: tofu init
```

`version` follows the usual constraint syntax (`~> 1.0`); without it the newest stable version is used. Module sources with a sub-directory (`HOST/REPO/vpc/aws//modules/subnet`) work: the archive is unpacked as a whole.

### Install a provider

```hcl
terraform {
  required_providers {
    hello = {
      source  = "HOST/REPO/hello"
      version = "1.0.0"
    }
  }
}
```

`terraform init` (or `tofu init`) downloads the zip of the current platform, verifies its checksum against the signed `SHA256SUMS` and writes `.terraform.lock.hcl`. Commit the lock file; to pre-fill the checksums of every platform you build for, run `terraform providers lock -platform=linux_amd64 -platform=darwin_arm64` (`tofu providers lock ...`) with the registry reachable.

## Publish API

Neither client can publish, so the registry has its own endpoints under the repository, at `HOST/repos/REPO/_api/...` (the leading `_api` can never be a valid Terraform name, so it never collides with registry reads). They need a token with write access (delete access for `DELETE`).

```sh
export OA_URL=https://HOST
export OA_TOKEN=<TOKEN>
```

### Publish a module

A module archive is a `.zip` or a `.tar.gz` (detected from the content, not from the file name or `Content-Type`) with the module's `*.tf` or `*.tf.json` files at its root or inside one top-level directory. The module is addressed by name, system (the provider the module targets, such as `aws`) and a [semver](https://semver.org) version without a `v` prefix.

```sh
tar -czf vpc-1.0.0.tar.gz -C ./vpc .
curl -fsS -X PUT -H "Authorization: Bearer $OA_TOKEN" \
  --data-binary @vpc-1.0.0.tar.gz \
  "$OA_URL/repos/REPO/_api/modules/vpc/aws/1.0.0"
```

Success is `201` with `{"id", "name", "system", "version", "sha256", "size"}`. Publishing a version that exists is `409`: versions are immutable, delete the version first (see [behavior](#behavior-and-limits)).

### Publish a provider

A provider version is one zip per platform, uploaded separately to `.../providers/TYPE/VERSION/OS/ARCH`. `OS` and `ARCH` are the lowercase Go names of the platform (`linux`, `darwin`, `windows`, `freebsd`; `amd64`, `arm64`, `386`, `arm`).

- The zip must contain the provider binary as `terraform-provider-TYPE_vVERSION` at its root (an `_x5` suffix and a `.exe` extension are accepted). `terraform-provider-hello_v1.0.0` in the zip for TYPE `hello`, VERSION `1.0.0`.
- By convention the zip is named `terraform-provider-TYPE_VERSION_OS_ARCH.zip`; the server derives the platform from the URL and serves the file under that name whatever the upload was called.
- The protocol versions the provider speaks come from `?protocols=5.0,6.0`, else from `metadata.protocol_versions` of a `terraform-registry-manifest.json` inside the zip (the manifest the provider scaffolding and GoReleaser setups for the Terraform plugin framework already ship), else the protocols already stored for the version when an earlier platform exists, else `5.0`. All platforms of one version must end with the same list: an explicit list that differs from the stored one is `409`.
- `X-Checksum-Sha256` (optional but recommended in CI): the hex SHA-256 of the file you meant to send. The server hashes the body while it receives it and answers `400` when the stored digest differs, so a truncated or corrupted transfer never becomes a published version.

```sh
for platform in linux/amd64 linux/arm64 darwin/arm64; do
  os=${platform%/*}; arch=${platform#*/}
  GOOS=$os GOARCH=$arch go build -o terraform-provider-hello_v1.0.0
  zip -q "terraform-provider-hello_1.0.0_${os}_${arch}.zip" terraform-provider-hello_v1.0.0 terraform-registry-manifest.json
  curl -fsS -X PUT -H "Authorization: Bearer $OA_TOKEN" \
    -H "X-Checksum-Sha256: $(sha256sum "terraform-provider-hello_1.0.0_${os}_${arch}.zip" | cut -d' ' -f1)" \
    --data-binary @"terraform-provider-hello_1.0.0_${os}_${arch}.zip" \
    "$OA_URL/repos/REPO/_api/providers/hello/1.0.0/$os/$arch"
done
```

Success is `201` with `{"id", "type", "version", "os", "arch", "protocols", "sha256", "size"}`. A GoReleaser build that already produces `terraform-provider-TYPE_VERSION_OS_ARCH.zip` files can be uploaded the same way, one `PUT` per zip.

### Delete

```sh
# one module version
curl -fsS -X DELETE -H "Authorization: Bearer $OA_TOKEN" "$OA_URL/repos/REPO/_api/modules/vpc/aws/1.0.0"
# one provider platform, or every platform of a version
curl -fsS -X DELETE -H "Authorization: Bearer $OA_TOKEN" "$OA_URL/repos/REPO/_api/providers/hello/1.0.0/linux/amd64"
curl -fsS -X DELETE -H "Authorization: Bearer $OA_TOKEN" "$OA_URL/repos/REPO/_api/providers/hello/1.0.0"
```

Deletes go to the [trash](../operations/trash.md) like every other format (`200` with `{"deleted": N}`, or `404` when nothing matches).

## Provider signing

Terraform and OpenTofu refuse a provider whose zip is not listed in a `SHA256SUMS` file signed with a key the registry publishes. OpenArtifact signs **on the server**:

- For every request of a provider version the server builds the `SHA256SUMS` file from the stored platform zips (`<sha256>  terraform-provider-TYPE_VERSION_OS_ARCH.zip`, one line per platform, sorted by file name) and signs it with an OpenPGP detached binary signature (`terraform-provider-TYPE_VERSION_SHA256SUMS.sig`). The registry answer carries the signer's public key (`signing_keys`), so clients need no key import.
- The signer is the **active GPG key of the repository**: a key activated for that repository, else the global active key. Manage keys with `oa gpg` (`generate`, `import`, `activate KEY --repository REPO`), **Settings > GPG and signing**, or the [admin GPG API](../security/signing.md#repository-metadata-signing-gpg). Keys are stored sealed under `OPENARTIFACT_SECRET_KEY`; see [signing](../security/signing.md) for the key settings and API.
- Without an active key the provider endpoints answer `503` with `no GPG signing key is configured for this repository` and `terraform init` fails. Modules are not signed and need no key.
- Because the signature is computed from the stored zips, adding a platform to an existing version changes `SHA256SUMS` and its signature; clients that already locked the version keep working with the checksums in their lock file.
- `SHA256SUMS` files and signatures produced elsewhere (a GoReleaser `signs:` step) cannot be uploaded: only the zips are stored, and the server signs them. The provider's own build can therefore stay unsigned.
- Changing the active key only affects later `init` runs: the registry answer always carries the public key that signed the `SHA256SUMS` it returns, and an existing lock file pins the zip hashes (`zh:`), not the key. No signature or key is stored: activating, rotating or removing a key needs no re-signing step and takes effect on the next request (a client that fetched the package document just before a rotation and the signature just after fails that one `init`; retry it).

```sh
oa gpg generate --name registry --algorithm rsa4096 --activate
# or: oa gpg activate registry --repository REPO
```

## Download tickets

Terraform and OpenTofu send **no credentials** to the download URLs a registry returns (`X-Terraform-Get` for modules, `download_url`, `shasums_url` and `shasums_signature_url` for providers); they also `HEAD` the archive before fetching it. A private repository therefore cannot protect those URLs with the API token.

Instead the authenticated registry call (which already passed RBAC, quarantine and license checks) mints a **download ticket**: the returned URL is `BASE_URL/terraform/dl/<ticket>/<file name>`, a capability URL signed with an HMAC key derived from the server's HMAC key (`AUTH_HMAC_KEY`) and valid for **15 minutes**. The URL ends in the real file name (`vpc-aws-1.0.0.zip` or `.tar.gz`, `terraform-provider-hello_1.0.0_linux_amd64.zip`) because Terraform and go-getter pick the unpacker from the extension.

- A ticket grants read access to exactly one file of one repository, nothing else. An expired, altered or foreign ticket answers `403` without saying which.
- Anyone who holds a ticket URL can download that file until it expires; do not paste them into public logs. They are not API tokens and cannot be used on any other route.
- The ticket route is outside RBAC by design and answers `GET` and `HEAD` with `Cache-Control: private, no-store`. Downloads are counted when the ticket is minted, by the authenticated call.
- Replicas must share the same `AUTH_HMAC_KEY` (the [secrets to preserve](../getting-started/upgrading.md#secrets-to-preserve)), otherwise a ticket minted by one replica is refused by another.
- Because the URL is absolute and built from `BASE_URL`, a wrong `BASE_URL` shows up here first: the registry call works and the download fails.

## Proxy repositories

A proxy repository serves a public registry through OpenArtifact: `registry.terraform.io` (the default, so `upstream_url` may be left out) or `registry.opentofu.org` (`upstream_url: https://registry.opentofu.org`). Any other registry that implements the same protocol works too. Clients use it exactly like a hosted repository, with the same host, credentials and [client setup](#client-setup); only the namespace has a special meaning:

- **The repository name is the upstream namespace.** A proxy repository named `hashicorp` serves `registry.terraform.io/hashicorp/*`: the provider source `HOST/hashicorp/random` and the module source `HOST/hashicorp/consul/aws`. Providers need the real namespace (their type and the checksums of their releases belong to it), so create one proxy repository per upstream namespace you use and merge them with a [group](#group-repositories) if you want a single name. The repository name must be a valid namespace (see [names](#behavior-and-limits)).
- **Service discovery of the upstream.** The upstream's `/.well-known/terraform.json` is read when needed and cached like the other documents; `modules.v1` and `providers.v1` may be relative or absolute URLs and are resolved against the discovery document. A registry that announces neither is answered with `502`.
- **Cached documents.** The module version lists, provider version lists, provider package documents and the discovery document are cached for the repository's cache TTL, and served stale (with a `Warning` header) when the upstream fails, up to the maximum stale age; a package that does not exist is cached negatively. See [cache TTLs](../repositories/hosted-proxy-group.md#cache-ttls). Version lists are cleaned: versions that are not valid semver and platforms that are not valid `os_arch` names are dropped.
- **Archives are fetched through OpenArtifact.** Module archives and provider zips usually live on other hosts (GitHub releases, a vendor release site, `releases.hashicorp.com`). The proxy rewrites `X-Terraform-Get`, `download_url`, `shasums_url` and `shasums_signature_url` to [download tickets](#download-tickets) of this server, so clients never contact those hosts: the file is fetched on the first download through the SSRF-safe client (every redirect hop is validated again), stored, and served from the cache afterwards. The upstream credentials of the repository are sent to the registry host only, never to the archive hosts.
- **Provider integrity.** The zip is checked against the `shasum` of the package document and against the line of the upstream `SHA256SUMS` file before it is stored; a mismatch or a body that is not a zip is `502` and nothing is stored. A cached zip is pinned: when the upstream later announces another `shasum` for the same version, the cached copy (and its `SHA256SUMS` and signature) is dropped and the new file is verified and fetched. The `SHA256SUMS`, its signature and the `signing_keys` of the upstream are served untouched: **the provider's own signing keys stay in place**, no key of OpenArtifact is involved, and clients verify the signature exactly as against the public registry.
- **Module integrity.** The registries publish no checksum of a module archive. The proxy stores the SHA-256 of the first copy it fetched and refuses a later copy of the same version that differs (`502`, logged, never stored), so a version cannot change under you silently while it is cached. The comparison needs the cached entry: it lives for the cache TTL, is kept for `max_stale_seconds` after that and is evicted by the proxy cache worker an hour later (see [upstream failures](../repositories/hosted-proxy-group.md#upstream-failures-and-serve-stale)); a changed archive is `502` until then, after which the next download pins what the upstream serves. An archive that was not cached is not compared with anything, so for modules that must never change, publish them to a hosted repository.
- **Module sources.** Two kinds of source are fetched, always through OpenArtifact's SSRF-safe client:
  - **GitHub repositories.** The public registries answer almost every module download with `git::https://github.com/ORG/REPO?ref=SHA`. The proxy accepts `git::https://github.com/ORG/REPO[.git]`, `https://github.com/ORG/REPO[.git]` and the bare `github.com/ORG/REPO`, each with an optional `?ref=` (a commit, tag or branch; `HEAD` when absent) and a `depth` parameter, which is ignored. It downloads `https://codeload.github.com/ORG/REPO/tar.gz/REF` and serves it as a `.tar.gz`. Owner, repository and ref are validated strictly (no `..`, no `//`, no leading `-`, no spaces).
  - **Plain archive URLs.** `https://...` (or `http://...`) ending in `.zip`, `.tar.gz`, `.tgz`, `.tar.bz2`, `.tbz2`, `.tar.xz`, `.txz` or `.tar`, or with an `?archive=zip` (or `tar.gz`, ...) hint, which the proxy strips before fetching. A `github.com` URL that is not a bare repository address (for example a release archive) is a plain archive URL.

  A `//subdir` selector is supported on GitHub sources and on `.zip`, `.tar.gz` and `.tgz` URLs (`https://host/x.tar.gz//modules/net`). For a plain archive the subdirectory is relative to the archive root; for GitHub it is relative to the repository root. It must be a clean relative path (no `..`, no empty segment, no backslash).

  **Repacking.** The module is repacked before it is cached and served: for GitHub sources the single wrapper directory of the tarball (`REPO-SHA/`) is stripped, and with a subdirectory only the entries under it are kept, with that prefix removed. A `.zip` stays a zip; everything else is served as a `.tar.gz`. The repack applies the same limits as published modules (entry count, expanded size, compression ratio, clean entry names) and refuses symlinks, hard links, device files, duplicate entries and path traversal; such an archive is a **`502`** and is never stored.

  **Immutability.** The SHA-256 of the repacked bytes is pinned when the version is first cached. A branch or tag that moves after the cache entry expires answers `502` instead of silently changing the module; use a commit SHA or a release tag (which the registries do) to avoid it.

  Every other go-getter source is refused with **`501`** and a message pointing here: forced getters other than `git::` on GitHub (`hg::`, `s3::`, `gcs::`, ...), git hosts other than `github.com`, scp-style addresses (`git@github.com:org/repo`), `ssh://` URLs, URLs with credentials, a `//subdir` on an archive format other than zip or tar.gz, and the getter parameters `sshkey`, `checksum` and `filename` (and, on GitHub sources, anything but `ref` and `depth`).
- **Module details** (`GET /MODULE/SYSTEM/VERSION`) are generated from the version list: `published_at` is absent and `downloads` is `0`.
- **Quarantine, blocking and RBAC** apply as for other proxies: a blocked repository answers `403`, a quarantined package answers `403` with code `ARTIFACT_QUARANTINED` and no ticket is issued. Proxy caches are not counted against the storage quota.
- **Downloads need `BASE_URL`** and the same `AUTH_HMAC_KEY` on every replica, as for hosted repositories; without a ticket key the download calls answer `503`.

Create one with the API or the web UI:

```sh
curl -fsS -X POST -H "Authorization: Bearer $OA_TOKEN" -H "Content-Type: application/json" \
  "$OA_URL/api/v1/repositories" \
  -d '{"name":"hashicorp","format":"terraform","repo_type":"proxy"}'
```

A provider block then reads `source = "HOST/hashicorp/random"`.

## Group repositories

A group merges hosted and proxy repositories of format `terraform` behind one namespace (the group's name) and is read-only. The merge works by module name and provider type, not by upstream namespace, so `random` from a proxy of `hashicorp` and an in-house `random` in a hosted repository are the same provider as far as the group is concerned:

- **Hosted wins per name.** A module name (`MODULE/SYSTEM`) or provider type that a hosted member the caller may read holds is served only by the hosted members; proxy members never fill in versions of it. A name no readable hosted member holds is served by the proxies in member order. This prevents a public module or provider from shadowing an internal one (dependency confusion); [`merge_proxy_versions`](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name) set to `true` turns the rule off.
- **Version lists are merged** across the members that serve the name, sorted ascending; a version offered twice is served by the first member in order. Provider platforms are merged per version. The unversioned module call (`MODULE/SYSTEM` and `MODULE/SYSTEM/download`) means the newest stable version of the merged list.
- **Downloads come from the owning member.** The package document and the module download are answered by the member that serves the version; the ticket URLs they carry belong to that member's repository. A provider keeps the **signing key of its member**: a hosted member signs with its own repository key, a proxy member serves the upstream's key and checksums. A group therefore has no key of its own.
- **Details and ids are rewritten** to the group's namespace (`id`, `namespace`), because clients address the group.
- RBAC: reading the group needs read access to the group; a member the caller cannot read is skipped, and the hosted-wins rule fails closed (a name held by a hosted member the caller cannot read is hidden from the proxies too).

## Service discovery

`GET /.well-known/terraform.json` on the server root, public:

```json
{"modules.v1": "https://HOST/repos/", "providers.v1": "https://HOST/repos/"}
```

The values are `BASE_URL` plus `/repos/`; the clients append `REPO/...` to them. The response is `application/json`, as Terraform requires.

## Endpoints

Relative to `HOST/repos/REPO`. Responses are JSON; errors are `{"errors": ["message"]}` with the matching status. Reads need `read`, `PUT` needs `write`, `DELETE` needs `delete`; anonymous reads work on public repositories.

Registry protocol (what the clients call):

| Method and path | Purpose |
|---|---|
| `GET /MODULE/SYSTEM/versions` | `{"modules": [{"versions": [{"version": "1.0.0"}]}]}`; `404` without versions |
| `GET /MODULE/SYSTEM/VERSION/download` | `204` with `X-Terraform-Get: <ticket URL>` |
| `GET /MODULE/SYSTEM/download` | `302` to the download path of the newest stable version (newest version if there is no stable one) |
| `GET /MODULE/SYSTEM/VERSION`, `GET /MODULE/SYSTEM` | version details: `id`, `namespace`, `name`, `provider`, `version`, `published_at`, `downloads` (latest for the second form) |
| `GET /TYPE/versions` | `{"versions": [{"version", "protocols", "platforms": [{"os", "arch"}]}]}` |
| `GET /TYPE/VERSION/download/OS/ARCH` | package details: `protocols`, `os`, `arch`, `filename`, `download_url`, `shasums_url`, `shasums_signature_url`, `shasum` (SHA-256 of the zip) and `signing_keys`; `503` without a signing key |
| `GET`, `HEAD /terraform/dl/TICKET/FILE` (server root) | the archive, `SHA256SUMS` or signature named in the [ticket](#download-tickets) |
| `GET /.well-known/terraform.json` (server root) | [service discovery](#service-discovery) |

Publish API:

| Method and path | Purpose |
|---|---|
| `PUT /_api/modules/MODULE/SYSTEM/VERSION` | publish a module archive (`201`, `409` if the version exists) |
| `PUT /_api/providers/TYPE/VERSION/OS/ARCH[?protocols=5.0,6.0]` | publish one platform zip; optional `X-Checksum-Sha256` |
| `DELETE /_api/modules/MODULE/SYSTEM/VERSION` | delete a module version |
| `DELETE /_api/providers/TYPE/VERSION/OS/ARCH` | delete one platform |
| `DELETE /_api/providers/TYPE/VERSION` | delete every platform of a version |

The `versions` endpoints answer ETag/304 like the other index documents; they are `public` in the HTTP cache sense only for anonymous reads of public repositories.

## Behavior and limits

- **Names.** Namespace, module name, system and provider type match Terraform's own rule: letters, digits, `-` and `_`, starting and ending with a letter or digit, at most 64 characters. `packages` is reserved (the web UI owns `/repos/REPO/packages/...`) and is refused as a module name or provider type; `versions` is refused as a module system (it would collide with the `versions` endpoint). Both are `400`. A malformed name, version, OS or architecture is `400`.
- **Versions** are semver 2.0 without a `v` prefix, at most 100 characters; pre-releases (`1.0.0-rc.1`) are allowed, build metadata (`1.0.0+b1`) is refused. The newest-stable rule of the unversioned module download skips pre-releases.
- **Immutable versions.** A live version is never overwritten: a second `PUT` is `409`. Delete it (it goes to the [trash](../operations/trash.md), which does not block the coordinates) and publish again. For a provider, a platform is immutable as well; adding a missing platform to a published version is allowed. With [immutable releases](../operations/trash.md#immutable-releases) on, deleting a stable version is `409` unless an admin passes `?force=true`.
- **Archive safety.** Uploaded archives are validated before they are stored and a failure is never `500`. A corrupt or unsafe archive is `400`: not a valid zip or tar.gz, zero bytes, absolute paths, `..` segments, backslashes, NUL bytes, symbolic links and hard links, duplicate entries, more than 20000 entries, an entry or total expanded size above 2 GiB (1 GiB total for a module), a compression ratio above 1000 once the total passes 100 MiB. A well-formed archive of the wrong shape is `422`: a module without `*.tf` or `*.tf.json` files at its root or in its single top-level directory, a provider zip without `terraform-provider-TYPE_vVERSION` at its root, or an invalid `terraform-registry-manifest.json`.
- **Size limits.** Module archives up to 256 MiB, provider zips up to 1 GiB; larger bodies are `413`.
- **Quota.** The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is reserved before the upload is stored and released on every failure; over quota is `507`. A module version is one artifact, each provider platform is one artifact.
- **Quarantine and license policy.** A [quarantined](../security/scanning.md#quarantine-after-upload) module or provider platform is not served: the registry call that would mint the ticket answers `403` with code `ARTIFACT_QUARANTINED` and issues none (a ticket minted before the quarantine keeps working until it expires, at most 15 minutes). Terraform uploads declare no license, so a license policy with the `block` action does not refuse them at upload; set a license [property](../repositories/properties.md) to bring the [after-upload path](../security/scanning.md#quarantine-after-upload) into play.
- **Trash.** Each module version and each provider platform is its own item (`terraform_module`, `terraform_provider`), listed with the name `modules/MODULE/SYSTEM` or `providers/TYPE` and, for a provider, the platform as `OS_ARCH`; restore every platform to bring a version back. See [trash](../operations/trash.md).
- **Cleanup.** A cleanup policy counts versions per module and per provider, all platforms of a provider version together. See [cleanup policies](../repositories/cleanup-and-quotas.md#supported-formats).
- **Scanning and SBOM.** Module and provider archives are listed and searched like other artifacts. Module `.tf` files are not parsed for dependencies, so no SBOM components or vulnerability findings come from them today.
- **Webhooks and metrics.** `artifact.uploaded` and `artifact.deleted` fire as for other formats; `openartifact_uploads_total{format="terraform"}` counts publish requests.
- **Catalog.** The web UI shows one row per module version and one per provider platform, with the install snippet for the module or provider source.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `must contain at least one dot` | The registry host in the source has no dot, so the client reads it as a namespace on the public registry. Use a real DNS name (`artifacts.example.com`); `localhost` cannot be used. For a test on one machine add an `/etc/hosts` entry and a certificate for that name. |
| `Failed to request discovery document` ... `x509: certificate signed by unknown authority` | The client does not trust the certificate. Install the CA in the system store or set `SSL_CERT_FILE`; there is no insecure mode. |
| `Failed to request discovery document` ... `http: server gave HTTP response to HTTPS client` or connection refused | The registry host is reached on the wrong port or without TLS. Put TLS in front of OpenArtifact; discovery is always `https://`. |
| Discovery fails with a content type or invalid response error | The discovery response is not `application/json`: a reverse proxy intercepts `/.well-known/` or rewrites the content type, or the host is not OpenArtifact. `curl -i https://HOST/.well-known/terraform.json` must show `200` and `Content-Type: application/json`. |
| `403`, `Unauthorized` or `401` on `init` for a private repository | No `credentials` block (or `TF_TOKEN_...`) for the exact host, an expired token, or a token without read access to the repository. |
| Registry call works, download fails with `403` | The ticket expired (15 minutes between the registry call and the download, for instance a very slow `init` with a stalled plugin cache) or a replica without the same `AUTH_HMAC_KEY` minted it. Retry; check the replicas' `AUTH_HMAC_KEY`. |
| Registry call works, download fails with a connection error or a `http://` URL | `BASE_URL` is not the public HTTPS URL. Fix it and restart; download URLs are built from it. |
| `no GPG signing key is configured for this repository` (`503`) | No active key for the repository and no global active key. `oa gpg generate --activate` (global) or `oa gpg activate KEY --repository REPO`. |
| `Failed to install provider` ... `the current package for ... doesn't match any of the checksums` | The version was changed after the lock file was written (platform added, version deleted and published again). Run `terraform init -upgrade` or `terraform providers lock` for the platforms you use. |
| `provider ... does not support the platform linux_arm64` / `no available releases match` | No zip was uploaded for that OS and architecture, or the version constraint matches nothing. `GET /repos/REPO/TYPE/versions` lists the platforms per version. |
| `501` with `unsupported module source` on a proxy repository | The registry points the module at a source the proxy does not fetch: a git host other than `github.com`, `s3::`, `hg::`, `gcs::`, an scp or `ssh://` address, or getter parameters such as `sshkey` or `checksum` (see [proxy repositories](#proxy-repositories)). Use a hosted repository for the module, or the source address directly. |
| `502` on a module download after it worked | The module changed upstream (a moving branch or tag) or the archive failed the repack checks (symlinks, traversal, limits). The pinned copy is kept until the cache worker evicts it; see the immutability note under [proxy repositories](#proxy-repositories). |
| `502` on a proxy repository when downloading a provider or module | The upstream or the release host is down, redirects to a blocked address, or served a file that fails its checksum or is not an archive; the file is not stored. The server log names the repository, the path and the digests. A changed module archive is refused the same way, see [module integrity](#proxy-repositories). |
| `409` on publish | The version (or provider platform) already exists, or the `protocols` of a new platform differ from the stored ones. Delete it first, or pass the same `?protocols=`. |
| `400` with `checksum` in the message on provider publish | `X-Checksum-Sha256` did not match the received bytes: retry the upload. |
| `422` or `400` on publish | The archive was refused, see [archive safety](#behavior-and-limits): the provider binary is named wrongly or the module has no `.tf` files. |
