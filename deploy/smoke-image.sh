#!/usr/bin/env bash
# Proves the Cloud Run image keeps its data across instances, without GCP: start it with a local Litestream replica,
# write a week note, stop it with SIGTERM (as Cloud Run does), start a fresh container on the same replica and read
# the note back. Run by CI on every PR and before every deploy.
#   bash deploy/smoke-image.sh cloud0:ci
set -euo pipefail
IMAGE="${1:?usage: $0 <image>}"
NAME="cloud0-smoke-$$"
PORT="${PORT:-8087}"
URL="http://127.0.0.1:${PORT}/api/v1"
WEEK=2026-W20
NOTE="smoke test $(date -u +%Y%m%dT%H%M%SZ)"
REPLICA="$(mktemp -d)"
chmod 777 "$REPLICA"  # the image runs as uid 65532
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true' EXIT

start() {
  # Throwaway config: serve only needs it to be valid; with no credentials the GCP sources report errors, never crash.
  docker run -d --name "$NAME" -p "${PORT}:8087" -v "${REPLICA}:/replica" \
    -e CLOUD0_LITESTREAM_URL=file:///replica/cloud0.db \
    -e CLOUD0_GCP_PROJECTS=ci-project -e CLOUD0_BIGQUERY_PROJECT=ci-project \
    -e CLOUD0_BILLING_EXPORT_TABLE=ci-project.ci_dataset.ci_table \
    "$IMAGE" >/dev/null
  for _ in $(seq 1 60); do
    if curl -fsS -m 5 -o /dev/null "${URL}/health"; then return 0; fi
    if [ "$(docker inspect -f '{{.State.Running}}' "$NAME")" != true ]; then break; fi
    sleep 1
  done
  docker logs "$NAME"
  echo "cloud0 did not become healthy" >&2
  exit 1
}

stop() {
  docker stop -t 10 "$NAME" >/dev/null
  echo "--- last log lines ---"
  docker logs "$NAME" 2>&1 | tail -5
  docker rm "$NAME" >/dev/null
}

echo "1. first start: no replica yet, fresh database"
start
curl -fsS -m 10 -X PUT -H 'Content-Type: application/json' \
  -d "{\"body\":\"${NOTE}\"}" "${URL}/annotations/${WEEK}" >/dev/null
echo "   wrote the ${WEEK} note; stopping right away so only the shutdown sync can save it"
stop

echo "2. second start: new container, empty disk, same replica"
start
got="$(curl -fsS -m 10 "${URL}/annotations/${WEEK}")"
stop
if ! printf '%s' "$got" | grep -qF "$NOTE"; then
  echo "the note did not survive the restart; got: $got" >&2
  exit 1
fi
echo "ok: the note survived a restart through the Litestream replica"
