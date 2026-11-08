<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Konflux

[Konflux](https://konflux-ci.dev) builds, tests and releases container images with Tekton. OpenArtifact can be the registry Konflux pushes to, the package registry proxy for hermetic builds, and the place where images are bundled and promoted. This page needs no OpenArtifact code beyond a release that includes the [Tekton catalog](tekton.md); everything is configuration.

## 1. Registry for builds

Create two hosted docker repositories, a user per tenant namespace and a deploy token for it.

```sh
oa repo create konflux --format docker
oa repo create konflux-ta --format docker
```

Create a **deploy** [token](../security/authentication.md#api-tokens) for the tenant's user and a [permission target](../security/rbac.md) that limits the user to the tenant's images: `repository_filter: ^konflux$`, `path_pattern: team-a/.*`, `actions: [read, write]`.

The Konflux UI creates repositories through image-controller, which only speaks to Quay. To keep the UI flow, use the Quay-compatible API described in [Konflux image controller](konflux-image-controller.md). The registry path is `HOST/konflux/team-a/<component>`: the first segment is the repository, the rest is the image ([docker format](../formats/docker.md)).

Create the push secret in the tenant namespace and link it to the build service account:

```sh
kubectl create secret docker-registry oa-push -n team-a \
  --docker-server=oa.example.com --docker-username=oa --docker-password="$TOKEN"
kubectl patch serviceaccount build-pipeline-<COMPONENT> -n team-a \
  -p '{"secrets":[{"name":"oa-push"}]}'
```

Set the Component `spec.containerImage` to `oa.example.com/konflux/team-a/<COMPONENT>`.

Trusted Artifacts are large and short-lived. Point the `ociStorage` parameter at `oa.example.com/konflux-ta/team-a` and give `konflux-ta` a [cleanup policy](../repositories/cleanup-and-quotas.md) with `keep_since_days: 1`.

### Trust the OpenArtifact certificate

Put the CA that signed the OpenArtifact certificate into the `trusted-ca` ConfigMap (key `ca-bundle.crt`). Chains and `buildah-oci-ta` read it. On OpenShift a certificate issued by the cluster `service-ca` is already trusted by the prefetch task.

The Tekton tasks of the [catalog](tekton.md) take a `CA_FILE`, which they export as `SSL_CERT_FILE`. If OpenArtifact itself must reach an issuer or webhook receiver on a private address, set `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true` on the server.

### Tokens without a stored secret

Tasks from the catalog authenticate with the pod's projected service account token through [OIDC token exchange](ci.md#kubernetes-and-tekton-service-accounts), so they need no secret. Stock Konflux build tasks read a static docker config; use a deploy token with an expiry and rotate the Secret before it passes.

## 2. Hermetic builds

Konflux can send Hermeto dependency downloads through a package registry proxy. OpenArtifact proxy repositories apply quarantine, curation, minimum-age and licence policy to everything a build pulls. Set the proxy keys in the `konflux-info/cluster-config` ConfigMap (the `KonfluxInfo` custom resource):

```yaml
allow-package-registry-proxy: "true"
package-registry-proxy-npm-url: https://oa.example.com/repos/npm-proxy/
package-registry-proxy-yarn-url: https://oa.example.com/repos/npm-proxy/
package-registry-proxy-pnpm-url: https://oa.example.com/repos/npm-proxy/
package-registry-proxy-pip-url: https://oa.example.com/repos/pypi-proxy/simple/
package-registry-proxy-cargo-url: sparse+https://oa.example.com/repos/cargo-proxy/index/
package-registry-proxy-gomod-url: https://oa.example.com/repos/go-proxy
package-registry-proxy-bundler-url: https://oa.example.com/repos/gems-proxy/
```

The URLs must not contain credentials because the ConfigMap is readable by every authenticated user. Make the proxy repositories public-read and reachable only from inside the cluster (a Service and a NetworkPolicy). The formats are described in [npm](../formats/npm.md), [PyPI](../formats/pypi.md), [Cargo](../formats/cargo.md), [Go](../formats/go.md) and [RubyGems](../formats/rubygems.md).

Notes:

- Go modules: the checksum database is not proxied, so the prefetch step needs access to `sum.golang.org`.
- Bundler: Hermeto asks the proxy for `/downloads/<gem>`; OpenArtifact serves it as an alias of `/gems/<gem>`.
- Maven, RPM and generic files have no proxy key. Use `artifacts.lock.yaml` or `rpms.lock.yaml` with OpenArtifact repository URLs.

## 3. Conforma

[Conforma](https://conforma.dev) verifies the build. Add OpenArtifact to its rule data so dependencies fetched through the proxies pass `maven_repos`, `rpm_repos` and `base_image_registries`. To work offline, pull the policy and the acceptable-bundles data through a docker proxy repository and use `oci::oa.example.com/<proxy-repo>/...` sources.

Trust the catalog tasks with one `trusted_task_rules` allow rule pinned to the signing identity of the catalog bundles (see [Tekton catalog](tekton.md#trust-the-catalog-in-conforma)).

To gate a promotion on Conforma, list the check in the stage's `required_checks` and report it with the `oa-report-check` task. A promotion without a successful check answers 422 `CHECKS_REQUIRED` and names the missing checks. See [release bundles](../repositories/release-bundles.md).

## 4. Rebuild when something changes

OpenArtifact [webhooks](../operations/webhooks.md) call a Tekton Triggers EventListener, which annotates the Konflux Component to start a build.

1. Create a webhook in cloudevents format with a secret. Its URL is the EventListener Service. The target is a private address, so set `OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true`.
2. Validate `X-Hub-Signature-256` with the `github` interceptor and the shared secret.
3. Filter in CEL. Every manifest push fires an event, including cosign `.sig`, `.att` and `.sbom` tags, so exclude them:

```yaml
- ref: {name: cel}
  params:
    - name: filter
      value: >-
        body.type == 'io.openartifact.artifact.uploaded' &&
        !body.data.version.matches('^sha256-[0-9a-f]{64}(\\.(sig|att|sbom|src))?$')
```

4. The TriggerTemplate runs `kubectl annotate components/<COMPONENT> build.appstudio.openshift.io/request=trigger-pac-build --overwrite` with a service account allowed to patch Components. Delivery is at-least-once: key your trigger on `X-OA-Delivery`.

Events such as `scan.completed`, `vulnerability.found` and `quarantine.changed` are available too, in the same formats.

MintMaker keeps the digests of the catalog bundles current when the bundles are referenced by digest.

## 5. Release

The `push-to-openartifact` managed pipeline in `integrations/tekton/pipelines/` has the contract of `push-to-external-registry` from the release service catalog. It runs Conforma, copies the Snapshot into an OpenArtifact docker repository, seals a bundle, reports the Conforma result as a bundle check and promotes. Reference it from the ReleasePlanAdmission `spec.pipeline.pipelineRef` with the `git` resolver and supply the extra parameters `server`, `registryHost`, `repository`, `stage`.

For a smaller flow, run `oa-release` from an integration test scenario or by hand.

## 6. Gate an integration test

Use `oa-gate` as an integration test scenario. It aggregates the vulnerability gate, licence policy, quarantine and attestation presence of each image in the Snapshot and returns the standard `TEST_OUTPUT`:

```yaml
taskRef:
  resolver: bundles
  params:
    - {name: bundle, value: "ghcr.io/openartifact/tekton-catalog/task-oa-gate@sha256:<DIGEST>"}
    - {name: name, value: oa-gate}
    - {name: kind, value: task}
params:
  - {name: SERVER, value: https://oa.example.com}
  - {name: REPOSITORY, value: konflux}
  - {name: SNAPSHOT, value: $(params.SNAPSHOT)}
```

The same check from a shell: `oa gate konflux --digest sha256:<DIGEST>` exits 1 when the gate fails.

## Related

- [Tekton catalog](tekton.md)
- [CI integration](ci.md)
- [Docker format](../formats/docker.md)
- [Konflux image controller](konflux-image-controller.md)
- [Attestations](../security/attestations.md)
