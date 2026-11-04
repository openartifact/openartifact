<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# CI integration

Use this page to let a CI job publish build outputs to OpenArtifact, group them into a [release bundle](../repositories/release-bundles.md) and promote the bundle to the next stage. The job needs only the `oa` CLI and a token that is not an administrator's.

Plugins exist for two CI systems. Any other CI works the same way from a shell step.

| CI system | Plugin |
|---|---|
| Jenkins | [openartifact-plugin](https://github.com/openartifact/openartifact-plugin): pipeline steps and a freestyle build step, with a badge per job |
| Drone and Woodpecker | [drone-openartifact](https://github.com/openartifact/drone-openartifact): a plugin container with `upload` and `promote` modes |

The plugins never call the REST API. They install `oa`, run the commands below and parse the JSON they print.

## Publish and promote a build

```sh
export OA_SERVER=https://oa.example.com
export OA_TOKEN=...            # a deploy token, see "Least-privilege CI token"

# 1. publish the build outputs and record which build produced them
oa --format json upload releases-dev 'dist/**' --target-path "builds/$BUILD_NUMBER/" \
   --build-name "$JOB_NAME" --build-number "$BUILD_NUMBER" --build-url "$BUILD_URL" \
   --vcs-revision "$GIT_COMMIT"

# 2. collect everything this build uploaded into a draft bundle
oa --format json bundle create shop "$VERSION" --pipeline release
oa --format json bundle add    shop "$VERSION" --from-build "$JOB_NAME/$BUILD_NUMBER"

# 3. freeze it, then promote it to the next stage
oa --format json bundle seal shop "$VERSION"
oa --format json promote     shop "$VERSION" --stage prod --notes "build $BUILD_NUMBER"
```

Every command exits `0` on success and `1` on failure. With `--format json` it prints one JSON document. The document shapes, the remote path rules of `upload` and the meaning of `--from-build` are in the [CLI reference](cli.md#ci-commands).

Promotion adds metadata rows that point at the same blobs. It costs the same for a 1 KiB bundle and a 10 GiB bundle ([details](../repositories/release-bundles.md#what-a-copy-is)).

### Supported formats

- `oa upload` publishes to hosted `deb`, `ivy`, `maven`, `raw`, `rpm` and `sbt` repositories.
- `bundle add` resolves artifacts of the same six formats. Add items of other formats through the [bundles API](../repositories/release-bundles.md#bundles-api).
- For every other format, publish with the native client in the job: `cargo publish`, `docker push`, `helm push`, `npm publish`, `twine upload`.
- Terraform and OpenTofu have no publish command. Use `curl` against the [publish API](../formats/terraform.md#publish).

## Install `oa` in the job

You need `oa` 0.10.0 or newer. Download the archive for the runner from the [GitHub releases](https://github.com/openartifact/openartifact/releases) and verify it against `SHA256SUMS`:

```sh
V=0.19.1 OS=linux ARCH=amd64
base=https://github.com/openartifact/openartifact/releases/download/v$V
curl -fsSLO "$base/oa_${V}_${OS}_${ARCH}.tar.gz"
curl -fsSL  "$base/SHA256SUMS" | grep "oa_${V}_${OS}_${ARCH}.tar.gz" | sha256sum -c -
tar -xzf "oa_${V}_${OS}_${ARCH}.tar.gz" oa
```

`oa --format json version` prints `{"version","go","os","arch"}`. See [installation](../getting-started/installation.md#release-archives) and [signing](../security/signing.md) for the signed checksums.

## Least-privilege CI token

CI does not need an administrator. An operator prepares the following once:

1. **Repositories.** A build repository the job uploads to (`releases-dev`) and a release repository (`releases-prod`). Both are hosted. They have the same format when bundles are promoted between them.
2. **A [pipeline](../repositories/release-bundles.md#pipelines-api).** Bind its stages to those repositories, for example `dev` to `releases-dev` and `prod` to `releases-prod`. Only administrators create pipelines, so the pipeline decides where a bundle can go.
3. **A CI user, a permission target and a deploy token.**
   - Create a [permission target](../security/rbac.md) that gives the user `read` and `write` on exactly those repositories: `repository_filter: ^releases-(dev|prod)$`, `path_pattern: .*`, `actions: [read, write]`.
   - Create a **deploy** [token](../security/authentication.md#api-tokens) for the user. A deploy token can never get `manage`.

With that token a job can upload, set properties, and create, fill, seal and promote bundles. It cannot create or delete repositories, edit pipelines, list all bundles or touch other repositories.

| Step | Needs |
|---|---|
| `upload` | `read` and `write` on the repository |
| `bundle create` | `read` on at least one repository |
| `bundle add`, `seal` | being the bundle's creator, and `read` on the source repositories |
| `promote` | `read` on the source repositories and `write` on the stage's repository |

Bundles are private to their creator. Another CI user sees one only with `read` on every source repository, and can change it only when it created it. Promotion needs no ownership, so a release job can promote what a build job created. The full table is in [bundle permissions](../repositories/release-bundles.md#permissions).

To sign the manifest at seal time, give the job the key id for `--gpg-key`. Listing keys needs an administrator.

## Tokenless CI: OIDC token exchange

A long-lived CI secret can leak, outlive its project and is awkward to rotate. With token exchange, the job proves who it is with the ID token its CI system signs for it. OpenArtifact trades that for an access token that lives at most an hour. No secret is stored in the CI system.

```text
CI job ──ID token (aud = oa.example.com)──▶ POST /api/v1/auth/oidc/exchange
                                              │ verifies the signature against the issuer's keys,
                                              │ matches a trust policy, checks the claims
CI job ◀──access token (deploy, 15 min)───────┘ of the service account the policy maps to
```

### Set it up (administrator, once)

**1. Create a service account** with `POST /api/v1/service-accounts` or **Admin > Service accounts**.

A service account is a non-human principal. It cannot sign in to the web UI, has no password and is hidden from the user list. Like a user, it holds [global roles](../security/rbac.md), [project roles](../security/projects.md) and group memberships, and it can own API tokens. Give it exactly what CI needs, as in [Least-privilege CI token](#least-privilege-ci-token).

**2. Create a trust policy** with `POST /api/v1/oidc-trust-policies` or **Admin > Service accounts > Trust policies**.

| Field | Meaning |
|---|---|
| `name` | Unique. The job can send it as `policy` to pick this policy (optional) |
| `issuer` | The CI system's issuer URL, see below |
| `audience` | The `aud` the job must request |
| `conditions` | Claim checks that must all pass, see below |
| `service_account_id` | The service account whose identity the token carries |
| `token_ttl_seconds` | Lifetime of the issued token: 60 to 3600, default 900 |
| `enabled` | A disabled policy matches nothing |

**Issuer.** Use `https://token.actions.githubusercontent.com` for GitHub Actions and `https://gitlab.com` (or your self-managed GitLab URL) for GitLab CI. The issuer must use `https` and must be reachable from the server. The only exception is a loopback host (`localhost`, `127.0.0.1` or `::1`), which may use `http`, for example for a test issuer on the same machine.

**Audience.** Use your server URL or any string you choose. Do not use a value another service accepts.

**Conditions.** A policy takes 1 to 16 conditions, and a token must pass all of them:

```json
[{"claim": "repository", "patterns": ["acme/shop"]}, {"claim": "ref", "patterns": ["refs/tags/v*"]}]
```

- A condition passes when the claim matches any of its patterns.
- `*` matches any run of characters, including `/`. `?` matches one character.
- Matching is case-sensitive. A missing claim never matches.
- A pattern cannot be only `*`.

**Pin the owner for shared issuers.** GitHub Actions and gitlab.com sign every customer's tokens under one issuer. A policy for them needs one condition that pins who the job belongs to, and OpenArtifact refuses the policy without it:

- GitHub: `sub`, `repository`, `repository_id`, `repository_owner` or `repository_owner_id`.
- GitLab: `sub`, `project_path`, `project_id`, `namespace_path` or `namespace_id`.

The pattern must start with a literal character: `acme/*` works, `*/shop` does not. A self-hosted issuer is yours alone and has no such rule, but it still needs at least one condition.

### GitHub Actions

Allow the job to request an ID token with `permissions: id-token: write`, ask for the audience of the policy and exchange it:

```yaml
jobs:
  publish:
    runs-on: ubuntu-latest
    permissions:
      id-token: write
      contents: read
    steps:
      - uses: actions/checkout@v4
      - name: Get an OpenArtifact token
        run: |
          set -euo pipefail
          OA_SERVER=https://oa.example.com
          ID_TOKEN=$(curl -fsS -H "Authorization: Bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \
            "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=$OA_SERVER" | jq -r .value)
          OA_TOKEN=$(curl -fsS -X POST "$OA_SERVER/api/v1/auth/oidc/exchange" \
            -H 'Content-Type: application/json' \
            --data "$(jq -n --arg token "$ID_TOKEN" '{token: $token}')" | jq -r .access_token)
          echo "::add-mask::$OA_TOKEN"
          echo "OA_TOKEN=$OA_TOKEN" >> "$GITHUB_ENV"
      - run: oa upload releases-dev 'dist/**' --target-path "builds/$GITHUB_RUN_NUMBER/"
        env:
          OA_SERVER: https://oa.example.com
```

A matching policy: issuer `https://token.actions.githubusercontent.com`, audience `https://oa.example.com`, conditions `repository` = `acme/shop` and `ref` = `refs/tags/v*` (publish from release tags only) or `environment` = `production` when the job uses a [deployment environment](https://docs.github.com/en/actions/deployment/targeting-different-environments).

### GitLab CI

Declare an ID token with the audience; GitLab puts it in the variable you name:

```yaml
publish:
  image: alpine:3
  id_tokens:
    OA_ID_TOKEN:
      aud: https://oa.example.com
  script:
    - apk add --no-cache curl jq
    - |
      OA_TOKEN=$(curl -fsS -X POST https://oa.example.com/api/v1/auth/oidc/exchange \
        -H 'Content-Type: application/json' \
        --data "$(jq -n --arg token "$OA_ID_TOKEN" '{token: $token}')" | jq -r .access_token)
    - curl -fsS -H "Authorization: Bearer $OA_TOKEN" -T app.tar.gz https://oa.example.com/repos/raw/releases-dev/app.tar.gz
```

A matching policy: issuer `https://gitlab.com`, audience `https://oa.example.com`, conditions `project_path` = `acme/shop` and `ref_protected` = `true`.

### Call the exchange API

`POST /api/v1/auth/oidc/exchange` needs no credentials. The body is `{"token": "<ID token>", "policy": "<name>"}`.

`policy` is optional. Send it only when several policies of one issuer accept the same token. Otherwise the oldest matching policy wins.

The answer is:

```json
{"access_token": "oa_dep_...", "token_type": "Bearer", "expires_in": 900, "expires_at": "2026-11-01T12:15:00Z"}
```

Use the access token anywhere a token goes: `Authorization: Bearer`, `OA_TOKEN`, or as the password of a native client.

### What the server enforces

- **Signature.** The server verifies it against the issuer's published keys. Only asymmetric algorithms are accepted. The server contacts only issuers named by an enabled policy.
- **Private networks.** The server fetches the issuer's keys with the same outbound-request protection as proxy upstreams. `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS` also governs a self-hosted issuer on a private network.
- **Claims.** `iss`, `aud` (against the policy), `exp`, `nbf` and `iat` are checked with one minute of clock skew. A token without `exp` or `iat` is refused.
- **Key caching.** The server caches the issuer's discovery document and keys for an hour. An unknown `kid` triggers one refresh, at most every 30 seconds per issuer. Key rotation needs no restart.
- **Token kind.** The token is always a **deploy** token. It can read, write and delete what the service account's roles allow, and never gets `manage`.
- **Rate limit.** 60 exchanges per minute per client address. Behind a proxy, set `trustedProxies`. Excess calls get `429` with `Retry-After`.

### Revoke access

- **Disable the service account.** The server refuses new exchanges for it. Tokens it already issued also stop working, because every token request checks that the account behind the token is still enabled.
- **Disable or delete a policy.** New exchanges through that policy stop. Tokens it already issued keep working until they expire, within their TTL (default 900 seconds, at most 3600). To stop them sooner, disable the service account.

### Audit and errors

Every refusal is the same `401 invalid_token` ("token not accepted"), whatever the cause: bad signature, expired, wrong audience, unknown issuer, no policy matched or disabled account. The endpoint tells a caller nothing about your policies. The server log and the audit log carry the reason.

- `auth.oidc_exchange` is logged for each token issued, with the policy, service account, issuer and subject.
- `auth.oidc_exchange_failed` is logged for a validly signed token that no policy accepted. It shows that a repository tried and failed. Unverifiable tokens are only logged, so garbage cannot fill the audit log.
- Creating, changing and deleting service accounts and policies is audited as `service_account.*` and `oidc_policy.*`.

You can also manage both with [`oa apply`](declarative.md).

## Troubleshooting

| Message | Cause |
|---|---|
| `HTTP 401` | missing, wrong or revoked token; `OA_SERVER` points elsewhere |
| `HTTP 403 forbidden` | the token lacks `read`/`write` on the repository, or the route is administrative (list bundles, pipelines, repository management) |
| `HTTP 404 bundle not found` | the bundle does not exist, or the token cannot read every source repository |
| `format not supported by oa upload` | the repository is not deb, ivy, maven, raw, rpm or sbt |
| `no artifact carries build.name=... and build.number=...` | nothing was uploaded with those `--build-name` / `--build-number` values, or in a repository the token cannot read; the name is everything before the last `/` of `--from-build` |
| `can only promote sealed bundles` | run `bundle seal` first |
| `target_repo is required` | the pipeline stage is not bound to a repository: pass `--target-repo` |
| `HTTP 401 token not accepted` on the exchange | the ID token is invalid, expired or for another audience, or no enabled trust policy of its issuer matches its claims, or the service account is disabled; check the audience the job requested and the policy's conditions, and the server log for the reason |
