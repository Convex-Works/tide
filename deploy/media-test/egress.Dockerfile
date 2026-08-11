# Config is baked in, not bind-mounted: CI drives compose from inside a
# container, where host bind mounts do not resolve on the daemon's filesystem.
FROM livekit/egress:v1.13.0

COPY deploy/media-test/egress.yaml /etc/egress.yaml
