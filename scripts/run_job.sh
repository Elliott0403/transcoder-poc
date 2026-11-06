#!/usr/bin/env bash
# Create a job via POST /jobs, then poll GET /jobs/{jobID} until it finishes or fails.
#
# Usage:
#   scripts/run_job.sh <vod|clip> <input path or URL> <outputFolder> [host]
#
# Example:
#   scripts/run_job.sh vod "$PWD/testdata/input.mp4" vod-001
set -euo pipefail

type=${1:?type is required (vod|clip)}
input=${2:?input is required}
folder=${3:?outputFolder is required}
host=${4:-localhost:8080}
interval=${INTERVAL:-2}

# Relative local paths are resolved because the server may run in another directory.
if [[ "$input" != *://* && -e "$input" ]]; then
  input=$(cd "$(dirname "$input")" && pwd)/$(basename "$input")
fi

echo "==> POST /jobs"
resp=$(curl -sS -X POST "$host/jobs" -H 'Content-Type: application/json' \
  -d "{\"type\":\"$type\",\"inputURL\":\"$input\",\"outputFolder\":\"$folder\"}")
echo "$resp"

job_id=$(sed -nE 's/.*"jobID":([0-9]+).*/\1/p' <<<"$resp")
if [[ -z "$job_id" ]]; then
  echo "failed to create job" >&2
  exit 1
fi

echo "==> GET /jobs/$job_id (every ${interval}s)"
start=$(date +%s)
while true; do
  status=$(curl -sS "$host/jobs/$job_id" || true)
  # Drop the urls object to keep each progress line short.
  echo "$(date +%H:%M:%S) $(sed -E 's/,"urls":\{[^}]*\}//' <<<"$status")"
  if grep -qE '"state":"(finished|failed)"' <<<"$status"; then
    break
  fi
  sleep "$interval"
done

echo "==> elapsed $(( $(date +%s) - start ))s"
echo "$status"
grep -q '"state":"finished"' <<<"$status"
