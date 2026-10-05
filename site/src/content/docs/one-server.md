# One server

Goal: tide on one Linux server with a public IPv4 address, behind Caddy for HTTPS.

Requires: the server, a DNS name pointing at it (`APP_HOST`), and Docker with `buildx` on the machine you build on.

## 1. Build

From the repository root:

```sh
docker buildx build --platform linux/amd64 --output type=local,dest=out .
```

`out/tide` is the static binary. Copy it to `/usr/local/bin/tide` on the server.

## 2. Open the firewall

| Port       | For                               |
| ---------- | --------------------------------- |
| `80/tcp`   | Caddy's certificates              |
| `443/tcp`  | the app, its API and signaling    |
| `7882/udp` | audio and video                   |
| `7881/tcp` | media for networks that block UDP |

Media does not pass through Caddy: browsers send it straight to the server.

## 3. Put Caddy in front

`/etc/caddy/Caddyfile`:

```
APP_HOST
reverse_proxy 127.0.0.1:8080
```

Caddy gets the certificate and passes WebSockets through. Browsers need HTTPS for the camera and microphone.

## 4. Run tide

`/etc/tide.env`:

```sh
TIDE_BASE_URL=https://APP_HOST
TIDE_ADDR=127.0.0.1:8080
TIDE_TRUSTED_PROXIES=127.0.0.1
```

`/etc/systemd/system/tide.service`:

```ini
[Unit]
Description=tide
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/tide.env
ExecStart=/usr/local/bin/tide
DynamicUser=yes
StateDirectory=tide
WorkingDirectory=/var/lib/tide
Restart=on-failure
TimeoutStopSec=20

[Install]
WantedBy=multi-user.target
```

```sh
systemctl enable --now tide
journalctl -u tide | grep 'tide:'   # the mode line, e.g. "tide: anonymous, …"
```

That is an anonymous meeting server: anyone who opens `https://APP_HOST` can create a room.

tide finds its public address itself. Set `TIDE_MEDIA_NODE_IP` to the server's public IPv4 address when that address is on a load balancer or NAT, or the server can't reach the internet. A set address replaces the server's own, which matters for recording (step 6).

## 5. Sign-in (optional)

Register an OIDC client with the redirect URI `https://APP_HOST/api/auth/callback` (see [Prepare](/docs/prepare#2-oidc-client)), then add to `/etc/tide.env` and `chmod 600` it:

```sh
TIDE_OIDC_ISSUER=<OIDC_ISSUER>
TIDE_OIDC_CLIENT_ID=<OIDC_CLIENT_ID>
TIDE_OIDC_CLIENT_SECRET=<OIDC_CLIENT_SECRET>
# openssl rand -base64 48
TIDE_SESSION_SECRET=<SESSION_SECRET>
TIDE_DB_PATH=/var/lib/tide/tide.db
```

Keep comments on lines of their own: systemd makes a trailing `# …` part of the value.

Rooms now persist. Back up the database ([Operate](/docs/operate#back-up-the-database)).

## 6. Recording (optional)

The recorder runs in Docker, so run tide in Docker Compose beside it instead of under systemd. Keep Caddy and the firewall as they are. Create a bucket and key as in [Prepare](/docs/prepare#3-bucket).

`.env` (`chmod 600`):

```sh
TIDE_BASE_URL=https://APP_HOST
TIDE_ADDR=127.0.0.1:8080
TIDE_TRUSTED_PROXIES=127.0.0.1
TIDE_DB_PATH=/data/tide.db
TIDE_SESSION_SECRET=<SESSION_SECRET>
TIDE_OIDC_ISSUER=<OIDC_ISSUER>
TIDE_OIDC_CLIENT_ID=<OIDC_CLIENT_ID>
TIDE_OIDC_CLIENT_SECRET=<OIDC_CLIENT_SECRET>
TIDE_S3_ENDPOINT=<S3_ENDPOINT>
TIDE_S3_BUCKET=<S3_BUCKET>
TIDE_S3_REGION=<S3_REGION>
TIDE_S3_ACCESS_KEY=<S3_ACCESS_KEY>
TIDE_S3_SECRET_KEY=<S3_SECRET_KEY>
# openssl rand -hex 8
TIDE_MEDIA_API_KEY=<MEDIA_API_KEY>
# openssl rand -hex 32, for this and the password
TIDE_MEDIA_API_SECRET=<MEDIA_API_SECRET>
TIDE_RECORDER_REDIS_PASSWORD=<RECORDER_PASSWORD>
TIDE_RECORDER_TEMPLATE_URL=http://127.0.0.1:8080/egress-template
```

`compose.yaml`:

```yaml
services:
  tide:
    image: <REGISTRY>/tide:<VERSION>
    env_file: .env
    network_mode: host
    volumes:
      - tide-data:/data
    restart: unless-stopped
    stop_grace_period: 20s

  recorder:
    image: livekit/egress:v1.13.0
    network_mode: service:tide
    entrypoint:
      - /bin/sh
      - -c
      - >-
        until wget -q -O /dev/null http://127.0.0.1:8080/healthz; do sleep 1; done;
        exec /entrypoint.sh
    environment:
      LIVEKIT_API_KEY: ${TIDE_MEDIA_API_KEY}
      LIVEKIT_API_SECRET: ${TIDE_MEDIA_API_SECRET}
      EGRESS_CONFIG_BODY: |
        ws_url: ws://127.0.0.1:7880
        redis:
          address: 127.0.0.1:6379
          password: "${TIDE_RECORDER_REDIS_PASSWORD}"
        health_port: 8081
    cap_add: [SYS_ADMIN]
    security_opt: [seccomp:unconfined]
    shm_size: 1gb
    tmpfs:
      - /recordings:mode=1777
    restart: unless-stopped

volumes:
  tide-data:
```

The recorder shares tide's network, so it reaches tide's media server and coordination endpoint on loopback, where nothing else can. tide uses the host's network because media is UDP. The recorder joins meetings like a browser, through the address tide advertises: leave `TIDE_MEDIA_NODE_IP` unset, or set it only to an address on the server's own interface, or recordings can't connect. Build the image as in [Install](/docs/install#1-build-the-image), then `docker compose up -d`.

## Done when

- [ ] `curl -fsS https://APP_HOST/healthz` prints `ok`
- [ ] `curl -s -o /dev/null -w '%{http_code}\n' https://APP_HOST/rtc/validate` prints `401`: signaling reaches the media server
- [ ] tide's log shows the mode you meant and no `refusing to start`
- [ ] checks 4 and 5 of [Verify](/docs/verify) pass
