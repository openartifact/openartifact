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

Supported by `oa upload`: hosted `raw`, `maven`, `ivy`, `sbt`, `deb` and `rpm` repositories. For every other format publish with the native client in the job (`npm publish`, `docker push`, `twine upload`, `helm push`, `cargo publish`, ...). `bundle add` resolves artifacts of the same six formats, so bundles built by the CLI contain raw, Maven, Ivy, sbt, Debian and RPM artifacts; items of other formats can be added through the [bundles API](../repositories/release-bundles.md#bundles-api).

## Installing `oa`

`oa` 0.10.0 or newer is needed; `oa --format json version` prints `{"version","go","os","arch"}`. Download the archive for the runner from the [GitHub releases](https://github.com/openartifact/openartifact/releases) and verify it against `SHA256SUMS`:

```sh
V=0.10.0 OS=linux ARCH=amd64
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

## Troubleshooting

| Message | Cause |
|---|---|
| `HTTP 401` | missing, wrong or revoked token; `OA_SERVER` points elsewhere |
| `HTTP 403 forbidden` | the token lacks `read`/`write` on the repository, or the route is administrative (list bundles, pipelines, repository management) |
| `HTTP 404 bundle not found` | the bundle does not exist, or the token cannot read every source repository |
| `format not supported by oa upload` | the repository is not raw, maven, ivy, sbt, deb or rpm |
| `no artifact carries build.name=... and build.number=...` | nothing was uploaded with those `--build-name` / `--build-number` values, or in a repository the token cannot read; the name is everything before the last `/` of `--from-build` |
| `can only promote sealed bundles` | run `bundle seal` first |
| `target_repo is required` | the pipeline stage is not bound to a repository: pass `--target-repo` |
