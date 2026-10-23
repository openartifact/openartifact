<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Scanning, SBOM, licenses and quarantine

On-demand vulnerability, secret and license scans and SBOM generation per repository, plus quarantine to block downloads of flagged artifacts.

Scans are **API-driven and manual**: vulnerability, secret and SBOM scans never run on upload. The one automatic check is a [license policy with action `block`](#blocking-licenses), which rejects an upload whose package metadata declares a blocked license. All scan endpoints are under `/api/v1/repositories/{name}/…`, need an admin ([rbac.md](rbac.md)) and store their results in the database. Scans are keyed by a caller-chosen `artifact_id` (at most 1024 characters, no control characters). Use the identifier the artifact is served under, otherwise [quarantine](#quarantine) cannot match it.

| Scan | Input | Source of data |
|---|---|---|
| [Vulnerabilities](#vulnerability-scans) | package name, version, ecosystem | OSV API, or the `trivy` / `grype` binary ([installing them](#installing-trivy-and-grype)) |
| [Secrets](#secret-scans) | text and/or metadata JSON you send | built-in regex rules |
| [Licenses](#license-scans-and-policies) | `artifact_id` | the stored package file ([how](licenses.md)) plus the artifact's [properties](../repositories/properties.md) |
| [SBOM](#sbom) | `artifact_id`, format | `artifact_id`; components from the stored package file and the `dependencies` property |

Vulnerability scans look up the name/version you pass, secret scans inspect only the content you pass, and SBOM components come from the stored package file plus the properties you set. License scans also open the stored package file: they read the licenses it declares itself ([License metadata](licenses.md)) and add the ones in the artifact's properties.

## Vulnerability scans

```sh
curl -sS -X POST HOST/api/v1/repositories/REPO/vuln/scan -H 'Authorization: Bearer <TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{"artifact_id":"lodash@4.17.20","package_name":"lodash","package_version":"4.17.20","ecosystem":"npm","scanner":"osv"}'
curl -sS -H 'Authorization: Bearer <TOKEN>' 'HOST/api/v1/repositories/REPO/vuln/scans?vulnerable=true'
```

| Endpoint | |
|---|---|
| `POST …/vuln/scan` | body `artifact_id`, `package_name`, `package_version`, `ecosystem` (all required), `scanner` (`osv` default, `trivy`, `grype`) and/or `scanners` (a list of those names, to run several at once); an unknown name is `400 validation_failed` |
| `GET …/vuln/scanners` | which scanners can run now and, if not, why ([readiness](#checking-scanner-readiness)) |
| `GET …/vuln/scans[?vulnerable=true]` | list the results of one repository |
| `GET /api/v1/vuln/scans` | the [overview](#the-overview-across-repositories): scans of all repositories, worst first |
| `GET /api/v1/vuln/scans/summary` | counts for the overview's cards |
| `GET/DELETE …/vuln/scans/{id}` | read / delete one result |
| `GET …/vuln/scans/{id}/export` | one scan's findings as [SARIF, CSV or JSON](#exporting-findings-sarif-csv-json) |
| `GET /api/v1/vuln/scans/export` | the findings of all scans the overview's filters select, same formats |
| `POST …/vuln/scans/{id}/quarantine` | body `{"quarantined":true\|false}` |

- `POST` returns at once with `scan_status: "scanning"`; the scan runs in the background (120 s limit). Poll `GET …/vuln/scans/{id}` until the status is `completed`, `partial` or `failed`. At most 4 scans run at a time per server; further requests get 429 `too_many_scans` with `Retry-After: 10`.
- A result carries `vulnerabilities` (id, summary, severity, CVSS, fixed-in version, references, published date), the counts `vuln_count`, `critical_count`, `high_count`, `medium_count`, `low_count`, the `scanner` that ran (several are stored sorted and comma-joined, for example `osv,trivy`) and `failed_scanners`.
- `ecosystem` uses these names: `npm`, `pypi`, `maven`, `cargo`, `go`, `rubygems`, `nuget`, `deb`, `rpm`, `helm` (OSV); the other backends support a subset: Trivy `npm`, `pypi`, `maven`, `cargo`, `go`, `rubygems`, `nuget`; Grype `npm`, `pypi`, `maven`, `cargo`, `rubygems`, `nuget`, `go`. Both scan the one package you name, not its dependencies: Trivy is given a generated lockfile that pins it (`package-lock.json`, `Cargo.lock`, `Gemfile.lock`, `packages.lock.json`, or `requirements.txt`, `go.mod`, `pom.xml`), Grype a Package URL (`pkg:npm/%40angular/core@8.0.0`). `deb` and `rpm` are not scannable with Grype (the result would depend on a distribution that a name and version do not carry) and are reported as `unsupported_ecosystem` rather than as clean.
- `osv` needs outbound HTTPS to `api.osv.dev` and nothing else. `trivy` and `grype` are separate programs that the **release image does not contain**; see [Installing Trivy and Grype](#installing-trivy-and-grype). A scan that selects one that is not installed (or has no vulnerability database yet) is recorded as failed with an actionable `scan_error`.

### The overview across repositories

`GET /api/v1/vuln/scans` (administrators) lists the scans of every repository at once, which is what the **Security → Vulnerabilities** page opens with. Rows are summaries (the repository's name and format, the package, the counts, the status, the scanners, the quarantine flag) without the findings; read those with `GET …/vuln/scans/{id}`. The order is fixed, worst first: most critical findings, then high, medium and low, then newest, then id.

| Query | Meaning |
|---|---|
| `repo` | only this repository (by name) |
| `severity` | `critical`, `high`, `medium` or `low`: scans with at least one finding of that severity **or worse** |
| `status` | `completed`, `partial`, `failed` or `scanning` (pending or running) |
| `scanner` | `osv`, `trivy` or `grype`; a scan run by several matches each |
| `quarantined` | `true` or `false` |
| `limit` | page size, default 25, at most 100 |
| `cursor` | the `next_cursor` of the previous page; send the same filters with it |

The response is `{scans, has_more, next_cursor}`: keyset paging, so no row is counted and a deep page costs what the first costs. An unknown filter value or a malformed cursor is `400 validation_failed`. `GET /api/v1/vuln/scans/summary[?repo=NAME]` returns `{total, completed, partial, failed, scanning, quarantined, severity_at_least: {critical, high, medium, low}}` in one query; `severity_at_least.high` is the number of scans with a critical or a high finding, matching the `severity` filter. Failed and partial scans, critical findings, quarantined results and a repository filter are each served from an index (migration `041_scan_jobs_vuln_problems.no_tx.sql` adds the one for failed and partial scans, built concurrently).

### Scanner failures

A scanner that cannot produce a result is never reported as "no vulnerabilities":

| `scan_status` | Meaning |
|---|---|
| `completed` | every requested scanner ran; `vulnerabilities` is its (deduplicated, by id) findings |
| `partial` | several scanners were requested and some failed; `vulnerabilities` holds the findings of the ones that ran and `failed_scanners` names the others |
| `failed` | no scanner produced a result; `vulnerabilities` is empty and the counts are 0, which means *not scanned*, not *clean* |

For `partial` and `failed` results `scan_error` summarizes each failed scanner as `<scanner>: <reason>`, separated by `; `, for example `trivy: scanner is not available on the server (scanner_unavailable), setup guide: <url>; osv: scan timed out`. The reason is one of a fixed set, so the response never contains command lines, paths, stderr or upstream bodies (those are written to the server log only):

| Cause | `scan_error` reason |
|---|---|
| `trivy`/`grype` not installed or not startable | `scanner is not available on the server (scanner_unavailable), setup guide: <url>` |
| `trivy`/`grype` installed but its vulnerability database is not downloaded yet, or the CLI reports a missing/corrupt database | `scanner vulnerability database is not ready (scanner_db_unavailable), setup guide: <url>` |
| 120 s limit reached | `scan timed out` |
| the scan was canceled | `scan was canceled` |
| the CLI exited with an error | `scanner exited with an error` |
| the CLI or OSV printed something that is not valid JSON | `scanner output could not be parsed` |
| the CLI printed more than 32 MiB | `scanner output exceeded the size limit` |
| ecosystem unknown to that scanner | `ecosystem is not supported by this scanner` |
| OSV unreachable or answering non-200 | `vulnerability database request failed` |
| anything else | `scan failed` |

Failed vulnerability scans stored before this behavior (which kept the raw scanner error) have their `scan_error` replaced by `scan failed` when the database is migrated. The web UI shows them as *Not scanned* and *Partial* instead of as clean.

The MCP tool `get_vulnerabilities` summarizes stored results and counts `failed` and `partial` scans in `failed_scans` ([MCP](../integrations/mcp.md)).

## Installing Trivy and Grype

`osv` works out of the box. `trivy` and `grype` are external programs: the server runs them as subprocesses and keeps their vulnerability databases in a cache directory. Neither is part of the release image (it is shell-less and holds only the server), so enable them with one of:

| Where the server runs | How |
|---|---|
| Binary / VM | install `trivy` and/or `grype` so they are on the server's `PATH`, or set [`OPENARTIFACT_TRIVY_PATH` / `OPENARTIFACT_GRYPE_PATH`](../getting-started/configuration.md#vulnerability-scanners-scanners) |
| Any, no rebuild | `OPENARTIFACT_TRIVY_INSTALL=auto` and/or `OPENARTIFACT_GRYPE_INSTALL=auto`: the server downloads a pinned, checksum-verified release into the cache directory ([details](#runtime-download-install-auto)) |
| Docker Compose | `OPENARTIFACT_SCANNERS=trivy` (or `grype`, `all`) in `docker/.env`, then `docker compose build openartifact`; the compose file already mounts a `scanner_cache` volume |
| Your own build | `docker build -f docker/Dockerfile --build-arg SCANNERS=trivy .` (`none` default, `trivy`, `grype`, `all`) |
| Released image, Kubernetes | build a derived image (below), and give the pod a writable volume for the cache directory |

The server resolves the binaries when it starts and logs, per scanner, the path it found (`vulnerability scanner available`, with `binary`, `cache_dir`, `db_state`) or a warning when it is missing. A binary installed later is found on the next request (no restart), but its database download only starts at server start (or, with `install: auto`, once the installer has put the binary in place).

### Runtime download (`install: auto`)

With `OPENARTIFACT_TRIVY_INSTALL=auto` (`scanners.trivy.install`) and/or `OPENARTIFACT_GRYPE_INSTALL=auto` the server installs the scanner itself, so the released image and a plain binary install work without a derived image. The default is `none`: nothing is downloaded and executed unless you ask for it.

- **Precedence**: a configured path (`OPENARTIFACT_TRIVY_PATH`) always wins, then a binary on `PATH`, then the installed copy. A configured path that does not exist is an error, never replaced by a download.
- **What is downloaded**: the release compiled into the server (Trivy 0.76.1, Grype 0.121.0 at the time of writing, the same versions the `SCANNERS` image variant bundles; `linux/amd64` and `linux/arm64` only). The URL is `<downloadBaseUrl>/<owner>/<repo>/releases/download/v<version>/<archive>.tar.gz`, for example `https://github.com/aquasecurity/trivy/releases/download/v0.76.1/trivy_0.76.1_Linux-64bit.tar.gz`.
- **How it is verified**: the archive is streamed to a temporary file under a size cap (256 MiB) while its sha256 is computed; a mismatch with the checksum pinned in the server deletes it and nothing is extracted. Only the scanner binary is extracted (cap 512 MiB), and the extracted binary's sha256 must also match its pinned value. An archive containing a symlink, a hard link, an absolute path or a `..` path, or a binary that is not a regular file, is rejected whole. The file is written 0755, fsynced and renamed into `<cacheDir>/bin/<scanner>/<version>/<scanner>`, so a crash never leaves a half-written binary. Nothing is executed before the checks pass.
- **Restarts**: an installed copy is re-hashed at startup against the pinned checksum and reused without downloading (also in offline mode); a copy that fails the check is discarded and downloaded again. Install the cache directory on a persistent volume to avoid downloading on every start.
- **The installed file is trusted only while it is private**: `<cacheDir>/bin`, `bin/<scanner>`, `bin/<scanner>/<version>` and the binary must be real directories and a regular file (no symlinks), owned by the user the server runs as, and not writable by group or others. This is checked when the copy is installed or reused and again before every use; if the file changed since it was verified (another inode, size or modification time) it is hashed again and must still match the pinned checksum. A violation is reported as `scanner_install_failed` ("install directory is not private to the server's user") and the binary is not executed. The binary is written non-executable and only given mode 0755 after its checksum matched.
- **A persistent cache directory is required**: with no `OPENARTIFACT_SCANNER_CACHE_DIR` and no user cache directory (no `$XDG_CACHE_HOME` or `$HOME`) the default falls back to the shared temp directory; `install: auto` is then refused (logged at error level, status `scanner_install_failed` with "set OPENARTIFACT_SCANNER_CACHE_DIR") because a downloaded binary would be executed from a directory every local user can write to.
- **Several replicas on one volume**: each scanner has a lock file (`<cache directory>/.trivy.lock` and `.grype.lock`; advisory `flock`) held while the binary is installed and while its database is updated, so replicas that share a `ReadWriteMany` cache volume take turns and download once instead of racing; a replica waiting for the lock waits until the holder finishes (or its own shutdown). Interrupted installs leave `.archive-*` and `.bin-*` files that are cleaned up only when older than the install timeout (10 minutes), so another process's running download is never deleted. `flock` is advisory and local to the volume's semantics: on a network filesystem without lock support, give each replica its own cache volume (or an `emptyDir`). On non-Unix platforms there is no cross-process lock.
- **Retries**: a failed download or checksum is retried with the same backoff as the database updater (1 minute up to 1 hour). Offline mode only adopts an already installed copy.
- **`noexec` mounts**: after installing, the server runs the binary once (`--version`). If the cache directory is on a `noexec` mount that fails and the scanner reports `scanner_install_noexec`; mount the volume with exec, point `OPENARTIFACT_SCANNER_CACHE_DIR` somewhere executable, or provide the binary yourself.
- **Mirrors**: `OPENARTIFACT_SCANNER_DOWNLOAD_BASE_URL` (`scanners.downloadBaseUrl`, default `https://github.com`) must be an `https` URL without credentials, query or fragment (a path prefix is allowed). The mirror must serve GitHub's release layout: `<base>/aquasecurity/trivy/releases/download/v0.76.1/trivy_0.76.1_Linux-64bit.tar.gz`, `…/aquasecurity/trivy/…/trivy_0.76.1_Linux-ARM64.tar.gz`, `<base>/anchore/grype/releases/download/v0.121.0/grype_0.121.0_linux_amd64.tar.gz` and `…_linux_arm64.tar.gz`. The pinned checksums apply, so a mirror cannot substitute other bytes. `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` are honored. The mirror host itself may be on a private network. A redirect is followed only over `https`, at most 3 times, and credentials are never forwarded. Direct connections are refused at connect time (after name resolution, so a name that resolves to an internal address is caught too) when they would reach a private, loopback or link-local address, except the mirror itself. **Behind a proxy** the proxy resolves and connects, so the server can only check the scheme and literal IP addresses in a redirect: the proxy's own egress policy is what keeps a redirect away from internal hosts.
- **Egress**: `github.com` and `release-assets.githubusercontent.com` (GitHub redirects release assets there; the redirect host can change, so allow-list it by name only if you must, and check the server log if downloads fail behind a firewall), or only your mirror. The databases need further hosts (see [Network access](#network-access)).
- **Status**: while downloading, the [readiness](#checking-scanner-readiness) endpoint reports `install_state: "installing"` and code `scanner_installing`; after a failure `install_state: "failed"` with `scanner_install_failed` or `scanner_install_noexec`. The Prometheus counter `openartifact_scanner_install_attempts_total{scanner,result}` counts attempts; `result` is one of `installed`, `reused`, `unsupported_platform`, `download_failed`, `checksum_mismatch`, `extract_failed`, `noexec`, `insecure_location`, `failed`.
- **Bumps**: the pinned versions and checksums are in `internal/vuln/scanners_manifest.json`, compiled into the server. They change only with a new OpenArtifact release: `scripts/update-scanner-manifest.sh` reads the versions from the digest-pinned images in `docker/Dockerfile`, downloads the upstream `*_checksums.txt` and archives, verifies them and rewrites the manifest (`--check` reports a stale one); a unit test fails if the manifest and the Dockerfile disagree. An operator who wants another version installs it and sets `OPENARTIFACT_TRIVY_PATH`.

Security note: `auto` makes the server download and execute third-party code at runtime. The checksums come from the upstream release's own `checksums.txt` files (fetched over TLS when the manifest was generated, not signature-verified), so the guarantee is that the server runs the build that was pinned in its release, not that upstream is trustworthy. If that is not acceptable, bundle the binaries at build time (the `SCANNERS` build arg) or provide them yourself.

### Databases and the cache directory

- **Scans never download.** A scan runs the CLI offline against the databases already in the cache directory (`trivy --skip-db-update --offline-scan`, `GRYPE_DB_AUTO_UPDATE=false`), so a slow or blocked network cannot eat the 120 s scan limit. If the database is not there, the scan fails at once with `scanner_db_unavailable`.
- **A background updater downloads them.** At startup (and then every `OPENARTIFACT_SCANNER_UPDATE_INTERVAL`, default 6h, at least 1h, `0` = once) the server runs `trivy image --download-db-only` and `grype db update` for each scanner that is installed. A failure is logged and retried with exponential backoff (1 minute up to 1 hour). The first download takes from under a minute (Trivy) to a few minutes (Grype); until it finishes the scanner is `updating` (see [readiness](#checking-scanner-readiness)).
- **The scanners get a trimmed environment**: Trivy and Grype run with `PATH`, `HOME`, `TMPDIR`/`TMP`/`TEMP`, `USER`, `LANG`/`LC_*`, `TZ`, the proxy variables (`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, `ALL_PROXY`, in either case), `SSL_CERT_FILE`, `SSL_CERT_DIR`, `XDG_CACHE_HOME`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `DOCKER_CONFIG` and any `TRIVY_*` / `GRYPE_*` variable of the server; the server's other variables (database password, signing keys, storage credentials) are not passed on. A scanner that needs another variable (for example a private registry token) must be given it as `TRIVY_*` / `GRYPE_*`.
- **Cache directory**: `OPENARTIFACT_SCANNER_CACHE_DIR` (`scanners.cacheDir`, an absolute path). Trivy uses `<dir>/trivy`, Grype `<dir>/grype`. The default is `<user cache dir>/openartifact/scanners` (`$XDG_CACHE_HOME` or `~/.cache`, else the temp directory, where `install: auto` is refused, see above). The image sets `/var/cache/openartifact/scanners`, owned by uid 10001, and declares it a `VOLUME`; keep it on a volume when the root filesystem is read-only, and persist it so a restart does not download again. In the image the environment variable is already set, so change it with the variable rather than the YAML file.
- **Disk**: measured with Trivy 0.76.1 and Grype 0.121.0, the Trivy database takes about 1.6 GB on disk and the Grype database about 3.3 GB; a Grype download takes a few minutes on a fast link. Size the cache volume for both plus headroom for a second copy of the database being updated: Trivy refreshes into a staging directory (`<cacheDir>/trivy/.staging`, seeded with hard links so an unchanged database is not downloaded again) and swaps the finished database in atomically (`renameat2` on Linux), so scans keep using the old database for the whole refresh; Grype activates a database by renaming its own download directory, and unfinished `grype-db-download*` directories (up to the database's size) are removed before and after each run. The Java index (`trivy-java-db`, several hundred MB) is **not** downloaded unless `OPENARTIFACT_TRIVY_JAVA_DB_REPOSITORY` is set: OpenArtifact scans package names and versions, not JARs.

### Network access

| Scanner | Needs outbound HTTPS to | Used for |
|---|---|---|
| `osv` | `api.osv.dev` | every scan |
| `trivy` | `mirror.gcr.io` (tried first; serves the database itself) and `ghcr.io` (its blobs come from `pkg-containers.githubusercontent.com`) | database download, by the background updater only |
| `grype` | `grype.anchore.io` (serves the listing and the archive itself) | database download, by the background updater only |
| any, with `install: auto` | `github.com` and `release-assets.githubusercontent.com` (or your `downloadBaseUrl`) | the scanner binary, once per version |

The scan itself needs no network for `trivy` and `grype`. In a restricted network, either allow those hosts or point the updater at a mirror: `OPENARTIFACT_TRIVY_DB_REPOSITORY` (an OCI repository holding a copy of `trivy-db`) and `OPENARTIFACT_GRYPE_DB_URL` (the URL of a grype `listing.json`). An HTTP(S) proxy configured in the server's environment (`HTTPS_PROXY`, `NO_PROXY`) is passed to the CLIs.

### Offline (air-gapped) mode

`OPENARTIFACT_SCANNER_OFFLINE=true` stops the updater from ever contacting the network. Fill the cache directory yourself, for example:

```sh
trivy image --download-db-only --cache-dir /srv/scanners/trivy            # on a connected host
GRYPE_DB_CACHE_DIR=/srv/scanners/grype grype db update                    # on a connected host
# copy /srv/scanners to the server's OPENARTIFACT_SCANNER_CACHE_DIR, owned by the server's user
```

With offline mode on and no database in the directory, the scanner reports `missing` and scans fail with `scanner_db_unavailable`. Refresh the files by hand to update; the server notices new database files on the next request.

### Docker and Kubernetes

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

Kubernetes and Helm: mount a writable volume (an `emptyDir` is enough, a PVC avoids re-downloading after a restart) at the cache directory, writable by uid/gid 10001 (set the pod's `fsGroup: 10001`). Allow egress to the hosts above (or set the mirror variables), and run with a read-only root filesystem as usual. With several replicas on separate volumes (an `emptyDir` or a `ReadWriteOnce` PVC per pod) each pod downloads its own copy; replicas that share one `ReadWriteMany` volume take turns through the [per-scanner lock](#runtime-download-install-auto) and download once.

### Checking scanner readiness

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

### Why external binaries

The design follows [zot](https://zotregistry.dev/)'s CVE scanning: scans run offline against a database that a background task refreshes on an interval with backoff, with a configurable database repository and readiness reporting. zot embeds Trivy as a Go library; OpenArtifact runs the CLIs instead, because that keeps the server binary at its current size and its module graph and vulnerability surface free of Trivy's, lets operators upgrade Trivy independently of the server, and is the only way to offer Grype too. Embedding would be reconsidered if Trivy exposed a stable library API.

## Exporting findings (SARIF, CSV, JSON)

Vulnerability and secret scans can be downloaded for other tools. All export routes need an admin, take `?format=sarif|csv|json` (default `json`; anything else is `400 validation_failed`) and answer with `Content-Disposition: attachment`. The **Security → Vulnerabilities** and **Secrets** pages have SARIF, CSV and JSON buttons: the vulnerability page exports the whole selection of its active filters (all pages, not just the loaded rows), the secrets page the selected repository.

| Export | Route |
|---|---|
| One vulnerability scan | `GET /api/v1/repositories/{name}/vuln/scans/{id}/export` |
| Vulnerability scans across repositories | `GET /api/v1/vuln/scans/export`, with the [overview's](#the-overview-across-repositories) `repo`, `severity`, `status`, `scanner` and `quarantined` filters (`limit` and `cursor` are ignored; at most 2000 scans are read) |
| Secret scans of a repository | `GET /api/v1/repositories/{name}/secrets/scans/export[?flagged=true]` |
| One secret scan | `GET /api/v1/repositories/{name}/secrets/scans/{id}/export` |

**SARIF** is 2.1.0 (`application/sarif+json`), checked against the official `sarif-schema-2.1.0.json` by an integration-tagged test that fetches it from the OASIS repository:

- One **run** per scanner (`osv`, `trivy`, `grype`; the secret export has one run, `openartifact-secrets`). A scan that ran several scanners stores one merged finding list that does not say which scanner found what, so it gets its own run named after all of them (`osv+trivy`) instead of being duplicated across runs. A `partial` scan marks its run `executionSuccessful: false`; failed and pending scans have no findings and add none. With nothing to report the file holds one empty run.
- One **rule** per vulnerability id (the CVE/GHSA/OSV id; `helpUri` is `https://osv.dev/vulnerability/<id>`) or per secret type. The severity is the result `level` (critical and high are `error`, medium `warning`, low and unknown `note`) and the rule's `security-severity` property, which GitHub turns into its critical/high/medium/low label: the finding's CVSS score when the scanner gave one, otherwise 9.5, 8.0, 5.5, 3.0 (0.1 for unknown).
- One **result** per package and vulnerability (or secret finding), located by the relative artifact path `<repository>/<artifact_id>` (`artifactLocation.uri`, each segment percent-encoded, line 1) with the package URL as a logical location (`pkg:npm/lodash@4.17.20`; `pkg:generic/...?ecosystem=deb` for ecosystems without a Package URL type) and `partialFingerprints["openartifact/v1"]`, a hash of the repository, artifact, package and vulnerability id, so an upload of the same finding matches the earlier one.
- GitHub's limits are respected: rule ids are at most 255 characters (a longer id is cut and given a hash suffix), messages at most 1024, and at most 25000 results. If there are more, the **least severe are dropped**, the affected runs get `properties.truncated: true` and the response has `X-Export-Truncated: true` (also set when the cross-repository export hit its 2000-scan cap).
- Secret exports contain the secret type, description, severity and artifact only: no value, not even the redacted one, and no `line_hint`.

**CSV** has one row per finding: `repository, artifact_id, package_name, package_version, ecosystem, scanner, scan_id, scan_status, vulnerability_id, severity, cvss, fixed_in, summary, published_at, references` for vulnerabilities and `repository, artifact_id, scan_id, scan_status, type, description, severity` for secrets. Every cell that starts with `=`, `+`, `-`, `@`, a tab or a carriage return is prefixed with `'`, so a spreadsheet does not evaluate an advisory summary as a formula.

**JSON** is the scan as the read route returns it (`vulnerabilities` included); the cross-repository export is `{"scans": [...], "truncated": false}` with a `repository_name` on each scan, and the secret export drops `redacted_value` and `line_hint`.

### Uploading to GitHub code scanning

```sh
curl -sS -H 'Authorization: Bearer <TOKEN>' \
  'HOST/api/v1/vuln/scans/export?format=sarif&severity=high' -o openartifact.sarif

gh api repos/OWNER/REPO/code-scanning/sarifs \
  -f commit_sha="$(git rev-parse HEAD)" -f ref=refs/heads/main \
  -f sarif="$(gzip -c openartifact.sarif | base64 -w0)"
```

The API takes the gzipped, base64-encoded file (at most 10 MB compressed) and needs a token with `security_events` write access. In Actions use `github/codeql-action/upload-sarif` with `sarif_file: openartifact.sarif` instead. GitHub shows each run as its own tool; artifact paths are not files of the GitHub repository, so alerts appear in the Security tab without a source line.

### Importing into DefectDojo

Import the file as scan type **SARIF** (UI: *Import Scan Results*, or `POST /api/v2/import-scan/` with `scan_type=SARIF` and `file=@openartifact.sarif`). Each result's `level`, `ruleId` and artifact path become the finding's severity, title and file path; use *Reimport* so later uploads update the same findings.

## Secret scans

```sh
curl -sS -X POST HOST/api/v1/repositories/REPO/secrets/scan -H 'Authorization: Bearer <TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{"artifact_id":"app-1.0.tgz","content":"<text to inspect>","metadata":{"name":"app"}}'
```

| Endpoint | |
|---|---|
| `POST …/secrets/scan` | `artifact_id` required; `content` (text) and/or `metadata` (object) are scanned |
| `GET …/secrets/scans[?flagged=true]` | list |
| `GET/DELETE …/secrets/scans/{id}` | read / delete |
| `GET …/secrets/scans/export[?flagged=true]`, `GET …/secrets/scans/{id}/export` | findings of all (or flagged) scans of the repository, or of one scan, as [SARIF, CSV or JSON](#exporting-findings-sarif-csv-json); never a secret value |
| `POST …/secrets/scans/{id}/quarantine` | `{"quarantined":true\|false}`; applies to the scan's `artifact_id` |

The result has `scan_status` `clean` or `flagged`, `finding_count` and `findings`. Findings never contain the secret: `type`, `description`, `severity`, `redacted_value` (at most the first eighth of the value, up to four characters, then `****`) and a `line_hint` around the match with every detected secret in the window masked.

| Rule (`type`) | Severity |
|---|---|
| `aws_access_key`, `aws_secret_key`, `stripe_key`, `private_key` | critical |
| `github_token`, `github_oauth`, `github_app_token`, `slack_token`, `google_api_key`, `npm_token`, `docker_hub_token` | high |
| `generic_api_key`, `generic_password`, `jwt_token` | medium |

The rule set is built in and not configurable.

## License scans and policies

A scan evaluates two sources and uses both:

1. **The stored package file**, when `artifact_id` is the id of a stored package of a format whose file declares licenses (PyPI, npm, Cargo, NuGet, Maven, RubyGems): the licenses the package declares in its own metadata. See [License metadata](licenses.md) for what is read and in which order.
2. **The artifact's properties** (set them with the properties API, see [properties](../repositories/properties.md)): `license`, `License`, `spdx_id`, `spdxId`, or a comma-separated `licenses` / `Licenses`. This is how a license is recorded for raw, Go, Conan and imported artifacts, and for ids that are not a stored package.

A property can add a license; it cannot hide the one the package file declares. Common names are normalized to SPDX ids (for example `MIT License` → `MIT`, `Apache 2.0` → `Apache-2.0`, `gpl-3.0` → `GPL-3.0-only`); anything else is kept as written. Entries that are not SPDX expressions after normalization (a raw `BSD`, the classifier `BSD License`, free text) are listed in `unresolved_licenses` and flagged in the web UI: they are kept as written because they do not name one license, so a policy can only match them by that exact text. Each result carries `license_source` (`artifact`, or `client_declared` when the licenses only come from properties). A package file whose metadata is oversized or ambiguous makes the scan answer `422 unsafe_artifact`; with a blocking policy, so does a package file that cannot be read (`422 unreadable_artifact`). A request containing `metadata` is rejected.

| Endpoint | |
|---|---|
| `POST …/license/scan` | body `{"artifact_id":"…"}`; returns the licenses, `policy_violations` (always an array), `quarantined` and `quarantine_approved` |
| `GET …/license/scans`, `GET …/license/scans/{id}` | results (with `quarantined` / `quarantine_approved`; `policy_violations` are only returned by `POST …/license/scan`) |
| `POST …/license/scans/{id}/quarantine` | body `{"quarantined":true\|false}`; quarantine an artifact by hand or [approve](#quarantine-after-upload) it |
| `GET/POST …/license/policies`, `PUT/DELETE …/license/policies/{id}` | policies of this repository (the list also shows global ones) |
| `GET/POST /api/v1/admin/license/policies` | global policies (apply to every repository) |

Policy fields: `name` (required, max 255), `description`, `action` (`warn` default, `block`, `allow`), `allowed_licenses`, `denied_licenses`, `enabled`. **`enabled` defaults to `false` when omitted on create**, and disabled policies are ignored.

Evaluation: a license is a violation when any identifier in it is in `denied_licenses`; and if `allowed_licenses` is set, a violation is raised when none of the artifact's licenses is allowed. Matching is case-insensitive and treats the spellings of one license version as the same: `GPL-3.0`, `GPL-3.0-only`, `GPL-3.0+`, `GPL-3.0-or-later` and `GPLv3` all match one policy entry, so a policy entry written with any of them covers the others (`GPL-3.0` is a different entry from `GPL-2.0`). SPDX expressions are split: `A AND B` needs every identifier allowed, `A OR B` needs one. Each violation is returned with `policy_id`, `policy_name`, `action` and `violation`. **An artifact that declares no license never violates a policy**, not even an allow list: policies match licenses, they do not require one. License fields given as objects (`{"type": "MIT"}`, the legacy npm and Maven shapes) are read like plain strings. A declared name that is not a known alias or SPDX identifier is compared as written: it cannot match a deny entry, but it is not in an allow list either.

What the `action` does:

| `action` | License scan, properties change | Upload |
|---|---|---|
| `allow` | violations are reported; nothing else happens | not evaluated |
| `warn` | violations are reported; nothing else happens | the violation is logged and the upload goes through |
| `block` | the artifact is [quarantined](#quarantine-after-upload) | rejected with `403 LICENSE_BLOCKED` |

Only enabled policies count, and a repository's own policies apply together with the global ones.

### Blocking licenses

When the repository has an enabled policy with `action: "block"`, an upload is rejected if a license declared in the package file itself is in `denied_licenses`, or if `allowed_licenses` is set and none of the declared licenses is allowed. The check runs after the metadata has been read and validated and before anything is stored, so a rejected upload leaves no package behind:

```http
HTTP/1.1 403 Forbidden
{"error":"upload rejected: the package license is blocked by a license policy (license GPL-3.0-only is denied)",
 "code":"LICENSE_BLOCKED","licenses":["GPL-3.0-only"],
 "policies":[{"policy_id":"…","policy_name":"no copyleft","action":"block","violation":"license GPL-3.0-only is denied"}]}
```

`licenses` lists the normalized licenses that were read from the package and `policies` the violated blocking policies. If the policies cannot be read the upload is refused rather than admitted unchecked: `500` with `code` `LICENSE_POLICY_UNAVAILABLE`.

The license is read from the uploaded package file itself, not from what the client says about it (details and precedence in [License metadata](licenses.md)); the client's own fields are only a fallback for a file that declares none. Per format:

| Format | Where the license is read |
|---|---|
| npm | `license` (a string or `{"type": …}`), else the legacy `licenses` array, of the `package.json` inside the tarball of every version in the publish body; the manifest in the publish JSON is only the fallback. Unpublish (`-rev`) requests are not checked |
| Cargo | `package.license` of the `Cargo.toml` inside the `.crate`; the publish metadata's `license` is only the fallback. The crate is read before the duplicate-version check |
| RubyGems | the gemspec `licenses` in the gem's `metadata.gz`. Checked before the duplicate-version check, so re-pushing a version under a block policy answers 403, not 409 |
| PyPI | the wheel's `METADATA` or the sdist's `PKG-INFO` (`.tar.gz`, `.zip`): `License-Expression`, else a short single-line `License:`, else the `License ::` classifiers. The form fields `license_expression`, `license` and `classifiers` are only the fallback for files that declare none or are not a wheel or sdist |
| NuGet | `<license type="expression">` in the root `.nuspec`, else a `licenseUrl` that names one license (`https://licenses.nuget.org/<expression>` or a canonical URL such as apache.org or opensource.org). Any other `licenseUrl` carries no identifier and is not checked |
| Maven | the `<licenses><license><name>` of an uploaded `.pom` (the `<url>` when a license has no name and the URL is a canonical one); licenses are not inherited from a parent pom. A rejected `.pom` also quarantines the sibling files of the same group, artifact and version that were already uploaded (best effort), because `mvn deploy` sends the jar first |
| Ivy / sbt | `<license name>` of an uploaded `ivy.xml`, or `<licenses>` of a `.pom`; other files of the module are not checked |
| Debian | the `License:` field of the control file (few `.deb` files have one) |
| RPM | the `License` tag of the package header |
| Conda | `license` in `info/index.json`, for both `.tar.bz2` and `.conda` |
| Helm | the `artifacthub.io/license` chart annotation |
| Docker / OCI | the `org.opencontainers.image.licenses` annotation of the manifest, or the same label in the image config when that blob is already stored in the repository. Checked when the manifest is pushed; layers pushed before it stay stored as unreferenced blobs |
| Conan, Go, raw | the upload carries no license: not checked at upload. Set the license as a [property](../repositories/properties.md) and the [after-upload path](#quarantine-after-upload) applies |

A package whose own licenses cannot be read from its file while the repository has a blocking policy (a PyPI `.egg`, a damaged archive, an unparsable `.pom`) is refused with `422` and code `LICENSE_METADATA_UNREADABLE`, because the client's own fields must not decide a blocking policy; without one the fields are the fallback. A package whose own metadata cannot be read safely (a metadata file or decompressed stream over the size limits, or two candidate metadata files in one archive) is refused with `422` and code `LICENSE_METADATA_UNSAFE`. Names are matched after the normalization described above. Free text that is not a known name or SPDX identifier (a Maven `<name>` such as `Our Company License`) cannot match a deny list, so only an allow list rejects it. Proxy-cache fills and group repositories are not upload paths and are not checked on the way in; an artifact fetched through a proxy can still be quarantined by a license scan or a license property. Creating or changing a policy does not re-examine artifacts that are already stored: scan them again.

### Quarantine after upload

A license that is only known after the upload (a license scan, or a change of the artifact's license [properties](../repositories/properties.md) through `PUT`/`PATCH`, which also covers raw, Go, Conan and imported artifacts) cannot reject anything any more. Instead, when a `block` policy is violated the artifact is **quarantined**: downloads answer `403 ARTIFACT_QUARANTINED` (see [Quarantine](#quarantine)) until an admin approves it.

- A quarantined artifact stays quarantined across re-scans and edits of its license properties; changing the license to something allowed is not a way around the block. Only `POST …/license/scans/{id}/quarantine` with `{"quarantined":false}` releases it (and `true` quarantines it again by hand). The web UI's license scan page has the same Approve and Quarantine buttons.
- An approval is stored with the licenses it was granted for (`quarantine_approved: true`). Re-scanning the same licenses keeps it approved; if the licenses recorded later differ, including a different spelling of the version such as `GPL-3.0-or-later` after approving `GPL-3.0-only`, and a blocking policy is violated, the artifact is quarantined again.
- If a property change is saved but the policy cannot be evaluated, the request answers `500` ("properties saved, but the license policy could not be evaluated") and the artifact is not quarantined until the next scan.
- The quarantine matches on the scan's `artifact_id`, exactly like the other scan types, so scan under the identifier the artifact is served under.
- Licenses suggested by [AI analysis](../integrations/ai-analysis.md) are advisory and are not evaluated against policies.

## SBOM

SBOM generation (CycloneDX 1.4-1.6, SPDX 2.3) and VEX documents are described in [SBOM and VEX](sbom.md).

## Quarantine after upload

A license that is only known after the upload (a license scan, or a change of the artifact's license [properties](../repositories/properties.md) through `PUT`/`PATCH`, which also covers raw, Go, Conan and imported artifacts) cannot reject anything any more. Instead, when a `block` policy is violated the artifact is **quarantined**: downloads answer `403 ARTIFACT_QUARANTINED` (see [Quarantine](#quarantine)) until an admin approves it.

- A quarantined artifact stays quarantined across re-scans and edits of its license properties; changing the license to something allowed is not a way around the block. Only `POST …/license/scans/{id}/quarantine` with `{"quarantined":false}` releases it (and `true` quarantines it again by hand). The web UI's license scan page has the same Approve and Quarantine buttons.
- An approval is stored with the licenses it was granted for (`quarantine_approved: true`). Re-scanning the same licenses keeps it approved; if the licenses recorded later differ, including a different spelling of the version such as `GPL-3.0-or-later` after approving `GPL-3.0-only`, and a blocking policy is violated, the artifact is quarantined again.
- If a property change is saved but the policy cannot be evaluated, the request answers `500` ("properties saved, but the license policy could not be evaluated") and the artifact is not quarantined until the next scan.
- The quarantine matches on the scan's `artifact_id`, exactly like the other scan types, so scan under the identifier the artifact is served under.
- Licenses suggested by [AI analysis](../integrations/ai-analysis.md) are advisory and are not evaluated against policies.

## SBOM

```sh
curl -sS -X POST HOST/api/v1/repositories/REPO/sbom/generate -H 'Authorization: Bearer <TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{"artifact_id":"my-lib@1.2.3","format":"cyclonedx","spec_version":"1.4"}'
curl -sS -OJ -H 'Authorization: Bearer <TOKEN>' HOST/api/v1/repositories/REPO/sbom/<ID>/download
```

| Endpoint | |
|---|---|
| `POST …/sbom/generate` | `artifact_id`, `format` (`cyclonedx` with `spec_version` `1.4`, or `spdx` with `2.3`); returns 201 |
| `GET …/sbom`, `GET …/sbom/{id}` | list / report (format, spec version, content type, component count, timestamps) |
| `GET …/sbom/{id}/download` | the document: CycloneDX JSON (`application/json`) or SPDX tag-value text (`text/spdx`), served as attachment `sbom.cyclonedx` / `sbom.spdx` |
| `DELETE …/sbom/{id}` | delete |

The document names the package from `artifact_id` (split at the last `@` into name and version; version is `unknown` if there is none) with a `pkg:` identifier typed from the repository format, and lists as components the entries of the artifact's `dependencies` property, a JSON array string such as `[{"name":"left-pad","version":"1.3.0"}]`. `metadata` in the request is rejected. Documents are stored in the blob store.

## Quarantine

Quarantining a vulnerability, secret or license scan result makes the server refuse downloads of that artifact in every repository format, via the hosted, proxy-cache and API download paths, with:

```http
HTTP/1.1 403 Forbidden
{"error":"artifact is quarantined","code":"ARTIFACT_QUARANTINED"}
```

If the quarantine state cannot be read the request fails closed with 500. Set `quarantined` back to `false` (or delete a vulnerability or secret scan result) to release the artifact. License scan results cannot be deleted: approve them with `POST …/license/scans/{id}/quarantine` (see [Quarantine after upload](#quarantine-after-upload)); a block policy quarantines license results automatically, the other scan types only when you set `quarantined`.

An artifact matches when its scan's `artifact_id` equals one of the identifiers the download handler knows it by (a leading slash is ignored). Examples from the format handlers: npm `name@version` or `name/-/<tarball>`; Maven the repository path (`com/acme/lib/1.0/lib-1.0.jar`); PyPI, Debian, RPM, Helm, RubyGems and Conda the file name or the component/package ID returned by the repository package listing; raw the file path. A scan recorded under an identifier the client never requests quarantines nothing.

AI analysis may *recommend* quarantine but never applies it: the recommendation is stored for review ([AI analysis](../integrations/ai-analysis.md)). The web UI's vulnerability, secret and license scan pages expose the same quarantine toggle.
