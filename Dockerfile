# syntax=docker/dockerfile:1.7

FROM node:22.17.0-alpine3.22 AS web-builder
WORKDIR /src/web
ARG VITE_KLISI_TEST=false
ENV VITE_KLISI_TEST=${VITE_KLISI_TEST}

# The vendored cuelume tarball is a package.json file dependency, so npm ci
# needs it at the same relative path as the lockfile records.
COPY web/package.json web/package-lock.json ./
COPY web/vendor ./vendor
RUN npm ci

COPY web/ ./
RUN npx vite build

FROM golang:1.24.13-alpine3.22 AS go-builder
WORKDIR /src/server

COPY server/go.mod server/go.sum ./
RUN go mod download

COPY server/ ./
# Go embed paths cannot cross module boundaries. Match the Makefile staging
# path before compiling with the embed build tag.
COPY --from=web-builder /src/web/build ./web/build
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -tags embed \
    -ldflags="-s -w" \
    -o /out/klisi \
    ./cmd/klisi

FROM alpine:3.22.5 AS runtime-files
RUN apk add --no-cache ca-certificates tzdata \
    && mkdir -p /runtime/data \
    && chown 65532:65532 /runtime/data

FROM scratch
COPY --from=runtime-files /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=runtime-files /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=runtime-files --chown=65532:65532 /runtime/data /data
COPY --from=go-builder /out/klisi /klisi

# /data must be backed by a writable volume. Override KLISI_DB_PATH if the
# volume is mounted elsewhere; SQLite also creates -wal and -shm sidecars.
ENV KLISI_ADDR=:8080 \
    KLISI_DB_PATH=/data/klisi.db
VOLUME ["/data"]
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/klisi"]
