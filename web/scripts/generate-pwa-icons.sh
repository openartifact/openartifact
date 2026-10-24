#!/bin/sh
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Regenerates the raster icons in public/ from the PNG exports of the official
# brand repository (github.com/openartifact/brand, CC-BY-SA-4.0). Needs
# ImageMagick (`magick`). The PNGs are committed; run this only when the brand
# mark changes.
#
#   BRAND_DIR=/path/to/brand sh scripts/generate-pwa-icons.sh
#
# The default server manifest references icon-192, icon-512 and
# icon-maskable-512 by these exact names and sizes.
set -eu

cd "$(dirname "$0")/.."

: "${BRAND_DIR:?set BRAND_DIR to a checkout of the openartifact/brand repository}"
mark="$BRAND_DIR/logo/png/openartifact-mark-color-1024.png"
fav="$BRAND_DIR/logo/png"
[ -f "$mark" ] || { echo "missing $mark" >&2; exit 1; }

out=public/icons
bg='#F9FAFB' # brand light background

# The mark's artwork is not centred in its 1024 canvas; trim it so the
# placement below centres the visible artwork.
trimmed=$(mktemp "${TMPDIR:-/tmp}/oa-mark.XXXXXX.png")
trap 'rm -f "$trimmed"' EXIT
magick "$mark" -trim +repage "$trimmed"

# tile SIZE ARTWORK_PERCENT OUT [ROUND]: the mark centred on the brand
# background, optionally with rounded corners. No transparency in the
# full-bleed variants.
tile() {
  size=$1 pct=$2 file=$3 round=${4:-0}
  art=$((size * pct / 100))
  magick -size "${size}x${size}" "xc:$bg" \
    \( "$trimmed" -resize "${art}x${art}" \) -gravity center -composite \
    -alpha off -depth 8 -strip "$file"
  if [ "$round" -gt 0 ]; then
    magick "$file" \( -size "${size}x${size}" xc:black -fill white \
      -draw "roundrectangle 0,0 $((size - 1)),$((size - 1)) $round,$round" \) \
      -alpha off -compose CopyOpacity -composite -depth 8 -strip "$file"
  fi
}

# "any" icons: rounded tile so the mark reads on light and dark launchers.
tile 192 62 "$out/icon-192.png" 43
tile 512 62 "$out/icon-512.png" 115
# Maskable: the platform crops to a circle of 80% of the width, so the artwork
# (a square whose diagonal must fit inside it) stays within a 56% box.
tile 512 56 "$out/icon-maskable-512.png"
# iOS rounds the corners itself and fills transparency with black: full bleed.
tile 180 62 "$out/apple-touch-icon.png"

# Favicon fallbacks for browsers without SVG favicon support.
cp "$fav/openartifact-mark-color-32.png" public/favicon-32.png
cp "$fav/openartifact-mark-color-16.png" public/favicon-16.png
