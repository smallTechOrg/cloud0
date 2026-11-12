#!/busybox/sh
# Cloud Run entrypoint (Dockerfile target `cloudrun`). The instance disk starts empty, so: restore the SQLite file
# from its Litestream replica (skipped on the very first start, when there is none yet), apply migrations, then
# serve while Litestream streams every change back.
set -eu
: "${CLOUD0_LITESTREAM_URL:?set CLOUD0_LITESTREAM_URL, for example gcs://my-bucket/cloud0.db}"
/usr/local/bin/litestream restore -config /etc/litestream.yml -if-db-not-exists -if-replica-exists "$CLOUD0_DB_PATH"
/cloud0 migrate up

/usr/local/bin/litestream replicate -config /etc/litestream.yml -exec "/cloud0 serve" &
pid=$!
# On SIGTERM, give Litestream's 1-second sync two ticks to push the latest writes before it stops cloud0 and does
# its final sync. Litestream's exit-time sync alone missed a write made just before SIGTERM about once in 16 CI runs.
# Cloud Run allows 10 s after SIGTERM.
trap 'sleep 2; kill -TERM "$pid" 2>/dev/null || true' TERM INT
set +e
wait "$pid"; status=$?
# A trapped signal interrupts the first wait; wait again for Litestream to finish its shutdown.
if kill -0 "$pid" 2>/dev/null; then wait "$pid"; status=$?; fi
exit "$status"
