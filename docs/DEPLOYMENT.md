# Deployment

klisi runs as four long-lived processes: klisi, LiveKit server, Redis, and
LiveKit Egress. It also needs an S3-compatible object store and an external
OpenID Connect issuer. The object store and issuer are dependencies, not klisi
processes.

This guide starts with Kubernetes. The manifests in `deploy/k8s/` are a
reference kustomize base. Build an overlay for every installation. The base has
example domains, storage settings, images, and a LiveKit webhook key marker;
do not apply it unchanged.

## Topology

```
┌──────────────┐  HTTPS: SPA, API, OIDC callback  ┌──────────────┐
│ Browser      │◄────────────────────────────────►│ klisi        │
│              │  WSS signaling through ingress  └──┬───────┬───┘
│              │◄──────────────────────────────┐     │       │
│              │  WebRTC media                │     │       │ presigned URLs,
└──────┬───────┘                               │     │       │ object management
       │                                       ▼     │       ▼
       │                                ┌─────────────┴┐  ┌──────────────┐
       └───────────────────────────────►│ LiveKit     │  │ S3-compatible│
                                        └──────┬──────┘  │ store        │
                                               │ Redis    └──────▲───────┘
                                        ┌──────▼──────┐      OGG / MP4
                                        │ Redis       │          │
                                        └──────┬──────┘          │
                                               │ jobs             │
                                        ┌──────▼──────┐          │
                                        │ Egress      ├──────────┘
                                        └─────────────┘

klisi ── OIDC discovery, code flow ──► external OIDC issuer
Egress ── loads /egress-template ────► klisi
Hosts' machines (moil) ── WSS /moil/v1/connect ──► klisi
                       ── presigned GET/PUT ─────► S3-compatible store
```

The ingress has two HTTP routes: the klisi origin and the LiveKit signaling
origin. Signaling is WebSocket traffic on LiveKit port 7880. Media does not use
that ingress. Browsers send WebRTC media directly to the address and ports
that LiveKit advertises.

## Hard constraints

- Run exactly one klisi replica. SQLite is its durable store. Lobby requests,
  rate limits, and other ephemeral coordination live in that process. Two
  replicas would split in-memory state and contend over a database that was not
  designed as a shared multi-writer service.
- Keep the klisi Deployment strategy set to `Recreate`. Mount one durable,
  writable volume at `KLISI_DB_PATH`. The production image defaults to
  `/data/klisi.db` and also writes SQLite `-wal` and `-shm` sidecars there.
- Give Egress enough CPU and memory for Chrome and GStreamer. Mount about 1 GiB
  of memory-backed storage at `/dev/shm`. Room-composite jobs can use several
  CPU cores.
- Egress needs `SYS_ADMIN` for its headless Chrome setup. The reference base
  also follows the development stack with an unconfined seccomp profile. These
  settings violate Pod Security `baseline`. Put Egress in a dedicated namespace
  with an explicit policy exception, or make an equally narrow exception in
  the cluster's policy engine. Do not relax the namespace used by unrelated
  workloads. If Egress moves to another namespace, copy its Secrets there,
  replace short Service names with cluster FQDNs, and allow the required
  cross-namespace traffic in NetworkPolicies.
- The host networking or host ports in media option (b) also violate Pod
  Security `baseline`. Use a dedicated LiveKit namespace or a scoped policy
  exception when selecting that option. Patch the webhook, Redis, and Egress
  URLs to cluster FQDNs if LiveKit moves out of the application namespace.
- Redis is coordination for LiveKit and Egress, not application storage. klisi
  does not require Redis persistence. An empty Redis may interrupt active rooms
  and jobs, but no klisi record or completed recording is restored from it.

## Use the reference base

`deploy/k8s/` contains one Deployment for each process, Services for klisi,
LiveKit signaling, and Redis, and a PVC for SQLite. It does not create an
Ingress, an object store, an OIDC issuer, a namespace, or Secrets. It also does
not expose LiveKit media until an overlay selects an option below.

Create these Secrets in the workload namespace:

| Secret            | Keys                                   | Used by                |
| ----------------- | -------------------------------------- | ---------------------- |
| `klisi-secrets`   | `session-secret`, `oidc-client-secret` | klisi                  |
| `livekit-secrets` | `api-key`, `api-secret`, `keys`        | klisi, LiveKit, Egress |
| `s3-secrets`      | `access-key`, `secret-key`             | klisi, Egress          |

