<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Declarative configuration (`oa apply`)

`oa apply` reads YAML manifests and makes an OpenArtifact server match them: users, groups, projects, repositories, permission targets, SSO providers, webhooks, storage tiers and tier policies. It is idempotent, so you can run it from a laptop, from CI, or from a Kubernetes Job on every deploy. `oa diff` shows what would change, and `oa export` writes the current server state back out as manifests.

All three commands call the admin API, so they need an administrator token (`OA_TOKEN`, or `oa auth login`). See [CLI](cli.md) for the server and token settings.

```sh
oa diff   -f examples/declarative            # what would change? exit 2 when something would
oa apply  -f examples/declarative            # create and update
oa apply  -f examples/declarative --prune    # also delete what apply created and you removed
oa export -o exported/                       # server state to manifests
```

Ready-to-use manifests for every kind are in [`examples/declarative/`](../../examples/declarative).

## Manifest format

A manifest file holds one or more YAML documents separated by `---`. Every document has the same four fields:

```yaml
apiVersion: openartifact.io/v1     # the only version
kind: Repository
metadata:
  name: npm-hosted
spec:
  format: npm
  description: Internal npm packages
```

- Unknown fields are errors, not ignored. Problems are reported with `file:line` and all at once: `repos.yaml:6: Repository/x: format: invalid value "nope" (one of "cargo", ...)`.
- `spec` is the desired state of the fields you list. A field you leave out is not touched on the server, so a manifest can be partial. To reset a field, state its value.
- `-f` takes a file, a directory (only `*.yaml` and `*.yml`; add `-R` to descend into subdirectories) or `-` for stdin, and can be repeated. Objects are applied in dependency order regardless of file order.
- Two documents for the same kind and name are an error.

`oa apply -f DIR --validate-only` checks the manifests offline, without a server or token.

### Kinds

| Kind | `metadata` | Identity | Spec fields |
|---|---|---|---|
| `User` | `name` | username | `email`, `full_name`, `disabled`, `must_change_password` |
| `Group` | `name` | group name | `description`, `members` (usernames) |
| `Project` | `name` | project name | `display_name`, `description`, `allowed_formats`, `allowed_repo_types`, `repo_name_prefix`, `allow_public_repos`, `max_repositories`, `token_max_ttl_days`, `members` (`username`, `role`), `groups` (`group`, `role`) |
| `Repository` | `name`, `project` (optional) | repository name | `format` and `repo_type` (immutable), `description`, `public`, `presign_downloads`, `trash_retention_days`, `immutable_releases`, `quota`, `cleanup_policies`; proxy: `upstream_url`, `upstream_username`, `upstream_password`, `cache_ttl_seconds`, `negative_cache_ttl_seconds`, `max_stale_seconds`; group: `member_repos`, `merge_proxy_versions`; hosted rpm: `sign_packages` |
| `PermissionTarget` | `name` | target name (globally unique) | `repository_filter`, `path_pattern` (both regular expressions), `actions`, `assignments` (`users`, `groups`) |
| `SSOProvider` | `name` (the slug) | slug | `kind` (immutable), `display_name`, `enabled`, `auto_provision_users`, `link_by_email`, `claim_mapping`, `group_mapping`, `permission_mapping`, `oidc_config`, `saml_config` |
| `Webhook` | `name`, `repository` (empty for a global webhook) | `name`, or `repository/name` | `url`, `events`, `secret`, `content_type`, `payload_format`, `ssl_verify`, `max_retries`, `timeout_seconds`, `enabled` |
| `StorageTier` | `name` | tier name | `backend` (immutable, must be an existing [storage backend](../operations/storage-tiering.md)), `rank`, `promote_on_read` |
| `TierPolicy` | `name` | policy name | `target_tier`, `from_tier`, `enabled`, `position`, `min_age_seconds`, `min_idle_seconds`, `min_size_bytes`, `max_size_bytes`, `repo_glob`, `path_glob`, `formats` |

Field names and values are those of the REST API (see [REST API](api.md) and `internal/api/openapi.json`). Notes per kind:

