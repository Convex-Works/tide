# Transcripts

A completed recording can carry a transcript with speaker labels, made by
[moil](https://git.convex.works/ConvexWorks/moil) on a computer the **room's
owner** paired with tide: never on the server, and never on anyone else's machine.
The owner can already download the recording, so transcription adds no new reader
of the audio.

Everything here sits behind `TIDE_MOIL`, off by default because moil is alpha
(macOS only, unsigned, no sandbox).

## Turning moil back on

This is not a resume, because time kept passing and recordings kept ending:

- A `pending` row's 14 days count from its request, so an older row fails on the
  first pass. A host can request it again.
- The first pass creates `pending` rows for recordings that completed meanwhile,
  when their room's owner had a machine by the time each ended, so those machines
  get that backlog. With moil off no observer ran, so those transcripts have no
  names.

## Machines

A host pairs a computer with moil's device flow (RFC 8628). The app opens
`/machines?code=XXXX-XXXX`, the host checks the machine's name and the moil address
the app is pairing with, and confirms. The moil SDK (`server/third_party/moil`)
serves the machine side under `/moil/`, and each machine keeps one WebSocket open
to `/moil/v1/connect`.

A machine state tide doesn't know, which only a newer moil SDK could report, shows
as `busy`, since moil offers jobs only to idle machines. Text machines report is
shown without control or format characters.

## The bundle

tide publishes one moil bundle, `transcribe`, from
`server/internal/transcripts/bundle/`, and the binary embeds it. Machine owners
read and approve it by hash in the moil app. A test pins that hash: a new hash asks
every owner to review and approve again, so it only ever changes on purpose. The
bundle runs with the owner's permissions and no sandbox.

Its [README](../../server/internal/transcripts/bundle/README.md) documents its
inputs, outputs, speaker matching and limits.

**Names are written by the bundle, not by tide,** because hooks get the machine's
own outputs. Diarization decides who the speakers are, and the timeline only says
what to call them. Names are whatever people typed, and nothing checks a name
against a voice. tide reads the integer `meta.speakers`.

## Intent and projection

The `transcripts` table is the source of truth: one row per recording that should
have a transcript. A moil job is a disposable projection of a `pending` row, the
way recording rows are reconciled against Egress. The reconciler runs at startup,
every minute and whenever it is nudged:

1. It creates a `pending` row for every completed recording with a file and no row,
   when the room's owner had a machine paired by the time the recording ended.
   Pairing is the opt-in; hosts without a machine never see transcripts.
2. It submits a job for every `pending` row it isn't following yet, with job ID
   `recording-<id>`. moil treats resubmitting a live ID as a no-op, so a restart,
   which loses moil's in-memory jobs, just submits them again.
3. It cancels every job whose row is gone or no longer `pending`.

A job is eligible only for the room owner's machines, and `Prepare` checks again
that the machine taking an attempt belongs to the room's current owner. An
administrator can request a transcript for a room they manage, but it still runs
on that owner's machines.

How jobs end, in the cases that aren't obvious:

- A machine that ends a job as cancelled when tide didn't cancel it fails the row,
  rather than have tide submit it again as fast as the machine answers.
- If tide can't save a transcript a machine made, it tries again every pass until
  a deadline, then fails the row, blaming its storage.
- tide cancelling a job, and server shutdown, leave the row as it is.
- If recording a job's end fails, the row stays `pending` and each pass tries to
  record the end again rather than run the job again. An end tide couldn't record
  by the time it stopped leaves the row `pending`, and the next start submits the
  job again.

## Job params are a contract

A job's title is the room's name. Its params are the recording's times, for the
owner's hooks, which a machine runs after a job succeeds (moil's
`spec/machine.md` §2). A hook filing meeting notes can tell when the meeting was.

```json
{"recording": {"started_at": "2026-09-27T22:10:00Z", "ended_at": "2026-09-27T22:52:14Z", "duration_s": 2529}}
```

Changing this shape breaks the hooks that read it. The bundle itself ignores it.
The params say nothing more about the room than the title does: participants' names
reach the machine through the `speakers.json` input, never through the params.

## Files

**URLs are minted when a machine takes an attempt,** not at submission, because a
job can wait days for a laptop to wake. A missing speaker timeline just means no
such input, and so does a 403, which S3 answers for a missing object when the key
lacks `ListBucket`. Any other `Stat` failure puts the job back in the queue, because
the attempt would otherwise lose the names for good.

**Machines never write where tide serves from.** Each attempt uploads to a staging
directory of its own, under `transcripts-staging/`. When the job succeeds, tide
checks each file against what moil says the machine uploaded, before asking storage
anything; checks storage has it at that size; copies it beside the recording under
the recording's basename, as a video player expects sidecar captions; checks the
copy's size too, since storage that reports no entity tag copies whatever is staged
by then; and removes the staged copy.

A staging directory is unique by its random part, because moil numbers the attempts
of a resubmitted job from 1 once it has forgotten the last run. A machine that takes
a job again shortly after letting it go gets the same directory, so a machine that
keeps doing so costs tide at most one directory per reuse window.

**Nothing outlives its recording.** `Prepare` queues each staging key for removal
when its URL expires, so a late upload from a machine that lost tide but not S3 is
removed too. A job knows from its submission where its files go beside the
recording, so a transcript copied beside a recording deleted meanwhile is removed
again, however tide's work on it ends ([removing files](recording.md#removing-files)).

## Status

`RecordingInfo.transcript` combines the row with the live job. `available` means the
reconciler won't request one on its own, because the recording ended before the
owner paired a machine. `waiting` carries a `message` saying why no machine is on it.

Transcripts appear only in recording management. There is nothing in the meeting
itself: no live captions, summaries or editing.
