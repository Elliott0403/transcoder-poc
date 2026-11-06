#!/usr/bin/env bash
# Prepare local test videos under testdata/ (git-ignored).
#
# Usage:
#   scripts/testdata.sh          # synthetic 40s 1080p video with audio  -> testdata/input.mp4
#   scripts/testdata.sh long     # synthetic 5min 1080p video (~580MB)   -> testdata/input_5min.mp4
#   scripts/testdata.sh noaudio  # synthetic 20s 1080p video, no audio   -> testdata/input_noaudio.mp4
#   scripts/testdata.sh bbb      # Big Buck Bunny 720p, ~10min (~400MB)  -> testdata/bbb/big_buck_bunny_720p.mp4
set -euo pipefail

cd "$(dirname "$0")/.."
mkdir -p testdata

synthetic() {
  local out=$1 duration=$2
  echo "generating $out (${duration}s, 1920x1080, with audio)"
  ffmpeg -y -hide_banner -loglevel error \
    -f lavfi -i "testsrc2=size=1920x1080:rate=30:duration=${duration}" \
    -f lavfi -i "sine=frequency=440:duration=${duration}" \
    -c:v libx264 -preset ultrafast -pix_fmt yuv420p -c:a aac -shortest \
    "$out"
}

case "${1:-short}" in
short)
  synthetic testdata/input.mp4 40
  ;;
long)
  synthetic testdata/input_5min.mp4 300
  ;;
noaudio)
  echo "generating testdata/input_noaudio.mp4 (20s, 1920x1080, no audio)"
  ffmpeg -y -hide_banner -loglevel error \
    -f lavfi -i "testsrc2=size=1920x1080:rate=30:duration=20" \
    -c:v libx264 -preset ultrafast -pix_fmt yuv420p \
    testdata/input_noaudio.mp4
  ;;
bbb)
  # Blender only publishes the 720p H.264 cut as a zipped .mov; remux it to mp4 without re-encoding
  # and drop the timecode track carried over from the .mov.
  mkdir -p testdata/bbb
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  echo "downloading Big Buck Bunny 720p (~400MB)"
  curl -fL --retry 3 -o "$tmp/bbb.zip" \
    https://download.blender.org/peach/bigbuckbunny_movies/big_buck_bunny_720p_h264.mov.zip
  unzip -q -o "$tmp/bbb.zip" -d "$tmp"
  ffmpeg -y -hide_banner -loglevel error -i "$tmp/big_buck_bunny_720p_h264.mov" \
    -map 0:v:0 -map 0:a:0 -c copy -write_tmcd 0 -movflags +faststart \
    testdata/bbb/big_buck_bunny_720p.mp4
  ;;
*)
  echo "unknown option: $1 (expected: short | long | noaudio | bbb)" >&2
  exit 1
  ;;
esac

echo "done:"
find testdata -name '*.mp4' -exec ls -lh {} \;
