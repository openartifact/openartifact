<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Tekton catalog

The OpenArtifact Tekton catalog gives Tekton pipelines, including [Konflux](konflux.md) ones, tasks that publish to OpenArtifact, gate images and promote bundles without a stored secret. It lives in `integrations/tekton/` and needs Tekton Pipelines 0.62 or newer.

## Use a task from a bundle

Every release publishes each StepAction, Task and Pipeline as a signed Tekton bundle, `ghcr.io/openartifact/tekton-catalog/<KIND>-<NAME>:<VERSION>`. Reference it by digest:

```yaml
taskRef:
  resolver: bundles
  params:
    - {name: bundle, value: "ghcr.io/openartifact/tekton-catalog/task-oa-gate@sha256:<DIGEST>"}
    - {name: name, value: oa-gate}
    - {name: kind, value: task}
```

The tasks run StepActions through `ref: {name: ...}`, so install the two StepActions (`oa-exchange-token`, `oa-upload`) in the same namespace, or install the whole catalog with `kubectl apply -f integrations/tekton/stepactions -f integrations/tekton/tasks -f integrations/tekton/pipelines`. Under the bundles resolver the Tekton pods need the OpenArtifact CA if the server uses a private one.

## What is in it

| Name | Kind | Does |
|---|---|---|
| `oa-exchange-token` | StepAction | Exchanges the projected service account token for a short-lived OpenArtifact token and writes a docker `config.json` |
| `oa-upload` | StepAction | `oa upload` for deb, ivy, maven, raw, rpm and sbt repositories |
| `oa-publish` | Task | Uploads files from a Trusted Artifact, or copies Snapshot images into a docker repository |
| `oa-bundle-promote` | Task | `oa bundle create`, `add --digest`, `seal` and `oa promote --stage`; `MODE` splits sealing from promoting |
| `oa-gate` | Task | `oa gate` for a Snapshot or digests; emits `TEST_OUTPUT`, `SCAN_OUTPUT` and `GATE_RESULT` |
| `oa-sbom-fetch` | Task | Downloads the SBOM and attestations of an image |
| `oa-report-check` | Task | Reports a Conforma or test result on a bundle with `oa bundle check report` |
| `oa-release` | Pipeline | Publish, gate, seal, report the gate, promote |
| `push-to-openartifact` | Pipeline | Managed release pipeline with the contract of `push-to-external-registry` |

All tasks use the image `ghcr.io/openartifact/oa-tekton`, which holds `oa`, `cosign`, `oras`, `skopeo` and `jq`. It is built from `docker/Dockerfile.oa-tekton`.

## Authentication

Each task declares a projected `serviceAccountToken` volume with audience `openartifact` and a 600 second lifetime. Create an [OIDC trust policy](ci.md#kubernetes-and-tekton-service-accounts) whose `sub` condition matches the pipeline service account, and the task needs no secret. Pass `POLICY` when several policies could match.

## Publish and promote in a pipeline

```yaml
- name: seal
  taskRef: {name: oa-bundle-promote}
  params:
    - {name: SERVER, value: https://oa.example.com}
    - {name: REPOSITORY, value: konflux}
    - {name: BUNDLE_NAME, value: app}
    - {name: BUNDLE_VERSION, value: 1.4.0}
    - {name: DIGESTS, value: ["sha256:<DIGEST>"]}
    - {name: MODE, value: seal}
```

A stage with `required_checks` needs the checks reported before `MODE: promote`; see `oa-release` for the order.

## Pipelines as Code

Use the catalog from a `.tekton` pipeline with the bundles resolver, or fetch a task by raw URL:

```yaml
pipelinesascode.tekton.dev/task: "[https://raw.githubusercontent.com/openartifact/openartifact/<TAG>/integrations/tekton/tasks/oa-gate.yaml]"
```

Pin a tag or commit, never a branch.

## Trust the catalog in Conforma

Conforma accepts a task only when it is in `trusted_tasks` or matches an allow rule. One rule for all catalog bundles, pinned to the identity that signs them:

```yaml
trusted_task_rules:
  allow:
    - name: OpenArtifact catalog
      pattern: "oci://ghcr.io/openartifact/tekton-catalog/*"
      signing_identity:
        issuer: https://token.actions.githubusercontent.com
        subject: https://github.com/openartifact/openartifact/.github/workflows/tekton-catalog.yml@refs/tags/*
```

Verify a bundle by hand:

```sh
cosign verify \
  --certificate-identity-regexp '^https://github.com/openartifact/openartifact/.github/workflows/tekton-catalog.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/openartifact/tekton-catalog/task-oa-gate@sha256:<DIGEST>
```

## The `oa` commands behind it

| Command | Use |
|---|---|
| `oa oidc exchange` | Token exchange; `--docker-config DIR` writes `config.json` |
| `oa bundle add --digest` | Put image manifests in a bundle |
| `oa bundle check report` | Report a check on a bundle |
| `oa gate` | Gate for a digest or a scan; exit 1 on failure |
| `oa attestations` | List the attestations of a subject digest |

## Related

- [Konflux](konflux.md)
- [CI integration](ci.md)
- [CLI](cli.md)
