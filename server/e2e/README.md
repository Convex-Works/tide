# Transcripts end to end, with the real moil

The transcripts pipeline (ARCHITECTURE.md §8.1) as people run it: tide
composed as `main` composes it, machines running the real `moil` binary
and real uv, and real S3 storage. The other transcript tests use moil's
in-process fake machine (`moiltest`); these check that the real one agrees
with it.

```sh
make moil-e2e                          # builds moil from ../moil, then runs the suite
make moil-e2e MOIL_REPO=~/src/moil     # moil checked out elsewhere
cd server && MOIL_BIN=/path/to/moil go test -race -count=1 ./e2e/
```

Without `MOIL_BIN` the tests skip, so `go test ./...`, `make check` and CI
stay green without moil. The suite takes about ten seconds, its tests
running in parallel.

It needs:

- a `moil` binary (`cargo build --release -p moil-cli` in moil's repository);
- [uv](https://docs.astral.sh/uv/): `$MOIL_UV`, on `PATH`, or in
  `~/.local/bin`. The stub needs Python 3.12 or newer, which uv downloads
  the first time unless it manages one already;
- Docker, to run the MinIO image `deploy/compose.yaml` pins on a random
  loopback port. MinIO no longer publishes images, so the first run builds
  it from source (`deploy/minio.Dockerfile`), which takes a minute or two. To use another store instead, set `TIDE_E2E_S3_ENDPOINT`,
  `TIDE_E2E_S3_ACCESS_KEY` and `TIDE_E2E_S3_SECRET_KEY` (and optionally
  `TIDE_E2E_S3_BUCKET`, default `tide-e2e`, and `TIDE_E2E_S3_REGION`).
  Machines take plain http only from loopback, so it must be https or on
  127.0.0.1.

## What it proves

| Test | Story |
|---|---|
| `TestAHostPairsAMachineAndGetsTranscripts` | `moil pair` shows a code; the host finds the machine under it, with the moil address it pairs with, and confirms, with a real session and CSRF header, and `moil pair` succeeds. `moil review` lists the bundle `/machines` names, the owner approves it with `moil approve`, and once `moil agent` runs, `/machines` shows it idle and approved. A forged `egress_ended` webhook is refused; LiveKit's signed one completes the recording and sends it to the machine. The transcript is `running` (held mid-job), then `completed`, and downloads from S3, named like the recording, holding the recording's SHA-256. Deleting the recording leaves nothing under its prefix. Unpairing mid-job ends the job's process; the agent logs that tide removed it and never reconnects, and `moil review` says the service no longer accepts the machine. |
| `TestTranscriptsSurviveATideRestart` | tide stops as `main` does (`httpapi.Serve`: HTTP drains, moil closes, the background work returns, then the database closes) while the machine is on a transcript, and a new tide starts on the same database and address. The agent reconnects, drops the attempt the old tide gave it and ends its process, and runs the resubmitted job, which completes. The transcript row goes from `pending` to `completed`, and nothing in between. |
| `TestOnlyTheOwnersMachinesGetTheJob` | Bob's machine is paired, approved and idle while alice's recording waits for her offline laptop, well past moil's bid window; it never starts an attempt. Alice's laptop comes online and transcribes it. Bob's machine then transcribes bob's own recording, so it could have. |
| `TestARealMeetingIsTranscribed` | Opt-in: with `TIDE_E2E_MEETING` naming a meeting recording, tide publishes the real transcribe bundle, and a real machine transcribes the recording with the real models, from `egress_ended` to downloadable text (a timed, labelled utterance per line) and WebVTT captions. See below. |
| `TestTheStubIsABundleMoilAccepts` | `moil check` accepts the stub bundle, and `moil hash` agrees with the Go SDK's hash of it. |

## Transcribe a real meeting

`TestARealMeetingIsTranscribed` runs only when asked, because the real
bundle's first run downloads 2.9 GB of models and builds a 1 GB Python
environment:

```sh
TIDE_E2E_MEETING=~/meetings/ES2004a.ogg \
TIDE_E2E_MOIL_CACHE=~/Library/Caches/moil \
TIDE_E2E_MEETING_SPEAKERS=4 \
  make moil-e2e
```

`TIDE_E2E_MOIL_CACHE` lends the machine a moil cache that already holds
the models and environment (`moil run` keeps one there), and
`TIDE_E2E_MEETING_SPEAKERS`, if set, is how many speakers it must find. On
an M3 Max, the 17.5-minute, four-speaker [AMI] meeting ES2004a is
transcribed 32 seconds after egress ends, with all four speakers found. Run
it whenever the vendored bundle changes.

[AMI]: https://groups.inf.ed.ac.uk/ami/corpus/

## How it's built

- `tide_test.go`: tide in this process: `httpapi.New` behind an
  `http.Server` with `main`'s timeouts on 127.0.0.1, run by `httpapi.Serve`
  as `main` runs it, with SQLite on disk, plus `Restart`. Hosts use the API
  as the SPA does. Recordings are made as `recording.Start` and Egress would
  make them: the row, the file in S3 at the key Egress is given, and
  webhooks signed as LiveKit signs them.
- `machine_test.go`: a machine driven with the moil command line, as moil's
  own end-to-end tests drive it: its own `MOIL_HOME`, `MOIL_SECRETS=file`,
  `moil pair`, `moil approve`, `moil agent`.
- `testdata/transcribe-stub`: the bundle tide publishes in these tests,
  since the real one downloads 2.9 GB of models. It keeps the real one's
  contract (one input, outputs by extension, progress events, `speakers` in
  the result's meta), and writes the input's SHA-256 and size instead of a
  transcript. It holds mid-job while a `hold` file exists, and records each
  attempt it starts in `attempts.log`, both in `$TRANSCRIBE_STUB_DIR`: each
  machine's uv is a wrapper that sets it. Change it, then run
  `uv lock --script job.py` and `moil check` there.
- `s3_test.go`: MinIO in Docker, or the store the environment names.

Tests wait for conditions with deadlines, never for fixed times, except
where they check that something doesn't happen. Should the test binary die
before its cleanups run, as when `-timeout` fires, a reaper process removes
the MinIO container and kills every process the tests started.
