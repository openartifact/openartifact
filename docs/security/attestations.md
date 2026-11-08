<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Image attestations

OpenArtifact indexes the signatures, SLSA provenance and SBOMs that cosign and Tekton Chains push next to an image in a hosted [docker repository](../formats/docker.md), and verifies them against a trust configuration you set per repository. Use it to see who built an image, and to refuse to promote an image that has no verified provenance.

Pushes are never blocked: the signature and the attestation arrive after the image, so verification only labels what is stored and gates promotion.

## Trust the signer of your builds

A repository verifies nothing until you tell it which keys or identities to trust. Send the configuration with `PUT /api/v1/repositories/{name}/attestation-trust` (repository admin or global manage). The repository settings page in the web UI edits the same document.

Trust a static key (a cosign or Chains key pair, ECDSA, RSA or Ed25519):

```bash
curl -X PUT "$OA_URL/api/v1/repositories/konflux/attestation-trust" \
  -H "Authorization: Bearer $OA_TOKEN" -H 'Content-Type: application/json' \
  -d "$(jq -n --rawfile key cosign.pub '{keys: [$key], require_verified_provenance: false}')"
```

Trust a keyless identity (a Fulcio certificate): give the Fulcio root and the identities allowed to sign.

```json
{
  "keys": [],
  "fulcio": {
    "roots": ["-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----\n"],
    "identities": [
      {
        "issuer": "https://kubernetes.default.svc",
        "subject": "https://kubernetes.io/namespaces/tekton-chains/serviceaccounts/tekton-chains-controller"
      }
    ]
  },
  "require_verified_provenance": true
}
```

The fields:

| Field | Meaning |
|---|---|
| `keys` | PEM public keys (`PUBLIC KEY` blocks). At most 16. |
| `fulcio.roots` | PEM certificates of the Fulcio roots. At most 16. Needs `identities`. |
| `fulcio.identities` | Signers a certificate may name. At most 32. Each has an `issuer` and exactly one of `subject` or `subject_regexp`. |
| `require_verified_provenance` | Refuse to promote a docker image that has no verified SLSA provenance. |

A `subject_regexp` must match the whole subject, not a part of it. An identity matches the certificate's e-mail or URI subject alternative name and the OIDC issuer recorded in its extension.

## See what an image carries

List the attachments of an image digest. The read permission on the repository is enough:

```bash
curl -H "Authorization: Bearer $OA_TOKEN" \
  "$OA_URL/api/v1/repositories/konflux/attestations?subject=sha256:<DIGEST>"
```

Each entry has `kind` (`signature`, `attestation` or `sbom`), `predicate_type`, `signer_identity`, `issuer`, `verified`, `verify_error` and `created_at`. Fetch the decoded in-toto statement of one with `GET .../attestations/{digest}`. The `oa attestations` command prints the same list.

In the web UI the image detail page shows the attachments with a verified badge, the signer and the predicate.

## Verify again after you change the trust

Changing the trust configuration does not touch what is indexed. Verify again, for one image or for the whole repository:

```bash
curl -X POST -H "Authorization: Bearer $OA_TOKEN" \
  "$OA_URL/api/v1/repositories/konflux/attestations/reverify?subject=sha256:<DIGEST>"
curl -X POST -H "Authorization: Bearer $OA_TOKEN" \
  "$OA_URL/api/v1/repositories/konflux/attestations/reverify"
```

Both answer `{"reverified": n}`, the number of attachment manifests read again. Without `subject` only images that already have an indexed attachment are visited. To index an image whose rows are gone, for example after a restore, or one that arrived by promotion, pass `subject` and `image`.

## Require verified provenance on promotion

