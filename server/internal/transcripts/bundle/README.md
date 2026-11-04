# transcribe

Meeting transcription with speaker labels, run on the machine of someone who
lent it: who said what, and when. This is the bundle tide sends when a
meeting's recording is ready, and the example to copy for a real, model-heavy
moil job.

```sh
moil run examples/bundles/transcribe --input audio.ogg=meeting.ogg \
    --output transcript.json --output transcript.vtt --output transcript.txt
```

The first run downloads 2.9 GB of model weights into moil's asset cache,
checking each file's hash, and builds a 1 GB Python environment. Later runs
start in seconds.

Only `job.py`, `job.py.lock` and `manifest.json` are the bundle. moil
ignores this README and `test_job.py`, so they aren't part of the bundle
hash.

## How it works

1. **Decode.** PyAV, whose wheels bundle FFmpeg, turns the first audio track
   into 16 kHz mono. Anything FFmpeg reads works: OGG/Opus and MP4/AAC from
   LiveKit Egress, WebM, WAV, MP3. A recording that breaks off partway is
   transcribed up to the break.
2. **Diarize.** [Nemotron 3 Diarization] gives, for every 10 ms, the
   probability that each of up to 8 speakers is talking. It walks the
   recording in 30 s chunks and carries a cache of each speaker's voice
   between them, so memory stays flat and a speaker keeps their label for
   the whole meeting.
3. **Transcribe.** Only the stretches where someone speaks are transcribed,
   in chunks of up to 30 s cut at the quietest moment, 8 chunks at a time,
   by [Parakeet TDT 0.6B v3]. It detects the language itself (25 European
   languages), punctuates, and times every token.
4. **Attribute.** Each word goes to the speaker whose probability of
   speaking, summed over the word, is highest. Consecutive words from one
   speaker with no pause over 1.5 s form an utterance. Each utterance is
   streamed as a `data` event as soon as the next one begins.

