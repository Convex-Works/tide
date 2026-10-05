# Transcripts

Optional. A host pairs their own computer with tide; recordings of rooms they own are transcribed there, never on the server. Transcripts come back as plain text and WebVTT with speaker labels, stored beside the recording.

Off by default. The desktop app that runs the jobs, moil, is alpha: macOS only, unsigned, and jobs run unsandboxed with the host's permissions. Turn it on only if the hosts accept that.

## Turn it on

1. Grant the bucket key `GetObject`, `PutObject` and `DeleteObject` on `transcripts-staging/*`. Optionally expire that prefix after 8 days; tide cleans it itself.
2. Make sure `TIDE_S3_PUBLIC_ENDPOINT` is HTTPS and reachable from hosts' own networks. Machines refuse plain HTTP.
3. Make sure `TIDE_BASE_URL` is exactly the address hosts open, scheme included. Pairing fails from any other address.
4. The ingress must pass WebSocket upgrades on `/moil/v1/connect` and `POST /moil/v1/pair` with `Content-Type: application/json`. The ingress in [Install](/docs/install) does both.
5. Set `TIDE_TRANSCRIPTS=true` in the overlay's `tide.yaml` and apply.

Done when `https://<APP_HOST>/machines` loads for a signed-in host.

## For hosts

Open `/machines` and choose **Add a machine**, or use the moil command line:

```sh
moil pair https://<APP_HOST>/moil   # confirm the code on the page it opens
moil review tide                    # read the transcription bundle
moil approve tide <hash>            # approve exactly that code
moil agent                          # take jobs until stopped
```

The first job downloads about 2.9 GB of models; transcribing uses up to 10 GB of memory. A host can pair up to 10 machines.

## Turn it off

Set `TIDE_TRANSCRIPTS=false`. Nothing is deleted. When turned back on, transcripts that waited more than 14 days fail and can be requested again, and recordings made while it was off are queued for hosts who had a machine paired.
