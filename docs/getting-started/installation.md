<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Installation

Run OpenArtifact with Docker Compose, on Kubernetes with Helm, or as a single binary against your own PostgreSQL.

OpenArtifact is one Go binary with the web UI embedded. It needs PostgreSQL (the compose file and CI use `postgres:18-alpine`) and a blob store (local filesystem by default; see [storage](../operations/storage.md)).

| Method | Use it for |
|---|---|
| [Docker Compose](#docker-compose) | Evaluation, small single-host installs; builds the image from the checkout and bundles PostgreSQL |
| [Helm](#helm) | Kubernetes; optional CloudNativePG database, HA, Ingress/Gateway API |
| [Release binary or source build](#binary-and-build-from-source) | Bare metal / VMs / systemd, with your own PostgreSQL |

## Docker Compose

```sh
cp docker/.env.example docker/.env      # set POSTGRES_PASSWORD
make docker-up                          # build and start; stamps version, commit and build time
```

`make docker-up` runs `docker compose -f docker/docker-compose.yml --env-file docker/.env up -d --build` with the build metadata exported (see the table below). Running that `docker compose` command directly works too, but compose cannot read the `VERSION` file or run `git`, so the image then reports version `dev` and commit/build time `unknown` (the System page labels them "Not recorded").

The UI and API are on <http://localhost:8080>. The port is bound to `127.0.0.1` (change with `OPENARTIFACT_BIND`, e.g. `0.0.0.0`; put a TLS reverse proxy in front for anything else). PostgreSQL is not published to the host. Compose refuses to start while `POSTGRES_PASSWORD` is unset.

Which `.env.example`:

| File | Used by |
|---|---|
| `docker/.env.example` → `docker/.env` | Compose (`POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB`) and `make docker-run` (`DB_*`, via `--env-file`) |
| [`.env.example`](../../.env.example) (repo root) | Full environment-variable reference for the binary; Compose does not read it. See [configuration](configuration.md) |

Compose also substitutes these variables from `docker/.env` (they are not listed in `docker/.env.example`):

| Variable | Default | Effect |
|---|---|---|
| `AUTH_HMAC_KEY` | empty (random key per boot) | Signs OIDC/SAML login state; set it for SSO that survives restarts |
| `OPENARTIFACT_BIND` | `127.0.0.1` | Host address the port 8080 mapping binds to |
| `OPENARTIFACT_VERSION` | `dev` | Build argument; the version reported by `/health`, the log, `-version` and the System page. `make docker-up` sets it from the `VERSION` file |
| `OPENARTIFACT_COMMIT` | `unknown` | Build argument; commit reported by the System page. `make docker-up` sets the 7-character short SHA |
| `OPENARTIFACT_BUILD_TIME` | `unknown` | Build argument; build time, RFC 3339 in UTC. `make docker-up` sets it |
| `OPENARTIFACT_RUNTIME` | `dhi` | Build argument `RUNTIME`: runtime base image. `dhi` is Docker Hardened Images (`dhi.io/static`) and needs `docker login dhi.io` with a Docker ID; set `OPENARTIFACT_RUNTIME=distroless` to use the anonymous `gcr.io/distroless/static-debian13` instead (same layout, also shell-less) |

The `openartifact` service passes only the variables listed in its `environment:` block. Anything else (`BASE_URL`, `OPENARTIFACT_SECRET_KEY`, `METRICS_AUTH_TOKEN`, `OPENARTIFACT_TRUSTED_PROXIES`, ...) must be added there, or via a compose override file, before it reaches the server. The service runs as uid 10001 with a read-only root filesystem; data lives in the named volumes `postgres_data`, `artifacts_data` (`/var/lib/artifacts`) and `backups_data` (`/var/lib/openartifact/backups`).

The image has no shell, `wget` or `nc`; its health check is `openartifact-server -healthcheck`, and the server waits for PostgreSQL itself (`DB_CONNECT_TIMEOUT`, default 60s). See the [0.5.0 upgrade notes](upgrading.md#050-unreleased) for debugging and custom CAs.

Released images are published to `ghcr.io/openartifact/openartifact` (`linux/amd64`, `linux/arm64`; tags in [upgrading](upgrading.md#versions-and-image-tags)). To run one instead of building, replace the `build:` block of the `openartifact` service with `image: ghcr.io/openartifact/openartifact:<version>`. Verify signatures as described in [signing](../security/signing.md).

### First login

On first start with an empty user table, OpenArtifact creates the user `admin` with a random 32-character password and prints it once to **stderr** (not through the structured logger, so it is not in a `LOG_OUTPUT` file):

```text
===== OPENARTIFACT BOOTSTRAP =====
 admin password: <random>
 Change immediately at /api/v1/auth/change-password
===================================
```

Read it from the container output:

```sh
docker compose -f docker/docker-compose.yml logs openartifact | grep -A3 'OPENARTIFACT BOOTSTRAP'
```

The account is flagged `must_change_password`: until the password is changed, every API call except `POST /api/v1/auth/change-password` and `POST /api/v1/auth/logout` is rejected, and the UI redirects to the change-password page. New passwords must be at least 12 characters. With several replicas starting on an empty database, only the replica that creates the user prints a password.

Next: create a repository, then an API token for clients ([authentication](../security/authentication.md), [RBAC](../security/rbac.md)).

### Reset the admin password

`resetpw` is a break-glass tool that writes directly to the database. It is built from `./cmd/resetpw` and shipped next to `openartifact` in the release archives. It never takes the password as an argument.

| Input | Meaning |
|---|---|
| `OPENARTIFACT_RESET_PASSWORD` | New password. If unset, one line is read from stdin; an interactive terminal is refused (input would echo) |
| `-username NAME` / `OPENARTIFACT_RESET_USERNAME` | User to reset (default `admin`) |
| `-sslmode MODE` / `DB_SSLMODE` | `disable`, `allow`, `prefer`, `require`, `verify-ca` or `verify-full` (default `require`, like the server) |
| `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME` | Connection settings; defaults `localhost`, `5432`, `postgres`, empty, `artifact_repo`. Read from the environment only, not from the YAML file |

```sh
OPENARTIFACT_RESET_PASSWORD='<new-password>' go run ./cmd/resetpw
# or: printf '%s\n' '<new-password>' | go run ./cmd/resetpw -username admin
# or, from a release archive: ./resetpw -username admin < password-file
```

- The password must be 12 to 1024 characters; the tool fails if the user does not exist.
- In one transaction it sets the new Argon2id hash, **clears** `must_change_password` (you chose the password, so no forced change follows) and deletes all of the user's sessions.
- A running server may take up to `DB_AUTH_CACHE_TTL_MS` (default 2000 ms) to see the change, because `resetpw` bypasses its auth cache.
- With the default compose file PostgreSQL is only on the compose network. To reach it from the host, add `ports: ["127.0.0.1:5432:5432"]` to the `postgres` service, then use `DB_HOST=127.0.0.1 DB_USER=… DB_PASSWORD=… DB_NAME=… DB_SSLMODE=disable` (the bundled database has no TLS).

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
- First-login password: find the `OPENARTIFACT BOOTSTRAP` block in the pod logs (`kubectl -n openartifact logs <pod>`), as above.

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

The version comes from the `VERSION` file and is injected with `-ldflags "-X main.version=… -X main.commit=… -X main.buildTime=…"`. Override with `make build CI_VERSION=1.2.3 CI_COMMIT=abc1234`. A plain `go build ./cmd/server` reports version `dev`. The commit is always the 7-character short SHA and the build time RFC 3339 in UTC, in the Makefile, the Dockerfile and GoReleaser alike. `openartifact-server -version` prints `<version> <commit> <build time> <go>/<os>/<arch>`. Useful targets: `make migrate` (runs `go run ./cmd/server -migrate`), `make docker-build` (image `openartifact:latest`), `make docker-run` (that image against an external PostgreSQL from `docker/.env`). Development targets are in [development](../development/README.md).

## Behind a reverse proxy and TLS

- Set `BASE_URL` to the public URL (default `http://localhost:8080`). It is used for download and index URLs generated by the format handlers and for SSO callbacks (the SAML entity ID and ACS URL are derived from it).
- `OPENARTIFACT_TRUSTED_PROXIES` (comma-separated CIDRs): only requests whose TCP peer is in this list may set `X-Forwarded-For`/`X-Real-IP`. Without it the peer address is always the client IP, so IP allowlist, rate limiting and login throttling see the proxy's address.
- Session and CSRF cookies get `Secure` when the request arrived over TLS or carries `X-Forwarded-Proto: https`, so have the proxy set that header.
- To terminate TLS in OpenArtifact: `SERVER_TLS_ENABLED=true` with `SERVER_TLS_CERT` and `SERVER_TLS_KEY` (PEM files; both required, minimum TLS 1.2). Only in this mode does the server send `Strict-Transport-Security`; behind a TLS-terminating proxy, set HSTS on the proxy.
- Uploads can be large, and read/write timeouts default to `0` (unlimited). Raise or disable any body-size and timeout limit on the proxy as well.
- `GET /health` (always 200, JSON with version and component status) and `GET /ready` (503 when the database is down) are the probe endpoints; the compose healthcheck uses `/health`.
