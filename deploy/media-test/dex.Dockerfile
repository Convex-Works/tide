# Config is baked in, not bind-mounted: CI drives compose from inside a
# container, where host bind mounts do not resolve on the daemon's filesystem.
FROM ghcr.io/dexidp/dex:v2.43.1

COPY deploy/media-test/dex.yaml /etc/dex/config.yaml
