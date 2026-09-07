#!/usr/bin/env bash
# hack/gen-icons.sh
#
# Renders every favicon / PWA raster from the three SVG sources in ui/icons/.
# Run it by hand after editing any of them and commit what it writes.
#
# The outputs are COMMITTED rather than generated during the build, so neither
# `make ui` nor CI needs an image toolchain. That is the whole reason this
# script is not a package.json script and does not hang off the Makefile's ui
# target.
#
# librsvg does the rasterising, not ImageMagick's own SVG support: the chip is a
# gradient-filled rect and the mark on it is a filled path with two nonzero-rule
# holes in it, and IM's internal MSVG delegate renders both poorly. ImageMagick
# is used only to re-read the results and assert them.
#
# There is no favicon.ico. netra declares an SVG icon in ui/index.html, and the
# clients that go looking for a bare /favicon.ico are RSS readers, Windows
# bookmark thumbnails and old IE — none of which an authenticated fleet console
# targets. internal/hub/web's SPA fallback answers that path with index.html
# like any other unknown path; nothing here changes that.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$ROOT/ui/icons"
OUT="$ROOT/ui/public"
MASTER="$SRC/icon.svg" # the mark: ships as the tab icon AND renders the PWA rasters

for tool in rsvg-convert magick; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "gen-icons: $tool not found — brew install librsvg imagemagick" >&2
		exit 1
	fi
done
mkdir -p "$OUT"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# --- icon.svg: served directly as the modern tab icon ------------------------
# The master itself, copied verbatim. There is no separate favicon source beside
# it: Safari plates favicons on a light bar, where a hollow mark lands as an
# orange scribble on a white card, and a gradient-filled chip is already a solid
# object there. The master IS the tab icon.
cp "$MASTER" "$OUT/icon.svg"

# --- transparent, from the master --------------------------------------------
# -b none, and these still come out transparent: the master paints a chip, not a
# canvas, so the four corners outside its rx-4 radius stay clear and the mark
# lands as a chip rather than a square tile. The renderer must not supply a
# ground of its own here, or the check below — which is what catches a master
# that grew a background rect — could never fail.
rsvg-convert -b none -w 192 -h 192 "$MASTER" -o "$OUT/icon-192.png"
rsvg-convert -b none -w 512 -h 512 "$MASTER" -o "$OUT/icon-512.png"

# --- opaque, from the tiled sources ------------------------------------------
# No -b none here, and that is the point: iOS flattens alpha onto black and
# Android fills it with the launcher's own colour, so these two carry netra's
# #131312 edge to edge instead of letting the OS choose.
rsvg-convert -w 180 -h 180 "$SRC/icon-tile.svg" -o "$OUT/apple-touch-icon.png"
rsvg-convert -w 512 -h 512 "$SRC/icon-maskable.svg" -o "$OUT/icon-maskable-512.png"

# --- verify the grounds survived ---------------------------------------------
# Rendering can succeed and still produce the wrong thing — a tiled source that
# lost its background rect yields a touch icon iOS flattens onto black; a master
# that GREW one stamps a square of netra's ground onto every tab bar it lands
# in. Either fails silently in a viewer and only shows up on a real device, so
# assert every ground here instead.
#
# The four split two and two, and the halves are not the same decision:
#
#   icon-192/512          transparent — from ui/icons/icon.svg, whose chip leaves
#                         the canvas corners clear.
#   apple-touch/maskable  opaque — from the tiled sources, and deliberately NOT
#                         following the master: iOS flattens alpha onto black and
#                         Android fills it with the launcher's colour, so these
#                         two must carry their own ground whatever the favicon
#                         does.
#
# A mismatch here means a source changed, not that the check needs relaxing.
fail=0
check_alpha() { # <file> <expected true|false>
	local got
	# ImageMagick 7 prints "True"/"False"; 6 printed "true"/"false". Fold the
	# case so this script is not pinned to one major version.
	got="$(magick identify -format '%[opaque]' "$1" | tr '[:upper:]' '[:lower:]')"
	if [[ "$got" != "$2" ]]; then
		echo "gen-icons: $(basename "$1") is opaque=$got, expected $2" >&2
		fail=1
	fi
}
check_alpha "$OUT/icon-192.png" false
check_alpha "$OUT/icon-512.png" false
check_alpha "$OUT/apple-touch-icon.png" true
check_alpha "$OUT/icon-maskable-512.png" true

# icon.svg ships as SVG, so there is no raster to read — render one here just to
# assert it. Three samples, one per way this file can quietly go wrong:
#
#   corner (0,0)     must be CLEAR, or the icon is a square tile and not a chip
#   ground (256,43)  must be OPAQUE — y 4.51 in the 24-space, which is 1.5 units
#                    below the chip's top edge and 1.5 above the mark's, so it
#                    reads the chip and never the mark. The mark fills 12 of the
#                    chip's 18 units and is centred, which leaves exactly 3 units
#                    of chip above it; this sample sits in the middle of them
#   ink (256,161)    must be netra's #131312 — y 8.66 in the 24-space, the centre
#                    of the mark's TOP panel (which spans y 6 to 11.33), on the
#                    chip's centre column. NOT the chip's own centre, where the
#                    old trace's descending stroke used to cross: the server mark
#                    is two stacked panels with a gap, and (12,12) lands in that
#                    gap and reads the gradient. The nearest thing that could
#                    still fool this point is the top panel's indicator slot, and
#                    that sits at x 8.67-10.67, well left of the column sampled
#
# The first two are asserted on ALPHA, not on a hex value: the chip is painted
# with a gradient, so any single sample sits at one arbitrary point on the ramp
# and a hex equality would pin the check to that point and break on every
# retune. The third IS a hex equality, because the mark is flat-filled and
# losing it — a dropped fill attribute, a path that failed to parse — is the
# one failure that leaves a perfectly plausible-looking orange chip behind.
#
# -alpha on before every read. Without it a fully opaque image carries no alpha
# channel, and then %[fx:...a] does not report 1 and %[hex:...] returns six
# digits instead of eight — the comparisons would be measuring ImageMagick's
# channel bookkeeping rather than the icon.
rsvg-convert -b none -w 512 -h 512 "$OUT/icon.svg" -o "$TMP/icon-favicon.png"
fav_corner="$(magick "$TMP/icon-favicon.png" -alpha on -format '%[fx:p{0,0}.a]' info:)"
fav_ground="$(magick "$TMP/icon-favicon.png" -alpha on -format '%[fx:p{256,43}.a]' info:)"
fav_ink="$(magick "$TMP/icon-favicon.png" -alpha on -format '%[hex:p{256,161}]' info: | tr '[:upper:]' '[:lower:]')"
if [[ "$fav_corner" != "0" ]]; then
	echo "gen-icons: icon.svg's canvas corner has alpha $fav_corner, expected 0" >&2
	fail=1
fi
if [[ "$fav_ground" != "1" ]]; then
	echo "gen-icons: icon.svg's chip has alpha $fav_ground, expected 1" >&2
	fail=1
fi
if [[ "$fav_ink" != "131312ff" ]]; then
	echo "gen-icons: icon.svg's mark is #$fav_ink at the top panel's centre, expected #131312ff" >&2
	fail=1
fi

[[ "$fail" == 0 ]] || exit 1
echo "gen-icons: wrote $(cd "$OUT" && ls icon.svg icon-*.png apple-touch-icon.png | tr '\n' ' ')-> $OUT"
