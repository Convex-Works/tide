# Deploy tide

tide is a simple, self-hostable video conference service. This runbook takes a Kubernetes cluster to a working installation. Follow the steps in order; each one ends with checks that prove it is done.

Agents: the whole runbook is one file at [/llms-full.txt](/llms-full.txt). Every page is also Markdown: add `.md` to its path.

## Steps

1. [Prepare](/docs/prepare): collect hostnames, credentials and secrets, and choose a media network path.
2. [Install](/docs/install): build the image, create the secrets, write an overlay, apply it.
3. [Verify](/docs/verify): prove sign-in, calls and recording work.
4. [Operate](/docs/operate): backups, upgrades and incidents.

## What you will run

| Component    | Source                              | Replicas  | Keeps state                     |
| ------------ | ----------------------------------- | --------- | ------------------------------- |
| tide         | `Dockerfile` in the repository root | exactly 1 | SQLite on a persistent volume   |
| media server | `deploy/k8s/media.yaml`             | 1         | no                              |
| recorder     | `deploy/k8s/recorder.yaml`          | 1         | no; recordings go to the bucket |
| redis        | `deploy/k8s/redis.yaml`             | 1         | no; never persist or back it up |

tide serves the web app and API, signs hosts in, decides who may join, and starts and stops recordings. Browsers send audio and video to the media server directly. The recorder joins a meeting as a hidden participant and writes the file to your bucket.

You bring: an OpenID Connect issuer, an S3-compatible bucket, two DNS names with TLS, and a way for browsers to reach the media server over UDP.

## Rules

- Run exactly one tide replica with the `Recreate` strategy. Its database and in-memory lobby cannot be shared.
- Never set `TIDE_DEV_MODE` in production.
- Put tide in a namespace of its own: the recorder needs the `SYS_ADMIN` capability and an unconfined seccomp profile.

## Reference

- [Configuration](/docs/configuration): every environment variable.
- [Transcripts](/docs/transcripts): optional transcripts made on hosts' own computers.
- [Local development](/docs/local): run tide on a laptop.
