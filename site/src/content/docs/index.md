# Deploy tide

tide is a simple, lightweight video conference service that you host yourself. It is one program. The web app and the media server run inside it.

This guide puts tide on one Linux server. For Kubernetes, see [Kubernetes](#kubernetes).

## You need

- A Linux server with Docker and a public IPv4 address.
- A DNS name for the server. This guide uses `meet.example.com`.

## 1. Open the ports

| Port                | For                                 |
| ------------------- | ----------------------------------- |
| `80/tcp`, `443/tcp` | the web app                         |
| `7882/udp`          | audio and video                     |
| `7881/tcp`          | audio and video when UDP is blocked |

## 2. Get tide

```sh
mkdir /srv/tide && cd /srv/tide
git clone https://github.com/Convex-Works/tide.git src
```

## 3. Write the configuration

`/srv/tide/.env`:

```sh
TIDE_BASE_URL=https://meet.example.com
TIDE_ADDR=127.0.0.1:8080
TIDE_TRUSTED_PROXIES=127.0.0.1
```

`/srv/tide/compose.yaml`:

```yaml
services:
  caddy:
    image: caddy:2
    command: caddy reverse-proxy --from ${TIDE_BASE_URL} --to 127.0.0.1:8080
    network_mode: host
    volumes:
      - caddy-data:/data
    restart: unless-stopped

  tide:
    build: src
    env_file: .env
    network_mode: host
    volumes:
      - tide-data:/data
    restart: unless-stopped
    stop_grace_period: 20s

volumes:
  caddy-data:
  tide-data:
```

Caddy gets the HTTPS certificate. Browsers need HTTPS to use the camera.

## 4. Start tide

```sh
docker compose up -d --build
docker compose logs tide | grep 'tide: '
```

The log shows `tide: anonymous, …`. Anyone who opens `https://meet.example.com` can make a room. tide deletes a room 24 hours after its last use.

## 5. Check tide

```sh
curl -fsS https://meet.example.com/healthz                               # ok
curl -s -o /dev/null -w '%{http_code}\n' https://meet.example.com/rtc/validate  # 401
```

Make a room. Join it from a phone on mobile data. Make sure that you see and hear each other.

## Add sign-in

With sign-in, only your users can make rooms, and rooms stay. Guests join with the room link.

1. In your OpenID Connect provider, make a client with a secret. Set its redirect URI to `https://meet.example.com/api/auth/callback`. Allow the scopes `openid profile email groups`.
2. Add these lines to `.env`. Use the issuer URL exactly as the provider shows it.

   ```sh
   TIDE_OIDC_ISSUER=https://id.example.com
   TIDE_OIDC_CLIENT_ID=tide
   TIDE_OIDC_CLIENT_SECRET=<client secret>
   # openssl rand -base64 48
   TIDE_SESSION_SECRET=<random>
   ```

3. Run `chmod 600 .env` and `docker compose up -d`.

To let only some groups sign in, set `TIDE_USER_GROUPS`. See [Configuration](/docs/configuration).

## Add recording

Recording needs sign-in.

1. Make an S3 bucket. Make an access key that can get, put and delete objects in `recordings/`.
2. Add these lines to `.env`:

   ```sh
   TIDE_S3_ENDPOINT=https://s3.eu-central-1.amazonaws.com
   TIDE_S3_REGION=eu-central-1
   TIDE_S3_BUCKET=<bucket>
   TIDE_S3_ACCESS_KEY=<access key>
   TIDE_S3_SECRET_KEY=<secret key>
   # openssl rand -hex 8
   TIDE_MEDIA_API_KEY=<random>
   # openssl rand -hex 32
   TIDE_MEDIA_API_SECRET=<random>
   # openssl rand -hex 32
   TIDE_RECORDER_REDIS_PASSWORD=<random>
   TIDE_RECORDER_TEMPLATE_URL=http://127.0.0.1:8080/egress-template
   ```

3. Add Redis and the recorder to `compose.yaml`, under `services`:

   ```yaml
   redis:
     image: redis:7.4.8-alpine3.21
     network_mode: host
     user: '999:1000'
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
       test: ['CMD-SHELL', 'redis-cli -h 127.0.0.1 ping | grep -q PONG']
       interval: 2s
       retries: 30
     restart: unless-stopped

   recorder:
     image: livekit/egress:v1.13.0
     network_mode: service:tide
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
   ```

4. Run `docker compose up -d`.
5. Record 30 seconds of a call. Download the recording from the dashboard.

## Kubernetes

You need Kubernetes 1.29 or later, a node with a public IPv4 address, an ingress, and a registry.

1. Build and push the image from the repository:

   ```sh
   docker buildx build --platform linux/amd64 -t <registry>/tide:<version> --push .
   ```

2. Make the namespace and the secrets. Keep nothing else in this namespace: tide uses host ports, and the recorder needs `SYS_ADMIN`. Also make a TLS secret `tide-tls` for the host.

   ```sh
   kubectl create namespace tide
   kubectl label namespace tide pod-security.kubernetes.io/enforce=privileged
   kubectl label node <node> tide/media=public
   kubectl -n tide create secret generic tide-secrets \
     --from-literal=session-secret="$(openssl rand -base64 48)" \
     --from-literal=oidc-client-secret=<client secret, or unused>

   # Only for recording:
   kubectl -n tide create secret generic media-secrets \
     --from-literal=api-key="$(openssl rand -hex 8)" \
     --from-literal=api-secret="$(openssl rand -hex 32)" \
     --from-literal=recorder-redis-password="$(openssl rand -hex 32)"
   kubectl -n tide create secret generic s3-secrets \
     --from-literal=access-key=<access key> \
     --from-literal=secret-key=<secret key>
   ```

3. Make `deploy/overlays/production/kustomization.yaml`:

   ```yaml
   apiVersion: kustomize.config.k8s.io/v1beta1
   kind: Kustomization
   namespace: tide
   resources:
     - ../../k8s
     - ingress.yaml
   components:
     - ../../k8s/recording # only for recording
   images:
     - name: registry.example.com/tide
       newName: <registry>/tide
       newTag: <version>
   patches:
     - path: tide.yaml
   ```

4. Make `tide.yaml` in the same folder. Without sign-in, set `TIDE_OIDC_ISSUER` to `""`. For recording, also set `TIDE_S3_ENDPOINT`, `TIDE_S3_PUBLIC_ENDPOINT`, `TIDE_S3_RECORDER_ENDPOINT`, `TIDE_S3_BUCKET` and `TIDE_S3_REGION`.

   ```yaml
   apiVersion: apps/v1
   kind: Deployment
   metadata:
     name: tide
   spec:
     template:
       spec:
         nodeSelector:
           tide/media: public
         containers:
           - name: tide
             ports:
               - containerPort: 7881
                 hostPort: 7881
                 protocol: TCP
               - containerPort: 7882
                 hostPort: 7882
                 protocol: UDP
             env:
               - name: TIDE_BASE_URL
                 value: https://<host>
               - name: TIDE_OIDC_ISSUER
                 value: <issuer>
               - name: TIDE_OIDC_CLIENT_ID
                 value: <client id>
   ---
   apiVersion: v1
   kind: PersistentVolumeClaim
   metadata:
     name: tide-data
   spec:
     storageClassName: <storage class>
   ```

5. Make `ingress.yaml` in the same folder. This example is for ingress-nginx. Keep the long timeouts.

   ```yaml
   apiVersion: networking.k8s.io/v1
   kind: Ingress
   metadata:
     name: tide
     annotations:
       nginx.ingress.kubernetes.io/proxy-read-timeout: '3600'
       nginx.ingress.kubernetes.io/proxy-send-timeout: '3600'
   spec:
     ingressClassName: nginx
     tls:
       - hosts: [<host>]
         secretName: tide-tls
     rules:
       - host: <host>
         http:
           paths:
             - path: /
               pathType: Prefix
               backend:
                 service: { name: tide, port: { number: 8080 } }
   ```

6. Apply, then do the checks in [step 5](#5-check-tide).

   ```sh
   kubectl kustomize deploy/overlays/production | grep -nE '<|example\.com'   # must print nothing
   kubectl apply -k deploy/overlays/production
   kubectl -n tide rollout status deploy/tide
   ```

Run one replica only. Set `TIDE_TRUSTED_PROXIES` to your ingress pods' addresses, or all visitors share one rate limit. Without a public node, use a UDP load balancer: see the end of `deploy/k8s/tide.yaml`.

## Upgrade

A restart ends all meetings. Upgrade when no meeting runs. Back up first: tide cannot go back to an older database.

```sh
git -C src pull && docker compose up -d --build
```

On Kubernetes, push a new image, set its tag in `kustomization.yaml`, and apply.

## Back up

With sign-in, tide keeps rooms in `/data/tide.db`. Do not copy the file while tide runs. Use SQLite's backup:

```sh
docker run --rm --volumes-from "$(docker compose ps -q tide)" -v "$PWD:/backup" alpine \
  sh -c 'apk add -q sqlite && sqlite3 /data/tide.db ".backup /backup/tide.db"'
```

On Kubernetes, run `sqlite3` in a pod that mounts `tide-data`. Back up the bucket with your provider's tools.

## Problems

Read the log first: `docker compose logs`, or `kubectl -n tide logs deploy/tide --all-containers`. It names a bad or missing variable.

| Problem                              | Cause and fix                                                                                     |
| ------------------------------------ | ------------------------------------------------------------------------------------------------- |
| Sign-in shows 502                    | tide cannot reach the provider, or `TIDE_OIDC_ISSUER` is not exact.                               |
| Calls connect, but there is no video | `7882/udp` is closed. Behind NAT, set `TIDE_MEDIA_NODE_IP` to the public IPv4 address.            |
| A recording stays at "starting"      | The recorder cannot reach tide. Make sure that both use the same key, secret and Redis password.  |
| A recording fails after a minute     | The recorder cannot reach the media. Set `TIDE_MEDIA_NODE_IP` only to an address that is local.   |
| A download fails                     | Browsers cannot reach the bucket. Set `TIDE_S3_PUBLIC_ENDPOINT`.                                  |
| A person cannot see or hear another  | Before they reload, they run `tideDiagnostics()` in the browser console. It saves a file for you. |

## Transcripts

Hosts can transcribe recordings on their own Mac with the moil app. Nothing runs on the server. moil is alpha and unsigned. Its jobs run with the host's permissions.

1. Let the bucket key also get, put and delete objects in `transcripts-staging/`.
2. Make sure that `TIDE_S3_PUBLIC_ENDPOINT` (or `TIDE_S3_ENDPOINT`) is HTTPS.
3. Set `TIDE_TRANSCRIPTS=true`.

Hosts open `/machines` and select **Add a machine**, or use the command line:

```sh
moil pair https://meet.example.com/moil
moil review tide
moil approve tide <hash>
moil agent
```

A job needs up to 10 GB of memory. The first job downloads 2.9 GB.
