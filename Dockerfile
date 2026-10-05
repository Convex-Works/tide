# syntax=docker/dockerfile:1.7

FROM node:22.17.0-alpine3.22 AS web-builder
WORKDIR /src/web
ARG VITE_TIDE_TEST=false
ENV VITE_TIDE_TEST=${VITE_TIDE_TEST}

# The vendored cuelume tarball is a package.json file dependency, so npm ci
# needs it at the same relative path as the lockfile records.
COPY web/package.json web/package-lock.json ./
COPY web/vendor ./vendor
RUN npm ci

COPY web/ ./
RUN npx vite build

FROM golang:1.26.5-alpine3.23 AS go-builder
WORKDIR /src/server

COPY server/go.mod server/go.sum ./
# go.mod replaces the moil SDK with its vendored copy (ARCHITECTURE.md §17),
# which module resolution needs before anything is downloaded.
COPY server/third_party ./third_party
RUN go mod download

COPY server/ ./
# Go embed paths cannot cross module boundaries. Match the Makefile staging
# path before compiling with the embed build tag.
COPY --from=web-builder /src/web/build ./web/build
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -tags embed \
    -ldflags="-s -w" \
    -o /out/tide \
    ./cmd/tide

FROM alpine:3.22.5 AS runtime-files
RUN apk add --no-cache ca-certificates tzdata \
    && mkdir -p /runtime/data \
    && chown 65532:65532 /runtime/data

FROM scratch
COPY --from=runtime-files /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=runtime-files /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=runtime-files --chown=65532:65532 /runtime/data /data
COPY --from=go-builder /out/tide /tide

# /data must be backed by a writable volume. Override TIDE_DB_PATH if the
# volume is mounted elsewhere; SQLite also creates -wal and -shm sidecars.
ENV TIDE_ADDR=:8080 \
    TIDE_DB_PATH=/data/tide.db
VOLUME ["/data"]
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/tide"]
