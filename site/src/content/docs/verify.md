# Verify

Goal: prove that calls, sign-in, recording and downloads work. A running pod is not proof.

Requires: the installation from [Install](/docs/install) or [One server](/docs/one-server), and for the checks marked **person**, a person with two devices (and, with sign-in, an account at your OIDC issuer).

## 1. tide answers

```sh
curl -fsS https://<APP_HOST>/healthz
```

Expect `ok`.

## 2. Sign-in is what you meant

```sh
curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' https://<APP_HOST>/api/auth/login
```

With sign-in, expect `302` and a URL on your issuer containing `redirect_uri=https%3A%2F%2F<APP_HOST>%2Fapi%2Fauth%2Fcallback`. A `502` means tide cannot reach the issuer, or `TIDE_OIDC_ISSUER` does not match the issuer's discovery document exactly.

Anonymous, expect `302` and a URL back on `APP_HOST`: tide issued an anonymous session instead.

## 3. Signaling reaches the media server

```sh
curl -sS -o /dev/null -w '%{http_code}\n' https://<APP_HOST>/rtc/validate
```

Expect `401`: the media server answered and wants a token. A `200` means the request got the web app instead, so the path never reached tide's media server.

This proves the route, not the media path.

## 4. A call carries audio and video (person)

Ask a person to open `https://<APP_HOST>` (signing in, if there is sign-in), create a room, and join it from a second device on a **different network**, such as a phone on mobile data. Both must see and hear each other for a minute.

If they connect but see no video, browsers cannot reach `<NODE_IP>` on `7882/udp` (or `7881/tcp`). Recheck the firewall, `TIDE_MEDIA_NODE_IP` and the [media option](/docs/prepare#4-media-network-path).

## 5. A recording lands in the bucket (person)

Only with recording. Ask the person to record 30 seconds of that call, stop, and download it from the dashboard. Then confirm the object:

```sh
aws s3 ls "s3://<S3_BUCKET>/recordings/" --recursive --endpoint-url "<S3_ENDPOINT>"
```

Expect one `.mp4` (or `.ogg` for audio only) with a non-zero size. If the recording shows as failed, read the recorder's log: `kubectl -n tide logs deploy/tide -c recorder`, or `docker compose logs recorder`. If it never leaves "starting", the recorder cannot reach tide: check it runs in tide's network, uses the same Redis as tide, and has the same media key, secret and Redis password. If tide itself won't start with recording, its log names the Redis address it waited for: read the Redis container's log (`kubectl -n tide logs deploy/tide -c redis`, or `docker compose logs redis`). If it fails within a minute, its browser could not reach media: with `TIDE_MEDIA_NODE_IP` set, the recorder must reach that address from inside tide's network ([Prepare](/docs/prepare#4-media-network-path)). If the download fails, `S3_PUBLIC_ENDPOINT` is not reachable from browsers.

## Done when

- [ ] checks 1 to 3 print what is expected
- [ ] a person confirmed a two-way call across networks
- [ ] with recording, a recording reached the bucket and downloaded
- [ ] with sign-in, backups are scheduled ([Operate](/docs/operate))
