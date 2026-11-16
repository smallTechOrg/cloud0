#!/usr/bin/env bash
# Smoke test of the deployable image against a real Postgres, as on Cloud Run: run `migrate up` like the
# cloud0-migrate job, start `serve` with CLOUD0_PASSWORD, check the sign-in flow (an API call without the password
# gets 401, a page redirects to /login, signing in gives a working session), write a week note, replace the
# container and read the note back. Run by CI on every PR and before every deploy.
#   bash deploy/smoke-image.sh cloud0:ci 'postgres://postgres:postgres@127.0.0.1:5432/cloud0?sslmode=disable'
set -euo pipefail
IMAGE="${1:?usage: $0 <image> <postgres url reachable from the host network>}"
DATABASE_URL="${2:?usage: $0 <image> <postgres url reachable from the host network>}"
NAME="cloud0-smoke-$$"
PORT="${PORT:-8087}"
SITE="http://127.0.0.1:${PORT}"
URL="${SITE}/api/v1"
WEEK=2026-W20
NOTE="smoke test $(date -u +%Y%m%dT%H%M%SZ)"
PASSWORD="smoke-$$-$RANDOM"
AUTH=":${PASSWORD}"  # HTTP Basic for scripts: there is no username
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true' EXIT

# Throwaway config: serve only needs it to be valid; with no credentials the GCP sources report errors, never crash.
ENV=(-e CLOUD0_DATABASE_URL="$DATABASE_URL" -e CLOUD0_PORT="$PORT" -e CLOUD0_PASSWORD="$PASSWORD"
     -e CLOUD0_GCP_PROJECTS=ci-project -e CLOUD0_BIGQUERY_PROJECT=ci-project
     -e CLOUD0_BILLING_EXPORT_TABLE=ci-project.ci_dataset.ci_table)

fail() { echo "$1" >&2; exit 1; }

start() {
  docker run -d --name "$NAME" --network host "${ENV[@]}" "$IMAGE" serve >/dev/null
  for _ in $(seq 1 60); do
    if curl -fs -m 5 -u "$AUTH" -o /dev/null "${URL}/health"; then return 0; fi  # silent: refused until cloud0 listens
    if [ "$(docker inspect -f '{{.State.Running}}' "$NAME")" != true ]; then break; fi
    sleep 1
  done
  docker logs "$NAME"
  fail "cloud0 did not become healthy"
}

stop() { docker stop -t 10 "$NAME" >/dev/null; docker rm "$NAME" >/dev/null; }

echo "1. migrate up (as the cloud0-migrate job does)"
docker run --rm --network host "${ENV[@]}" "$IMAGE" migrate up

echo "2. serve: sign-in flow"
start
code="$(curl -s -m 10 -o /dev/null -w '%{http_code}' "${URL}/health")"
[ "$code" = 401 ] || fail "an API call without the password got $code, want 401"
page="$(curl -s -m 10 -o /dev/null -w '%{http_code} %{redirect_url}' "${SITE}/")"
[ "$page" = "303 ${SITE}/login?next=%2F" ] || fail "a page without the password got '$page', want 303 to /login"
curl -fs -m 10 "${SITE}/login" | grep -q 'Sign in' || fail "the /login page did not render"
JAR="$(mktemp)"
curl -fsS -m 10 -c "$JAR" -X POST -d "{\"password\":\"${PASSWORD}\"}" "${URL}/login" >/dev/null
code="$(curl -s -m 10 -b "$JAR" -o /dev/null -w '%{http_code}' "${URL}/health")"
[ "$code" = 200 ] || fail "the session from /api/v1/login got $code, want 200"
echo "   API 401, page -> /login, signing in gives a working session"

echo "3. a week note survives replacing the container"
curl -fsS -m 10 -u "$AUTH" -X PUT -H 'Content-Type: application/json' \
  -d "{\"body\":\"${NOTE}\"}" "${URL}/annotations/${WEEK}" >/dev/null
stop
start
got="$(curl -sS -m 10 -u "$AUTH" "${URL}/annotations/${WEEK}" || true)"
stop
printf '%s' "$got" | grep -qF "$NOTE" || fail "the note did not survive; got: $got"
echo "ok: migrations, sign-in and data in Postgres all work from the image"
