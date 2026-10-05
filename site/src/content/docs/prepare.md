# Prepare

Goal: have every value the install step needs, written down.

Requires: admin access to a Kubernetes cluster, a DNS zone, an OpenID Connect issuer, an S3-compatible object store, and `openssl`.

## 1. Hostnames

Choose two names and point both at your ingress:

| Placeholder  | Example             | Serves                             |
| ------------ | ------------------- | ---------------------------------- |
| `APP_HOST`   | `meet.example.com`  | the web app and API                |
| `MEDIA_HOST` | `media.example.com` | media server signaling (WebSocket) |

Both need TLS certificates your ingress serves.

## 2. OIDC client

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

Create a bucket and an access key that can `GetObject`, `PutObject` and `DeleteObject` on `recordings/*`. Add `transcripts-staging/*` if you will turn on [transcripts](/docs/transcripts).

Record `S3_BUCKET`, `S3_REGION`, `S3_ACCESS_KEY`, `S3_SECRET_KEY` (16 characters or more), and the store's URL as each caller reaches it:

| Record as              | Caller   | Requirement                                                      |
| ---------------------- | -------- | ---------------------------------------------------------------- |
| `S3_ENDPOINT`          | tide     | reachable from the tide pod                                      |
| `S3_PUBLIC_ENDPOINT`   | browsers | public HTTPS with a matching certificate; download links name it |
| `S3_RECORDER_ENDPOINT` | recorder | reachable from the recorder pod                                  |

With a public store such as AWS S3, all three are the same HTTPS URL. tide uses path-style addressing.

## 4. Media network path

Audio and video do not pass through the ingress. Browsers connect straight to the media server's public address on these ports:

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
openssl rand -base64 48   # SESSION_SECRET
openssl rand -hex 8       # MEDIA_API_KEY: letters, digits, - and _ only
openssl rand -base64 48   # MEDIA_API_SECRET
```

tide refuses to start if the session or media secret is shorter than 32 characters, or the OIDC or S3 secret shorter than 16.

## Done when

- [ ] `APP_HOST` and `MEDIA_HOST` resolve to the ingress, with certificates
- [ ] `REGISTRY`, `VERSION` and a durable `STORAGE_CLASS`
- [ ] `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`
- [ ] `S3_BUCKET`, `S3_REGION`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, `S3_ENDPOINT`, `S3_PUBLIC_ENDPOINT`, `S3_RECORDER_ENDPOINT`
- [ ] `MEDIA_OPTION`, `NODE_IP`, and for `host` or `tcp` the node's name `NODE_NAME`
- [ ] `SESSION_SECRET`, `MEDIA_API_KEY`, `MEDIA_API_SECRET`

Next: [Install](/docs/install).
