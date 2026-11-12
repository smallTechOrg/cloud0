#!/busybox/sh
# Cloud Run entrypoint (Dockerfile target `cloudrun`). The instance disk starts empty, so: restore the SQLite file
# from its Litestream replica (skipped on the very first start, when there is none yet), apply migrations, then
# serve while Litestream streams every change back. Litestream runs as PID 1: on SIGTERM it stops cloud0, pushes
# the last changes and exits.
set -eu
: "${CLOUD0_LITESTREAM_URL:?set CLOUD0_LITESTREAM_URL, for example gcs://my-bucket/cloud0.db}"
/usr/local/bin/litestream restore -config /etc/litestream.yml -if-db-not-exists -if-replica-exists "$CLOUD0_DB_PATH"
/cloud0 migrate up
exec /usr/local/bin/litestream replicate -config /etc/litestream.yml -exec "/cloud0 serve"
