# Install

Goal: tide, the media server, the recorder and redis running in the `tide` namespace, reachable at `APP_HOST` and `MEDIA_HOST`.

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
# The recorder needs SYS_ADMIN and an unconfined seccomp profile.
# The namespace holds only tide, so allow it there and nowhere else.
kubectl label namespace tide pod-security.kubernetes.io/enforce=privileged
```

## 3. Create the secrets

Secret names and keys must match exactly; the manifests reference them.

```sh
kubectl -n tide create secret generic tide-secrets \
  --from-literal=session-secret="$SESSION_SECRET" \
  --from-literal=oidc-client-secret="$OIDC_CLIENT_SECRET"

kubectl -n tide create secret generic media-secrets \
  --from-literal=api-key="$MEDIA_API_KEY" \
  --from-literal=api-secret="$MEDIA_API_SECRET" \
  --from-literal=keys="$MEDIA_API_KEY: $MEDIA_API_SECRET"

kubectl -n tide create secret generic s3-secrets \
  --from-literal=access-key="$S3_ACCESS_KEY" \
  --from-literal=secret-key="$S3_SECRET_KEY"
```

Also create `tide-tls`, a TLS secret covering `APP_HOST` and `MEDIA_HOST`, with cert-manager or `kubectl create secret tls`.

## 4. Write the overlay

The base in `deploy/k8s/` is not deployable as is: every line marked `# OVERLAY:` needs a value. Create `deploy/overlays/production/` with these files and replace every `<PLACEHOLDER>` with its value from 01.

`kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: tide
resources:
  - ../../k8s
  - ingress.yaml
images:
  - name: registry.example.com/tide
    newName: <REGISTRY>/tide
    newTag: <VERSION>
patches:
  - path: tide.yaml
  - path: media.yaml
  - path: recorder.yaml
  # and media-lb.yaml under resources, or media-host.yaml / media-tcp.yaml here
```

`tide.yaml`:

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
            - name: TIDE_MEDIA_PUBLIC_URL
              value: wss://<MEDIA_HOST>
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

`recorder.yaml`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: recorder-config
data:
  config.yaml: |
    ws_url: ws://media:7880
    redis:
      address: redis:6379
    health_port: 8081
    s3:
      region: <S3_REGION>
      endpoint: <S3_RECORDER_ENDPOINT>
      bucket: <S3_BUCKET>
      force_path_style: true
```

`media.yaml` signs the media server's webhooks with `MEDIA_API_KEY` (never the secret) and advertises `NODE_IP`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: media-config
data:
  config.yaml: |
    port: 7880
    rtc:
      tcp_port: 7881
      udp_port: 7882
      use_external_ip: false
    redis:
      address: redis:6379
    webhook:
      api_key: <MEDIA_API_KEY>
      urls:
        - http://tide:8080/api/webhooks/media
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: media
spec:
  template:
    spec:
      containers:
        - name: media
          args: ['--config', '/etc/media/config.yaml', '--node-ip', '<NODE_IP>']
```

Then add exactly one file for your `MEDIA_OPTION`, and list it in `kustomization.yaml` where the table says:

| `MEDIA_OPTION` | File              | List it under |
| -------------- | ----------------- | ------------- |
| `lb`           | `media-lb.yaml`   | `resources`   |
| `host`         | `media-host.yaml` | `patches`     |
| `tcp`          | `media-tcp.yaml`  | `patches`     |

`media-lb.yaml`: a UDP load balancer. Add your provider's annotation for a static address; that address is `NODE_IP`. Clients that block UDP cannot connect with this option alone.

```yaml
apiVersion: v1
kind: Service
metadata:
  name: media-udp
spec:
  type: LoadBalancer
  externalTrafficPolicy: Local
  selector:
    app.kubernetes.io/name: media
    app.kubernetes.io/component: media
  ports:
    - name: ice-udp
      port: 7882
      targetPort: ice-udp
      protocol: UDP
```

`media-host.yaml`: run on the public node's network. First run `kubectl label node <NODE_NAME> tide/media=public`.

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: media
spec:
  template:
    spec:
      hostNetwork: true
      dnsPolicy: ClusterFirstWithHostNet
      nodeSelector:
        tide/media: public
```

`media-tcp.yaml`: TCP only, on the public node. Label it as for `host`.

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: media
spec:
  template:
    spec:
      nodeSelector:
        tide/media: public
      containers:
        - name: media
          ports:
            - containerPort: 7881
              hostPort: 7881
              protocol: TCP
```

`ingress.yaml`, for ingress-nginx. The media host carries WebSockets, so keep the long timeouts.

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
    - hosts: [<APP_HOST>, <MEDIA_HOST>]
      secretName: tide-tls
  rules:
    - host: <APP_HOST>
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service: { name: tide, port: { number: 8080 } }
    - host: <MEDIA_HOST>
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service: { name: media, port: { number: 7880 } }
```

Keep the pinned media server, recorder and redis images: they are released and tested together.

## 5. Apply

```sh
# Must print nothing. Each line it prints is a value still missing.
kubectl kustomize deploy/overlays/production | grep -nE '<[A-Z_]+>|example\.com|replace-with'

kubectl apply -k deploy/overlays/production
for d in redis media recorder tide; do kubectl -n tide rollout status deploy/$d --timeout=5m; done
```

## 6. Rate limits behind the ingress

tide limits joins, sign-ins and lobby waits per client. Behind an ingress every request comes from the ingress, so set `TIDE_TRUSTED_PROXIES` in `tide.yaml` to an IP or CIDR that contains only your ingress controller pods. If you cannot name one, leave it empty: all visitors then share one budget, by default 10 joins a minute, and you may need to raise `TIDE_JOIN_RATE_LIMIT`. Never trust a range other pods can send from.

## Done when

- [ ] all four Deployments report `successfully rolled out`
- [ ] `curl -fsS https://APP_HOST/healthz` prints `ok`
- [ ] `kubectl -n tide logs deploy/tide` shows `tide listening on` and no `refusing to start`

Next: [Verify](/docs/verify).