Set `livekit-secrets/keys` to the LiveKit `key: secret` mapping accepted by
`LIVEKIT_KEYS`. Set the `webhook.api_key` marker in `livekit-config` to the same
API key identifier. Never put the API secret in a ConfigMap.

An installation overlay must patch every line marked `# OVERLAY:`. At minimum,
patch the image, public origins, OIDC values, all three S3 views, bucket and
region, storage class, LiveKit webhook key identifier, and one media path. Add
TLS-enabled ingress routes for klisi and LiveKit signaling. Keep the internal
server URLs (`http://klisi:8080` and `ws://livekit:7880`) inside the cluster.

Render and check an overlay before applying it:

```sh
kubectl kustomize deploy/overlays/production \
  | kubeconform -kubernetes-version 1.33.1 -strict
kubectl apply -k deploy/overlays/production
```

## Configuration

The server reads these variables. Production mode has no usable default for
secret values and refuses weak or shipped development secrets.

| Variable                    | Default                            | Purpose and production rule                                                                                                                                                       |
| --------------------------- | ---------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `KLISI_ADDR`                | `:8080`                            | HTTP listen address. Keep this behind the HTTPS ingress.                                                                                                                          |
| `KLISI_BASE_URL`            | `http://localhost:8080`            | Public klisi origin. Set an HTTPS origin. It controls redirects, the OIDC callback, secure cookies, and the default Egress template URL.                                          |
| `KLISI_SESSION_SECRET`      | none in production                 | HMAC key for session and OIDC state cookies. Required; use at least 32 characters of random data.                                                                                 |
| `KLISI_DB_PATH`             | `./data/klisi.db`                  | SQLite file. In the image this defaults to `/data/klisi.db`; point it at the mounted durable volume.                                                                              |
| `KLISI_LIVEKIT_URL`         | `ws://localhost:7880`              | Server-side LiveKit WebSocket URL used by SDK clients. Use the cluster Service URL.                                                                                               |
| `KLISI_LIVEKIT_PUBLIC_URL`  | `ws://localhost:7880`              | Browser-facing signaling URL returned with meeting tokens. Use the WSS ingress origin.                                                                                            |
| `KLISI_LIVEKIT_API_KEY`     | none in production                 | LiveKit key identifier shared by klisi, LiveKit, Egress, and webhook signing. Required.                                                                                           |
| `KLISI_LIVEKIT_API_SECRET`  | none in production                 | LiveKit signing secret. Required; use at least 32 characters.                                                                                                                     |
| `KLISI_OIDC_ISSUER`         | `http://localhost:5556/dex`        | Exact issuer URL used for discovery and ID token verification. Set the external production issuer.                                                                                |
| `KLISI_OIDC_CLIENT_ID`      | `klisi`                            | Registered OIDC client ID.                                                                                                                                                        |
| `KLISI_OIDC_CLIENT_SECRET`  | none in production                 | Registered OIDC client secret. Required; use at least 16 characters.                                                                                                              |
| `KLISI_USER_GROUPS`         | empty                              | Comma-separated, case-sensitive OIDC groups allowed to sign in. Empty permits every verified OIDC user; administrators are always allowed.                                        |
| `KLISI_ADMIN_GROUPS`        | empty                              | Comma-separated, case-sensitive OIDC groups whose members can administer every room. Empty grants no global administration.                                                       |
| `KLISI_S3_ENDPOINT`         | `http://localhost:9000`            | S3 endpoint as seen by klisi for deletes and object management.                                                                                                                   |
| `KLISI_S3_PUBLIC_ENDPOINT`  | `http://localhost:9000`            | Browser-reachable S3 endpoint used to sign five-minute download URLs, and the URLs hosts' machines use to fetch recordings and upload transcripts. The hostname in the signature must be the hostname the caller uses. Use HTTPS: moil machines refuse plain HTTP unless klisi itself is on loopback. |
| `KLISI_S3_EGRESS_ENDPOINT`  | `http://minio:9000`                | S3 endpoint as seen by Egress. klisi sends it with every recording request.                                                                                                       |
| `KLISI_S3_BUCKET`           | `klisi-recordings`                 | Existing bucket for timestamped OGG audio and MP4 video objects under `recordings/<room>/<recording-id>/`.                                                                         |
| `KLISI_S3_ACCESS_KEY`       | none in production                 | S3 access key sent to the server-side client and Egress request. Required.                                                                                                        |
| `KLISI_S3_SECRET_KEY`       | none in production                 | S3 secret key. Required; use at least 16 characters.                                                                                                                              |
| `KLISI_S3_REGION`           | `us-east-1`                        | S3 signing region. It must match the store.                                                                                                                                       |
| `KLISI_EGRESS_TEMPLATE_URL` | `<KLISI_BASE_URL>/egress-template` | URL Egress Chrome loads for the room composite. Use the internal klisi Service URL when Egress can reach it.                                                                      |
| `KLISI_TRUSTED_PROXIES`     | empty                              | Comma-separated proxy IPs or CIDRs whose `X-Forwarded-For` value klisi may trust. Leave empty unless rate limits must use forwarded client IPs. Restrict it to ingress addresses. |
| `KLISI_DEV_MODE`            | `false`                            | Enables shipped development secrets and the unauthenticated development token route. Never enable it in production.                                                               |
| `KLISI_JOIN_RATE_LIMIT`     | `10`                               | Guest joins per client IP per minute.                                                                                                                                             |
| `KLISI_WAIT_RATE_LIMIT`     | `20`                               | Lobby wait streams per client IP per minute.                                                                                                                                      |
| `KLISI_LOGIN_RATE_LIMIT`    | `10`                               | Sign-in redirects per client IP per minute.                                                                                                                                       |
| `KLISI_PAIR_RATE_LIMIT`     | `10`                               | Machines starting a moil pairing (`POST /moil/v1/pair`, which takes no credentials) per client IP per minute.                                                                     |
| `KLISI_TRANSCRIPTS`         | `false`                            | Turns on transcripts and machine pairing through moil (see [Serve transcripts](#serve-transcripts)). moil is alpha, so leave it off unless you mean to run it. Read with `strconv.ParseBool`; anything else means off. |

Rate limits count an IPv6 client by its /64, the network one subscriber gets,
and an IPv4 client by its address; both come from `X-Forwarded-For` only
through `KLISI_TRUSTED_PROXIES`. Pairing codes have fixed limits: a signed-in
host may look up, confirm and deny 20 a minute, and a client address 60,
whoever is signed in. `POST /moil/v1/pair` takes only
`Content-Type: application/json`, which the moil app sends; a proxy or WAF in
front of klisi must pass it through.

## Choose the WebRTC media path

Make this choice before deploying LiveKit. The base config uses the single-port
UDP mux on 7882 and ICE-TCP on 7881, but exposes neither one publicly.

```
Can the cluster provide a stable public UDP LoadBalancer on one port?
  ├─ yes ─► (a) dedicated media LoadBalancer
  └─ no
      Does one schedulable node have a stable public IPv4 address?
        ├─ yes ─► (b) hostPort or hostNetwork
        └─ no  ─► (c) TCP-only fallback, with a quality cost
```

In every option, route WSS signaling on port 7880 through the normal HTTPS
ingress. Do not confuse a successful WebSocket connection with a working media
path. Test from outside the cluster and from Egress; Egress Chrome must be able
to reach the same advertised candidate.

### Option a: dedicated UDP LoadBalancer

Use this when the Kubernetes provider supports UDP LoadBalancer Services and a
stable public IPv4 address. Copy the commented `livekit-media` Service from
`deploy/k8s/livekit.yaml` into the overlay. Keep it separate from the signaling
Service. Expose UDP 7882, preserve local traffic where the provider requires it,
and pass the allocated address to LiveKit with `--node-ip`.

This option keeps media routing declarative and does not expose a node directly.
It costs a load balancer and depends on provider-specific UDP behavior,
source-address handling, health checks, and NAT hairpin support. A DNS name is
not a substitute for `--node-ip`; resolve and manage a stable address.

LiveKit's ICE-TCP port is designed for direct node exposure, not an HTTP ingress
or TLS-terminating load balancer. If clients need ICE-TCP fallback with this
option, expose TCP 7881 on the selected public node as described in option (b),
or design and test a provider-supported L4 path separately.

### Option b: hostPort or hostNetwork

Use this when one Kubernetes node has a stable public IPv4 address. Pin the
LiveKit pod to that node. Open UDP 7882 and TCP 7881 in the node firewall. Add
`--node-ip <public-ip>` to the LiveKit arguments. Then choose one of these
overlay patches:

- Uncomment `hostPort: 7882` and `hostPort: 7881`; or
- enable `hostNetwork: true` and `dnsPolicy: ClusterFirstWithHostNet`.

Do not enable both. Host networking is the most direct path and matches
LiveKit's normal Kubernetes model. Host ports keep the pod network namespace
but still reserve those ports on the node. Either choice limits scheduling to
one LiveKit pod per node, exposes node-level ports, and needs the Pod Security
exception described above. Keep the node dedicated enough that another
workload cannot claim those ports.

The UDP mux on 7882 carries normal media. ICE-TCP on 7881 is the fallback for
clients whose network blocks UDP. Both transports are encrypted by WebRTC; port
7881 is not HTTP and must not pass through the HTTPS ingress.

### Option c: TCP-only fallback

Use this only when UDP exposure is impossible. Advertise the public node and
expose TCP 7881 directly, but do not expose UDP 7882. Clients first fail the UDP
candidate and then use ICE-TCP.

TCP avoids some UDP firewall restrictions, but packet loss causes
head-of-line blocking and congestion recovery that is poorly suited to live
audio and video. Expect more latency, freezes, and quality reduction on lossy
links. TCP 7881 is also not TURN/TLS on 443, so very restrictive networks may
still block it. Adding TURN/TLS is a separate deployment decision outside this
base.

## Configure the S3 views

There are three endpoint views because the caller and the signer matter:

| View    | Variable                   | Caller                                                                                                                                   |
| ------- | -------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| Server  | `KLISI_S3_ENDPOINT`        | klisi deletes and manages objects. Use a private Service endpoint when available.                                                        |
| Browser | `KLISI_S3_PUBLIC_ENDPOINT` | klisi signs a URL containing this origin, then redirects the browser to it. It must resolve publicly and its TLS certificate must match. |
| Egress  | `KLISI_S3_EGRESS_ENDPOINT` | Egress uploads OGG audio or MP4 video. Use the endpoint reachable from the Egress namespace.                                             |

With an in-cluster MinIO service, the server and Egress views usually share an
internal endpoint while the browser view uses a public object-storage ingress.
With AWS S3 or another public S3 service reachable from all three callers, all
views can collapse to one HTTPS endpoint. Keep the region, bucket, access key,
secret key, and path-style compatibility consistent. klisi forces path-style
bucket lookup for S3-compatible stores.

Recordings do not pass through the klisi pod. klisi includes the Egress endpoint
and S3 credentials in each Egress request, and the worker writes the OGG or
MP4 object directly to the bucket.

## Serve transcripts

Transcripts are off unless `KLISI_TRANSCRIPTS=true`. Off, klisi serves no
`/moil/` routes and no machine, pairing or transcript API, and the app hides
`/machines` and every transcript control; machines and transcripts already
recorded stay in the database for when it is turned back on, and deleting a
recording still removes its transcript files.

Transcripts need nothing deployed: they run on computers hosts pair through the
moil app (Architecture §8.1). klisi serves the machines' side of moil on its
own origin, under `/moil/`. With transcripts on, these must hold:

- The klisi ingress passes WebSocket upgrades on `/moil/v1/connect`, as it
  already does for any HTTP/1.1 upgrade. Each machine keeps one connection open
  for as long as its app runs. klisi pings every 20 seconds, so any idle
  timeout of a minute or more is enough.
- `KLISI_S3_PUBLIC_ENDPOINT` is HTTPS and reachable from hosts' own networks,
  not only from their browsers. Machines download the recording and upload
  the transcript to staging keys under `transcripts-staging/` with URLs klisi
  presigns for one attempt; klisi then copies the files beside the recording.
  The S3 key therefore needs `GetObject`, `PutObject` and `DeleteObject` on
  `transcripts-staging/*` as well as on `recordings/*`. klisi removes staging
  objects itself; a lifecycle rule that expires `transcripts-staging/` after 8
  days is a harmless backstop.
- `KLISI_TRUSTED_PROXIES` names the ingress, so that the pairing rate limit
  counts clients rather than the proxy.
- `KLISI_BASE_URL` is exactly the origin hosts reach klisi at, scheme and port
  included. The moil app confirms a pairing only on the origin it pairs with,
  so a machine pairing through another address (`www.` versus the apex, an
  internal host, plain http behind a TLS proxy) is refused. Hosts should use
  the address `/machines` shows.

A host can pair at most 10 machines. Confirming an eleventh is refused with a
message saying to unpair one, and its code keeps waiting, so the host can
unpair a machine on `/machines` and confirm it again.

Paired machines, transcript state and the queue of objects to remove live in
SQLite with the rest of klisi's records. Transcript files live in the bucket
next to their recordings. Deleting a recording or a room queues all of their
files for removal and removes them at once; klisi retries any removal that
fails, every minute.

## Configure OIDC

Use any spec-compliant OpenID Connect issuer. Dex is for development only.
Pocket ID, authentik, and Keycloak work when configured as normal authorization
code clients.

Register this redirect URI exactly:

```
<KLISI_BASE_URL>/api/auth/callback
```

klisi requests `openid profile email groups` and uses PKCE with S256. The ID
token must contain a non-empty `sub`; this is the stable room-owner identity.
`name` and `email` are used for display and should be supplied. `groups`
controls login and global room administration when the corresponding group
configuration is non-empty. Group matching is exact and case-sensitive. The
code accepts an empty email and falls back from a missing name to email, but
that produces a poor host identity in the UI. The issuer URL must match the
token issuer and discovery document exactly.

OIDC group membership is captured when the Klisi session is created. Providers
that do not push group-change or back-channel logout events cannot revoke that
cached authorization before the signed session expires.

## Diagnose a media incident

When a participant reports that they could not see or hear someone, ask them to
run `klisiDiagnostics()` in the browser console **on the affected tab, before
reloading**. A reload discards the evidence, which is why these incidents have
been hard to attribute.

It saves a JSON file containing the bounded event ledger (what livekit-client
actually told that client), any listener exceptions klisi caught, and the
current subscription state per publication. Nothing is transmitted; the file
stays on their machine until they send it to you. It contains participant
identities and display names, so handle it as personal data.

Read it against `docs/ARCHITECTURE.md` §9.1. An entry sequence that stops after
`ParticipantConnected` means LiveKit stopped forwarding for that participant. A
complete sequence with stale UI means the projection failed to converge.

Upgrade `livekit-client` and LiveKit server as a tested pair, behind the media
gate. The client version is pinned exactly for that reason.

## Back up state

Back up the SQLite database on the klisi PVC. The database uses WAL mode, so do
not copy only `klisi.db` while the server is running.

Use one of these methods:

- Run SQLite's online `.backup` command from a trusted helper that can attach
  the PVC. The backup API reads a consistent snapshot including committed WAL
  data. The production klisi image does not include the `sqlite3` shell.
- Stop the klisi Deployment, wait for the process to close the database, copy
  the database from the volume, then start klisi again. A storage-level snapshot
  is also safe when it captures the database and WAL files atomically.

Test restores. Protect backups like production data because they contain room,
recording, owner, session-revocation, paired-machine, and transcript records.

Recording files already live in S3. Apply the object store's versioning,
replication, retention, and backup policy separately. Do not back up Redis for
klisi recovery.

## Upgrade

Back up SQLite first. Build or pull the new klisi image, patch the image in the
overlay, and apply it. The `Recreate` strategy stops the old single replica
before starting the new one. The release artifact is one binary, so there is no
Node runtime or separate SPA rollout.

On SIGTERM klisi stops taking connections, ends lobby streams at once (lobby
requests live in memory, so waiting guests ask again), and gives requests in
flight 10 seconds. It then disconnects paired machines, saving what they last
reported; they reconnect to the new replica, which starts their unfinished
transcript jobs again. It closes the database last. That takes up to about 15
seconds, well within Kubernetes' default 30-second grace; give `docker stop`
`--time 20` rather than its default 10. A second SIGTERM stops klisi at once.

Database initialization and additive migrations run synchronously when klisi
opens the store, before the HTTP listener starts. Wait for `/healthz` to become
ready and inspect startup logs. Treat database changes as forward migrations;
verify release notes and restore procedure before rolling back an image.

Upgrade LiveKit server and Egress as a tested pair. Active rooms and recording
jobs are sensitive to restarts, so schedule those changes and verify media,
webhooks, and one complete recording after the rollout.
