# Deployment

tide is one executable. It serves the web app and API, signs hosts in (or
doesn't), and runs the media server that carries every meeting's audio and
video. A deployment without recording is that one process. Recording adds the
recorder (headless Chrome and GStreamer) and the Redis it and tide's media
server share, beside tide, plus an S3-compatible bucket it writes to.

This guide covers three ways to run it: [the binary on a
server](#run-the-binary-on-a-server) behind a TLS proxy, [Docker
Compose](#run-with-docker-compose) with the recorder, and [the Kubernetes
reference base](#kubernetes). The design behind it is
[Architecture §2.1](ARCHITECTURE.md#21-one-binary-modes-and-the-embedded-media-server).

## Choose a mode

Configuration decides what a deployment is:

| Configuration                           | What tide is                                              | Processes               | You bring                        |
| --------------------------------------- | --------------------------------------------------------- | ----------------------- | -------------------------------- |
| nothing                                 | **Anonymous**: anyone creates rooms; rooms live in memory | tide                    | TLS in front                     |
| `TIDE_OIDC_ISSUER`                      | **Signed in**: hosts sign in, rooms persist in SQLite     | tide                    | TLS, an OIDC issuer, a disk      |
| `TIDE_OIDC_ISSUER` + `TIDE_S3_ENDPOINT` | **Signed in with recording**                              | tide + recorder + Redis | TLS, an OIDC issuer, a disk, S3  |
| … + `TIDE_TRANSCRIPTS=true`             | **… with transcripts** made on hosts' own computers       | tide + recorder + Redis | as above; hosts run the moil app |

tide refuses to start rather than guess: S3 without sign-in, transcripts
without recording, user or administrator groups without sign-in, a missing or
weak secret, or an unparseable address or port stop it with a message naming
every problem. A variable set to the empty string counts as unset. tide logs
its mode in one line at startup, for example `tide: anonymous, media server
embedded (node IP discovered), recording off`.

An anonymous deployment is an open meeting server: anyone who can reach it can
hold meetings on it. Rooms are deleted 24 hours after they were created or last
joined, and all of them when tide restarts. At most 10,000 rooms exist at
once; past that, creating one answers 503 until old ones are swept. Configure
sign-in to know who hosts.

## What tide listens on

| Address          | Carries                                               | Expose                              |
| ---------------- | ----------------------------------------------------- | ----------------------------------- |
| `:8080/tcp`      | the app, the API, lobby streams, and `/rtc` signaling | through a TLS proxy or ingress only |
| `:7882/udp`      | all meeting media, multiplexed on one port            | publicly, directly                  |
| `:7881/tcp`      | media for networks that block UDP                     | publicly, directly                  |
| `127.0.0.1:7880` | the media server's own API and signaling              | never; tide forwards `/rtc` to it   |

With recording, tide also connects out to Redis at `TIDE_RECORDER_REDIS_ADDR`
([Redis, with recording](#redis-with-recording)); it listens on nothing more.

Browsers only give a page the camera and microphone over HTTPS, so tide
always sits behind something that terminates TLS. Signaling rides the same
origin (`wss://<host>/rtc`), so one certificate and one route cover everything
but media; the proxy must pass WebSocket upgrades there. tide takes only GET
requests without a body on `/rtc`, which is all signaling ever sends. Media does not pass through the proxy: browsers send it straight to
the address the media server advertises, on 7882/udp or 7881/tcp. Both are
encrypted by WebRTC; neither is HTTP.

### The media address

`TIDE_MEDIA_NODE_IP` is the address the media server advertises to browsers.
Unset, tide uses 127.0.0.1 when `TIDE_BASE_URL` is a loopback address (a
laptop), and otherwise discovers its public address over STUN at startup. Set
it when discovery would be wrong or impossible:

- the media path is a load balancer or a forwarded port on another address;
- the server has no outbound internet access for STUN, or its outbound
  traffic leaves through another address (a NAT gateway);
- the meeting server is for a private network, where the LAN address is the
  right one.

A DNS name is not a substitute; give an IPv4 address.

A discovered address is advertised _beside_ the machine's own interface
addresses; a configured one _replaces_ them. That matters with recording: the
recorder runs in tide's network and connects to media like a browser does.
With a discovered address it uses tide's own interface address; with
`TIDE_MEDIA_NODE_IP` set it must reach that address, which works when the
address is on one of the machine's interfaces (a VM with its public IP on
`eth0`) and otherwise needs the network to route it back to tide (hairpin).
With recording, prefer discovery unless the address is local.

Change the ports with `TIDE_MEDIA_UDP_PORT` and `TIDE_MEDIA_TCP_PORT`. Two
tides on one host must differ in everything they listen on: `TIDE_ADDR`,
`TIDE_MEDIA_UDP_PORT`, `TIDE_MEDIA_TCP_PORT` and `TIDE_MEDIA_API_PORT` (the
loopback API, 7880). With recording each needs a Redis of its own: two media
servers on one Redis take each other for nodes of one cluster.

### Redis, with recording

The recorder takes its jobs over Redis, so with recording on, tide's media
server and the recorder share one, at `TIDE_RECORDER_REDIS_ADDR` (default
`127.0.0.1:6379`) with the password `TIDE_RECORDER_REDIS_PASSWORD` as
`requirepass`. Once the media server uses Redis, its signal relay goes over it
too. Redis holds nothing durable: run it without persistence, bound to
loopback or a private network only, since recording jobs carry the S3
credentials. tide waits up to 30 seconds for it at startup, then refuses to
start, naming the address; it also refuses a Redis that answers without a
password, or rejects this one. If Redis restarts, signaling pauses: joins wait,
clients reconnect on their own, and media keeps flowing; a recording starting
or stopping in that moment may fail. Without recording, tide uses no Redis.

## Run the binary on a server

The quickest real deployment: a Linux VM with a public IPv4 address, tide
under systemd, and Caddy in front for HTTPS.

Build the binary for the server's platform. `make build` writes `bin/tide`
for the machine it runs on; from anywhere with Docker:

```sh
docker buildx build --platform linux/amd64 --output type=local,dest=out .
# out/tide is the static binary
```

Copy it to `/usr/local/bin/tide`. Open 80/tcp and 443/tcp (Caddy and its
certificates), 7881/tcp and 7882/udp (media) in the server's firewall.

Caddy's whole configuration, in `/etc/caddy/Caddyfile`:

```
meet.example.com
reverse_proxy 127.0.0.1:8080
```

Caddy gets and renews the certificate, and passes WebSocket upgrades and
streams through as they are.

### Anonymous

`/etc/tide.env`:

```sh
TIDE_BASE_URL=https://meet.example.com
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

`systemctl enable --now tide`, then open `https://meet.example.com`.
`TIDE_TRUSTED_PROXIES` lets the per-client rate limits see the client's
address rather than Caddy's.

### Signed in

Register an OIDC client ([Configure OIDC](#configure-oidc)) and add to
`/etc/tide.env`, which now holds secrets (`chmod 600`):

```sh
TIDE_OIDC_ISSUER=https://id.example.com
TIDE_OIDC_CLIENT_ID=tide
# The issuer's client secret, 16 characters or more.
TIDE_OIDC_CLIENT_SECRET=…
# openssl rand -base64 48
TIDE_SESSION_SECRET=…
TIDE_DB_PATH=/var/lib/tide/tide.db
```

systemd reads only whole-line comments in an environment file: a `# …` after
a value becomes part of it.

Rooms now persist in SQLite under the service's state directory; back it up
([Back up state](#back-up-state)). Recording needs the recorder, which runs in
Docker, and Redis: the next section runs both with Docker Compose. To keep
tide under systemd instead, Redis can come from the distribution, configured
like this and listening where tide looks by default:

```
bind 127.0.0.1
port 6379
requirepass <TIDE_RECORDER_REDIS_PASSWORD>
save ""
appendonly no
```

## Run with Docker Compose

This runs the signed-in deployment with recording on one Linux host: tide,
Redis and the recorder. tide uses the host's network, because media is UDP and
Docker's port publishing adds a proxy and a NAT that WebRTC doesn't need;
Redis and the recorder share it, so all three meet on loopback, where nobody
else can.

`.env`, beside `compose.yaml` (`chmod 600`; Compose reads it both for tide's
environment and for the recorder's configuration below):

```sh
TIDE_BASE_URL=https://meet.example.com
TIDE_ADDR=127.0.0.1:8080
TIDE_TRUSTED_PROXIES=127.0.0.1
TIDE_DB_PATH=/data/tide.db
# openssl rand -base64 48
TIDE_SESSION_SECRET=…
TIDE_OIDC_ISSUER=https://id.example.com
TIDE_OIDC_CLIENT_ID=tide
TIDE_OIDC_CLIENT_SECRET=…
TIDE_S3_ENDPOINT=https://s3.example.com
TIDE_S3_BUCKET=tide-recordings
TIDE_S3_REGION=us-east-1
TIDE_S3_ACCESS_KEY=…
TIDE_S3_SECRET_KEY=…
# openssl rand -hex 8
TIDE_MEDIA_API_KEY=…
# openssl rand -hex 32, for this and the password
TIDE_MEDIA_API_SECRET=…
TIDE_RECORDER_REDIS_PASSWORD=…
TIDE_RECORDER_TEMPLATE_URL=http://127.0.0.1:8080/egress-template
```

`compose.yaml`:

```yaml
services:
  tide:
    image: registry.example.com/tide:1.0.0
    env_file: .env
    network_mode: host
    volumes:
      - tide-data:/data
    restart: unless-stopped
    stop_grace_period: 20s

  # Redis for tide's media server and the recorder, on the host's loopback.
  # The password goes to Redis through a file, not its command line.
  redis:
    image: redis:7.4.8-alpine3.21
    network_mode: host
    user: "999:1000"
    environment:
      REDISCLI_AUTH: ${TIDE_RECORDER_REDIS_PASSWORD}
    command:
      - sh
      - -ec
      - |
        umask 077
        printf 'requirepass %s\n' "$$REDISCLI_AUTH" > /tmp/redis.conf
        exec redis-server /tmp/redis.conf --bind 127.0.0.1 --port 6379 --save '' --appendonly no
    healthcheck:
      test: ["CMD-SHELL", "redis-cli -h 127.0.0.1 ping | grep -q PONG"]
      interval: 2s
      retries: 30
    restart: unless-stopped

  recorder:
    image: livekit/egress:v1.13.0
    network_mode: service:tide
    # The recorder checks Redis once at startup and exits without it.
    depends_on:
      redis:
        condition: service_healthy
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

Put Caddy in front exactly as for the binary. Leave out Redis, the recorder,
the `TIDE_S3_*`, media key and Redis password lines for a deployment without
recording, and the OIDC and session lines too for an anonymous one. tide finds
Redis at its default address and waits for it at startup. tide's
image runs as a non-root user and keeps its database at `/data/tide.db`.

Leave `TIDE_MEDIA_NODE_IP` unset unless discovery can't find the right
address: unset, the recorder connects to media through tide's own address.
Set, it must be one of the host's own addresses or reach the host back, or
recordings can't connect ([The media address](#the-media-address)).

## Kubernetes

`deploy/k8s/` is a reference kustomize base: one tide Deployment (signed in,
media server inside, no recording), its Service and a PVC for SQLite.
`deploy/k8s/recording/` is a kustomize component that turns recording on: it
gives tide the object store and runs Redis and the recorder as sidecars in
tide's pod. Redis is a native sidecar (an init container with
`restartPolicy: Always`, Kubernetes 1.29 or later), so it is up before tide
and the recorder start; it listens on the pod's loopback only.
Neither creates an Ingress, an object store, an OIDC issuer, a namespace or
Secrets, and the base deliberately exposes no media path until an overlay
selects one. Build an overlay for every installation; every line marked
`# OVERLAY:` needs a value.

### Hard constraints

- Run exactly one tide replica with the `Recreate` strategy. SQLite is its
  durable store, and lobby requests, rate limits and every meeting live in
  that process.
- Mount one durable, writable volume at `TIDE_DB_PATH`. The image defaults to
  `/data/tide.db` and also writes SQLite `-wal` and `-shm` sidecars there.
  The base mounts its claim in every mode: an anonymous tide writes nothing to
  it, and turning sign-in on later needs no migration. Give it a storage class
  either way.
- The recorder needs `SYS_ADMIN`, an unconfined seccomp profile, about 1 GiB
  of memory-backed `/dev/shm` and several CPU cores for Chrome and GStreamer.
  With the recording component, tide's pod therefore violates Pod Security
  `baseline`. Run tide in a namespace of its own with an explicit exception;
  do not relax a namespace shared with unrelated workloads.
- Media path (b) below, host ports or host networking, also violates
  `baseline`.

### Secrets

| Secret          | Keys                                               | Needed                       |
| --------------- | -------------------------------------------------- | ---------------------------- |
| `tide-secrets`  | `session-secret`, `oidc-client-secret`             | always                       |
| `media-secrets` | `api-key`, `api-secret`, `recorder-redis-password` | with the recording component |
| `s3-secrets`    | `access-key`, `secret-key`                         | with the recording component |

The base passes `session-secret` in every mode, and tide checks a session
secret whenever it is set, so it is at least 32 characters even for an
anonymous deployment: `openssl rand -base64 48`. An anonymous deployment
doesn't read `oidc-client-secret`; any value will do. Generate the media key
with `openssl rand -hex 8` (letters, digits, `-` and `_` only), and the API
secret and the Redis password with `openssl rand -hex 32`: the recorder's
configuration embeds the password in YAML, so keep it to hex.

### Overlay

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: tide
resources:
  - ../../k8s
  - ingress.yaml
components:
  - ../../k8s/recording # leave out for no recording
images:
  - name: registry.example.com/tide
    newName: <registry>/tide
    newTag: <version>
patches:
  - path: tide.yaml # the OVERLAY values, and the media path
```

The ingress has one host and one route, to the `tide` Service on 8080. It
must pass WebSocket upgrades (signaling is a GET with `Upgrade` on `/rtc`, and
`/moil/v1/connect` with transcripts on) and keep long-lived connections: with
ingress-nginx, set `proxy-read-timeout` and `proxy-send-timeout` to `3600`.

Render and check an overlay before applying it:

```sh
kubectl kustomize deploy/overlays/production \
  | kubeconform -kubernetes-version 1.33.1 -strict
kubectl apply -k deploy/overlays/production
```

### Choose the media path

Make this choice before deploying. tide's pod declares 7882/udp and 7881/tcp
but exposes neither.

```
Can the cluster provide a stable public UDP LoadBalancer on one port?
  ├─ yes ─► (a) dedicated media LoadBalancer
  └─ no
      Does one schedulable node have a stable public IPv4 address?
        ├─ yes ─► (b) hostPort or hostNetwork
        └─ no  ─► (c) TCP-only fallback, with a quality cost
```

Only option (a) sets `TIDE_MEDIA_NODE_IP`. With (b) and (c), leave it unset:
tide discovers the node's public address over STUN and advertises the pod's
own address beside it, which is what the recorder sidecar connects to. Set it for (b)
or (c) only when the cluster's outbound traffic leaves through another
address than the node's, and then check a recording. Do not confuse a working
WebSocket with a working media path: test a call from outside the cluster, and
a recording.

**(a) Dedicated UDP LoadBalancer.** Copy the commented `tide-media` Service
from `deploy/k8s/tide.yaml` into the overlay, with the provider's
static-address annotation, and set that address as `TIDE_MEDIA_NODE_IP`. This
keeps media routing declarative and exposes no node, at the cost of a load
balancer and provider-specific UDP behavior (source addresses, health checks,
hairpin). With recording, the sidecar then reaches media only through the load
balancer's address, so the cluster must route it back to the pod (kube-proxy
usually does): verify with a test recording. ICE-TCP on 7881 is meant for
direct exposure, not an HTTP ingress; if clients need it with this option,
expose 7881/tcp as in (b).

**(b) hostPort or hostNetwork.** Pin tide's pod to the node with the public
address (the commented `nodeSelector`), open 7882/udp and 7881/tcp in that
node's firewall, and either uncomment both `hostPort` lines or set
`hostNetwork: true` with `dnsPolicy: ClusterFirstWithHostNet`, never both.
With host networking, the loopback-only ports (tide's 7880, and with
recording Redis's 6379) bind the node's loopback, so keep the node free of
anything else on them.

**(c) TCP only.** When UDP exposure is impossible, expose only 7881/tcp on the
public node. Clients fail the UDP candidate and fall back to ICE-TCP; expect
more latency and freezes on lossy links. TCP 7881 is not TURN/TLS on 443, so
very restrictive networks may still block it.

## Configuration

Every variable is optional: `tide` with an empty environment is an anonymous
deployment on `http://localhost:8080`, and a variable set to the empty string
counts as unset. Each group turns something on, and is then checked as a
whole. Outside `TIDE_DEV_MODE`, a required secret that is missing, too short,
or equal to a value shipped in this repository refuses startup.

| Variable                       | Default                                | Meaning and rule                                                                                                                                                                                                                                                  |
| ------------------------------ | -------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `TIDE_ADDR`                    | `:8080`                                | HTTP listen address. Keep it behind the TLS proxy; `127.0.0.1:8080` when the proxy is on the same host.                                                                                                                                                           |
| `TIDE_BASE_URL`                | `http://localhost:8080`                | The public origin, scheme and port included. Controls redirects, the OIDC callback, secure cookies, the signaling URL browsers get, and the default recorder template URL.                                                                                        |
| `TIDE_SESSION_SECRET`          | generated per start when anonymous     | HMAC key for session cookies. With sign-in: required. Anonymous: optional; unset, sessions end when tide restarts, as rooms do. Whenever set, at least 32 characters.                                                                                             |
| `TIDE_DB_PATH`                 | `./data/tide.db`                       | SQLite file, with sign-in. Anonymous tide keeps rooms in memory and ignores it. The image sets `/data/tide.db`.                                                                                                                                                   |
| `TIDE_OIDC_ISSUER`             | empty: anonymous                       | Turns sign-in on. The exact issuer URL used for discovery and token verification.                                                                                                                                                                                 |
| `TIDE_OIDC_CLIENT_ID`          | `tide`                                 | Registered OIDC client ID.                                                                                                                                                                                                                                        |
| `TIDE_OIDC_CLIENT_SECRET`      | none                                   | With sign-in: required, at least 16 characters.                                                                                                                                                                                                                   |
| `TIDE_USER_GROUPS`             | empty                                  | Comma-separated, case-sensitive OIDC groups allowed to sign in. Empty permits every verified user; administrators are always allowed. Set without `TIDE_OIDC_ISSUER`, tide refuses to start.                                                                      |
| `TIDE_ADMIN_GROUPS`            | empty                                  | Comma-separated, case-sensitive OIDC groups whose members manage every room. Set without `TIDE_OIDC_ISSUER`, tide refuses to start.                                                                                                                               |
| `TIDE_MEDIA_NODE_IP`           | 127.0.0.1 on loopback, else discovered | The IPv4 address advertised for media, replacing the machine's own ([The media address](#the-media-address)). Must parse as an IP.                                                                                                                                |
| `TIDE_MEDIA_UDP_PORT`          | `7882`                                 | Media over UDP, every interface. 1–65535.                                                                                                                                                                                                                         |
| `TIDE_MEDIA_TCP_PORT`          | `7881`                                 | Media over TCP for networks that block UDP. 1–65535.                                                                                                                                                                                                              |
| `TIDE_MEDIA_API_PORT`          | `7880`                                 | The media server's own API and signaling, on 127.0.0.1 only. A recorder in tide's network namespace signals there. Change it, with every other port, to run two tides on one host. 1–65535, not the TCP media port.                                               |
| `TIDE_MEDIA_API_KEY`           | generated per start without recording  | Key the media server signs tokens and webhooks with. Required with recording (the recorder uses it too) or an external media server. Letters, digits, `-` and `_`.                                                                                                |
| `TIDE_MEDIA_API_SECRET`        | generated per start without recording  | The secret for that key. Required with recording or an external media server, at least 32 characters.                                                                                                                                                             |
| `TIDE_MEDIA_URL`               | empty: embedded                        | An external media server as tide reaches it. Set only to keep the pre-1.0 topology ([External media server](#external-media-server)).                                                                                                                             |
| `TIDE_MEDIA_PUBLIC_URL`        | the base URL's origin, `ws`/`wss`      | The signaling URL browsers get. Leave unset with the embedded media server; required with `TIDE_MEDIA_URL`.                                                                                                                                                       |
| `TIDE_S3_ENDPOINT`             | empty: no recording                    | Turns recording on; needs sign-in. The S3 endpoint as tide reaches it.                                                                                                                                                                                            |
| `TIDE_S3_PUBLIC_ENDPOINT`      | `TIDE_S3_ENDPOINT`                     | The endpoint browsers download from and hosts' machines use; it is in every presigned URL. With `TIDE_TRANSCRIPTS=true`, HTTPS.                                                                                                                                   |
| `TIDE_S3_RECORDER_ENDPOINT`    | `TIDE_S3_ENDPOINT`                     | The endpoint the recorder uploads to. tide sends it with every recording.                                                                                                                                                                                         |
| `TIDE_S3_BUCKET`               | `tide-recordings`                      | An existing bucket. Recordings go under `recordings/<room>/<recording-id>/`.                                                                                                                                                                                      |
| `TIDE_S3_REGION`               | `us-east-1`                            | S3 signing region.                                                                                                                                                                                                                                                |
| `TIDE_S3_ACCESS_KEY`           | none                                   | With recording: required.                                                                                                                                                                                                                                         |
| `TIDE_S3_SECRET_KEY`           | none                                   | With recording: required, at least 16 characters.                                                                                                                                                                                                                 |
| `TIDE_RECORDER_TEMPLATE_URL`   | `<TIDE_BASE_URL>/egress-template`      | The page the recorder's Chrome loads to draw a meeting. With the recorder in tide's network: `http://127.0.0.1:8080/egress-template`.                                                                                                                             |
| `TIDE_RECORDER_REDIS_ADDR`     | `127.0.0.1:6379`                       | The Redis tide's media server and the recorder share, as `host:port` (a name or an address), with recording and the embedded media server ([Redis, with recording](#redis-with-recording)). Keep it off public networks: recording jobs carry the S3 credentials. |
| `TIDE_RECORDER_REDIS_PASSWORD` | none                                   | With recording and the embedded media server: required, at least 32 characters. Redis's `requirepass` (tide refuses a Redis without one), and the recorder's `redis.password`.                                                                                    |
| `TIDE_TRUSTED_PROXIES`         | empty                                  | Comma-separated IPs or CIDRs of proxies whose `X-Forwarded-For` tide believes. Only your proxy or ingress. An invalid entry refuses startup.                                                                                                                      |
| `TIDE_DEV_MODE`                | `false`                                | Supplies the publicly known development value for any secret left unset and exposes `/api/dev/token`. Turns nothing else on. Never in production.                                                                                                                 |
| `TIDE_JOIN_RATE_LIMIT`         | `10`                                   | Joins per client per minute. Also sizes, in buckets of their own, room lookups and, when anonymous, room creation.                                                                                                                                                |
| `TIDE_WAIT_RATE_LIMIT`         | `20`                                   | Lobby wait streams per client per minute.                                                                                                                                                                                                                         |
| `TIDE_LOGIN_RATE_LIMIT`        | `10`                                   | Sign-in redirects per client per minute; when anonymous, new anonymous sessions.                                                                                                                                                                                  |
| `TIDE_PAIR_RATE_LIMIT`         | `10`                                   | With transcripts, machines starting a moil pairing (`POST /moil/v1/pair`) per client per minute.                                                                                                                                                                  |
| `TIDE_TRANSCRIPTS`             | `false`                                | Turns on transcripts and machine pairing ([Serve transcripts](#serve-transcripts)); needs recording. Read with `strconv.ParseBool`; anything else means off.                                                                                                      |

Rate limits count an IPv6 client by its /64 and an IPv4 client by its
address; both come from `X-Forwarded-For` only through `TIDE_TRUSTED_PROXIES`.
Anonymous sessions cost nothing to make, so when anonymous, room lookups count
them per client address as they do guests, and room creation is also capped
at 10,000 rooms in all (503 past that). With `TIDE_TRANSCRIPTS=true`,
pairing codes have fixed limits: a signed-in host may look up, confirm and
deny 20 a minute, and a client address 60. `POST /moil/v1/pair` takes only
`Content-Type: application/json`, which the moil app sends; a proxy or WAF in
front of tide must pass it through.

## Restarts end meetings

The media server lives in tide's process, so restarting tide (an upgrade, a
crash, a node drain) ends every meeting. Stopping tide tells every participant
the server is going away: browsers disconnect rather than retry, and the
meeting page offers **Rejoin**, which asks tide for a new token once it is
back. Guests go through the lobby again; in an anonymous deployment the rooms
themselves are gone. A recording running across the restart fails, and says
so on the dashboard. Deploy when no meeting is live, or keep an external media
server.

Deleting a room also ends any meeting running in it, so nobody stays in a
meeting under a link someone else can now create.

### External media server

With `TIDE_MEDIA_URL` set, tide behaves as before it embedded one: it starts
no media server, connects to no Redis, and talks to the media server at that
URL. Then:

- `TIDE_MEDIA_PUBLIC_URL`, `TIDE_MEDIA_API_KEY` and `TIDE_MEDIA_API_SECRET`
  are required; `TIDE_MEDIA_NODE_IP`, the media ports and
  `TIDE_RECORDER_REDIS_*` are unused.
- The media server needs the same key and secret, and must send its webhooks
  to `<tide>/api/webhooks/media`, signed with that key; without them
  recordings never leave "starting" and removed participants can rejoin.
- Redis between the media server and the recorder is yours to run, as is the
  media server's own network path and signaling route.

A tide restart then leaves meetings running; the media server's restarts end
them instead.

## Configure the S3 views

There are three endpoint views because the caller and the signer matter:

| View     | Variable                    | Caller                                                                                                                                  |
| -------- | --------------------------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| Server   | `TIDE_S3_ENDPOINT`          | tide deletes and manages objects. Use a private endpoint when available.                                                                |
| Browser  | `TIDE_S3_PUBLIC_ENDPOINT`   | tide signs a URL containing this origin, then redirects the browser to it. It must resolve publicly and its TLS certificate must match. |
| Recorder | `TIDE_S3_RECORDER_ENDPOINT` | The recorder uploads OGG audio or MP4 video. Use the endpoint reachable from where it runs.                                             |

With an in-cluster MinIO service, the server and recorder views usually share
an internal endpoint while the browser view uses a public object-storage
ingress. With AWS S3 or another public S3 service reachable from all three
callers, leave the other two unset and they follow `TIDE_S3_ENDPOINT`. Keep the
region, bucket, access key, secret key and path-style compatibility
consistent; tide forces path-style bucket lookup.

Recordings do not pass through tide. tide sends the recorder endpoint and the
S3 credentials with each recording job, and the recorder writes the object
directly to the bucket. The bucket key needs `GetObject`, `PutObject` and
`DeleteObject` on `recordings/*`.

## Serve transcripts

Transcripts need recording, and are off unless `TIDE_TRANSCRIPTS=true`. Off,
tide serves no `/moil/` routes and no machine, pairing or transcript API, and
the app hides `/machines` and every transcript control; machines and
transcripts already recorded stay in the database for when it is turned back
on, and deleting a recording still removes its transcript files.

Turning transcripts back on after a while off is not a resume. A transcript
waiting for a machine fails after 14 days counted from when it was requested,
time off included, so the ones older than that fail at once; a host can
request them again. And the recordings that completed while transcripts were
off get transcripts queued on the first pass, when their room's owner had a
machine paired by then, so those hosts' machines start on that backlog.

Transcripts need nothing deployed: they run on computers hosts pair through the
moil app (Architecture §8.1). tide serves the machines' side of moil on its
own origin, under `/moil/`. With transcripts on, these must hold:

- The proxy or ingress passes WebSocket upgrades on `/moil/v1/connect`, as it
  already must for `/rtc`. Each machine keeps one connection open for as long
  as its app runs. tide pings every 20 seconds, so any idle timeout of a minute
  or more is enough.
- `TIDE_S3_PUBLIC_ENDPOINT` is HTTPS and reachable from hosts' own networks,
  not only from their browsers. Machines download the recording and upload
  the transcript to staging keys under `transcripts-staging/` with URLs tide
  presigns for one attempt; tide then copies the files beside the recording.
  The S3 key therefore needs `GetObject`, `PutObject` and `DeleteObject` on
  `transcripts-staging/*` as well as on `recordings/*`. tide removes staging
  objects itself; a lifecycle rule that expires `transcripts-staging/` after 8
  days is a harmless backstop.
- `TIDE_TRUSTED_PROXIES` names the proxy, so that the pairing rate limit
  counts clients rather than the proxy.
- `TIDE_BASE_URL` is exactly the origin hosts reach tide at, scheme and port
  included. The moil app confirms a pairing only on the origin it pairs with,
  so a machine pairing through another address (`www.` versus the apex, an
  internal host, plain http behind a TLS proxy) is refused. Hosts should use
  the address `/machines` shows.

A host can pair at most 10 machines. Confirming an eleventh is refused with a
message saying to unpair one, and its code keeps waiting, so the host can
unpair a machine on `/machines` and confirm it again.

Paired machines, transcript state and the queue of objects to remove live in
SQLite with the rest of tide's records. Transcript files live in the bucket
next to their recordings. Deleting a recording or a room queues all of their
files for removal and removes them at once; tide retries any removal that
fails, every minute.

## Configure OIDC

Sign-in is optional; without `TIDE_OIDC_ISSUER` tide is anonymous. Use any
spec-compliant OpenID Connect issuer. Dex is for development only. Pocket ID,
authentik, and Keycloak work when configured as normal authorization code
clients.

Register this redirect URI exactly:

```
<TIDE_BASE_URL>/api/auth/callback
```

tide requests `openid profile email groups` and uses PKCE with S256. The ID
token must contain a non-empty `sub`; this is the stable room-owner identity.
`name` and `email` are used for display and should be supplied. `groups`
controls login and global room administration when the corresponding group
configuration is non-empty. Group matching is exact and case-sensitive. The
code accepts an empty email and falls back from a missing name to email, but
that produces a poor host identity in the UI. The issuer URL must match the
token issuer and discovery document exactly.

OIDC group membership is captured when the tide session is created. Providers
that do not push group-change or back-channel logout events cannot revoke that
cached authorization before the signed session expires.

## Diagnose a media incident

When a participant reports that they could not see or hear someone, ask them to
run `tideDiagnostics()` in the browser console **on the affected tab, before
reloading**. A reload discards the evidence, which is why these incidents have
been hard to attribute.

It saves a JSON file containing the bounded event ledger (what livekit-client
actually told that client), any listener exceptions tide caught, and the
current subscription state per publication. Nothing is transmitted; the file
stays on their machine until they send it to you. It contains participant
identities and display names, so handle it as personal data.

Read it against `docs/ARCHITECTURE.md` §9.1. An entry sequence that stops after
`ParticipantConnected` means the media server stopped forwarding for that
participant. A complete sequence with stale UI means the projection failed to
converge.

The media server and `livekit-client` are upgraded as a tested pair, behind the
media gate; with the media server inside tide, that pair ships in one tide
release.

## Back up state

An anonymous deployment has no state worth keeping: rooms live in memory.

With sign-in, back up the SQLite database. It uses WAL mode, so do not copy
only `tide.db` while the server is running. Use one of these methods:

- Run SQLite's online `.backup` command from a trusted helper that can reach
  the volume. The backup API reads a consistent snapshot including committed
  WAL data. The production tide image does not include the `sqlite3` shell.
- Stop tide, wait for it to close the database, copy the database, then start
  tide again. A storage-level snapshot is also safe when it captures the
  database and WAL files atomically.

Test restores. Protect backups like production data because they contain room,
recording, owner, session-revocation, paired-machine, and transcript records.

Recording files already live in S3. Apply the object store's versioning,
replication, retention, and backup policy separately. The recorder's Redis
holds nothing to back up.

## Upgrade

Back up SQLite first. Build or pull the new tide image or binary and roll it
out; on Kubernetes, patch the image in the overlay and apply it. The
`Recreate` strategy stops the old single replica before starting the new one.
The release artifact is one binary, so there is no Node runtime or separate
SPA rollout. Every upgrade ends live meetings ([Restarts end
meetings](#restarts-end-meetings)).

On SIGTERM tide stops taking connections, ends lobby streams at once (lobby
requests live in memory, so waiting guests ask again), and gives requests in
flight 10 seconds. With `TIDE_TRANSCRIPTS=true` it then disconnects paired
machines, saving what they last reported; they reconnect to the new process,
which starts their unfinished transcript jobs again. It then stops the media
server, ending every meeting, and closes the database last. That takes up to about 15 seconds, well within Kubernetes' default
30-second grace; give `docker stop` `--time 20` rather than its default 10. A
second SIGTERM stops tide at once.

Database initialization and additive migrations run synchronously when tide
opens the store, before it listens. Wait for `/healthz` and inspect the startup
log, including its mode line. Treat database changes as forward migrations;
verify release notes and restore procedure before rolling back an image.

Transcripts became opt-in with the `TIDE_TRANSCRIPTS` switch. An install that
uses transcripts must set `TIDE_TRANSCRIPTS=true` when it upgrades from a
release without that switch; otherwise transcripts and machine pairing turn
off, and machines can't connect until it is set. Nothing is lost meanwhile
(see [Serve transcripts](#serve-transcripts)).

### Upgrading to the one-binary release

- **Sign-in and recording are switched on by configuration.**
  `TIDE_OIDC_ISSUER` and `TIDE_S3_ENDPOINT` no longer have defaults. An
  install without `TIDE_OIDC_ISSUER` starts anonymous: it keeps rooms in
  memory and doesn't open `TIDE_DB_PATH` at all, so the existing rooms seem to
  vanish until the issuer is set again (nothing is deleted). Without
  `TIDE_S3_ENDPOINT`, recording is off and its controls disappear; recordings
  already made stay in the database and the bucket.
- **`TIDE_DEV_MODE` no longer turns anything on** but development secrets and
  `/api/dev/token`. A development setup must set the issuer and S3 endpoint
  itself, as `make dev` does.
- **An install with `TIDE_MEDIA_URL` set keeps working unchanged**: the
  external media server, its Redis and the recorder stay as they are.
- **Moving to the embedded media server**, when no meeting is live (a
  recording in flight fails):
  1. Remove `TIDE_MEDIA_URL` and `TIDE_MEDIA_PUBLIC_URL`; expose 7882/udp and
     7881/tcp on tide instead of the media server ([Choose the media
     path](#choose-the-media-path)), setting `TIDE_MEDIA_NODE_IP` only for a
     load balancer; and run the recorder in tide's network namespace
     (`ws_url: ws://127.0.0.1:7880`). tide's media server and the recorder
     share one Redis: an existing Redis can stay, with
     `TIDE_RECORDER_REDIS_ADDR`, `TIDE_RECORDER_REDIS_PASSWORD` and the
     recorder's `redis` settings pointing at it, as long as no other media
     server uses it. On Kubernetes the base and its recording component bring
     a Redis of their own in tide's pod.
  2. On Kubernetes, delete the old media server, Redis and recorder first,
     with the media server's own Services and configuration: it holds the hostPorts (or the
     load balancer's address) tide is about to take, and a tide pod pinned to
     the same node can't schedule while it runs. With the old reference
     names (`media-udp` only exists for a load balancer; reuse its static
     address for `tide-media`):

     ```sh
     kubectl -n <namespace> delete deployment media redis recorder
     kubectl -n <namespace> delete service media redis media-udp --ignore-not-found
     kubectl -n <namespace> delete configmap media-config recorder-config
     ```

  3. Apply the new overlay, then remove the media server's signaling host
     from the ingress and DNS. Browsers signal at `wss://<tide host>/rtc`
     from then on.
