#!/bin/sh
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Regenerates the PNG icons in public/icons from the SVG sources in
# scripts/pwa-icons (the favicon mark on the brand indigo). Needs rsvg-convert.
# The PNGs are committed; run this only when the mark or brand colour changes.
set -eu

cd "$(dirname "$0")/.."

out=public/icons
src=scripts/pwa-icons

rsvg-convert -w 192 -h 192 "$src/any.svg" -o "$out/icon-192.png"
rsvg-convert -w 512 -h 512 "$src/any.svg" -o "$out/icon-512.png"
rsvg-convert -w 512 -h 512 "$src/maskable.svg" -o "$out/icon-maskable-512.png"
# iOS rounds the corners itself and fills transparency with black: full bleed.
rsvg-convert -w 180 -h 180 "$src/maskable.svg" -o "$out/apple-touch-icon.png"
