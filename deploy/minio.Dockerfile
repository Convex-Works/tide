# MinIO and its client, built from source at the releases tide pins: MinIO
# no longer publishes images, so nothing can be pulled. For development and
# the test stacks only, whose root credentials are public. The two targets,
# minio and mc, are the two images the compose files name.
#
#   docker build --file deploy/minio.Dockerfile --target minio deploy

FROM golang:1.24-alpine AS source
RUN apk add --no-cache git
ENV CGO_ENABLED=0

FROM source AS build-minio
ARG MINIO_RELEASE=RELEASE.2025-04-22T22-12-26Z
RUN git clone --quiet --depth 1 --branch "$MINIO_RELEASE" https://github.com/minio/minio /src
WORKDIR /src
RUN go build -trimpath -ldflags "-s -w" -o /out/minio .

FROM source AS build-mc
ARG MC_RELEASE=RELEASE.2025-04-16T18-13-26Z
RUN git clone --quiet --depth 1 --branch "$MC_RELEASE" https://github.com/minio/mc /src
WORKDIR /src
RUN go build -trimpath -ldflags "-s -w" -o /out/mc .

# curl for the compose health checks, as the published images had.
FROM alpine:3.21 AS minio
RUN apk add --no-cache curl
COPY --from=build-minio /out/minio /usr/bin/minio
EXPOSE 9000 9001
ENTRYPOINT ["minio"]

FROM alpine:3.21 AS mc
COPY --from=build-mc /out/mc /usr/bin/mc
ENTRYPOINT ["mc"]