- **Repository**: `metadata.project` places the repository in a [project](../security/projects.md); the project's policy (name prefix, formats) applies. Proxy-only, group-only and rpm-only fields are rejected on other repository types. `cleanup_policies` are compared per policy `name`. A group repository is applied after its members.
- **Project**: `members` and `groups` are compared per username and group, so adding or removing one entry changes only that entry. `allowed_formats` cannot be an empty list; leave it out to allow every format. The project's original owner cannot be removed through `members`.
- **Group**: `members` is compared as a set of usernames. `oa export` leaves it out unless you pass `--with-members`, because membership is often managed by SSO group mapping.
- **User**: a manifest cannot set a password. Created users have none until an administrator sets one or they sign in through SSO; use `must_change_password` to force a change after the first login. Deleting a `User` manifest with `--prune` disables the account (the server never hard-deletes users) and `oa` does not prune disabled users again.
- **PermissionTarget**: `assignments.users` and `assignments.groups` are sets; the target is reconciled to exactly the listed principals.
- **SSOProvider**: the OIDC `client_secret` is required in `oidc_config` (as a `secretRef`). Creating a provider with `enabled: false` creates it enabled first and then disables it, as the API has no other path.
- **Webhook**: the webhook name is unique per repository; `oa` reports a repository webhook as `repository/name`.
- **StorageTier** and **TierPolicy**: see [storage tiering](../operations/storage-tiering.md). The `backend` of a tier must exist before `oa apply` runs; create it with `oa storage create`. The primary tier is created by the server and is never changed by `--prune`.

## Secrets

Manifests never contain secrets. Write a `secretRef` instead; `oa` resolves it on the machine that runs it:

```yaml
spec:
  upstream_url: https://registry.npmjs.org
  upstream_username: ci-reader
  upstream_password:
    secretRef: {env: NPM_UPSTREAM_PASSWORD}     # or: {file: /run/secrets/npm-upstream}
```

