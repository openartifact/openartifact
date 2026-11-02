<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Installation

Install OpenArtifact with Docker Compose, on Kubernetes with Helm, or as a single binary against your own PostgreSQL. Start with Compose to try it on one machine.

OpenArtifact is one Go binary with the web UI embedded. It needs PostgreSQL (the compose file and CI use `postgres:18-alpine`) and a blob store. The blob store is the local filesystem by default; see [storage](../operations/storage.md) for the others.

| Method | Use it for |
|---|---|
| [Docker Compose](#docker-compose) | Trying it out and small single-host installs. Builds the image from the checkout and bundles PostgreSQL. |
| [Helm](#helm) | Kubernetes, with an optional CloudNativePG database, high availability and Ingress or Gateway API. |
| [Release binary or source build](#binary-and-build-from-source) | Bare metal, VMs and systemd, with your own PostgreSQL. |

## Docker Compose

Use this to run OpenArtifact on one machine. It needs Docker with Compose, `make`, `openssl` and a checkout of this repository.

1. Create the settings file and fill in the three required secrets:

   ```sh
   cp docker/.env.example docker/.env
   for k in POSTGRES_PASSWORD OPENARTIFACT_SECRET_KEY AUTH_HMAC_KEY; do
     sed -i "s|^$k=.*|$k=$(openssl rand -hex 32)|" docker/.env
   done
   ```

   On macOS use `sed -i ''` instead of `sed -i`. Compose refuses to start while any of the three is empty.

2. Pick the base image of the runtime container. The default is Docker Hardened Images and needs a free Docker ID:

   ```sh
   docker login dhi.io
   ```

   Without a Docker ID, use the anonymous distroless base instead:

   ```sh
   export OPENARTIFACT_RUNTIME=distroless
   ```

3. Build and start:

   ```sh
   make docker-up
   ```

4. Open <http://localhost:8080> and sign in as `admin`. The first password is printed once in the server log; see [First login](#first-login).

Keep `OPENARTIFACT_SECRET_KEY` and `AUTH_HMAC_KEY`. They must stay the same when you upgrade or restore a backup, so keep `docker/.env` in your backup.

The port is bound to `127.0.0.1`. To publish it, set `OPENARTIFACT_BIND=0.0.0.0` and put a TLS reverse proxy in front ([reverse proxy and TLS](#behind-a-reverse-proxy-and-tls)). PostgreSQL is not published to the host.

Next: [create your first repository](first-repository.md).

`make docker-up` runs `docker compose -f docker/docker-compose.yml --env-file docker/.env up -d --build` and stamps the version, commit and build time into the image. Running that `docker compose` command yourself works too, but then the System page shows version `dev` and "Not recorded" for commit and build time.

### Compose settings reference

These variables go in `docker/.env`. Compose reads `docker/.env` only. The repository-root [`.env.example`](../../.env.example) is for the bare binary, and [configuration](configuration.md) explains every setting.

Required:

| Variable | Meaning |
|---|---|
| `POSTGRES_PASSWORD` | Password of the bundled PostgreSQL. `POSTGRES_USER` (`postgres`) and `POSTGRES_DB` (`artifact_repo`) are optional. |
| `OPENARTIFACT_SECRET_KEY` | Encrypts upstream passwords, webhook secrets and signing keys at rest. Never change it afterwards. |
| `AUTH_HMAC_KEY` | Signs OIDC and SAML login state. |

Generate each with `openssl rand -hex 32`.

Optional runtime settings:

| Variable | Default | Effect |
|---|---|---|
| `OPENARTIFACT_BOOTSTRAP_ADMIN_PASSWORD` | empty | Password of the first admin. Empty generates one; see [First login](#first-login). |
| `BASE_URL` | empty | Public URL, for SSO and webhooks. |
| `OPENARTIFACT_TRUSTED_PROXIES` | empty | CIDRs of reverse proxies whose forwarded headers are believed. |
| `OPENARTIFACT_ROLE` | `all` | Process role: `all`, `web` or `worker`. See [scaling](../operations/scaling.md). |
| `OPENARTIFACT_BIND` | `127.0.0.1` | Host address of the published port. |
| `OPENARTIFACT_PORT` | `8080` | Host port mapped to the container's 8080. |

Build options:

| Variable | Default | Effect |
|---|---|---|
| `OPENARTIFACT_RUNTIME` | `dhi` | Runtime base image: `dhi` or `distroless`. |
| `OPENARTIFACT_SCANNERS` | `none` | Scanners to bundle into the image: `none`, `trivy`, `grype` or `all`. |
| `OPENARTIFACT_VERSION` | `dev` | Image tag and reported version. `make docker-up` sets it from the `VERSION` file. |
| `OPENARTIFACT_COMMIT` | `unknown` | Reported commit. `make docker-up` sets the 7-character short SHA. |
| `OPENARTIFACT_BUILD_TIME` | `unknown` | Reported build time, RFC 3339 in UTC. `make docker-up` sets it. |

- `dhi` is Docker Hardened Images (`dhi.io/static`) and needs `docker login dhi.io`. `distroless` is `gcr.io/distroless/static-debian13`, pulled anonymously. Both images have the same layout and no shell.
- The default image bundles no scanner, so Trivy and Grype scans fail until you enable one. Rebuild with `OPENARTIFACT_SCANNERS=trivy` (or `grype`, `all`), or set `OPENARTIFACT_TRIVY_INSTALL=auto` and `OPENARTIFACT_GRYPE_INSTALL=auto` to let the server download the pinned, checksum-verified release into the `scanner_cache` volume. See [installing Trivy and Grype](../security/scanner-setup.md).
- Other scanner settings pass through from `docker/.env`: `OPENARTIFACT_SCANNER_OFFLINE`, `OPENARTIFACT_SCANNER_UPDATE_INTERVAL`, `OPENARTIFACT_SCANNER_DOWNLOAD_BASE_URL`, `OPENARTIFACT_TRIVY_DB_REPOSITORY` and `OPENARTIFACT_GRYPE_DB_URL`. The `scanner_cache` volume holds the databases, which take several GB with both scanners.
- The OpenTelemetry variables (`OPENARTIFACT_OTLP_ENABLED`, `OTEL_*`) also pass through; see [monitoring](../operations/monitoring.md#opentelemetry).

The compose file passes only the variables it lists. Others, such as `METRICS_AUTH_TOKEN`, `METRICS_ALLOW_ANONYMOUS` and the `*_PASSWORD_FILE` variables, reach the server only if you add them to the `environment:` block of the `openartifact` service or to a compose override file.

To run several projects side by side, give each its own project name and port. Add `-v` to `down` to drop that project's volumes:

```sh
OPENARTIFACT_PORT=18080 docker compose -p oa-test -f docker/docker-compose.yml --env-file docker/.env up -d --build
docker compose -p oa-test -f docker/docker-compose.yml --env-file docker/.env down -v
```

The service runs as uid 10001 with a read-only root filesystem. Data lives in four named volumes:

| Volume | Mounted at | Holds |
|---|---|---|
| `postgres_data` | `/var/lib/postgresql` | The database. |
| `artifacts_data` | `/var/lib/artifacts` | Package blobs. |
| `backups_data` | `/var/lib/openartifact/backups` | Backups. |
| `scanner_cache` | `/var/cache/openartifact/scanners` | Trivy and Grype binaries and databases. |

### Run a released image

Released images are published to `ghcr.io/openartifact/openartifact` for `linux/amd64` and `linux/arm64`; tags are listed in [upgrading](upgrading.md#versions-and-image-tags). To run one instead of building, replace the `build:` and `image:` lines of the `openartifact` service (and of `openartifact-worker`, if you use it) with `image: ghcr.io/openartifact/openartifact:<version>`. Verify signatures as described in [signing](../security/signing.md).

### Run web and worker as two processes

Set `OPENARTIFACT_ROLE=web` in `docker/.env` and add `--profile worker` to the `docker compose` command. The optional `openartifact-worker` service shares the image, database and volumes. See [scaling and roles](../operations/scaling.md#docker-compose).

### Operating the shell-less image

The runtime image has no shell, `wget` or `nc`, so `exec` into the container does not work.

- **Health check.** The container runs `/app/openartifact -healthcheck`. The server waits for PostgreSQL itself (`DB_CONNECT_TIMEOUT`, default 60s).
- **Debugging.** Attach a throwaway container to the server's network namespace: `docker run --rm -it --network container:<container> busybox`. On Kubernetes use `kubectl debug -it <pod> --image=busybox --target=openartifact`.
- **Custom CAs.** Mount the CA bundle into the container and set `SSL_CERT_FILE` (one file) or `SSL_CERT_DIR` (a directory) to it. `update-ca-certificates` cannot run in the image.
- **CLI.** The `oa` command is in the image at `/usr/local/bin/oa`.

### First login

On the first start with an empty database, OpenArtifact creates the user `admin`. By default it generates the password and prints it once.

#### Read the generated password

The server prints the password to stderr, not to the structured log:

```text
===== OPENARTIFACT BOOTSTRAP =====
 admin password: <random>
 valid for 24h0m0s (after that, reset it with cmd/resetpw)
 Change immediately at /api/v1/auth/change-password
 To choose the password instead, set OPENARTIFACT_BOOTSTRAP_ADMIN_PASSWORD_FILE
 (bootstrap.adminPasswordFile) before the first start.
===================================
```

Read it from the container output:

```sh
docker compose -f docker/docker-compose.yml logs openartifact | grep -A6 'OPENARTIFACT BOOTSTRAP'
```

- On Kubernetes, find the same block with `kubectl -n openartifact logs <pod>`.
- The generated account must change its password. The UI redirects you to the change-password page, and the API rejects everything else until you change it. New passwords need at least 12 characters.
- The generated password stops working after 24 hours, set by `OPENARTIFACT_BOOTSTRAP_GENERATED_PASSWORD_TTL` (`0` means never). Changing the password removes the limit.
- With several replicas starting at once, only the replica that creates the user prints a password.

An expired password is rejected like a wrong one (`401 invalid_credentials`). Reset it as described in [Reset the admin password](#reset-the-admin-password).

#### Choose the password yourself

Set the password, and optionally an admin API token, before the first start. Nothing is printed and no password change is forced. This is the right choice for automation.

| Environment variable | YAML key | Meaning |
|---|---|---|
| `OPENARTIFACT_BOOTSTRAP_ADMIN_PASSWORD_FILE` | `bootstrap.adminPasswordFile` | File holding the admin password. Preferred: a mounted file never shows up in `docker inspect`. |
| `OPENARTIFACT_BOOTSTRAP_ADMIN_PASSWORD` | `bootstrap.adminPassword` | The password itself, for compose and local trials. The file wins when both are set. |
| `OPENARTIFACT_BOOTSTRAP_ADMIN_TOKEN_FILE` | `bootstrap.adminTokenFile` | File holding an admin API token. It becomes the access token `bootstrap` of `admin`, so `oa apply` works on the first run. |
| `OPENARTIFACT_BOOTSTRAP_ADMIN_TOKEN_TTL` | `bootstrap.adminTokenTtl` | Lifetime of that token, for example `720h`. `0` (default) means no expiry. |
| `OPENARTIFACT_BOOTSTRAP_FORCE_PASSWORD_CHANGE` | `bootstrap.forcePasswordChange` | Force a change at the first login. Default `false`. |

Create the files:

```sh
umask 077
openssl rand -base64 24 > admin-password
echo "oa_pat_$(openssl rand -hex 32)" > admin-token
```

- The password must be 12 to 1024 characters. A trailing newline in the file is trimmed.
- The token must be `oa_pat_` followed by 32 to 128 characters from `A-Za-z0-9_-`, with at least 12 distinct characters. The command above always qualifies.
- The server stores only the token's SHA-256. The token carries the `manage` permission of `admin` and can be revoked in the UI.
- An invalid password, an unreadable file or an invalid token stops the server with an error naming the setting. The value is never logged.
- These settings apply only to an empty instance. They never reset an existing admin or add a token later.

Next: [create your first repository](first-repository.md).

### Reset the admin password

Use this when the generated password expired or the admin password is lost. `resetpw` writes directly to the database and never takes the password as an argument. It ships next to `openartifact` in the [release archives](#release-archives); the container image does not contain it.

| Input | Meaning |
|---|---|
| `OPENARTIFACT_RESET_PASSWORD` | New password. If unset, one line is read from stdin; an interactive terminal is refused. |
| `-username NAME` or `OPENARTIFACT_RESET_USERNAME` | User to reset. Default `admin`. |
| `-sslmode MODE` or `DB_SSLMODE` | `disable`, `allow`, `prefer`, `require`, `verify-ca` or `verify-full`. Default `require`. |
| `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME` | Connection settings. Defaults `localhost`, `5432`, `postgres`, empty, `artifact_repo`. Read from the environment only. |

#### With Docker Compose

Extract `resetpw` from the release archive that matches your version (it must be a Linux binary), then run it once inside the compose network. The service already carries the database settings:

```sh
tar -xzf openartifact_<version>_linux_amd64.tar.gz resetpw
printf '%s\n' '<new-password>' | OPENARTIFACT_VERSION=$(cat VERSION) \
  docker compose -f docker/docker-compose.yml --env-file docker/.env \
  run --rm -T --no-deps -v "$PWD/resetpw:/tmp/resetpw:ro" --entrypoint /tmp/resetpw openartifact
```

- `OPENARTIFACT_VERSION` makes compose find the image you started with `make docker-up`. Without it compose looks for `openartifact:dev` and builds it.
- Drop `--no-deps` if the database is not running.

#### With the binary

```sh
OPENARTIFACT_RESET_PASSWORD='<new-password>' DB_HOST=… DB_USER=… DB_PASSWORD=… DB_NAME=… ./resetpw
# or: printf '%s\n' '<new-password>' | ./resetpw -username admin
```

From a source checkout, `go run ./cmd/resetpw` works the same way.

What it does:

- The password must be 12 to 1024 characters. The tool fails if the user does not exist.
- It sets the new password, removes the forced change and any expiry (you chose the password), and ends all of the user's sessions.
- A running server may take up to `DB_AUTH_CACHE_TTL_MS` (default 2000 ms) to notice.

## Helm

The chart lives in its own repository, [openartifact/helm-charts](https://github.com/openartifact/helm-charts), and is published both as an OCI artifact and as a classic Helm repository:

```sh
# OCI
helm install openartifact oci://ghcr.io/openartifact/charts/openartifact \
  --namespace openartifact --create-namespace -f my-values.yaml

# Classic repository
helm repo add openartifact https://openartifact.github.io/helm-charts
helm repo update
helm install openartifact openartifact/openartifact \
  --namespace openartifact --create-namespace -f my-values.yaml
```

Key points from the [chart README](https://github.com/openartifact/helm-charts/blob/main/charts/openartifact/README.md) (the option reference is [`values.yaml`](https://github.com/openartifact/helm-charts/blob/main/charts/openartifact/values.yaml)):

- Needs Kubernetes 1.29+ and PostgreSQL: either `database.external.*` or `cnpg.enabled=true` (CloudNativePG operator).
- Blob storage: `filesystem` on a PVC or an object store (`s3`, `garage`, `seaweedfs`, `gcs`, `azure`, `oci`). More than one replica needs object storage or a `ReadWriteMany` volume.
- The chart-managed Secret holds `AUTH_HMAC_KEY` and `OPENARTIFACT_SECRET_KEY`, generated on first install and reused on upgrade. Back it up; see [upgrading](upgrading.md#secrets-to-preserve).
- Set `baseUrl` to the public URL and `config.trustedProxies` to your ingress controller's CIDR.
- Any server variable can be set through `env` or `secrets.extra`.
- First-login password: find the `OPENARTIFACT BOOTSTRAP` block in the pod logs (`kubectl -n openartifact logs <pod>`), see [Read the generated password](#read-the-generated-password).

## Binary and build from source

### Release archives

Each [GitHub release](https://github.com/openartifact/openartifact/releases) (tag `vX.Y.Z`) carries archives built by GoReleaser, plus `SHA256SUMS`:

| Archive | Contents | Platforms |
|---|---|---|
| `openartifact_<version>_<os>_<arch>.tar.gz` | `openartifact` (server), `resetpw`, `LICENSE`, `README.md`, `openartifact.example.yaml`, `.env.example` | linux amd64, arm64 |
| `oa_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) | `oa` CLI, `LICENSE` | linux, darwin, windows × amd64, arm64 |

Archives and checksums carry a build-provenance attestation; verification commands are in [signing](../security/signing.md). The CLI is covered in [CLI](../integrations/cli.md).

```sh
sha256sum -c SHA256SUMS --ignore-missing
tar -xzf openartifact_<version>_linux_amd64.tar.gz
cp openartifact.example.yaml openartifact.yaml   # edit database.*, baseUrl, storage.*
./openartifact verify openartifact.yaml
./openartifact -config openartifact.yaml
```

The example file ships `database.password: CHANGE_ME`, `sslMode: require` and a placeholder `corsOrigins` entry; adjust all three (the server refuses to start while the database password is still `CHANGE_ME`). The defaults `storage.path: /var/lib/artifacts` and `backup.backupDir: /var/lib/openartifact/backups` must exist or be creatable by the process user (the filesystem store creates its directory). Migrations run at startup; the first start creates the `admin` user (see [First login](#first-login)). Options and environment variables: [configuration](configuration.md).

### Build from source

Requires Go 1.27 (`go.mod`: `go 1.27.0`; CI and releases use 1.27.3) and [bun](https://bun.sh) 1.4.4 (the web UI is embedded into the binary).

```sh
make build        # web UI + bin/openartifact
make build-cli    # bin/oa
make run          # build, then run ./bin/openartifact
make help         # list all targets
```

The version comes from the `VERSION` file. Override it with `make build CI_VERSION=1.2.3 CI_COMMIT=abc1234`. A plain `go build ./cmd/server` reports version `dev`.

Check what a binary reports:

```sh
openartifact -version    # <version> <commit> <build time> <go>/<os>/<arch>
```

Other useful targets:

- `make migrate` runs the database migrations and exits.
- `make docker-build` builds the image `openartifact:latest`.
- `make docker-run` runs that image against an external PostgreSQL from `docker/.env`.

Development targets are in the [development guide](../development/README.md).

## Behind a reverse proxy and TLS

- Set `BASE_URL` to the public URL when you use SSO or webhooks (see [the public URL](#the-public-url-and-base_url)); package clients work without it.
- `OPENARTIFACT_TRUSTED_PROXIES` (comma-separated CIDRs): only requests whose TCP peer is in this list may set `X-Forwarded-For`/`X-Real-IP` and, for the URLs in package metadata, `X-Forwarded-Host`/`X-Forwarded-Proto`. Without it the peer address is always the client IP, so IP allowlist, rate limiting and login throttling see the proxy's address.
- Session and login-issued CSRF cookies get `Secure` when the request arrived over TLS or carries `X-Forwarded-Proto: https`, so have the proxy set that header. The CSRF cookie the server seeds on API reads, and the origin check that guards Basic-token requests from browsers, believe `X-Forwarded-Proto` and `X-Forwarded-Host` only from `OPENARTIFACT_TRUSTED_PROXIES` (or follow an `https://` `BASE_URL`), so list the proxy there.
- To terminate TLS in OpenArtifact: `SERVER_TLS_ENABLED=true` with `SERVER_TLS_CERT` and `SERVER_TLS_KEY` (PEM files; both required, minimum TLS 1.2). Only in this mode does the server send `Strict-Transport-Security`; behind a TLS-terminating proxy, set HSTS on the proxy.
- Uploads can be large, and read/write timeouts default to `0` (unlimited). Raise or disable any body-size and timeout limit on the proxy as well.
- `GET /health` returns JSON with the version and the status of each component. It answers 200 even when a component is degraded, and 503 only while the server is still starting.
- `GET /ready` answers 503 when the database is down. Use it as the readiness probe.
- The compose healthcheck runs `/app/openartifact -healthcheck`, which calls `/health`.

### The public URL and BASE_URL

Package metadata names the server in some places. Where the client protocol allows it, OpenArtifact writes a relative reference, so no host is involved. Where the protocol needs a full URL, the origin comes from the request the client made, so one instance answers on `127.0.0.1`, `localhost`, a service name and an ingress host alike, and a client that installed through one name keeps working through another.

The origin of a request is, in this order:

1. `BASE_URL`, when the operator set it. It is an explicit override and always wins; a path prefix in it (`https://example.com/artifacts`) is kept.
2. The `Host` header and TLS state of the request.
3. `X-Forwarded-Host` and `X-Forwarded-Proto`, replacing 2 only when the TCP peer is in `OPENARTIFACT_TRUSTED_PROXIES`. Anyone else cannot steer the URLs. Have the proxy forward the public host including a non-default port (`artifacts.example.com:8443`) and the scheme.

An invalid host (userinfo, a path, whitespace) is ignored and the request falls back to `BASE_URL`, then to `http://localhost:8080`. Responses that carry a derived origin send `Vary: Host, X-Forwarded-Host, X-Forwarded-Proto`, and their `ETag` includes the origin, so a cache or a conditional request never serves one host's URLs to another.

| Protocol | Generated URL | Form | Why |
|---|---|---|---|
| Helm | `index.yaml` chart `urls` | relative (`charts/NAME-VERSION.tgz`) | Helm resolves them against the repository URL (`pkg/repo/v1/chartrepo.go`, `ResolveReferenceURL`) |
| PyPI | simple pages (HTML and JSON) | relative | PEP 503 and PEP 691; pip resolves them with `urljoin` |
| Terraform / OpenTofu | provider `download_url`, `shasums_url`, `shasums_signature_url`, module `X-Terraform-Get` | path-absolute (`/terraform/dl/...`) | Terraform resolves them against the request URL (`registry_client.go`, `registry/client.go`) |
| Composer | `packages.json` `metadata-url`, `list`, `search` | path-absolute | `ComposerRepository::canonicalizeUrl` adds the repository origin to a value starting with `/` |
| npm | `dist.tarball` | request origin | pacote stores it verbatim as `resolved` |
| Composer | `dist.url` | request origin | Composer downloads it as is |
| Cargo | `config.json` `dl` and `api`, `login_url` | request origin | `dl` is a URL template |
| NuGet | service index and registration `@id`, `packageContent` | request origin | resource URLs are absolute |
| Conan 1.x | `download_urls`, `upload_urls` | request origin | a relative URL resolves against the remote without its trailing slash |
| Ansible | `download_url`, `href`, pagination | request origin | `ansible-core` 2.15 cannot resolve a root-relative `download_url` |
| PyPI | legacy `json/PACKAGE` file URLs | request origin | the legacy JSON API has no relative form |
| Docker / OCI | `Location` headers | path-absolute | registry clients resolve them against the registry |
| all | SSO and SAML callbacks, SAML metadata, webhook and event links, SBOM ids, MCP OAuth metadata, CSRF and `Secure` cookie decisions | configured `BASE_URL` | the IdP, the receiver or a mail reader has no request to take the origin from |

Set `BASE_URL` when you use SSO, SAML or webhooks, when the server is published under a path prefix (Terraform and Composer take only that path from it), or when you want the URLs pinned regardless of the request. Behind a proxy without `BASE_URL`, add the proxy to `OPENARTIFACT_TRUSTED_PROXIES`; otherwise the URLs name the proxy's backend address. Docker Compose passes both `BASE_URL` and `OPENARTIFACT_TRUSTED_PROXIES` through.

## Install the web UI as an app

The web UI is an installable app (PWA): it opens in its own window without browser chrome and gets an icon on the desktop, launcher or home screen. Installing needs a secure origin: serve OpenArtifact over HTTPS (see above) or open it on `localhost`. Over plain `http://` on any other host the browser offers no install and does not run the service worker; the UI itself keeps working in a tab.

| Platform | How |
|---|---|
| Desktop Chrome / Edge | Click the install icon at the right end of the address bar, or **Install app** in the user menu (shown only when the browser offers an install). |
| Android (Chrome) | Menu → **Install app** (or **Add to Home screen**), or the **Install app** item in the user menu. |
| iOS / iPadOS (Safari) | Share → **Add to Home Screen**. Safari fires no install event, so there is no in-app button. |

What the installed app does and does not do:

- It is the same UI, served by the same server, and signs in like any browser tab: no separate login mode, and sessions, tokens and permissions are unchanged.
- The service worker (`/sw.js`) caches only the build assets and the static app shell (offline page, icons, manifest). API, repository (`/repos/`), registry (`/v2/`), `/metrics`, `/health`, login and SSO responses are never cached and never touched by the worker, so no data is served from a previous session. A server built without a release stamp (version `dev`, a plain `go build`) is a development instance: the UI registers no worker there and removes one an earlier visit left, so a rebuild never offers an update.
- Without a network connection, opening a page shows a small "You are offline" page with a **Try again** link instead of the browser error page.
- After an upgrade the UI shows a **New version available** notice with a **Reload** button. The page is never reloaded on its own, so a form in progress is not lost; the new version is used after you click it (or reopen the app).
- A reverse proxy must pass `/sw.js` and `/manifest.webmanifest` on with their `Cache-Control: no-cache` header, or an upgrade is noticed late. To remove the app, uninstall it from the browser or OS like any other app.
