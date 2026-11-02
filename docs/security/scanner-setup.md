<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Scanner setup: Trivy and Grype

`osv` works out of the box. `trivy` and `grype` are external programs: the server runs them as subprocesses and keeps their vulnerability databases in a cache directory. Neither is part of the release image (it is shell-less and holds only the server), so enable them with one of:

| Where the server runs | How |
|---|---|
| Binary / VM | install `trivy` and/or `grype` so they are on the server's `PATH`, or set [`OPENARTIFACT_TRIVY_PATH` / `OPENARTIFACT_GRYPE_PATH`](../getting-started/configuration.md#vulnerability-scanners-scanners) |
| Any, no rebuild | `OPENARTIFACT_TRIVY_INSTALL=auto` and/or `OPENARTIFACT_GRYPE_INSTALL=auto`: the server downloads a pinned, checksum-verified release into the cache directory ([details](#runtime-download-install-auto)) |
| Docker Compose | `OPENARTIFACT_SCANNERS=trivy` (or `grype`, `all`) in `docker/.env`, then `docker compose build openartifact`; the compose file already mounts a `scanner_cache` volume |
| Your own build | `docker build -f docker/Dockerfile --build-arg SCANNERS=trivy .` (`none` default, `trivy`, `grype`, `all`) |
| Released image, Kubernetes | build a derived image (below), and give the pod a writable volume for the cache directory |

The server resolves the binaries when it starts and logs, per scanner, the path it found (`vulnerability scanner available`, with `binary`, `cache_dir`, `db_state`) or a warning when it is missing. A binary installed later is found on the next request (no restart), but its database download only starts at server start (or, with `install: auto`, once the installer has put the binary in place).

## Runtime download (`install: auto`)

With `OPENARTIFACT_TRIVY_INSTALL=auto` (`scanners.trivy.install`) and/or `OPENARTIFACT_GRYPE_INSTALL=auto` the server installs the scanner itself, so the released image and a plain binary install work without a derived image. The default is `none`: nothing is downloaded and executed unless you ask for it.

- **Precedence**: a configured path (`OPENARTIFACT_TRIVY_PATH`) always wins, then a binary on `PATH`, then the installed copy. A configured path that does not exist is an error, never replaced by a download.
- **What is downloaded**: the release compiled into the server (Trivy 0.76.1, Grype 0.121.0 at the time of writing, the same versions the `SCANNERS` image variant bundles; `linux/amd64` and `linux/arm64` only). The URL is `<downloadBaseUrl>/<owner>/<repo>/releases/download/v<version>/<archive>.tar.gz`, for example `https://github.com/aquasecurity/trivy/releases/download/v0.76.1/trivy_0.76.1_Linux-64bit.tar.gz`.
- **How it is verified**: the archive is streamed to a temporary file under a size cap (256 MiB) while its sha256 is computed; a mismatch with the checksum pinned in the server deletes it and nothing is extracted. Only the scanner binary is extracted (cap 512 MiB), and the extracted binary's sha256 must also match its pinned value. An archive containing a symlink, a hard link, an absolute path or a `..` path, or a binary that is not a regular file, is rejected whole. The file is written 0755, fsynced and renamed into `<cacheDir>/bin/<scanner>/<version>/<scanner>`, so a crash never leaves a half-written binary. Nothing is executed before the checks pass.
- **Restarts**: an installed copy is re-hashed at startup against the pinned checksum and reused without downloading (also in offline mode); a copy that fails the check is discarded and downloaded again. Install the cache directory on a persistent volume to avoid downloading on every start.
- **Private install directory**: the installed binary is used only while its directories and file are private to the server.
  - `<cacheDir>/bin` and everything below it must be real directories and a regular file, with no symlinks.
  - They must be owned by the server's user and not writable by group or others.
  - The server checks this when it installs or reuses the copy and before every use. If the file changed since it was verified, it is hashed again and must match the pinned checksum.
  - A violation is reported as `scanner_install_failed` ("install directory is not private to the server's user"), and the binary is not run.
- **A persistent cache directory is required**: with no `OPENARTIFACT_SCANNER_CACHE_DIR` and no user cache directory (no `$XDG_CACHE_HOME` or `$HOME`) the default falls back to the shared temp directory; `install: auto` is then refused (logged at error level, status `scanner_install_failed` with "set OPENARTIFACT_SCANNER_CACHE_DIR") because a downloaded binary would be executed from a directory every local user can write to.
- **Several replicas on one volume**: replicas that share a cache volume take turns when they install or update a scanner, so each scanner is downloaded once.
  - Use a volume that supports file locking. Many network filesystems do not; there, give each replica its own volume (or an `emptyDir`).
  - On non-Unix platforms there is no cross-process lock.
  - Leftovers of an interrupted install are removed after 10 minutes, so another process's running download is never deleted.
- **Retries**: a failed download or checksum is retried with the same backoff as the database updater (1 minute up to 1 hour). Offline mode only adopts an already installed copy.
- **`noexec` mounts**: after installing, the server runs the binary once (`--version`). If the cache directory is on a `noexec` mount that fails and the scanner reports `scanner_install_noexec`; mount the volume with exec, point `OPENARTIFACT_SCANNER_CACHE_DIR` somewhere executable, or provide the binary yourself.
- **Mirrors**: set `OPENARTIFACT_SCANNER_DOWNLOAD_BASE_URL` (`scanners.downloadBaseUrl`, default `https://github.com`) to use your own download host.
  - The URL must be `https`, without credentials, query or fragment. A path prefix is allowed.
  - The mirror must serve GitHub's release layout, for example `<base>/aquasecurity/trivy/releases/download/v0.76.1/trivy_0.76.1_Linux-64bit.tar.gz` and `<base>/anchore/grype/releases/download/v0.121.0/grype_0.121.0_linux_amd64.tar.gz` (also the `arm64` archives).
  - The pinned checksums still apply, so a mirror cannot substitute other bytes.
  - `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` are honored. The mirror host may be on a private network.
  - Redirects are followed only over `https`, at most 3 times, and credentials are never forwarded.
  - Direct connections to private, loopback or link-local addresses are refused, except to the mirror itself. Behind a proxy the server can only check the scheme and literal IPs in a redirect, so the proxy's egress policy must keep redirects away from internal hosts.
- **Egress**: `github.com` and `release-assets.githubusercontent.com` (GitHub redirects release assets there; the redirect host can change, so allow-list it by name only if you must, and check the server log if downloads fail behind a firewall), or only your mirror. The databases need further hosts (see [Network access](#network-access)).
- **Status**: while downloading, the [readiness](#checking-scanner-readiness) endpoint reports `install_state: "installing"` and code `scanner_installing`; after a failure `install_state: "failed"` with `scanner_install_failed` or `scanner_install_noexec`. The Prometheus counter `openartifact_scanner_install_attempts_total{scanner,result}` counts attempts; `result` is one of `installed`, `reused`, `unsupported_platform`, `download_failed`, `checksum_mismatch`, `extract_failed`, `noexec`, `insecure_location`, `failed`.
- **Versions**: the pinned versions and checksums are part of the OpenArtifact release and change only with a new release. To use another version, install it yourself and set `OPENARTIFACT_TRIVY_PATH` or `OPENARTIFACT_GRYPE_PATH`.

Security note: `auto` makes the server download and execute third-party code at runtime. The checksums come from the upstream release's own `checksums.txt` files (fetched over TLS when the manifest was generated, not signature-verified), so the guarantee is that the server runs the build that was pinned in its release, not that upstream is trustworthy. If that is not acceptable, bundle the binaries at build time (the `SCANNERS` build arg) or provide them yourself.

## Databases and the cache directory

- **Scans never download.** A scan runs the CLI offline against the databases already in the cache directory (`trivy --skip-db-update --offline-scan`, `GRYPE_DB_AUTO_UPDATE=false`), so a slow or blocked network cannot eat the 120 s scan limit. If the database is not there, the scan fails at once with `scanner_db_unavailable`.
- **A background updater downloads them.** At startup (and then every `OPENARTIFACT_SCANNER_UPDATE_INTERVAL`, default 6h, at least 1h, `0` = once) the server runs `trivy image --download-db-only` and `grype db update` for each scanner that is installed. A failure is logged and retried with exponential backoff (1 minute up to 1 hour). The first download takes from under a minute (Trivy) to a few minutes (Grype); until it finishes the scanner is `updating` (see [readiness](#checking-scanner-readiness)).
- **The scanners get a trimmed environment**: Trivy and Grype run with `PATH`, `HOME`, `TMPDIR`/`TMP`/`TEMP`, `USER`, `LANG`/`LC_*`, `TZ`, the proxy variables (`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, `ALL_PROXY`, in either case), `SSL_CERT_FILE`, `SSL_CERT_DIR`, `XDG_CACHE_HOME`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `DOCKER_CONFIG` and any `TRIVY_*` / `GRYPE_*` variable of the server; the server's other variables (database password, signing keys, storage credentials) are not passed on. A scanner that needs another variable (for example a private registry token) must be given it as `TRIVY_*` / `GRYPE_*`.
- **Cache directory**: `OPENARTIFACT_SCANNER_CACHE_DIR` (`scanners.cacheDir`, an absolute path). Trivy uses `<dir>/trivy`, Grype `<dir>/grype`. The default is `<user cache dir>/openartifact/scanners` (`$XDG_CACHE_HOME` or `~/.cache`, else the temp directory, where `install: auto` is refused, see above). The image sets `/var/cache/openartifact/scanners`, owned by uid 10001, and declares it a `VOLUME`; keep it on a volume when the root filesystem is read-only, and persist it so a restart does not download again. In the image the environment variable is already set, so change it with the variable rather than the YAML file.
- **Disk**: the Trivy database takes about 1.6 GB and the Grype database about 3.3 GB (Trivy 0.76.1, Grype 0.121.0). Size the cache volume for both plus room for a second copy while a database is being refreshed.
  - Scans keep using the old database until the new one is ready.
  - Unfinished Grype download directories are removed before and after each update.
  - The Trivy Java index (several hundred MB) is not downloaded unless you set `OPENARTIFACT_TRIVY_JAVA_DB_REPOSITORY`. OpenArtifact scans package names and versions, not JARs.

## Network access

| Scanner | Needs outbound HTTPS to | Used for |
|---|---|---|
| `osv` | `api.osv.dev` | every scan |
| `trivy` | `mirror.gcr.io` (tried first; serves the database itself) and `ghcr.io` (its blobs come from `pkg-containers.githubusercontent.com`) | database download, by the background updater only |
| `grype` | `grype.anchore.io` (serves the listing and the archive itself) | database download, by the background updater only |
| any, with `install: auto` | `github.com` and `release-assets.githubusercontent.com` (or your `downloadBaseUrl`) | the scanner binary, once per version |

The scan itself needs no network for `trivy` and `grype`. In a restricted network, either allow those hosts or point the updater at a mirror: `OPENARTIFACT_TRIVY_DB_REPOSITORY` (an OCI repository holding a copy of `trivy-db`) and `OPENARTIFACT_GRYPE_DB_URL` (the URL of a grype `listing.json`). An HTTP(S) proxy configured in the server's environment (`HTTPS_PROXY`, `NO_PROXY`) is passed to the CLIs.

## Offline (air-gapped) mode

`OPENARTIFACT_SCANNER_OFFLINE=true` stops the updater from ever contacting the network. Fill the cache directory yourself, for example:

```sh
trivy image --download-db-only --cache-dir /srv/scanners/trivy            # on a connected host
GRYPE_DB_CACHE_DIR=/srv/scanners/grype grype db update                    # on a connected host
# copy /srv/scanners to the server's OPENARTIFACT_SCANNER_CACHE_DIR, owned by the server's user
```

With offline mode on and no database in the directory, the scanner reports `missing` and scans fail with `scanner_db_unavailable`. Refresh the files by hand to update; the server notices new database files on the next request.

## Docker and Kubernetes

Build the image with the scanners bundled (the binaries are copied, digest-pinned, from the upstream `aquasec/trivy` and `anchore/grype` images; the final stage still has no shell):

```sh
docker build -f docker/Dockerfile --build-arg RUNTIME=distroless --build-arg SCANNERS=trivy -t openartifact:trivy .
```

To keep using a released image, derive from it:

```dockerfile
FROM ghcr.io/openartifact/openartifact:X.Y.Z
COPY --from=docker.io/aquasec/trivy:0.76.1@sha256:790fd66d1697395d8693d1acaa1ea702406a20f109b6fc7bac8db623eca6c06c /usr/local/bin/trivy /usr/local/bin/trivy
# grype: COPY --from=docker.io/anchore/grype:v0.121.0@sha256:621d56d1d68106b787022c81546f7e0a36202a25d1239f0c20195fab8c16f7ea /grype /usr/local/bin/grype
```

On Kubernetes and Helm:

- Mount a writable volume at the cache directory. An `emptyDir` is enough; a PVC avoids downloading again after a restart.
- Make it writable by uid and gid 10001 (set the pod's `fsGroup: 10001`).
- Allow egress to the hosts above, or set the mirror variables. Keep the read-only root filesystem.
- With a separate volume per pod, each pod downloads its own copy. Replicas that share one volume download once (see [several replicas](#runtime-download-install-auto)).

## Checking scanner readiness

`GET /api/v1/repositories/{name}/vuln/scanners` (admin only) reports each scanner in the order `osv`, `trivy`, `grype`:

```json
{"scanners":[
  {"name":"osv","available":true,"ready":true,"db_state":null,"db_updated_at":null,"install_state":null,"code":null,"message":null,"docs_url":null},
  {"name":"trivy","available":true,"ready":false,"db_state":"updating","db_updated_at":null,"install_state":"ready",
   "code":"scanner_db_unavailable","message":"The vulnerability database is being downloaded: retry in a few minutes.","docs_url":"…"},
  {"name":"grype","available":false,"ready":false,"db_state":null,"db_updated_at":null,"install_state":null,
   "code":"scanner_unavailable","message":"grype was not found on the server's PATH: …","docs_url":"…"}]}
```

`available` is whether the binary starts; `ready` is whether a scan can run now. `db_state` is `ready`, `updating`, `failed` (the last download failed: see the server log) or `missing`. `install_state` (`installing`, `ready` or `failed`, else `null`) tells the progress of the [runtime download](#runtime-download-install-auto). It is `null` when the binary is on `PATH`, configured, or `install` is `none`. The response never contains filesystem paths or the download host. The web UI uses it to disable scanners that are not ready in the *Scan package* form and to show `message` with a link to this section.

| `code` | Meaning | Fix |
|---|---|---|
| `scanner_unavailable` | the binary was not found or is not executable | install it, set `OPENARTIFACT_TRIVY_PATH` / `OPENARTIFACT_GRYPE_PATH`, or set `…_INSTALL=auto` |
| `scanner_installing` | the pinned release is being downloaded (`install: auto`) | wait a minute or two |
| `scanner_install_failed` | the download, checksum check or extraction failed; there is no pinned build for this platform (the message names it); the install directory is not private to the server's user; the cache directory is the shared temp directory; or the installed binary disappeared or changed after it was verified (restart the server) | check egress to the download host (or `downloadBaseUrl`), fix the directory ownership and permissions, set `OPENARTIFACT_SCANNER_CACHE_DIR`; the server log has the cause; on other platforms provide the binary |
| `scanner_install_noexec` | the binary was installed but cannot be executed (a `noexec` mount) | mount the cache directory with exec or provide the binary |
| `scanner_db_unavailable` | no vulnerability database yet (still downloading, download failing, or offline without one) | wait, allow egress / set a mirror, or fill the cache in offline mode |

## Why external binaries

The design follows [zot](https://zotregistry.dev/)'s CVE scanning: scans run offline against a database that a background task refreshes on an interval with backoff, with a configurable database repository and readiness reporting. zot embeds Trivy as a Go library; OpenArtifact runs the CLIs instead, because that keeps the server binary at its current size and its module graph and vulnerability surface free of Trivy's, lets operators upgrade Trivy independently of the server, and is the only way to offer Grype too. Embedding would be reconsidered if Trivy exposed a stable library API.
