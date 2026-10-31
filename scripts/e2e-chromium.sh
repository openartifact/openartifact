#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Start (and stop) the Chromium that renders the visual and colour-contrast
# specs (web/e2e/chromium). It is a headless-shell container pinned by tag and
# digest, so a CI run, a local run and a baseline update paint the same pixels
# (same engine, same fonts, same software rasteriser).
#
# Usage:
#   scripts/e2e-chromium.sh start   # listen on 127.0.0.1:$CHROMIUM_PORT (9223)
#   scripts/e2e-chromium.sh stop
# The specs reach it through CHROMIUM_CDP_URL (default http://127.0.0.1:9223).
# The server under test must listen on the host (the container shares its network).
#
# Environment: CHROMIUM_PORT (9223), CHROMIUM_NAME (oa-e2e-chromium),
#   CONTAINER_ENGINE (podman, else docker)
set -euo pipefail

# Bump the tag and the digest together (docker.io/chromedp/headless-shell, the
# manifest-list digest of the tag); Dependabot does not track either. A new
# Chromium can move glyph edges: refresh the baselines after bumping
# (cd web && bun run e2e:visual:update) and commit them.
image="docker.io/chromedp/headless-shell:157.0.8103.27@sha256:7bee201478b2f6266aae01243740ee85c16281e32f42d3aed9fdf04f4eace7a1"

port="${CHROMIUM_PORT:-9223}"
name="${CHROMIUM_NAME:-oa-e2e-chromium}"
engine="${CONTAINER_ENGINE:-$(command -v podman >/dev/null && echo podman || echo docker)}"

die() { echo "e2e-chromium: $*" >&2; exit 1; }

start() {
	command -v "$engine" >/dev/null || die "$engine is required"
	command -v curl >/dev/null || die "curl is required"
	"$engine" rm -f "$name" >/dev/null 2>&1 || true
	# --no-sandbox: the container is the sandbox. The remaining flags fix what would
	# otherwise differ between machines: software GL, no hinting, no subpixel text,
	# sRGB output, no scrollbars.
	"$engine" run -d --rm --name "$name" --network host \
		--entrypoint /headless-shell/headless-shell "$image" \
		--no-sandbox --remote-debugging-address=127.0.0.1 --remote-debugging-port="$port" \
		--use-gl=angle --use-angle=swiftshader --font-render-hinting=none --disable-lcd-text \
		--force-color-profile=srgb --hide-scrollbars about:blank >/dev/null
	for _ in $(seq 1 30); do
		if curl -fsS "http://127.0.0.1:${port}/json/version" >/dev/null 2>&1; then
			echo "e2e-chromium: listening on http://127.0.0.1:${port}"
			return 0
		fi
		sleep 1
	done
	"$engine" logs "$name" >&2 || true
	die "Chromium did not answer on 127.0.0.1:${port}"
}

stop() {
	"$engine" rm -f "$name" >/dev/null 2>&1 || true
}

case "${1:-}" in
start) start ;;
stop) stop ;;
*) die "usage: $0 start|stop" ;;
esac