With a timeline of who was active, speakers get names between steps 2 and 3
([Speaker names](#speaker-names)). The diarization, the words and the
utterances don't change.

## Input

The recording, under any name. If a job has other inputs besides the
timeline, `params.input` says which one is the recording.

An input named exactly `speakers.json` is the optional timeline, and doesn't
count as a recording:

```json
{"v": 1, "participants": [{"name": "Ada", "spans": [[12.4, 18.0], [31.2, 33.9]]}]}
```

Spans are `[start, end]` in seconds from the start of the recording file.
Participants may repeat a name: their spans are merged. Fields other than
these are ignored.

Without this input, the outputs and events are byte for byte those of
version 0.1.0. A timeline that can't be used (bad JSON, another shape, a `v`
other than 1, a span that isn't two finite numbers in order) never fails the
job: it logs one warning, and the job carries on as if there were none. One
bad entry rejects the whole file.

```sh
moil run examples/bundles/transcribe \
    --input audio.ogg=meeting.ogg --input speakers.json=timeline.json \
    --output transcript.txt
```

## Params

All optional.

| Param | Meaning |
|---|---|
| `input` | The name of the input holding the recording, when the job has several. It can't be `speakers.json`. |
| `max_speakers` | At most this many speakers, 1 to 8 (default 8), for example the meeting's participant count. When the diarizer hears more, the ones who speak least are dropped and their words go to the likeliest of the rest. |
| `device` | `mps`, `cuda`, `cpu` or `auto` (the default: MPS, then CUDA, then CPU). |

## Outputs

Each output the job declares is written in the format its extension
names. A declared output with any other extension fails the job before it
starts.

**`.json`**: the full transcript. Times are in seconds from the start of
the recording. Speakers are `S1`, `S2`… in the order they first speak. A
speaker the timeline names also has a `name`, in `speakers` and in each of
their utterances. Speakers who share a name keep their own ids.

```json
{
  "duration": 1049.35,
  "speakers": [{"id": "S1", "speaking_seconds": 338.81, "name": "Ada"}, …],
  "utterances": [
    {
      "start": 10.98, "end": 14.34, "speaker": "S1", "name": "Ada",
      "text": "Are we we're not allowed to do the lights so people can see that a bit better?",
      "words": [{"start": 10.98, "end": 11.14, "text": "Are"}, …]
    }, …
  ]
}
```

Parakeet detects the language but doesn't report it, so the transcript
doesn't name one.

**`.vtt`**: WebVTT captions of up to two lines, each cue tagged with its
speaker as a voice (`<v Speaker 1>`, or `<v Ada>` for a named speaker).

**`.txt`**: one line per utterance: `[00:01:20] Speaker 1: Okay. Hello everybody.`

In `.vtt` and `.txt`, a speaker who is alone on a name is labelled `Ada`.
Several speakers on one name are `Ada (1)`, `Ada (2)`, in the order they
first speak. The rest stay `Speaker 2`, with the number of their id.

## Events

| Event | When |
|---|---|
| `log` | The device chosen, the audio's length, how many speakers were heard, how much speech there is to transcribe. |
| `progress` | Decoding (0–5%), finding speakers (5–15%), transcribing, one step per batch of 8 chunks (15–97%), writing the outputs. |
| `data` | `{"utterance": {"start", "end", "speaker", "text"}}` for each utterance, in order, for a live transcript. A named speaker's utterances also have `name`, from the first one on, since matching only needs diarization and is done before transcription starts. |
| `result` | The files written, and `meta`: `duration`, `speakers` (how many), `utterances`, `words`, `device`, `seconds` spent per stage (`decode`, `diarize`, `transcribe`, `write`, `total`) and, with a usable timeline, `speaker_map`: `[{"id": "S1", "name": "Ada"}]`, listing only speakers in the transcript who got a name, and empty if none did. Names here are never numbered: two speakers on Ada are two entries saying `Ada`. |
| `error` | A sentence saying what went wrong. Out of memory, or a requested device the machine lacks, is `retryable`. An unreadable recording or a bad param isn't. |

## Speaker names

Diarization decides who the speakers are. The timeline only says what to
call them, so a timeline can't split, merge or move anyone's words.

Each 10 ms frame of speech counts for the kept speaker the transcript would
give it to, the likeliest one. For each speaker and each name, the job sums
the seconds of that speech inside the name's spans. A speaker takes their
best name only if all three hold:

- at least `MIN_NAME_SECONDS` (2) of overlap with it,
- at least `MIN_NAME_SHARE` (60%) of their overlap with all names, and
- at least `MIN_NAME_COVERAGE` (25%) of all their speech, so that a voice the
  timeline never saw, such as a shared video's, can't take the name of someone
  who talked over it.

Otherwise they stay `Speaker N`: an unknown label beats a wrong name. All three
numbers are starting points, not yet tuned on real recordings.

The match is for a whole speaker, never for a word. LiveKit samples who is
active every 400 ms with smoothing, too coarsely to say who said a word, but
enough to vote over a meeting. It isn't one to one either: two voices on one
mic, or one person the diarizer split, can both take the same name.

Its limits:

- Anyone with under 2 s of speech stays unnamed, and so can people who keep
  talking over each other.
- A voice on someone else's mic gets that person's name, because the mic is
  all the timeline knows about. So can anyone the timeline missed, if their
  speech mostly falls inside someone else's spans.
- The timeline has to line up with the file. Spans that are off by more than
  a moment lower the shares, and more speakers stay unnamed.
- A speaker the diarizer merged, as can happen beyond 8 people, covers several
  names and usually stays unnamed. So does a speaker holding the speech of
  people dropped by `max_speakers`.
- People with the same display name count as one. Nothing checks a name
  against a voice, and anyone can call themselves anything, including
  `Speaker 2` or `Ada (1)`.

Names are untrusted, since guests type their own. Control and format
characters, bidi ones among them, are removed, keeping the zero-width joiners
that Persian names and emoji need. Whitespace collapses to single spaces, and
names stop at 64 characters. A name with nothing left has no label, but its
spans still count, so a nameless guest's speech can't be pinned on someone
else. Voice tags in `.vtt` escape `&`, `<` and `>`, so a name can't close the
tag or pass for a cue timing. Treat names in the outputs as you would any
text from the meeting.

## Speed, size and memory

Measured on an M3 Max (36 GB) with a 4-speaker meeting ([AMI] ES2004a,
17.5 minutes), and the same meeting five times over (87 minutes):

| Recording | Device | Decode | Diarize | Transcribe | Total | Real time |
|---|---|---|---|---|---|---|
| 17.5 min | MPS | 1.8 s | 2.3 s | 18.9 s | 23.0 s | 46× |
| 17.5 min | CPU | 1.8 s | 6.1 s | 50.7 s | 58.6 s | 18× |
| 87 min | MPS | 8.0 s | 9.2 s | 92.5 s | 109.8 s | 48× |
| 87 min | CPU | 8.1 s | 30.2 s | 261.9 s | 300.2 s | 17× |

Diarization and transcription times include loading each model. These
are warm runs; the first run after the download took a few seconds longer.

| Download | Size |
|---|---|
| Parakeet TDT 0.6B v3 weights | 2.5 GB |
| Nemotron 3 Diarization weights | 397 MB |
| Python environment (torch, Transformers, PyAV…) | 1.0 GB on macOS; more on Linux, where torch brings CUDA libraries |

Peak memory (resident set) was 6.9 GB for the 87-minute recording on MPS,
7.3 GB for the 17.5-minute one on CPU and 9.3 GB for the 87-minute one on
CPU, so the manifest asks for 12 GiB.

## Accuracy

On ES2004a, 97.9% of words go to the right speaker, measured against the
[AMI] corpus's manual word-level annotations, and all four speakers keep
their labels across the 87-minute recording. What goes wrong is mostly
overlapping speech. The transcript follows one voice, so a quiet "mm" under
someone else's sentence goes unwritten, and the word it covers can go to
the murmurer.

Some other limits:

- At most 8 speakers. Without a timeline, speakers are anonymous: mapping
  `S1` to a participant is up to the service.
- Only the 25 languages Parakeet knows. Expect a poor transcript for
  anything else.

## Models, licenses and attribution

The weights aren't in the bundle. Each machine downloads them from Hugging
Face, at the pinned commits in `manifest.json`, and checks their SHA-256.

**Nemotron 3 Diarization** by NVIDIA,
[`nvidia/Nemotron-3-Diarization`](https://huggingface.co/nvidia/Nemotron-3-Diarization),
under the [OpenMDW License Agreement, version 1.1](https://openmdw.ai/license/1-1/).
Commercial use is allowed. Anyone who *distributes* the model, for example
by mirroring the weights and pointing the manifest at the mirror, must
include a copy of the agreement and keep NVIDIA's copyright notices and
notices of origin. The agreement puts no restrictions on outputs such as
transcripts. Rights end for anyone who sues claiming the model infringes a
patent or copyright. This is not the gated `Nemotron-3-Diarization-preview`
repository, whose license allows evaluation only.

**Parakeet TDT 0.6B v3** by NVIDIA,
[`nvidia/parakeet-tdt-0.6b-v3`](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3),
under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/). Commercial
use is allowed with attribution. Wherever you share the model or credit it,
use a line such as:

> Speech recognition: "parakeet-tdt-0.6b-v3" by NVIDIA, licensed under
> CC BY 4.0 (https://creativecommons.org/licenses/by/4.0/). Speaker
> diarization: "Nemotron-3-Diarization" by NVIDIA, licensed under
> OpenMDW-1.1 (https://openmdw.ai/license/1-1/).

If you change the weights, say so. A service built on this bundle, such as
tide, does well to show that credit too, for example on an about page.

This is a summary, not legal advice. The license texts are what count.

## Platforms

- **macOS on Apple silicon**: verified, on the GPU through MPS, and on the
  CPU. Intel Macs aren't supported, since torch no longer ships wheels for
  them.
- **Linux**: the lockfile's torch is the PyPI build, which bundles CUDA 13,
  so an NVIDIA GPU with a recent driver is used automatically. Without one,
  the job runs on the CPU.
- **Windows**: torch from PyPI is CPU-only on Windows. Using an NVIDIA GPU
  would need torch from PyTorch's own package index, declared in the
  script's metadata and locked again.

## Changing the bundle

Any change, even to a comment in `job.py`, changes the bundle hash, and
every machine owner must approve it again. `test_job.py` runs the real
`main()` and writers over scripted meetings, with no models and no torch.
`make check` runs it, or on its own:

```sh
uv run --script test_job.py
```

After editing the dependencies:

```sh
uv lock --script job.py
moil check .
```

Transformers is pinned to a GitHub commit, because Nemotron 3 Diarization
is newer than the latest release (5.17.0). When a release includes it,
depend on that release instead. The pinned commit prints a harmless
`[ERROR] … image_like_kwargs …` docstring warning to stderr on import.

[Nemotron 3 Diarization]: https://huggingface.co/nvidia/Nemotron-3-Diarization
[Parakeet TDT 0.6B v3]: https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3
[AMI]: https://groups.inf.ed.ac.uk/ami/corpus/
