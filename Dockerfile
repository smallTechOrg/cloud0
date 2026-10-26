# cloud0 — one container for local use and Cloud Run (not part of the Phase 1 gates).
# Build:   docker build -t cloud0 .
# Migrate: docker run --rm --env-file .env -e CLOUD0_DB_PATH=/data/cloud0.db -v cloud0-data:/data cloud0 migrate up
# Run:     docker run --rm --env-file .env -e CLOUD0_DB_PATH=/data/cloud0.db -v cloud0-data:/data -p 8087:8087 cloud0 serve
# (Mount the service-account key read-only and point GOOGLE_APPLICATION_CREDENTIALS at it; on Cloud Run leave it unset.)

# --- 1. frontend: Next.js static export -------------------------------------
FROM node:22-alpine AS web
WORKDIR /src/web
RUN corepack enable && corepack prepare pnpm@11.7.0 --activate
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# --- 2. backend: static Go binary with the UI embedded ----------------------
FROM golang:1.23-alpine AS go
WORKDIR /src
ENV GO111MODULE=on GOTOOLCHAIN=local CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/out/ ./internal/webui/dist/
RUN go build -trimpath -ldflags="-s -w" -o /out/cloud0 ./cmd/cloud0 \
 && mkdir -p /out/data && chown 65532:65532 /out/data

# --- 3. runtime ---------------------------------------------------------------
FROM gcr.io/distroless/static:nonroot
COPY --from=go /out/cloud0 /cloud0
COPY --from=go --chown=65532:65532 /out/data /data
ENV CLOUD0_BIND=0.0.0.0 CLOUD0_PORT=8087 CLOUD0_DB_PATH=/data/cloud0.db
EXPOSE 8087
VOLUME ["/data"]
USER nonroot
ENTRYPOINT ["/cloud0"]
CMD ["serve"]
