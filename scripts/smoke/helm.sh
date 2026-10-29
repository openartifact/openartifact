#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# helm: helm package, upload the chart (POST /charts), helm repo add/update,
# helm pull and helm template it back. Image: HELM_IMAGE (default docker.io/alpine/helm).
# Usage: OA_TOKEN=... scripts/smoke/helm.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init helm docker.io/alpine/helm:latest "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
helm version --short
cd /tmp
helm create "$PKG" >/dev/null
printf 'marker: %s\n' "$PKG" > "$PKG/templates/marker.yaml"
helm package "$PKG" --version "$VERSION" >/dev/null
curl -fsS -u "smoke:$OA_TOKEN" --data-binary @"$PKG-$VERSION.tgz" "$OA_URL/repos/$REPO/charts" >/dev/null
helm repo add smoke "$OA_URL/repos/$REPO" --username smoke --password "$OA_TOKEN" >/dev/null
helm repo update >/dev/null
mkdir -p /tmp/out && cd /tmp/out
helm pull "smoke/$PKG" --version "$VERSION" >/dev/null
tar -xzOf "$PKG-$VERSION.tgz" "$PKG/templates/marker.yaml" | grep -qx "marker: $PKG"
helm template r "smoke/$PKG" --version "$VERSION" | grep -qx "marker: $PKG"
SCRIPT
)"