- `env` reads an environment variable, `file` reads a file (a trailing newline is stripped). An unset or empty value is an error.
- Every secret that is going to be sent is resolved before the first change is made, so a missing variable aborts the run with nothing changed.
- The server never returns secrets. A secret that the server reports as already set is not compared and not sent again unless you pass `--force-secrets`. To rotate one, change the value and run `oa apply --force-secrets`.
- Plans, tables and `-o json` output show secrets as `(secret, will be sent)`, never the value.
- Where the server does not say whether a secret is set (a proxy repository's `upstream_password`), `oa` sends it when the object is created and with `--force-secrets`. Changing `upstream_url` (its origin) or `upstream_username` makes the server drop the stored password, so `oa apply` refuses that update without `--force-secrets` and tells you to pass it.

## Reading the plan

```text
$ oa diff -f examples/declarative
+ Repository/npm-hosted (create)
+   format: "npm"
+   quota.max_size_bytes: 10737418240
~ Repository/npm-proxy (update)
-   description: "Cache of the public npm registry"
- Webhook/old-sink (delete, managed by oa apply)

Plan: 1 to create, 1 to update, 1 to delete, 12 unchanged.
```

`oa diff` and `oa apply` print the same plan; `apply` then runs it and prints a result table (`ok`, `FAILED: ...`, `skipped`).

| Exit status | `oa diff` | `oa apply` |
|---|---|---|
| 0 | the server matches the manifests | everything applied |
| 1 | error (invalid manifest, unreachable server, plan problem, a failed change) | same |
| 2 | there are changes | not used |

`-o json` (or the global `--format json`) prints the plan as JSON with a `summary` and `items` (`action`, `kind`, `name`, `changes`), for scripts.

### What `apply` refuses to do

These abort before anything is changed, so a plan is never half-applied because of them:

- An **immutable** field differs (`Repository` `format` and `repo_type`, `SSOProvider` `kind`, `StorageTier` `backend`). The error says which field. Revert the manifest, or delete the object and apply again to recreate it.
- A reference points at an object that is in neither the manifests nor on the server (a project member that is not a user, a tier policy whose tier does not exist).
- Two live objects share the name your manifest uses.
- A secret cannot be resolved.

A failure while applying stops the run, and later objects are reported `skipped`. With `--continue-on-error`, every remaining change is still attempted (an object that depends on the failed one fails on its own); the exit status stays 1.

Order: users, groups, projects, repositories, permission targets, SSO providers, webhooks, storage tiers, tier policies. Deletes run in reverse.

`--kind K` (repeatable) limits the run to one or more kinds, e.g. `oa apply -f . --kind Repository --kind Webhook`.

## Ownership, adopting and pruning

`oa apply` records every object it creates or updates in the server table `declarative_objects` (kind, name, object id and a hash of the applied spec). That record is the ownership marker.

- **Adoption.** Applying a manifest for an object that already exists, because it was created in the UI or by the API, makes `oa` take it over: the marker is written, the fields in the manifest are reconciled. If nothing differs, `oa diff` still reports no changes.
- **`--prune`** deletes objects that have a marker but are no longer in the manifests you pass. Objects nobody applied with `oa` are never deleted by it.
- **`--prune-all`** deletes every object (of the kinds in scope) that is not in the manifests, marker or not. It skips the `User` kind unless you name it with `--kind User`.
- Prune is relative to the manifests you pass. `oa apply -f team-a/ --prune` deletes managed objects that are not in `team-a/`, including those applied from `team-b/`. Use one manifest tree per server, or scope runs with `--kind`.
- **Never pruned**, with either flag: the seeded permission targets `admin-all` and `anonymous-read`, the system project, the bootstrap `admin` user and the primary storage tier. Objects whose name matches more than one live object are not pruned either.
- Markers are not part of [backups](../operations/backup.md). After a restore, `--prune` deletes nothing until one `oa apply` records ownership again; `--prune-all` is not affected.
- Pruning a `Repository` deletes the repository with its artifacts, as `oa repo delete` does. Review `oa diff --prune` before running `oa apply --prune`.

## Export

```sh
oa export                              # YAML stream on stdout
oa export -o exported/                 # one file per object: exported/repository/npm-hosted.yaml
oa export --kind Repository --kind Webhook
oa export --with-members               # include Group members
```

Export writes the same manifest format as `apply` reads, so `oa export -o exported/ && oa diff -f exported/ -R` exits 0 (files are written into one subdirectory per kind, hence `-R`). Secrets are replaced with placeholders that name an environment variable, for example `secretRef: {env: OA_WEBHOOK_AUDIT_SINK_SECRET}`. Set the variable before applying the export to a different server. The server does not say whether a proxy repository has an `upstream_password`, so export omits it; add a `secretRef` for it yourself. Repositories are exported with `metadata.project` set to the project they live in (`system` for the default one). Applying an export to a server on which some of these objects already exist adopts them. `User` objects are exported only when you name `--kind User`.

## Known limits

- A `User` manifest has no password field, and a user cannot be hard-deleted: `--prune` disables the account.
- Webhook delivery URLs are resolved by the server when the webhook is created or changed. Hosts that do not resolve, or that point at private addresses, are rejected by the server with an error `oa` prints as is.
- Artifacts, tokens, scans and audit data are not configuration and are not managed here.
- Directories are scanned for `*.yaml` and `*.yml` only. A JSON manifest works through `-f -` (JSON is valid YAML).

## GitOps

The container image ships `oa` at `/usr/local/bin/oa`, so the manifests can be applied from the same image that runs the server. Keep the manifests in a ConfigMap or a Git repository, the administrator token and secret values in a Secret, and run `oa apply` after every install or upgrade. The command is idempotent, so running it on every sync is safe.

### Kubernetes Job (Helm hook)

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: openartifact-apply
  annotations:
    helm.sh/hook: post-install,post-upgrade
    helm.sh/hook-delete-policy: before-hook-creation
spec:
  backoffLimit: 3
  template:
    spec:
      restartPolicy: OnFailure
      containers:
        - name: apply
          image: ghcr.io/openartifact/openartifact:<version>
          command: ["oa", "apply", "-f", "/manifests", "-R"]
          env:
            - {name: OA_SERVER, value: "http://openartifact:8080"}
            - name: OA_TOKEN
              valueFrom: {secretKeyRef: {name: openartifact-admin-token, key: token}}
            - name: NPM_UPSTREAM_PASSWORD
              valueFrom: {secretKeyRef: {name: openartifact-upstreams, key: npm}}
          volumeMounts:
            - {name: manifests, mountPath: /manifests, readOnly: true}
      volumes:
        - name: manifests
          configMap: {name: openartifact-manifests}
```

A failed run exits non-zero, so the hook fails and Helm reports it. Set `image` to the version you deploy.

### Argo CD

Run the same Job as a `PostSync` hook so it applies after the server is healthy:

```yaml
metadata:
  annotations:
    argocd.argoproj.io/hook: PostSync
    argocd.argoproj.io/hook-delete-policy: BeforeHookCreation
```

For a dry run in a pipeline, use `oa diff -f ...` and fail the build on exit status 2 to detect drift.

### Getting the first token

On a new server, the bootstrap `admin` password is printed once in the server log. Sign in with `oa auth login`, or create a token in the UI (Settings, Tokens), and store it in the Secret above. Tokens that [expire](../security/authentication.md) need rotating in that Secret.
