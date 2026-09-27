#!/usr/bin/env bash
# Crop the raw dashboard captures (tools/captures/) down to the part worth showing
# on the landing page, and write the results into assets/img/.
#
# Parked: the feature section draws its own diagrams now, so nothing on the page
# references shot-*.webp any more. Kept because the geometry below (window choice,
# the 3/2 source scale, cutting in gutters) is the expensive part to rediscover.
# The captures are a 1600x900 dashboard window at 1.5x DPR, so source px = css * 3 / 2
# (windows use even numbers only, to keep that integral). Each window is sized so the
# crop lands in a gutter between cards, never through the middle of a row of text —
# that is what makes the shot read as a detail rather than a shrunken collage.
#
# Requires ffmpeg with libwebp.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$ROOT/tools/captures"
OUT="${1:-$ROOT/assets/img}"
mkdir -p "$OUT"

crop() { # name  x  y  w  h  src
  local name="$1" x="$2" y="$3" w="$4" h="$5" src="$6"
  local sx=$((x * 3 / 2)) sy=$((y * 3 / 2)) sw=$((w * 3 / 2)) sh=$((h * 3 / 2))
  ffmpeg -y -hide_banner -loglevel error -i "$SRC/$src.webp" \
    -vf "crop=${sw}:${sh}:${sx}:${sy}" -c:v libwebp -quality 82 -compression_level 6 \
    "$OUT/$name.webp"
  printf '%-16s css %sx%s @%s,%s  ->  %sx%s  %s KB\n' "$name" "$w" "$h" "$x" "$y" "$sw" "$sh" \
    "$(( $(stat -c%s "$OUT/$name.webp") / 1024 ))"
}

crop shot-combos     60  62 1000 280 virtual-combos
crop shot-upstreams  58 120 1000 280 upstreams
crop shot-settings   76 496 1000 404 settings
crop shot-tenants    70 138 1000 176 tenants
crop shot-overview  540  64 1000 436 overview
crop shot-tools     300 140 1240 340 tools-chat
crop shot-telemetry 436  80 1104 444 telemetry
