<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Terraform and OpenTofu

Use a repository of format `terraform` as a private registry for Terraform and OpenTofu modules and providers. A hosted repository stores what you publish, a proxy mirrors a public registry, and a group merges both behind one namespace. Provider checksums are signed by the server.

| Item | Value |
|---|---|
| Registry host | the OpenArtifact host, `HOST` below (no `https://`, no path) |
| Module source | `HOST/REPO/MODULE/SYSTEM` |
| Provider source | `HOST/REPO/TYPE` |
| Publish URL | `HOST/repos/REPO/_api/...` |
| Format id | `terraform` |
| Repository types | hosted, [proxy](#proxy-repositories), [group](#group-repositories); see [hosted, proxy, group](../repositories/hosted-proxy-group.md) |
| Default upstream (proxy) | `https://registry.terraform.io` |

The repository name is the namespace. A repository named `acme` makes `HOST/acme/vpc/aws` the module `vpc` for system `aws`, and `HOST/acme/random` the provider `random`. Modules and providers share the repository. Permissions, quota, [trash](../operations/trash.md), webhooks and [cleanup](../repositories/cleanup-and-quotas.md) work as for any other format.

The names `ivy`, `maven`, `raw` and `sbt` cannot be a namespace. Those repositories are served under their own prefix (`/repos/ivy/REPO`, ...), so the registry API would never reach them. See [URL conventions](README.md#url-conventions).

## Client setup

Terraform reads `~/.terraformrc` (or the file named by `TF_CLI_CONFIG_FILE`). OpenTofu reads `~/.tofurc` (or `TF_CLI_CONFIG_FILE`). The content is the same. The web UI connect panel shows the same snippets with your own URL.

Create an API token as described in [authentication](../security/authentication.md). Read access to the repository lets you install; write access lets you publish.

```hcl
credentials "HOST" {
  token = "<TOKEN>"
}
```

Spell the host exactly as in the source address, with the port if any. To avoid a file, export the token instead. Replace dots in the host name with underscores (a hyphen becomes a double underscore):

```sh
export TF_TOKEN_artifacts_example_com=<TOKEN>
```

`terraform login` and `tofu login` are not supported, because they use an OAuth flow OpenArtifact does not offer. A public repository (or `allowAnonymousRead`) is read without credentials. Writes always need a token.

### Registry requirements

Both clients impose these on the registry, not on OpenArtifact.

| Requirement | Why |
|---|---|
| HTTPS with a certificate the client trusts | Discovery and every registry call use `https://`. |
| A host name with a dot, such as `artifacts.example.com` | The client reads the first source segment as a registry host only if it contains a dot. A port is allowed. |
| The discovery document at `https://HOST/.well-known/terraform.json` | OpenArtifact serves it itself, without authentication. |
| A `credentials` block with an API token for private repositories | Both clients send it as `Authorization: Bearer <TOKEN>`. |
| The public host forwarded by any reverse proxy | Download URLs are resolved against the registry URL the client called. |

`localhost` and single-label names such as `oa` fail with `must contain at least one dot`.

Reverse proxy and TLS:

- Terminate TLS in front of OpenArtifact, or give the server its own certificate. Forward `/` unchanged, including `/.well-known/terraform.json` and `/terraform/dl/`.
- Under a path prefix, set `BASE_URL`. Only its path is used for ticket URLs. The discovery document still lives at the host root, so route `/.well-known/terraform.json` on the public host to the server too.
- Forward `Host` or `X-Forwarded-Host` with the public host and any non-default port. `X-Forwarded-*` is believed only from a peer in `OPENARTIFACT_TRUSTED_PROXIES`.
- Allow request bodies as large as your files: provider zips up to 1 GiB, module archives up to 256 MiB. Proxy buffering and `client_max_body_size`-style limits must not cut them.
- A private CA is trusted through the operating system store or `SSL_CERT_FILE`. Neither client has a flag to skip verification.

## Publish

Neither client can publish, so the registry has its own endpoints at `HOST/repos/REPO/_api/...`. The leading `_api` can never be a valid Terraform name, so it never collides with registry reads. You need a token with write access (delete access for `DELETE`). Proxy and group repositories are read-only.

```sh
export OA_URL=https://HOST
export OA_TOKEN=<TOKEN>
```

### Publish a module

A module archive is a `.zip` or a `.tar.gz`. The server detects the type from the content, not from the file name or `Content-Type`. The module's `*.tf` or `*.tf.json` files must sit at the archive root or inside one top-level directory. The module is addressed by name, system (the provider it targets, such as `aws`) and a [semver](https://semver.org) version without a `v` prefix.

```sh
tar -czf vpc-1.0.0.tar.gz -C ./vpc .
curl -fsS -X PUT -H "Authorization: Bearer $OA_TOKEN" \
  --data-binary @vpc-1.0.0.tar.gz \
  "$OA_URL/repos/REPO/_api/modules/vpc/aws/1.0.0"
```

Success is `201` with `{"id", "name", "system", "version", "sha256", "size"}`. Publishing an existing version is `409`; delete it first.

### Publish a provider

A provider version is one zip per platform, uploaded separately to `.../providers/TYPE/VERSION/OS/ARCH`. `OS` and `ARCH` are lowercase Go names (`linux`, `darwin`, `windows`, `freebsd`; `amd64`, `arm64`, `386`, `arm`).

- The zip holds the provider binary as `terraform-provider-TYPE_vVERSION` at its root. An `_x5` suffix and a `.exe` extension are accepted.
- Name the zip `terraform-provider-TYPE_VERSION_OS_ARCH.zip` by convention. The server derives the platform from the URL and serves the file under that name whatever the upload was called.
- Protocol versions come from the first of these that exists:
  1. `?protocols=5.0,6.0` on the request.
  2. `metadata.protocol_versions` in a `terraform-registry-manifest.json` inside the zip.
  3. The protocols already stored for the version.
  4. `5.0`.
- All platforms of one version must end with the same protocol list. An explicit list that differs from the stored one is `409`.
- `X-Checksum-Sha256` is optional but recommended in CI. Send the hex SHA-256 of the file. The server answers `400` when the stored digest differs, so a truncated transfer never becomes a published version.

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

Success is `201` with `{"id", "type", "version", "os", "arch", "protocols", "sha256", "size"}`. A GoReleaser build that already produces `terraform-provider-TYPE_VERSION_OS_ARCH.zip` files uploads the same way, one `PUT` per zip.

### Delete

```sh
# one module version
curl -fsS -X DELETE -H "Authorization: Bearer $OA_TOKEN" "$OA_URL/repos/REPO/_api/modules/vpc/aws/1.0.0"
# one provider platform
curl -fsS -X DELETE -H "Authorization: Bearer $OA_TOKEN" "$OA_URL/repos/REPO/_api/providers/hello/1.0.0/linux/amd64"
# every platform of a provider version
curl -fsS -X DELETE -H "Authorization: Bearer $OA_TOKEN" "$OA_URL/repos/REPO/_api/providers/hello/1.0.0"
```

Deletes go to the [trash](../operations/trash.md). Success is `200` with `{"deleted": N}`; nothing matching is `404`.

## Install and consume

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

`version` follows the usual constraint syntax (`~> 1.0`). Without it the newest stable version is used. A source with a sub-directory (`HOST/REPO/vpc/aws//modules/subnet`) works, because the archive is unpacked as a whole.

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

`init` downloads the zip of the current platform, verifies it against the signed `SHA256SUMS` and writes `.terraform.lock.hcl`. Commit the lock file. To pre-fill checksums for every platform you build for, run this with the registry reachable:

```sh
terraform providers lock -platform=linux_amd64 -platform=darwin_arm64   # or: tofu providers lock ...
```

### Service discovery

`GET /.well-known/terraform.json` on the server root is public and answers `application/json` with `Cache-Control: public, max-age=300`:

```json
{"modules.v1": "https://HOST/repos/", "providers.v1": "https://HOST/repos/"}
```

The values are `PREFIX/repos/`, where `PREFIX` is the path of an explicit `BASE_URL` (empty otherwise). Clients append `REPO/...` to them.

### Endpoints

Paths are relative to `HOST/repos/REPO`. Responses are JSON. Errors are `{"errors": ["message"]}` with the matching status. Reads need `read`, `PUT` needs `write`, `DELETE` needs `delete`. Anonymous reads work on public repositories.

| Method and path | Purpose |
|---|---|
| `GET /MODULE/SYSTEM/versions` | `{"modules": [{"versions": [{"version": "1.0.0"}]}]}`; `404` without versions |
| `GET /MODULE/SYSTEM/VERSION/download` | `204` with `X-Terraform-Get: <ticket URL>` |
| `GET /MODULE/SYSTEM/download` | `302` to the download path of the newest stable version (newest version if none is stable) |
| `GET /MODULE/SYSTEM/VERSION`, `GET /MODULE/SYSTEM` | details: `id`, `namespace`, `name`, `provider`, `version`, `published_at`, `downloads` (latest for the second form) |
| `GET /TYPE/versions` | `{"versions": [{"version", "protocols", "platforms": [{"os", "arch"}]}]}` |
| `GET /TYPE/VERSION/download/OS/ARCH` | package details: `protocols`, `os`, `arch`, `filename`, `download_url`, `shasums_url`, `shasums_signature_url`, `shasum` (SHA-256 of the zip), `signing_keys`; `503` without a signing key |
| `GET`, `HEAD /terraform/dl/TICKET/FILE` (server root) | the file named in the [ticket](#download-tickets) |
| `GET /.well-known/terraform.json` (server root) | [service discovery](#service-discovery) |
| `PUT /_api/modules/MODULE/SYSTEM/VERSION` | publish a module (`201`, `409` if it exists) |
| `PUT /_api/providers/TYPE/VERSION/OS/ARCH[?protocols=5.0,6.0]` | publish one platform zip; optional `X-Checksum-Sha256` |
| `DELETE /_api/modules/MODULE/SYSTEM/VERSION` | delete a module version |
| `DELETE /_api/providers/TYPE/VERSION/OS/ARCH` | delete one platform |
| `DELETE /_api/providers/TYPE/VERSION` | delete every platform of a version |

## Provider signing

Terraform and OpenTofu refuse a provider whose zip is not listed in a `SHA256SUMS` file signed with a key the registry publishes. OpenArtifact signs on the server.

- For every request of a provider version, the server builds `SHA256SUMS` from the stored zips and signs it with a detached OpenPGP signature. The file has one line per platform (`<sha256>  terraform-provider-TYPE_VERSION_OS_ARCH.zip`), sorted by file name.
- The registry answer carries the signer's public key in `signing_keys`, so clients need no key import.
- The signer is the active GPG key of the repository: a key activated for it, else the global active key. Manage keys with `oa gpg` (`generate`, `import`, `activate KEY --repository REPO`), **Settings > GPG and signing**, or the [admin GPG API](../security/signing.md#repository-metadata-signing-gpg). Keys are stored sealed under `OPENARTIFACT_SECRET_KEY`; see [signing](../security/signing.md).
- Without an active key the provider endpoints answer `503` with `no GPG signing key is configured for this repository`, and `init` fails. Modules are not signed and need no key.
- `SHA256SUMS` files and signatures produced elsewhere (a GoReleaser `signs:` step) cannot be uploaded. Only the zips are stored, and the server signs them. Your build can stay unsigned.

```sh
oa gpg generate --name registry --algorithm rsa4096 --activate
# or: oa gpg activate registry --repository REPO
```

Adding a platform to an existing version changes `SHA256SUMS` and its signature. Clients that already locked the version keep working with the checksums in their lock file.

Changing the active key affects only later `init` runs. No signature is stored, so activating, rotating or removing a key needs no re-signing and takes effect on the next request. An existing lock file pins the zip hashes (`zh:`), not the key. A client that fetched the package document just before a rotation and the signature just after fails that one `init`; retry it.

## Download tickets

Terraform and OpenTofu send no credentials to the download URLs a registry returns (`X-Terraform-Get` for modules; `download_url`, `shasums_url` and `shasums_signature_url` for providers). They also `HEAD` the archive first. A private repository therefore cannot protect those URLs with the API token.

Instead, the authenticated registry call mints a download ticket. That call has already passed access, quarantine and license checks. The returned URL is path-absolute: `PREFIX/terraform/dl/<ticket>/<file name>`. `PREFIX` is only the path of an explicit `BASE_URL`. Terraform resolves the URL against the registry call.

- The ticket is signed with a key derived from `AUTH_HMAC_KEY` and is valid for 15 minutes.
- The URL ends in the real file name (`vpc-aws-1.0.0.zip`, `terraform-provider-hello_1.0.0_linux_amd64.zip`), because the clients pick the unpacker from the extension.
- A ticket grants read access to exactly one file of one repository. An expired, altered or foreign ticket answers `403` without saying which.
- Anyone who holds a ticket URL can download that file until it expires. Keep them out of public logs. They cannot be used on any other route.
- The ticket route is outside access control by design. It answers `GET` and `HEAD` with `Cache-Control: private, no-store`.
- Downloads are counted when the ticket is minted.
- Replicas must share the same `AUTH_HMAC_KEY` (see [secrets to preserve](../getting-started/upgrading.md#secrets-to-preserve)). Otherwise a ticket minted by one replica is refused by another. Without a ticket key, download calls answer `503`.
- The download goes to the host the client used for the registry call. A deployment under a path prefix without `BASE_URL`, or a `BASE_URL` with the wrong path, shows up here first: the registry call works and the download fails.

## Proxy repositories

A proxy serves a public registry through OpenArtifact. Clients use it like a hosted repository, with the same host, credentials and [client setup](#client-setup).

Create one with the API or the web UI. `upstream_url` defaults to `https://registry.terraform.io`. For OpenTofu use `https://registry.opentofu.org`. Any registry implementing the same protocol works.

```sh
curl -fsS -X POST -H "Authorization: Bearer $OA_TOKEN" -H "Content-Type: application/json" \
  "$OA_URL/api/v1/repositories" \
  -d '{"name":"hashicorp","format":"terraform","repo_type":"proxy"}'
```

A provider block then reads `source = "HOST/hashicorp/random"`.

### Repository name

The repository name is the upstream namespace, and it must be a valid namespace (see [names](#limits-and-behaviour)). A proxy named `hashicorp` serves `registry.terraform.io/hashicorp/*`: the provider `HOST/hashicorp/random` and the module `HOST/hashicorp/consul/aws`.

Providers need the real namespace, because their type and release checksums belong to it. Create one proxy per upstream namespace you use. Merge them with a [group](#group-repositories) if you want a single name.

### Caching and upstream discovery

- The upstream's `/.well-known/terraform.json` is read when needed. `modules.v1` and `providers.v1` may be relative or absolute. A registry that announces neither is answered with `502`.
- Module version lists, provider version lists, provider package documents and the discovery document are cached for the repository's cache TTL. A package that does not exist is cached negatively. See [cache TTLs](../repositories/hosted-proxy-group.md#cache-ttls).
- When the upstream fails, cached documents are served stale with a `Warning` header, up to the maximum stale age.
- Version lists are cleaned: versions that are not valid semver and platforms that are not valid `os_arch` names are dropped.
- Proxy caches do not count against the storage quota.

### Archives

Module archives and provider zips usually live on other hosts, such as GitHub releases or `releases.hashicorp.com`. The proxy rewrites `X-Terraform-Get`, `download_url`, `shasums_url` and `shasums_signature_url` to [download tickets](#download-tickets) of this server. Clients never contact those hosts.

The file is fetched on the first download, stored and served from the cache afterwards. Every redirect hop is validated again. The upstream credentials of the repository go to the registry host only, never to archive hosts.

### Provider integrity

- The zip is checked against the `shasum` of the package document and against its line in the upstream `SHA256SUMS` before it is stored. A mismatch, or a body that is not a zip, is `502` and nothing is stored.
- A cached zip is pinned. When the upstream later announces another `shasum` for the same version, the cached copy, its `SHA256SUMS` and its signature are dropped. The new file is then verified and fetched.
- `SHA256SUMS`, its signature and the upstream `signing_keys` are served untouched. The provider's own signing keys stay in place, no OpenArtifact key is involved, and clients verify exactly as against the public registry.

### Module integrity

The registries publish no checksum of a module archive. The proxy stores the SHA-256 of the first copy it fetched. It refuses a later copy of the same version that differs (`502`, logged, never stored). A version therefore cannot change under you while it is cached.

The comparison needs the cached entry. The entry lives for the cache TTL, is kept for `max_stale_seconds` after that, and is evicted by the proxy cache worker an hour later (see [upstream failures](../repositories/hosted-proxy-group.md#upstream-failures-and-serve-stale)). A changed archive is `502` until then; afterwards the next download pins what the upstream serves. An archive that was not cached is compared with nothing. For modules that must never change, publish them to a hosted repository.

### Module sources

Two kinds of module source are fetched.

- **GitHub repositories.** Public registries answer almost every module download with `git::https://github.com/ORG/REPO?ref=SHA`. The proxy accepts `git::https://github.com/ORG/REPO[.git]`, `https://github.com/ORG/REPO[.git]` and bare `github.com/ORG/REPO`. Each may carry `?ref=` (a commit, tag or branch; `HEAD` when absent). A `depth` parameter is ignored. The proxy downloads `https://codeload.github.com/ORG/REPO/tar.gz/REF` and serves it as a `.tar.gz`. Owner, repository and ref are validated strictly: no `..`, no `//`, no leading `-`, no spaces.
- **Plain archive URLs.** An `https://` or `http://` URL ending in `.zip`, `.tar.gz`, `.tgz`, `.tar.bz2`, `.tbz2`, `.tar.xz`, `.txz` or `.tar`, or carrying an `?archive=zip` (or `tar.gz`, ...) hint, which the proxy strips before fetching. A `github.com` URL that is not a bare repository address, such as a release archive, is a plain archive URL.

A `//subdir` selector works on GitHub sources and on `.zip`, `.tar.gz` and `.tgz` URLs, for example `https://host/x.tar.gz//modules/net`. It is relative to the archive root (plain archive) or the repository root (GitHub). It must be a clean relative path: no `..`, no empty segment, no backslash.

The module is repacked before it is cached:

- For GitHub sources the single wrapper directory (`REPO-SHA/`) is stripped. With a subdirectory, only the entries under it are kept, with that prefix removed.
- A `.zip` stays a zip. Everything else is served as a `.tar.gz`.
- The repack applies the same limits as published modules. It refuses symlinks, hard links, device files, duplicate entries and path traversal. Such an archive is `502` and is never stored.

The SHA-256 of the repacked bytes is pinned when the version is first cached. A branch or tag that moves after the cache entry expires answers `502` instead of silently changing the module. Use a commit SHA or a release tag, as the registries do.

Every other source is refused with `501` and a message pointing here:

- forced getters other than `git::` on GitHub (`hg::`, `s3::`, `gcs::`, ...)
- git hosts other than `github.com`
- scp-style addresses (`git@github.com:org/repo`) and `ssh://` URLs
- URLs with credentials
- a `//subdir` on an archive format other than zip or tar.gz
- the getter parameters `sshkey`, `checksum` and `filename`, and on GitHub sources anything but `ref` and `depth`

### Module details

`GET /MODULE/SYSTEM/VERSION` on a proxy is generated from the version list. `published_at` is absent and `downloads` is `0`. A hosted repository returns the upload time and the real download count.

### Quarantine and access

A blocked repository answers `403`. A quarantined package answers `403` with code `ARTIFACT_QUARANTINED`, and no ticket is issued.

### Minimum age and curation

Neither applies to a terraform proxy. The API accepts `curation_mode` and `min_age_hours`, but they have no effect. The protocol's version lists, package documents and module download answers carry no publish time, and curation rules are not evaluated. See [minimum age](../repositories/hosted-proxy-group.md#minimum-age) and [package curation](../repositories/hosted-proxy-group.md#package-curation) for the formats that support them.

## Group repositories

A group merges hosted and proxy repositories of format `terraform` behind one namespace, the group's name. It is read-only.

The merge works by module name and provider type, not by upstream namespace. A `random` from a proxy of `hashicorp` and an in-house `random` in a hosted repository count as the same provider.

- **Hosted members win a name.** A module name (`MODULE/SYSTEM`) or provider type held by a hosted member the caller may read is served only by hosted members. Proxy members never add versions to it. A name no readable hosted member holds is served by the proxies in member order. This prevents a public package from shadowing an internal one. [`merge_proxy_versions`](../repositories/hosted-proxy-group.md#hosted-members-win-a-package-name) set to `true` turns the rule off.
- **Version lists are merged** across the members that serve the name, sorted ascending. A version offered twice is served by the first member in order. Provider platforms are merged per version. The unversioned module calls (`MODULE/SYSTEM` and `MODULE/SYSTEM/download`) mean the newest stable version of the merged list.
- **Downloads come from the owning member.** The package document and module download are answered by the member that serves the version, and the ticket URLs belong to that member's repository.
- **Signing keys stay with the member.** A hosted member signs with its repository key. A proxy member serves the upstream key and checksums. A group has no key of its own.
- **Details and ids are rewritten** to the group's namespace (`id`, `namespace`), because clients address the group.
- **Access.** Reading the group needs read access to it. A member the caller cannot read is skipped. The hosted-wins rule fails closed: a name held by a hosted member the caller cannot read is hidden from the proxies too.

## Limits and behaviour

- **Names.** Namespace, module name, system and provider type use Terraform's own rule: letters, digits, `-` and `_`, starting and ending with a letter or digit, at most 64 characters. `packages` is refused as a module name or provider type, because the web UI owns `/repos/REPO/packages/...`. `versions` is refused as a module system, because it would collide with the `versions` endpoint. Both are `400`, like any malformed name, version, OS or architecture.
- **Versions** are semver 2.0 without a `v` prefix, at most 100 characters. Pre-releases (`1.0.0-rc.1`) are allowed. Build metadata (`1.0.0+b1`) is refused. The unversioned module download skips pre-releases when it picks the newest stable version.
- **Immutable versions.** A live version is never overwritten; a second `PUT` is `409`. Delete it and publish again. The [trash](../operations/trash.md) does not block the coordinates. A provider platform is immutable too, but adding a missing platform to a published version is allowed. With [immutable releases](../operations/trash.md#immutable-releases) on, deleting a stable version is `409` unless an admin passes `?force=true`.
- **Size limits.** Module archives up to 256 MiB and provider zips up to 1 GiB. Larger bodies are `413`.
- **Archive safety.** Uploaded archives are validated before they are stored, and a failure is never `500`.
  - `400` for a corrupt or unsafe archive: not a valid zip or tar.gz, zero bytes, absolute paths, `..` segments, backslashes, NUL bytes, symlinks, hard links, duplicate entries, more than 20000 entries, an entry or total expanded size above 2 GiB (1 GiB total for a module), or a compression ratio above 1000 once the total passes 100 MiB.
  - `422` for a well-formed archive of the wrong shape: a module without `*.tf` or `*.tf.json` files at its root or in its single top-level directory, a provider zip without `terraform-provider-TYPE_vVERSION` at its root, or an invalid `terraform-registry-manifest.json`.
- **Quota.** The [storage quota](../repositories/cleanup-and-quotas.md#what-counts) is reserved before the upload is stored and released on every failure. Over quota is `507`. A module version is one artifact; each provider platform is one artifact.
- **Quarantine and license policy.** A [quarantined](../security/scanning.md#quarantine-after-upload) module or provider platform is not served: the registry call that would mint the ticket answers `403` with code `ARTIFACT_QUARANTINED`. A ticket minted before the quarantine keeps working until it expires, at most 15 minutes. Terraform uploads declare no license, so a `block` license policy does not refuse them at upload. Set a license [property](../repositories/properties.md) to bring the after-upload path into play.
- **Caching.** The `versions` endpoints answer ETag/304. They send `Cache-Control: public, max-age=N` for anonymous reads of a public repository and `private, no-cache` otherwise. The discovery document is always `public, max-age=300`. Ticket downloads are `private, no-store`.
- **Trash.** Each module version and each provider platform is its own item (`terraform_module`, `terraform_provider`), listed as `modules/MODULE/SYSTEM` or `providers/TYPE`, with the platform as `OS_ARCH` for providers. Restore every platform to bring a version back. See [trash](../operations/trash.md).
- **Cleanup.** A cleanup policy counts versions per module and per provider, with all platforms of a provider version together. See [cleanup policies](../repositories/cleanup-and-quotas.md#supported-formats).
- **Scanning and SBOM.** Archives are listed and searched like other artifacts. Module `.tf` files are not parsed for dependencies, so no SBOM components or vulnerability findings come from them.
- **Webhooks and metrics.** `artifact.uploaded` and `artifact.deleted` fire as for other formats. `openartifact_uploads_total{format="terraform"}` counts publish requests.
- **Catalog.** The web UI shows one row per module version and one per provider platform, with the install snippet for the module or provider source.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `must contain at least one dot` | The registry host has no dot, so the client reads it as a namespace on the public registry. Use a real DNS name. For a test on one machine, add an `/etc/hosts` entry and a certificate for that name. |
| `Failed to request discovery document` ... `x509: certificate signed by unknown authority` | The client does not trust the certificate. Install the CA in the system store or set `SSL_CERT_FILE`. There is no insecure mode. |
| `Failed to request discovery document` ... `server gave HTTP response to HTTPS client`, or connection refused | The host is reached on the wrong port or without TLS. Put TLS in front of OpenArtifact; discovery is always `https://`. |
| Discovery fails with a content type or invalid response error | A reverse proxy intercepts `/.well-known/` or rewrites the content type, or the host is not OpenArtifact. `curl -i https://HOST/.well-known/terraform.json` must show `200` and `Content-Type: application/json`. |
| `401`, `403` or `Unauthorized` on `init` for a private repository | No `credentials` block (or `TF_TOKEN_...`) for the exact host, an expired token, or a token without read access. |
| Registry call works, download fails with `403` | The ticket expired (15 minutes, for example a stalled plugin cache), or a replica with a different `AUTH_HMAC_KEY` minted it. Retry, and check `AUTH_HMAC_KEY` on all replicas. |
| Registry call works, download fails with a connection error or an `http://` URL | The proxy in front does not forward the public host or scheme (check `X-Forwarded-Host`, `X-Forwarded-Proto`, `OPENARTIFACT_TRUSTED_PROXIES`), or the path of `BASE_URL` is wrong. Fix it and restart. |
| `503` with `no GPG signing key is configured for this repository` | No active key for the repository and no global one. Run `oa gpg generate --activate`, or `oa gpg activate KEY --repository REPO`. |
| `Failed to install provider` ... `doesn't match any of the checksums` | The version changed after the lock file was written (platform added, or version deleted and republished). Run `terraform init -upgrade` or `terraform providers lock` for your platforms. |
| `does not support the platform linux_arm64` or `no available releases match` | No zip was uploaded for that OS and architecture, or the constraint matches nothing. `GET /repos/REPO/TYPE/versions` lists the platforms per version. |
| `501` with `unsupported module source` on a proxy | The registry points the module at a source the proxy does not fetch. See [module sources](#module-sources). Use a hosted repository for the module, or the source address directly. |
| `502` on a module download that worked before | The module changed upstream (moving branch or tag) or the archive failed the repack checks. The pinned copy is kept until the cache worker evicts it; see [module integrity](#module-integrity). |
| `502` on a proxy when downloading a provider or module | The upstream or release host is down, redirects to a blocked address, or served a file that fails its checksum or is not an archive. Nothing is stored. The server log names the repository, path and digests. |
| `409` on publish | The version or platform already exists, or the `protocols` of a new platform differ from the stored ones. Delete it first, or pass the same `?protocols=`. |
| `400` with `checksum` in the message on provider publish | `X-Checksum-Sha256` did not match the received bytes. Retry the upload. |
| `422` or `400` on publish | The archive was refused, see [archive safety](#limits-and-behaviour). The provider binary is named wrongly or the module has no `.tf` files. |
