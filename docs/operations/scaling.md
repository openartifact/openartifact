<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Scaling and roles

One OpenArtifact image can run in three roles: the whole server in one process (`all`, the default), the request-serving part only (`web`), or the background part only (`worker`). Splitting the roles lets you scale and update the part that answers clients separately from the part that does background work.

- [When to split](#when-to-split)
- [The three roles](#the-three-roles)
- [How work is handed over](#how-work-is-handed-over)
- [Configuration](#configuration)
- [Deployment shapes](#deployment-shapes)
- [Operating it](#operating-it)
- [Limits](#limits)

## When to split

Keep `all` for a single instance or when you run several identical replicas: nothing changes and no setting is needed.

Split when you want to:

- scale the HTTP side horizontally (several stateless `web` replicas, for example behind an HPA) without running every scheduler and scanner in each replica;
- keep background work (metadata regeneration, vulnerability scans, webhook delivery, replication) off the pods that answer clients;
- run `web` on a platform that scales on requests, down to zero (see [request-driven platforms](#request-driven-platforms)), with an always-on `worker`.

## The three roles

The role is the same image with a different `server.role`.

| Component | `all` | `web` | `worker` |
|---|---|---|---|
| HTTP API, web UI, every format endpoint | yes | yes | no |
| `/federation/v1` peer endpoints | yes | yes | no |
| MCP endpoint and AI endpoints | yes | yes | no |
| `/health`, `/ready`, `/metrics` | yes | yes | yes |
| Go profiler (when `pprofEnabled` is on) | yes | yes | yes, behind the bearer token |
| Background workers and schedulers | yes | no | yes |

The workers and schedulers a `worker` (and `all`) runs: metadata regeneration from the queue, webhook delivery and retries, cleanup, trash purge, audit retention, the storage tier mover, the blob deletion sweeper, replication, the recovery journal, the OSV mirror, exploit feeds, the proxy cache worker, auth purge, the presigned-upload worker, docker GC and the upload sweeper, federation pull, vulnerability scans, Dependency-Track pushes, AI analysis, GPG auto-generation, and the startup regeneration of stale metadata.

A `web` process starts none of them. Vulnerability scanners and their databases are installed or downloaded only on workers.

A `worker` serves no repository or API routes. Its HTTP listener (on `server.host` and `server.port`, like any role) answers only `/health`, `/ready`, `/metrics` and, when `pprofEnabled` is on, the Go profiler. `/metrics` and the profiler need the same bearer token (`METRICS_AUTH_TOKEN`) as `/metrics` does today; without `METRICS_AUTH_TOKEN` or `METRICS_ALLOW_ANONYMOUS=true`, `/metrics` is closed. See [metrics](monitoring.md#metrics).

## How work is handed over

A `web` process does not do the background work a request causes. It records the work durably in PostgreSQL and a worker picks it up.

| Work caused by a request | Where it is recorded | Done by |
|---|---|---|
| Metadata regeneration for npm, maven, deb, rpm, pacman, apk and group repositories | `worker_requests`, kind `regen:<format>` | a worker, after the usual debounce |
| Webhook delivery | a row in `webhook_deliveries` (pending, due at once) | a worker, which also handles retries |
| Vulnerability scan | the scan row is created as `scanning` and a `vuln:scan` request is queued | a worker runs the scanner |
| Dependency-Track push | `worker_requests`, kind `dtrack:push` | a worker |
| Federation "sync now" | `worker_requests`, kind `federation:kick` | a worker |

The request kinds in `worker_requests` are `regen:<format>` (`npm`, `maven`, `deb`, `rpm`, `pacman`, `apk`, `group`), `vuln:scan`, `dtrack:push` and `federation:kick`. The table has no foreign keys.

Delivery is at-least-once:

- A worker claims a request with a 30 second lease and renews the lease on every poll while the work runs. It deletes the request only when the work has finished.
- A request made again while its work is running is kept for another run.
- If a worker dies, another one takes the request over once the lease has lapsed.
- Requests for the same target coalesce: there is one row per kind and key.
- A worker polls every 1 second. A process with role `all` polls every 10 seconds; it only has rows to take when a separate `web` process exists. Metadata regeneration then still waits for the usual debounce.

Several replicas can claim requests at once: claims use `FOR UPDATE SKIP LOCKED`.

### What stays in the receiving process

Some work is not handed over; it runs in the process that received the request:

- admin "run now" actions (cleanup, docker GC, tier moves, trash purge);
- asynchronous backup and restore tasks and other long API tasks;
- AI analysis. Its queue is in memory and only a process that runs workers consumes it, so an analysis requested on a `web` process (a manual trigger, or auto-analysis after an upload) stays `pending`. Run AI analysis on `all` processes until it is handed over through `worker_requests` too;
- the scanner status the API reports (installed binary, database age): it describes the binary and database cache of the process that answers, so on a `web` process, which installs no scanner, it does not describe the worker that runs the scans;
- replication read-repair. Only a process that runs replication does it, and `web` does not, so a read that would trigger a repair falls back to the periodic verify pass. Replication writes are queued in the database and replicated by a worker. See [blob replication](replication.md).

## Configuration

| Env | YAML | Default | Values |
|---|---|---|---|
| `OPENARTIFACT_ROLE` | `server.role` | `all` | `all`, `web`, `worker` |

An unknown value fails configuration validation at startup. See [configuration](../getting-started/configuration.md#server-server) for the other server settings.

```yaml
server:
  role: web   # all | web | worker
```

Every process of a deployment MUST share:

- the same PostgreSQL database;
- the same blob storage (object storage, or the same filesystem volume);
- the same secrets and configuration: `OPENARTIFACT_SECRET_KEY`, `AUTH_HMAC_KEY`, signing keys and the config file.

## Deployment shapes

### Docker Compose

The bundled `docker/docker-compose.yml` has an optional `openartifact-worker` service in the `worker` profile. It uses the same image and the same environment, database and volumes as the `openartifact` service and publishes no ports. The role comes from `OPENARTIFACT_ROLE`, which defaults to `all`, so a plain `docker compose up` is unchanged.

To split, set the role of the main service in `docker/.env` and start the profile:

```sh
# docker/.env
OPENARTIFACT_ROLE=web
```

```sh
docker compose -f docker/docker-compose.yml --env-file docker/.env --profile worker up -d --build
```

The `openartifact` service then runs as `web` and `openartifact-worker` runs as `worker`. Without `--profile worker` and with `OPENARTIFACT_ROLE=web`, nothing does the [handed-over work](#how-work-is-handed-over).

### Kubernetes

The Helm chart is not part of this repository, so this describes the shape. Two Deployments run the same image and differ only in `OPENARTIFACT_ROLE`:

- `web`: behind the Service and Ingress, with an HPA. Readiness on `/ready`, liveness on `/health`.
- `worker`: no Service, 1 or 2 replicas, readiness on `/ready` and liveness on `/health`, scraped on `/metrics` with the bearer token.
- Both: shared storage (object storage, or a ReadWriteMany volume for the filesystem backend) and the same Secret and ConfigMap.

A generic excerpt showing only what differs between the two Deployments:

```yaml
# web
kind: Deployment
metadata:
  name: openartifact-web
spec:
  template:
    spec:
      containers:
        - name: openartifact
          image: ghcr.io/openartifact/openartifact:<version>
          env:
            - name: OPENARTIFACT_ROLE
              value: web
          readinessProbe:
            httpGet: {path: /ready, port: 8080}
          livenessProbe:
            httpGet: {path: /health, port: 8080}
---
# worker: no Service, no Ingress, no HPA
kind: Deployment
metadata:
  name: openartifact-worker
spec:
  replicas: 2
  template:
    spec:
      containers:
        - name: openartifact
          image: ghcr.io/openartifact/openartifact:<version>
          env:
            - name: OPENARTIFACT_ROLE
              value: worker
          readinessProbe:
            httpGet: {path: /ready, port: 8080}
          livenessProbe:
            httpGet: {path: /health, port: 8080}
```

Everything else (`envFrom` the same Secret and ConfigMap, the storage settings, the volume if you use the filesystem backend) is identical in both. Scrape the worker's `/metrics` with the bearer token, as in [monitoring](monitoring.md#metrics).

### Request-driven platforms

On platforms that scale on requests, including down to zero (issue #121), `web` is the part that suits them: it is stateless and starts no background work. Run the `worker` always on, since it has no inbound traffic to wake it. Work handed over by `web` waits in PostgreSQL until a worker takes it.

## Operating it

### Observing the queue

Pending requests per kind and the age of the oldest:

```sql
SELECT kind, count(*), min(requested_at) FROM worker_requests GROUP BY kind;
```

Pending webhook deliveries are the rows of `webhook_deliveries` with `status = 'pending'`.

### Without a worker

If no worker is running, requests accumulate: metadata stays stale, webhooks stay pending and scans stay `scanning` until a worker starts.

### Rolling updates

The same [graceful shutdown](monitoring.md#graceful-shutdown-and-rolling-updates) and `server.shutdownDelay` apply to every role. A request that a stopping worker was handling is taken over by another worker after its lease lapses (delivery is at-least-once).

### Several workers

Several `worker` replicas may run. Singleton schedulers already use PostgreSQL advisory locks, and request claims use `FOR UPDATE SKIP LOCKED`. Several `web` replicas are stateless. Mixed fleets work, for example `web` replicas plus one `all`.

## Limits

- `web` needs a worker (a `worker` or an `all` process) for everything in [how work is handed over](#how-work-is-handed-over).
- Admin "run now" actions, asynchronous backup and restore tasks and other long API tasks run in the process that received the request, not on a worker.
- Replication read-repair happens only in a process that runs replication; `web` does not, so such reads fall back to the periodic verify pass.
- A `worker` serves no repository or API routes: point clients and the web UI at `web` (or `all`).
