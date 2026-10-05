# Deploy tide

tide is a simple, self-hostable video conference service in one executable: the web app, the API and the media server run in one process. Recording adds one more, the recorder.

Agents: the whole runbook is one file at [/llms-full.txt](/llms-full.txt). Every page is also Markdown: add `.md` to its path.

## Modes

Configuration decides what tide is:

| Set                                     | You get                                                     | Runs                  |
| --------------------------------------- | ----------------------------------------------------------- | --------------------- |
| nothing                                 | anyone creates rooms; rooms last 24 hours unused, in memory | tide                  |
| `TIDE_OIDC_ISSUER`                      | hosts sign in; rooms persist                                | tide                  |
| `TIDE_OIDC_ISSUER` + `TIDE_S3_ENDPOINT` | recording                                                   | tide and the recorder |
| … + `TIDE_TRANSCRIPTS=true`             | [transcripts](/docs/transcripts)                            | tide and the recorder |

## Paths

- **One server**: the binary behind Caddy, or Docker Compose with the recorder. Read [One server](/docs/one-server).
- **Kubernetes**: follow these steps in order; each ends with checks that prove it is done.

1. [Prepare](/docs/prepare): collect the hostname, credentials and secrets, and choose a media network path.
2. [Install](/docs/install): build the image, create the secrets, write an overlay, apply it.
3. [Verify](/docs/verify): prove calls, sign-in and recording work.
4. [Operate](/docs/operate): backups, upgrades and incidents.

## What runs

| Component | Source                                               | Replicas  | Keeps state                      |
| --------- | ---------------------------------------------------- | --------- | -------------------------------- |
| tide      | `Dockerfile` in the repository root                  | exactly 1 | SQLite on a volume, with sign-in |
| recorder  | beside tide, with recording (`deploy/k8s/recording`) | with tide | no; recordings go to the bucket  |

tide serves the web app and API, signs hosts in, decides who may join, and carries every meeting's audio and video. Browsers signal through tide's own address and send media straight to it over UDP. The recorder joins a meeting as a hidden participant and writes the file to your bucket.

You bring: one DNS name with TLS, and a way for browsers to reach tide over UDP. For sign-in, an OpenID Connect issuer; for recording, an S3-compatible bucket.

## Rules

- Run exactly one tide replica with the `Recreate` strategy. Its database, lobby and meetings cannot be shared.
- Restarting tide ends every live meeting. Upgrade when none is running.
- Never set `TIDE_DEV_MODE` in production.
- With recording, put tide in a namespace of its own: the recorder needs the `SYS_ADMIN` capability and an unconfined seccomp profile.

## Reference

- [One server](/docs/one-server): tide on a single Linux machine.
- [Configuration](/docs/configuration): every environment variable.
- [Transcripts](/docs/transcripts): optional transcripts made on hosts' own computers.
- [Local development](/docs/local): run tide on a laptop.
