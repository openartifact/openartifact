#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# oci-supplychain: conformance of the OCI supply-chain flows Konflux and Tekton rely on, against a hosted
# docker repository: oras push of an OCI artifact, cosign sign / attest / attach sbom with a throwaway key,
# cosign copy, referrers discovery, blob fetch and cosign verify back.
# Image: OCI_SUPPLYCHAIN_IMAGE (default ghcr.io/openartifact/oa-tekton, which ships cosign, oras and jq).
# A plain-http OA_URL is handled with the insecure-registry flags of both clients.
# Usage: OA_TOKEN=... scripts/smoke/oci-supplychain.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init docker ghcr.io/openartifact/oa-tekton:0.21.0 "$@"
SMOKE_IMAGE="${OCI_SUPPLYCHAIN_IMAGE:-$SMOKE_IMAGE}"

smoke_run "$(
	cat <<'SCRIPT'
set -eu
HOST="${OA_URL#*://}"
INSECURE=""
PLAIN=""
case "$OA_URL" in http://*) INSECURE="--allow-insecure-registry"; PLAIN="--plain-http" ;; esac
export COSIGN_PASSWORD=""
export DOCKER_CONFIG=/tmp/docker
mkdir -p "$DOCKER_CONFIG" /tmp/w && cd /tmp/w
AUTH="$(printf 'oa:%s' "$OA_TOKEN" | base64 | tr -d '\n')"
printf '{"auths":{"%s":{"auth":"%s"}}}' "$HOST" "$AUTH" > "$DOCKER_CONFIG/config.json"

IMG="$HOST/$REPO/app"
echo "payload $PKG" > payload.txt
# shellcheck disable=SC2086
oras push $PLAIN "$IMG:$VERSION" payload.txt:application/vnd.smoke.payload
# shellcheck disable=SC2086
DIGEST="$(oras manifest fetch $PLAIN --descriptor "$IMG:$VERSION" | jq -r .digest)"
REF="$IMG@$DIGEST"
echo "subject $REF"

cosign generate-key-pair >/dev/null
# Key-based signing without a transparency log, stored in the cosign tag schema (sha256-<hex>.sig/.att)
# that Tekton Chains and Konflux write; cosign 3 needs the signing config and new bundle format turned off for it.
KEYSIGN="--yes --key cosign.key --use-signing-config=false --tlog-upload=false --new-bundle-format=false"
# shellcheck disable=SC2086
cosign sign $KEYSIGN $INSECURE "$REF"
echo '{"smoke":"predicate"}' > predicate.json
# shellcheck disable=SC2086
cosign attest $KEYSIGN $INSECURE --type custom --predicate predicate.json "$REF"
echo '{"spdxVersion":"SPDX-2.3","name":"smoke","SPDXID":"SPDXRef-DOCUMENT"}' > sbom.spdx.json
# shellcheck disable=SC2086
cosign attach sbom $INSECURE --sbom sbom.spdx.json --type spdx "$REF"

# the signature, attestation and sbom must be reachable as cosign tags and verify back
# shellcheck disable=SC2086
cosign verify --key cosign.pub --insecure-ignore-tlog $INSECURE "$REF" >/dev/null
# shellcheck disable=SC2086
cosign verify-attestation --key cosign.pub --insecure-ignore-tlog $INSECURE --type custom "$REF" >/dev/null
# shellcheck disable=SC2086
cosign download sbom $INSECURE "$REF" | jq -e '.spdxVersion == "SPDX-2.3"' >/dev/null

# copy the signed image into a second image name of the same repository: signatures follow
# shellcheck disable=SC2086
cosign copy $INSECURE "$REF" "$HOST/$REPO/app-copy:$VERSION"
# shellcheck disable=SC2086
oras manifest fetch $PLAIN --descriptor "$HOST/$REPO/app-copy:$VERSION" | jq -e --arg d "$DIGEST" '.digest == $d' >/dev/null

# cosign 3 bundle format: the signature is an OCI referrer of the subject (subject field), listed by the
# referrers API (oras falls back to the tag schema when a registry lacks it)
# shellcheck disable=SC2086
cosign sign --yes --key cosign.key --use-signing-config=false --tlog-upload=false $INSECURE "$REF"
# shellcheck disable=SC2086
oras discover $PLAIN --format json "$REF" > referrers.json
jq -e '.referrers | length >= 1' referrers.json >/dev/null
# shellcheck disable=SC2086
cosign verify --key cosign.pub --insecure-ignore-tlog $INSECURE "$REF" >/dev/null

# blob round trip
# shellcheck disable=SC2086
LAYER="$(oras manifest fetch $PLAIN "$REF" | jq -r '.layers[0].digest')"
# shellcheck disable=SC2086
oras blob fetch $PLAIN --output fetched.txt "$IMG@$LAYER"
[ "$(cat fetched.txt)" = "payload $PKG" ]
echo "oci-supplychain: ok"
SCRIPT
)"
