# Prepare

Goal: have every value the install step needs, written down.

Requires: admin access to a Kubernetes cluster, a DNS zone, and `openssl`. For sign-in, an OpenID Connect issuer; for recording, an S3-compatible object store.

## 1. Hostname

Choose one name, `APP_HOST` (for example `meet.example.com`), point it at your ingress, and give the ingress a TLS certificate for it. The web app, its API and the media server's signaling all use it.

## 2. OIDC client

Skip this for an anonymous installation, where anyone can create a room.

Register a confidential client using the authorization code flow:

- Redirect URI, exactly: `https://APP_HOST/api/auth/callback`
- Scopes: `openid profile email groups`. tide uses PKCE with S256.
- ID tokens must carry a non-empty `sub`. Include `name` and `email`; people see them.
- Optional: a `groups` claim, to limit who may sign in (`TIDE_USER_GROUPS`) or who manages every room (`TIDE_ADMIN_GROUPS`). Matching is exact and case-sensitive.

Record:

- `OIDC_ISSUER`: the issuer URL exactly as its discovery document states it
- `OIDC_CLIENT_ID`
- `OIDC_CLIENT_SECRET`: 16 characters or more

## 3. Bucket

Only with recording, which needs sign-in.

Create a bucket and an access key that can `GetObject`, `PutObject` and `DeleteObject` on `recordings/*`. Add `transcripts-staging/*` if you will turn on [transcripts](/docs/transcripts).

Record `S3_BUCKET`, `S3_REGION`, `S3_ACCESS_KEY`, `S3_SECRET_KEY` (16 characters or more), and the store's URL as each caller reaches it:

| Record as              | Caller   | Requirement                                                      |
| ---------------------- | -------- | ---------------------------------------------------------------- |
| `S3_ENDPOINT`          | tide     | reachable from the tide pod                                      |
| `S3_PUBLIC_ENDPOINT`   | browsers | public HTTPS with a matching certificate; download links name it |
| `S3_RECORDER_ENDPOINT` | recorder | reachable from the tide pod, where the recorder runs             |

With a public store such as AWS S3, all three are the same HTTPS URL. tide uses path-style addressing.

## 4. Media network path

Audio and video do not pass through the ingress. Browsers connect straight to tide's public address on these ports:

| Port       | Carries                              |
| ---------- | ------------------------------------ |
| `7882/udp` | all media, multiplexed               |
| `7881/tcp` | fallback for networks that block UDP |

Pick the first option the cluster supports and record it as `MEDIA_OPTION`, with the public IPv4 address as `NODE_IP`:

1. `lb`: the cluster can create a `LoadBalancer` Service for UDP with a stable public IPv4 address.
2. `host`: one schedulable node has a stable public IPv4 address. Open `7882/udp` and `7881/tcp` in its firewall.
3. `tcp`: neither. Expose `7881/tcp` on a public node. Calls lose quality on lossy networks; tell the user.

A DNS name is not a substitute for `NODE_IP`.

## 5. Secrets

```sh
openssl rand -base64 48   # SESSION_SECRET, with sign-in
openssl rand -hex 8       # MEDIA_API_KEY, with recording
openssl rand -hex 32      # MEDIA_API_SECRET, with recording
openssl rand -hex 32      # RECORDER_PASSWORD, with recording
```

tide refuses to start if the session secret, media secret or recorder password is shorter than 32 characters, or the OIDC or S3 secret shorter than 16. Keep the recorder password hex: the recorder's configuration embeds it in YAML.

## Done when

- [ ] `APP_HOST` resolves to the ingress, with a certificate
- [ ] `REGISTRY`, `VERSION`, and with sign-in a durable `STORAGE_CLASS`
- [ ] `MEDIA_OPTION`, `NODE_IP`, and for `host` or `tcp` the node's name `NODE_NAME`
- [ ] with sign-in: `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `SESSION_SECRET`
- [ ] with recording: `S3_BUCKET`, `S3_REGION`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, `S3_ENDPOINT`, `S3_PUBLIC_ENDPOINT`, `S3_RECORDER_ENDPOINT`, `MEDIA_API_KEY`, `MEDIA_API_SECRET`, `RECORDER_PASSWORD`

Next: [Install](/docs/install).