With `require_verified_provenance` set on the source or the target repository, promoting a [release bundle](../repositories/release-bundles.md#promoting-a-bundle) that holds a docker image without verified SLSA provenance answers `422` with the code `PROVENANCE_REQUIRED`. Provenance counts when its predicate is `https://slsa.dev/provenance/v0.2` or `https://slsa.dev/provenance/v1` and it verified against the **source** repository's trust configuration. Configure the trust where the images are built, and set the flag on the repository that must stay clean.

## What is detected and verified

| Pushed as | Kind | Verified against |
|---|---|---|
| Tag `sha256-<DIGEST>.att` (cosign, Chains) | `attestation` | The DSSE signature of each envelope layer |
| Tag `sha256-<DIGEST>.sig` | `signature` | The signature in the layer annotation, over the layer's simple signing payload |
| Tag `sha256-<DIGEST>.sbom` | `sbom` | Never: these SBOMs are unsigned. They are indexed as unverified |
| Referrer with a Sigstore bundle (v0.3) or a DSSE envelope as artifact type | `attestation` | The envelope inside, with the certificate the bundle carries |

An attachment verifies when the signature checks against a trusted key, or against the leaf certificate of a trusted Fulcio identity whose chain ends in a configured root. The statement must also name the image digest in its `subject`, or the signed payload must name it, so a valid attestation about another image cannot be filed under this one. An envelope larger than 4 MiB is indexed as unverified.

## Limitations

- **No transparency log.** OpenArtifact does not check Rekor, so a signature's inclusion in a log is not proven. Run Tekton Chains with `transparency.enabled: "false"`, and verify with `ec validate image ... --ignore-rekor` or `cosign verify-attestation --insecure-ignore-tlog`.
- **Keyless signatures are judged when first seen.** Fulcio certificates live about ten minutes, and without a log there is no signed time. OpenArtifact checks the certificate at the moment it first indexed the envelope, and keeps that time on every verification. A keyless signature imported long after it was made, or indexed after its certificate expired, shows as not verified.
- **Promotion does not index in the target.** A promoted image brings its signature and attestation tags, but the target repository's index holds nothing for it until you run `reverify` with `subject` and `image`. Provenance is judged in the source repository.
- **Message-signature bundles are not verified.** A Sigstore bundle that signs a blob rather than an attestation is listed as a signature with the reason in `verify_error`.

## Tekton Chains and Konflux

Konflux writes the `.sig` and `.att` tags next to every image by default, so no Chains change is needed. Settings to check:

- Keep `transparency.enabled: "false"` in the Chains configuration, as Konflux does. The verification then needs no log.
- For keyless signatures, trust the identity Konflux publishes as `tektonChainsIdentity` and the issuer it publishes as `defaultOIDCIssuer`, both in the `konflux-info` `cluster-config` ConfigMap. Put them in `fulcio.identities`. Add the Fulcio root of the cluster to `fulcio.roots`.
- For key-based signing, put the public key of the Chains signing secret in `keys`.
- Chains stores its attestations as `in-toto` statements with SLSA provenance predicate v0.2, or v1 with the `slsa/v1` format. Both count for `require_verified_provenance`.

Check the result from the Konflux side with the same identity:

```bash
cosign verify-attestation --type slsaprovenance \
  --certificate-identity "$TEKTON_CHAINS_IDENTITY" \
  --certificate-oidc-issuer "$DEFAULT_OIDC_ISSUER" \
  --insecure-ignore-tlog <OA-HOST>/konflux/<NAMESPACE>/<COMPONENT>@sha256:<DIGEST>
```

## Troubleshooting

| Symptom | Cause |
|---|---|
| `verify_error` says the repository trusts no key and no Fulcio identity | No trust configuration is set. Set it and run `reverify`. |
| `the certificate identity is not trusted` | The certificate's subject or issuer matches none of `fulcio.identities`. The row's `signer_identity` and `issuer` show what the certificate claims. |
| `does not chain to a trusted Fulcio root` | The root is not in `fulcio.roots`, or the certificate was not valid when the envelope was first seen. |
| `does not cover the image digest` | The statement is about another image than the tag it was pushed under. |
| Nothing is listed for an image | The attachment manifest was not a cosign tag or a referrer of that digest, or it was pushed to another image name. |
