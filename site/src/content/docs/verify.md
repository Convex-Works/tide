# Verify

Goal: prove that sign-in, calls, recording and downloads work. A running pod is not proof.

Requires: the installation from [Install](/docs/install), and a person with an account at your OIDC issuer for the checks marked **person**.

## 1. tide answers

```sh
curl -fsS https://<APP_HOST>/healthz
```

Expect `ok`.

## 2. Sign-in reaches the issuer

```sh
curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' https://<APP_HOST>/api/auth/login
```

Expect `302` and a URL on your issuer containing `redirect_uri=https%3A%2F%2F<APP_HOST>%2Fapi%2Fauth%2Fcallback`. A `502` means tide cannot reach the issuer, or `TIDE_OIDC_ISSUER` does not match the issuer's discovery document exactly.

## 3. Signaling answers

```sh
curl -fsS -o /dev/null -w '%{http_code}\n' https://<MEDIA_HOST>/
```

Expect `200`. This proves the ingress route, not the media path.

## 4. A call carries audio and video (person)

Ask a person to sign in at `https://<APP_HOST>`, create a room, and join it from a second device on a **different network**, such as a phone on mobile data. Both must see and hear each other for a minute.

If they connect but see no video, browsers cannot reach `<NODE_IP>` on `7882/udp` (or `7881/tcp`). Recheck the firewall, the `--node-ip` argument and the [media option](/docs/prepare#4-media-network-path).

## 5. A recording lands in the bucket (person)

Ask the person to record 30 seconds of that call, stop, and download it from the dashboard. Then confirm the object:

```sh
aws s3 ls "s3://<S3_BUCKET>/recordings/" --recursive --endpoint-url "<S3_ENDPOINT>"
```

Expect one `.mp4` (or `.ogg` for audio only) with a non-zero size. If the recording shows as failed, read `kubectl -n tide logs deploy/recorder`. If it never leaves "starting", the recorder cannot reach the media server or tide. If the download fails, `S3_PUBLIC_ENDPOINT` is not reachable from browsers.

## Done when

- [ ] checks 1 to 3 print what is expected
- [ ] a person confirmed a two-way call across networks
- [ ] a recording reached the bucket and downloaded
- [ ] backups are scheduled ([Operate](/docs/operate))
