# Install

Goal: tide running in the `tide` namespace, with the recorder beside it if you record, reachable at `APP_HOST`.

Requires: every value from [Prepare](/docs/prepare), a checkout of the tide repository, `kubectl`, `docker` with `buildx`, and a container registry the cluster can pull from (`REGISTRY`).

## 1. Build the image

From the repository root:

```sh
docker buildx build --platform linux/amd64 -t <REGISTRY>/tide:<VERSION> --push .
```

The image runs `/tide` as a non-root user, listens on `8080`, and keeps its database at `/data/tide.db`. If the registry is private, add an `imagePullSecrets` entry to the tide Deployment in step 4.

## 2. Create the namespace

```sh
kubectl create namespace tide
# The recorder needs SYS_ADMIN and an unconfined seccomp profile, and host
# ports break the baseline policy too. The namespace holds only tide, so
# allow it there and nowhere else.
kubectl label namespace tide pod-security.kubernetes.io/enforce=privileged
```

## 3. Create the secrets

Secret names and keys must match exactly; the manifests reference them.

```sh
kubectl -n tide create secret generic tide-secrets \
  --from-literal=session-secret="$SESSION_SECRET" \
  --from-literal=oidc-client-secret="$OIDC_CLIENT_SECRET"
```

With recording, also:

```sh
kubectl -n tide create secret generic media-secrets \
  --from-literal=api-key="$MEDIA_API_KEY" \
  --from-literal=api-secret="$MEDIA_API_SECRET" \
  --from-literal=recorder-redis-password="$RECORDER_PASSWORD"

kubectl -n tide create secret generic s3-secrets \
  --from-literal=access-key="$S3_ACCESS_KEY" \
  --from-literal=secret-key="$S3_SECRET_KEY"
```

For an anonymous installation, `tide-secrets` still has to exist. tide checks `session-secret` in every mode, so it is `SESSION_SECRET` as for sign-in (32 characters or more); `oidc-client-secret` is unread, so give it the word `unused`.

Also create `tide-tls`, a TLS secret for `APP_HOST`, with cert-manager or `kubectl create secret tls`.

## 4. Write the overlay

The base in `deploy/k8s/` is not deployable as is: every line marked `# OVERLAY:` needs a value. Create `deploy/overlays/production/` with these files and replace every `<PLACEHOLDER>` with its value from [Prepare](/docs/prepare).

`kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: tide
resources:
  - ../../k8s
  - ingress.yaml
  # and media-lb.yaml here for MEDIA_OPTION lb
components:
  - ../../k8s/recording # only with recording
images:
  - name: registry.example.com/tide
    newName: <REGISTRY>/tide
    newTag: <VERSION>
patches:
  - path: tide.yaml
  # and media-host.yaml or media-tcp.yaml here for MEDIA_OPTION host or tcp
```

`tide.yaml`. Leave out the `TIDE_S3_*` lines without recording, and the `TIDE_MEDIA_NODE_IP` lines unless `MEDIA_OPTION` is `lb`. For an anonymous installation, set `TIDE_OIDC_ISSUER` to `""` and leave out the other OIDC lines. Keep the volume patch in every mode: the base always mounts it, an anonymous tide just writes nothing there.

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: tide
spec:
  template:
    spec:
      containers:
        - name: tide
          env:
            - name: TIDE_BASE_URL
              value: https://<APP_HOST>
            - name: TIDE_MEDIA_NODE_IP # only for MEDIA_OPTION lb
              value: <NODE_IP>
            - name: TIDE_OIDC_ISSUER
              value: <OIDC_ISSUER>
            - name: TIDE_OIDC_CLIENT_ID
              value: <OIDC_CLIENT_ID>
            - name: TIDE_S3_ENDPOINT
              value: <S3_ENDPOINT>
            - name: TIDE_S3_PUBLIC_ENDPOINT
              value: <S3_PUBLIC_ENDPOINT>
            - name: TIDE_S3_RECORDER_ENDPOINT
              value: <S3_RECORDER_ENDPOINT>
            - name: TIDE_S3_BUCKET
              value: <S3_BUCKET>
            - name: TIDE_S3_REGION
              value: <S3_REGION>
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: tide-data
spec:
  storageClassName: <STORAGE_CLASS> # durable storage; 5Gi is plenty
```

Then add exactly one file for your `MEDIA_OPTION`, and list it in `kustomization.yaml` where the table says:

| `MEDIA_OPTION` | File              | List it under |
| -------------- | ----------------- | ------------- |
| `lb`           | `media-lb.yaml`   | `resources`   |
| `host`         | `media-host.yaml` | `patches`     |
| `tcp`          | `media-tcp.yaml`  | `patches`     |

`media-lb.yaml`: a UDP load balancer. Add your provider's annotation for a static address; that address is `NODE_IP`. Clients that block UDP cannot connect with this option alone. With recording, the recorder beside tide reaches media only through `NODE_IP`, so the cluster must route the load balancer's address back to the pod: check 5 of [Verify](/docs/verify) proves it.

```yaml
apiVersion: v1
kind: Service
metadata:
  name: tide-media
spec:
  type: LoadBalancer
  externalTrafficPolicy: Local
  selector:
    app.kubernetes.io/name: tide
    app.kubernetes.io/component: app
  ports:
    - name: media-udp
      port: 7882
      targetPort: media-udp
      protocol: UDP
```

`media-host.yaml`: tide's media ports on the public node. First run `kubectl label node <NODE_NAME> tide/media=public`. tide finds the node's public address itself and also offers the pod's own, which the recorder uses.

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
```

`media-tcp.yaml`: TCP only, on the public node. Label it as for `host`.

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
```

`ingress.yaml`, for ingress-nginx. Signaling is a WebSocket (a GET with `Upgrade`) on `/rtc` of the same host, so keep the long timeouts.

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: tide
  annotations:
    nginx.ingress.kubernetes.io/force-ssl-redirect: 'true'
    nginx.ingress.kubernetes.io/proxy-body-size: 1m
    nginx.ingress.kubernetes.io/proxy-read-timeout: '3600'
    nginx.ingress.kubernetes.io/proxy-send-timeout: '3600'
spec:
  ingressClassName: nginx
  tls:
    - hosts: [<APP_HOST>]
      secretName: tide-tls
  rules:
    - host: <APP_HOST>
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service: { name: tide, port: { number: 8080 } }
```

Keep the recorder's pinned image: it is released and tested with tide's media server.

## 5. Apply

```sh
# Must print nothing. Each line it prints is a value still missing.
kubectl kustomize deploy/overlays/production | grep -nE '<[A-Z_]+>|example\.com'

kubectl apply -k deploy/overlays/production
kubectl -n tide rollout status deploy/tide --timeout=5m
```

## 6. Rate limits behind the ingress

tide limits joins, sign-ins and lobby waits per client. Behind an ingress every request comes from the ingress, so set `TIDE_TRUSTED_PROXIES` in `tide.yaml` to an IP or CIDR that contains only your ingress controller pods. If you cannot name one, leave it empty: all visitors then share one budget, by default 10 joins a minute, and you may need to raise `TIDE_JOIN_RATE_LIMIT`. Never trust a range other pods can send from.

## Done when

- [ ] `kubectl -n tide rollout status deploy/tide` reports `successfully rolled out`
- [ ] `curl -fsS https://APP_HOST/healthz` prints `ok`
- [ ] `kubectl -n tide logs deploy/tide -c tide` shows `tide listening on`, the mode you meant, and no `refusing to start`

Next: [Verify](/docs/verify).
