#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Regenerates internal/vuln/scanners_manifest.json: the pinned Trivy and Grype
# release archives (with their upstream sha256) the server may download when
# scanners.<name>.install is "auto". Versions come from the digest-pinned
# upstream images in docker/Dockerfile, so the image variant and the runtime
# download never drift apart (internal/vuln has a test that enforces it).
#
# Usage: scripts/update-scanner-manifest.sh [--check]
#   --check  rewrite nothing; exit 1 when the committed manifest differs.
# Needs: bash, curl, jq, tar, sha256sum.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dockerfile="$root/docker/Dockerfile"
manifest="$root/internal/vuln/scanners_manifest.json"
check=false
[[ "${1:-}" == "--check" ]] && check=true

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# image_version <name>: the version tag of the "FROM ...<name>:<tag>@sha256 AS <name>-src" line, without a leading v.
image_version() {
  sed -nE "s#^FROM [^ ]*/$1:v?([0-9][^@ ]*)@sha256:[0-9a-f]{64} AS $1-src\$#\\1#p" "$dockerfile"
}

trivy_version="$(image_version trivy)"
grype_version="$(image_version grype)"
[[ -n "$trivy_version" && -n "$grype_version" ]] || { echo "cannot read pinned versions from $dockerfile" >&2; exit 1; }

# fetch <file> <url>: download over https only, including every redirect hop.
fetch() {
  curl --proto '=https' --proto-redir '=https' -fsSL --retry 3 -o "$1" "$2"
}

# entry <scanner> <owner/repo> <version> <goos/goarch> <archive file name>: JSON for one platform.
entry() {
  local scanner="$1" repo="$2" version="$3" platform="$4" file="$5"
  local rel="$repo/releases/download/v$version/$file"
  local sums="$tmp/$scanner-$version.sums"

  if [[ ! -f "$sums" ]]; then
    fetch "$sums" "https://github.com/$repo/releases/download/v$version/${scanner}_${version}_checksums.txt"
  fi

  local archive_sha
  archive_sha="$(awk -v f="$file" '$2 == f { print $1 }' "$sums")"
  [[ ${#archive_sha} -eq 64 ]] || { echo "no checksum for $file" >&2; exit 1; }

  fetch "$tmp/$file" "https://github.com/$repo/releases/download/v$version/$file"
  echo "$archive_sha  $tmp/$file" | sha256sum --check --status || { echo "checksum mismatch for $file" >&2; exit 1; }

  local dir="$tmp/x-$scanner-${platform//\//-}"
  mkdir -p "$dir"
  tar -xzf "$tmp/$file" -C "$dir" "$scanner"
  local binary_sha
  binary_sha="$(sha256sum "$dir/$scanner" | cut -d' ' -f1)"

  jq -n --arg platform "$platform" --arg path "$rel" --arg a "$archive_sha" --arg b "$binary_sha" \
    '{($platform): {path: $path, archive_sha256: $a, binary_sha256: $b}}'
}

platforms() { # scanner repo version amd64-file arm64-file
  # Sequential command substitutions, not <(...): a failing entry must stop the
  # script (set -e sees it here, but not in a process substitution).
  local amd64 arm64
  amd64="$(entry "$1" "$2" "$3" linux/amd64 "$4")"
  arm64="$(entry "$1" "$2" "$3" linux/arm64 "$5")"
  jq -n --argjson a "$amd64" --argjson b "$arm64" '$a + $b'
}

trivy="$(platforms trivy aquasecurity/trivy "$trivy_version" \
  "trivy_${trivy_version}_Linux-64bit.tar.gz" "trivy_${trivy_version}_Linux-ARM64.tar.gz")"
grype="$(platforms grype anchore/grype "$grype_version" \
  "grype_${grype_version}_linux_amd64.tar.gz" "grype_${grype_version}_linux_arm64.tar.gz")"

jq -n --arg tv "$trivy_version" --arg gv "$grype_version" --argjson t "$trivy" --argjson g "$grype" \
  '{scanners: {
      trivy: {version: $tv, binary: "trivy", platforms: $t},
      grype: {version: $gv, binary: "grype", platforms: $g}
  }}' >"$tmp/manifest.json"

if $check; then
  if ! diff -u "$manifest" "$tmp/manifest.json"; then
    echo "scanners_manifest.json is stale: run scripts/update-scanner-manifest.sh" >&2
    exit 1
  fi
  echo "scanners_manifest.json is up to date (trivy $trivy_version, grype $grype_version)"
else
  cp "$tmp/manifest.json" "$manifest"
  echo "wrote $manifest (trivy $trivy_version, grype $grype_version)"
fi
