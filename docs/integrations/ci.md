<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# CI integration

A CI job can publish build outputs to OpenArtifact, group them into a [release bundle](../repositories/release-bundles.md) and promote the bundle to the next stage, using only the `oa` CLI and a token that is not an administrator's. The same commands back the CI plugins:

| CI system | Plugin |
|---|---|
| Jenkins | [openartifact-plugin](https://github.com/openartifact/openartifact-plugin): pipeline steps and a freestyle build step, with a badge per job |
| Drone and Woodpecker | [drone-openartifact](https://github.com/openartifact/drone-openartifact): a plugin container with `upload` and `promote` modes |

The plugins never call the REST API: they install `oa` and run the commands below, parsing the JSON they print. Any other CI works the same way from a shell step.

## The flow

```sh
export OA_SERVER=https://oa.example.com
export OA_TOKEN=...            # a deploy token, see below

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

Every command exits `0` on success and `1` on failure and prints one JSON document with `--format json`; the exact shapes, the remote path rules of `upload` and the semantics of `--from-build` are in the [CLI reference](cli.md#ci-commands). Promotion adds metadata rows that reference the same blobs, so it costs the same for a 1 KiB or a 10 GiB bundle ([details](../repositories/release-bundles.md#what-a-copy-is)).

Supported by `oa upload`: hosted `deb`, `ivy`, `maven`, `raw`, `rpm` and `sbt` repositories. For every other format publish with the native client in the job (`cargo publish`, `docker push`, `helm push`, `npm publish`, `twine upload`, ...; Terraform and OpenTofu have no publish command, use `curl` against the [publish API](../formats/terraform.md#publish-api)). `bundle add` resolves artifacts of the same six formats, so bundles built by the CLI contain Debian, Ivy, Maven, raw, RPM and sbt artifacts; items of other formats can be added through the [bundles API](../repositories/release-bundles.md#bundles-api).

## Installing `oa`

`oa` 0.10.0 or newer is needed; `oa --format json version` prints `{"version","go","os","arch"}`. Download the archive for the runner from the [GitHub releases](https://github.com/openartifact/openartifact/releases) and verify it against `SHA256SUMS`:

```sh
V=0.16.0 OS=linux ARCH=amd64
base=https://github.com/openartifact/openartifact/releases/download/v$V
curl -fsSLO "$base/oa_${V}_${OS}_${ARCH}.tar.gz"
curl -fsSL  "$base/SHA256SUMS" | grep "oa_${V}_${OS}_${ARCH}.tar.gz" | sha256sum -c -
tar -xzf "oa_${V}_${OS}_${ARCH}.tar.gz" oa
```

See [installation](../getting-started/installation.md#release-archives) and [signing](../security/signing.md) for the signed checksums.

## Least-privilege CI token

CI does not need an administrator. An operator prepares, once:

1. The repositories: a build repository the job uploads to (`releases-dev`) and a release repository (`releases-prod`), both hosted and of the same format when bundles are promoted between them.
2. A [pipeline](../repositories/release-bundles.md#pipelines-api) whose stages are bound to those repositories, for example `dev` to `releases-dev` and `prod` to `releases-prod`. Only administrators create pipelines; this is the gate that decides where a bundle can go.
3. A user for CI, a [permission target](../security/rbac.md) giving it `read` and `write` on exactly those repositories (`repository_filter: ^releases-(dev|prod)$`, `path_pattern: .*`, `actions: [read, write]`), and a **deploy** [token](../security/authentication.md#api-tokens) for it. A deploy token can never be granted `manage`.

With that token a job can upload, set properties, create, fill, seal and promote bundles, and nothing else: it cannot create or delete repositories, edit pipelines, list all bundles or touch other repositories. What each step needs:

| Step | Needs |
|---|---|
| `upload` | `read` and `write` on the repository |
| `bundle create` | `read` on at least one repository |
| `bundle add`, `seal` | being the bundle's creator, and `read` on the source repositories |
| `promote` | `read` on the source repositories and `write` on the target repository (the stage's repository) |

Bundles are private to their creator: another CI user sees one only with `read` on every source repository, and can change it only when it created it (promotion needs no ownership, so a release job can promote what a build job created). The full table is in [bundle permissions](../repositories/release-bundles.md#permissions). To sign the manifest at seal time give the job the key id for `--gpg-key`; listing keys needs an administrator.

## Tokenless CI: OIDC token exchange

A long-lived CI secret can leak, outlive the project that used it and is awkward to rotate. With **CI OIDC token exchange** the job proves who it is with the ID token its CI system signs for it, and OpenArtifact trades that for an access token that lives at most an hour. Nothing secret is stored in the CI system.

```text
CI job ──ID token (aud = oa.example.com)──▶ POST /api/v1/auth/oidc/exchange
                                              │ verifies the signature against the issuer's JWKS,
                                              │ matches a trust policy, checks the claims
CI job ◀──access token (deploy, 15 min)───────┘ of the service account the policy maps to
```

An administrator sets this up once:

1. **A service account**: a non-human principal (`POST /api/v1/service-accounts`, or **Admin > Service accounts**). It cannot log in to the web UI, has no password and is hidden from the user list, but it holds [global roles](../security/rbac.md) (permission targets), [project roles](../security/projects.md) and group memberships like a user, and can own API tokens. Give it exactly what CI needs, as in [Least-privilege CI token](#least-privilege-ci-token).
2. **A trust policy** (`POST /api/v1/oidc-trust-policies`, or **Admin > Service accounts > Trust policies**):

| Field | Meaning |
|---|---|
| `name` | Unique; sent as `policy` by the job to pick this policy (optional, see below) |
| `issuer` | The CI system's issuer URL: `https://token.actions.githubusercontent.com` for GitHub Actions, `https://gitlab.com` (or your self-managed GitLab URL) for GitLab CI. Must be HTTPS and reachable from the server |
| `audience` | The `aud` the job must request; use your server URL or any string you choose, but not a value another service accepts |
| `conditions` | Claim checks, all of which must pass: `[{"claim": "repository", "patterns": ["acme/shop"]}, {"claim": "ref", "patterns": ["refs/tags/v*"]}]`. A condition passes when the claim matches any of its patterns. `*` matches any run of characters (including `/`), `?` one character, matching is case-sensitive, and a missing claim never matches. 1 to 16 conditions; a pattern cannot be only `*` |
| `service_account_id` | The service account whose identity the token carries |
| `token_ttl_seconds` | Lifetime of the issued token, 60 to 3600 (default 900) |
| `enabled` | A disabled policy matches nothing |

GitHub Actions and gitlab.com mint every customer's tokens under one issuer, so a policy for them must also pin who the job belongs to: one condition on `sub`, `repository`, `repository_id`, `repository_owner` or `repository_owner_id` (GitHub) or `sub`, `project_path`, `project_id`, `namespace_path` or `namespace_id` (GitLab), with patterns that start with a literal character (`acme/*`, not `*/shop`). OpenArtifact refuses such a policy otherwise. A self-hosted issuer is yours alone and has no such rule, but still needs at least one condition.

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

### The exchange API

`POST /api/v1/auth/oidc/exchange` needs no credentials. The body is `{"token": "<ID token>", "policy": "<name>"}`; `policy` is optional and only needed when several policies of one issuer accept the same token (otherwise the oldest matching policy wins). The answer is

```json
{"access_token": "oa_dep_...", "token_type": "Bearer", "expires_in": 900, "expires_at": "2026-11-01T12:15:00Z"}
```

Use it anywhere a token goes: `Authorization: Bearer`, `OA_TOKEN`, or as the password of a native client.

What the server enforces:

- The signature is verified against the issuer's keys, found through its `/.well-known/openid-configuration` document and `jwks_uri`, fetched with the same outbound-request protection as proxy upstreams (`OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS` also governs a self-hosted issuer on a private network). Only asymmetric algorithms are accepted, and only issuers named by an enabled policy are ever contacted.
- `iss`, `aud` (against the policy), `exp`, `nbf` and `iat` are checked (one minute of clock skew); a token without `exp` or `iat` is refused.
- Discovery documents and keys are cached for an hour; an unknown `kid` triggers one refresh, at most every 30 seconds per issuer, so key rotation needs no restart and a flood of forged tokens cannot make the server hammer the issuer.
- The token is always a **deploy** token: it can read, write and delete what the service account's roles allow, and can never get `manage`. Disabling the service account stops its tokens at once; deleting a policy does not revoke tokens it already issued, which expire within their TTL (at most an hour).
- At most 60 exchanges per minute per client address (behind a proxy set `trustedProxies`); more get `429` with `Retry-After`.
- Every refusal is the same `401 invalid_token` ("token not accepted"), whatever the cause (bad signature, expired, wrong audience, unknown issuer, no policy matched, disabled account), so the endpoint tells a caller nothing about the policies. The server log and the audit log carry the reason.

Audit log: `auth.oidc_exchange` for each token issued (policy, service account, issuer and subject), and `auth.oidc_exchange_failed` for a validly signed token that no policy accepted (the useful signal that a repository tried and failed; unverifiable tokens are only logged, so garbage cannot fill the audit log). Creating, changing and deleting service accounts and policies is audited as `service_account.*` and `oidc_policy.*`. Both can also be managed with [`oa apply`](declarative.md).

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
