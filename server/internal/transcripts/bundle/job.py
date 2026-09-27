# /// script
# requires-python = ">=3.12,<3.13"
# dependencies = [
#   "av==18.1.0",
#   "librosa==1.0.0",
#   "numpy==2.5.3",
#   "torch==2.14.0",
#   # Nemotron 3 Diarization is newer than the latest Transformers release
#   # (5.17.0), so this pins the commit the bundle was verified with. Swap in
#   # the first release that includes it once there is one.
#   "transformers @ https://github.com/huggingface/transformers/archive/96331a9f93b72697f160a958d2883d4b49a56739.tar.gz",
# ]
# ///
"""transcribe: meeting transcription with speaker labels.

Turns a meeting recording into a transcript of who said what, and when,
on the machine it runs on. Two NVIDIA models do the work: Nemotron 3
Diarization finds who speaks when (up to 8 speakers), Parakeet TDT 0.6B v3
transcribes the speech (25 European languages, detected on its own), and
each word goes to whoever was speaking at the time. Try it:

    moil run examples/bundles/transcribe --input audio.ogg=meeting.ogg \\
        --output transcript.json --output transcript.vtt --output transcript.txt

The job's one input is the recording, in any format FFmpeg reads: OGG/Opus
and MP4/AAC from LiveKit Egress, WebM, WAV, MP3... Params, all optional:

    input         which input is the recording, when the job has several
    max_speakers  at most this many speakers, 1 to 8: the meeting's
                  participant count, say
    device        "mps", "cuda" or "cpu" instead of the fastest available

Each output the job declares is written in the format its extension names:
.json is the full transcript with word timings, .vtt WebVTT captions with
speakers as voice tags, .txt plain text. While the job runs, every
utterance is also sent as a data event, so a service can show the
transcript as it grows.

The models (2.9 GB) are assets in manifest.json, pinned by hash: the
runtime downloads them once, checks them and links them into the job's
assets directory. The script only ever loads them from there.
"""

import json
import os
import sys
import time
import traceback
import warnings

# Events go to stdout, one JSON object per line. Libraries print there too
# now and then (this Transformers build lints its own docstrings on import),
# so everything else printed goes to stderr, which the runtime keeps for
# error reports, and stdout is left to events.
EVENTS = sys.stdout
sys.stdout = sys.stderr

# Transformers would otherwise ask the Hugging Face hub about newer model
# files. Everything the script needs is in its assets, so it stays offline,
# and quiet: no progress bars, no warning per batch about a length limit
# that Parakeet derives from each chunk's length anyway.
os.environ["HF_HUB_OFFLINE"] = "1"
os.environ["HF_HUB_DISABLE_TELEMETRY"] = "1"
os.environ["HF_HUB_DISABLE_PROGRESS_BARS"] = "1"
warnings.filterwarnings("ignore", message="Using the model-agnostic default `max_length`")
# The few operations Apple GPUs (MPS) lack run on the CPU instead of failing.
os.environ["PYTORCH_ENABLE_MPS_FALLBACK"] = "1"

# Imported only now, since they read the settings above when imported.
import av
import numpy as np
import torch
from transformers import AutoModelForAudioFrameClassification, AutoModelForTDT, AutoProcessor

SAMPLE_RATE = 16_000  # both models take 16 kHz mono audio
MAX_SPEAKERS = 8  # as many as Nemotron 3 Diarization tells apart
SPEAKING = 0.5  # the probability above which the diarizer counts someone as speaking
CHUNK_SECONDS = 30.0  # the longest stretch of audio transcribed in one go
BATCH_SIZE = 8  # chunks transcribed together
MAX_PAUSE = 1.5  # seconds of silence that end an utterance, even when the same person goes on

# How far through the job each stage ends, for progress. Transcription
# takes most of the time; the models load within their stages.
DECODED, DIARIZED, TRANSCRIBED = 0.05, 0.15, 0.97


def emit(type, **fields):
    """Sends one event to the runtime: a JSON object on its own line."""
    print(json.dumps({"v": 1, "type": type, **fields}), file=EVENTS, flush=True)


def log(message, level="info"):
    emit("log", level=level, message=message)


def progress(fraction, message):
    emit("progress", fraction=round(fraction, 3), message=message)


class JobError(Exception):
    """A failure the script can explain: its message goes to the service as is.

    retryable says whether another attempt, maybe on another machine, could
    succeed. A recording that can't be read won't read any better elsewhere.
    """

    def __init__(self, message, retryable=False):
        super().__init__(message)
        self.retryable = retryable


def main(job):
    params = job["params"] or {}
    if not isinstance(params, dict):
        raise JobError("params must be a JSON object")
    started = time.monotonic()
    timings = {}

    # Check what the job asks for before the slow work, so a mistake fails
    # in a second rather than after the models load.
    audio_path = pick_input(job["inputs"], params)
    max_speakers = pick_max_speakers(params)
    writers = pick_writers(job["outputs"])
    device = pick_device(params.get("device"))
    for name in sorted(set(params) - {"input", "max_speakers", "device"}):
        log(f"ignoring unknown param {name!r}", level="warn")
    log(f"running on {device}")

    progress(0, "decoding audio")
    stage = time.monotonic()
    audio = decode(audio_path)
    duration = len(audio) / SAMPLE_RATE
    timings["decode"] = seconds_since(stage)
    log(f"decoded {timestamp(duration)} of audio")

    # Models load from the job's assets: one directory per model, laid out
    # by the asset names in manifest.json.
    assets_dir = job["assets_dir"]

    progress(DECODED, "finding speakers")
    stage = time.monotonic()
    activity, frame_seconds = diarize(audio, f"{assets_dir}/diarization", device)
    speakers = pick_speakers(activity, max_speakers)
    timings["diarize"] = seconds_since(stage)

    # Only stretches where someone speaks are transcribed: it's faster, and
    # speech models tend to invent words in long silences.
    chunks = plan_chunks(speech_regions(activity, frame_seconds), audio)
    batches = [chunks[i : i + BATCH_SIZE] for i in range(0, len(chunks), BATCH_SIZE)]
    stage = time.monotonic()
    transcript = Transcript()
    if chunks:
        progress(DIARIZED, "loading the speech model")
        model, processor = load_asr(f"{assets_dir}/asr", device)
        speech = sum(end - start for start, end in chunks)
        log(f"transcribing {timestamp(speech)} of speech in {len(chunks)} chunks")
        for done, batch in enumerate(batches, start=1):
            for word in transcribe(model, processor, audio, batch):
                speaker = speaker_at(activity, frame_seconds, word, speakers)
                transcript.add(word, speaker)
            fraction = DIARIZED + (TRANSCRIBED - DIARIZED) * done / len(batches)
            progress(fraction, f"transcribed {timestamp(batch[-1][1])} of {timestamp(duration)}")
    transcript.finish()
    timings["transcribe"] = seconds_since(stage)

    # Outputs go in output_dir under the names the service declared; naming
    # an undeclared one in the result fails the attempt.
    progress(TRANSCRIBED, "writing the transcript")
    stage = time.monotonic()
    files = []
    for name, write in writers.items():
        with open(os.path.join(job["output_dir"], name), "w", encoding="utf-8") as f:
            write(f, transcript, duration)
        files.append(name)
    timings["write"] = seconds_since(stage)
    timings["total"] = seconds_since(started)

    emit(
        "result",
        files=files,
        meta={
            "duration": round(duration, 2),
            "speakers": len(transcript.speakers),
            "utterances": len(transcript.utterances),
            "words": sum(len(u["words"]) for u in transcript.utterances),
            "device": device,
            "seconds": timings,
        },
    )
    return 0


# Checking the job


def pick_input(inputs, params):
    """The path of the recording: the input params names, or the only one."""
    if "input" in params:
        if params["input"] not in inputs:
            raise JobError(f"params.input names {params['input']!r}, which isn't one of the job's inputs")
        return inputs[params["input"]]
    if len(inputs) != 1:
        raise JobError(f"the job has {len(inputs)} inputs: send just the recording, or name it in params.input")
    return next(iter(inputs.values()))


def pick_max_speakers(params):
    limit = params.get("max_speakers", MAX_SPEAKERS)
    if type(limit) is not int or not 1 <= limit <= MAX_SPEAKERS:
        raise JobError(f"max_speakers must be a whole number from 1 to {MAX_SPEAKERS}, not {limit!r}")
    return limit


def pick_writers(outputs):
    """Which function writes each declared output, going by its extension."""
    formats = {".json": write_json, ".vtt": write_vtt, ".txt": write_text}
    writers = {}
    for name in outputs:
        extension = os.path.splitext(name)[1].lower()
        if extension not in formats:
            raise JobError(f"can't write {name}: outputs must end in .json, .vtt or .txt")
        writers[name] = formats[extension]
    return writers


def pick_device(requested):
    """Where the models run: the fastest device the machine has, unless the job asks for one."""
    available = {
        "mps": torch.backends.mps.is_available(),  # Apple silicon
        "cuda": torch.cuda.is_available(),  # NVIDIA
        "cpu": True,
    }
    if requested is None or requested == "auto":
        return next(device for device, present in available.items() if present)
    if requested not in available:
        raise JobError(f"device must be mps, cuda, cpu or auto, not {requested!r}")
    if not available[requested]:
        # Another machine might have one.
        raise JobError(f"this machine has no {requested} device", retryable=True)
    return requested


# Audio


def decode(path):
    """Decodes a recording's first audio track to 16 kHz mono samples.

    PyAV's wheels bundle FFmpeg, so this needs nothing installed on the
    machine. A recording cut short, say by a crash mid-meeting, is
    transcribed up to where it breaks off.
    """
    pieces = []
    try:
        with av.open(path) as container:
            if not container.streams.audio:
                raise JobError("the recording has no audio track")
            resampler = av.AudioResampler(format="flt", layout="mono", rate=SAMPLE_RATE)
            try:
                for frame in container.decode(container.streams.audio[0]):
                    pieces += [f.to_ndarray().reshape(-1) for f in resampler.resample(frame)]
            except av.error.FFmpegError as error:
                if not pieces:
                    raise
                decoded = sum(len(piece) for piece in pieces) / SAMPLE_RATE
                log(
                    f"the recording breaks off after {timestamp(decoded)} ({error.strerror}); keeping what came before",
                    "warn",
                )
            pieces += [f.to_ndarray().reshape(-1) for f in resampler.resample(None)]
    except av.error.FFmpegError as error:
        # FFmpeg's own reason, without the path: that's this machine's business.
        raise JobError(f"can't read the recording: {error.strerror}") from error
    if not pieces:
        raise JobError("the recording holds no audio")
    return np.concatenate(pieces)


def plan_chunks(regions, audio):
    """Groups speech into chunks of at most 30 seconds to transcribe.

    Neighbouring stretches of speech share a chunk, which gives the model
    context. A stretch longer than a chunk is cut at its quietest moment,
    where a cut is least likely to split a word.
    """
    duration = len(audio) / SAMPLE_RATE
    chunks = []
    for start, end in regions:
        # A little margin keeps the edges of words that the diarizer clipped.
        start = max(start - 0.2, chunks[-1][1] if chunks else 0.0)
        end = min(end + 0.2, duration)
        if chunks and start - chunks[-1][1] < 2.0 and end - chunks[-1][0] <= CHUNK_SECONDS:
            chunks[-1][1] = end
            continue
        while end - start > CHUNK_SECONDS:
            cut = quietest_moment(audio, start + CHUNK_SECONDS - 10, start + CHUNK_SECONDS)
            chunks.append([start, cut])
            start = cut
        chunks.append([start, end])
    return chunks


def quietest_moment(audio, start, end):
    """The middle of the quietest 100 ms of audio between start and end, in seconds."""
    window = SAMPLE_RATE // 10
    samples = audio[int(start * SAMPLE_RATE) : int(end * SAMPLE_RATE)]
    windows = samples[: len(samples) // window * window].reshape(-1, window)
    return start + (np.square(windows).mean(axis=1).argmin() + 0.5) * window / SAMPLE_RATE


# Who speaks when


def diarize(audio, model_dir, device):
    """Finds who speaks when.

    Returns an array with a row per frame of audio and a column for each of
    8 possible speakers, holding the probability that the speaker is talking
    then, and the length of a frame in seconds (10 ms). Speakers are
    numbered in the order they first speak.
    """
    processor = AutoProcessor.from_pretrained(model_dir, local_files_only=True)
    model = AutoModelForAudioFrameClassification.from_pretrained(model_dir, local_files_only=True)
    model = model.to(device).eval()
    inputs = processor(audio, sampling_rate=SAMPLE_RATE).to(device, dtype=model.dtype)
    with torch.inference_mode():
        # The model walks through the recording in 30 s chunks, carrying a
        # cache of what each speaker sounds like from one to the next, so
        # memory stays flat however long the meeting runs.
        logits = model(**inputs).logits[0]
    frames = inputs.attention_mask[0].bool()
    activity = torch.sigmoid(logits[frames]).float().cpu().numpy()
    del model
    empty_device_cache(device)
    feature_extractor = processor.feature_extractor
    return activity, feature_extractor.hop_length / feature_extractor.sampling_rate


def pick_speakers(activity, limit):
    """The speakers worth keeping: those heard at all, and at most limit of
    them, preferring the ones who speak most. The words of any others go to
    whichever kept speaker sounds likeliest."""
    frames_speaking = (activity > SPEAKING).sum(axis=0)
    by_time = [int(s) for s in np.argsort(-frames_speaking, kind="stable") if frames_speaking[s] > 0]
    if len(by_time) > limit:
        log(f"heard {len(by_time)} speakers; keeping the {limit} who speak most")
    else:
        log(f"heard {len(by_time)} speakers")
    return sorted(by_time[:limit])


def speech_regions(activity, frame_seconds):
    """The stretches, in seconds, where anyone is speaking; pauses under 0.3 s don't count."""
    speaking = np.concatenate([[0], (activity > SPEAKING).any(axis=1).astype(np.int8), [0]])
    changes = np.diff(speaking)
    starts, ends = np.flatnonzero(changes == 1), np.flatnonzero(changes == -1)
    regions = []
    for start, end in zip(starts * frame_seconds, ends * frame_seconds):
        if regions and start - regions[-1][1] < 0.3:
            regions[-1][1] = end
        else:
            regions.append([start, end])
    return regions


def speaker_at(activity, frame_seconds, word, speakers):
    """The speaker most likely saying a word: the one whose probability of
    speaking, summed over the word's frames, is highest.

    A word nobody seems to be saying (the diarizer can miss the odd word)
    goes to whoever speaks nearest to it, looking up to 2 s either side.

    Words are judged one by one on purpose. Keeping a sentence with its
    first speaker fixes a stray word when someone murmurs "mm" over it, but
    breaks more real interruptions: on an AMI meeting it scored lower, as
    did counting only frames above the threshold instead of summing.
    """
    first = int(word["start"] / frame_seconds)
    last = max(int(word["end"] / frame_seconds), first + 1)
    for margin in (0, round(0.5 / frame_seconds), round(2 / frame_seconds)):
        window = activity[max(first - margin, 0) : last + margin, speakers]
        if window.size and window.max() > SPEAKING:
            break
    return speakers[int(window.sum(axis=0).argmax())]


# What they say


def load_asr(model_dir, device):
    # Half precision halves memory and runs faster on GPUs; CPUs want full.
    dtype = torch.float32 if device == "cpu" else torch.float16
    processor = AutoProcessor.from_pretrained(model_dir, local_files_only=True)
    model = AutoModelForTDT.from_pretrained(model_dir, local_files_only=True, dtype=dtype)
    return model.to(device).eval(), processor


def transcribe(model, processor, audio, chunks):
    """Transcribes a batch of chunks. Returns their words, with start and end
    times in seconds from the start of the recording."""
    clips = [audio[int(start * SAMPLE_RATE) : int(end * SAMPLE_RATE)] for start, end in chunks]
    inputs = processor(clips, sampling_rate=SAMPLE_RATE).to(model.device, dtype=model.dtype)
    with torch.inference_mode():
        output = model.generate(**inputs, return_dict_in_generate=True)
    # A TDT model predicts how long each token lasts along with the token,
    # which is what gives every word its timing.
    _, timed_tokens = processor.decode(output.sequences, durations=output.durations, skip_special_tokens=True)
    words = []
    for (offset, _), tokens in zip(chunks, timed_tokens):
        chunk_words = []
        for token in tokens:
            # Tokens are pieces of words; one starting with a space starts a new word.
            if token["token"].startswith((" ", "▁")) or not chunk_words:
                chunk_words.append({"start": offset + token["start"], "text": ""})
            chunk_words[-1]["text"] += token["token"].lstrip(" ▁")
            chunk_words[-1]["end"] = offset + token["end"]
        words += [word for word in chunk_words if word["text"]]
    return words


class Transcript:
    """Collects words, in the order spoken, into utterances: runs of one
    speaker's words without a long pause.

    Each utterance is sent as a data event as soon as the next one starts,
    so a service can show the transcript as it grows. Speakers are named
    S1, S2... in the order they first speak.
    """

    def __init__(self):
        self.utterances = []
        self.speakers = {}  # diarizer speaker number -> name

    def add(self, word, speaker):
        name = self.speakers.setdefault(speaker, f"S{len(self.speakers) + 1}")
        word = {"start": round(word["start"], 2), "end": round(word["end"], 2), "text": word["text"]}
        last = self.utterances[-1] if self.utterances else None
        if last and last["speaker"] == name and word["start"] - last["end"] < MAX_PAUSE:
            last["words"].append(word)
            last["end"] = word["end"]
            return
        if last:
            self.publish(last)
        self.utterances.append({"start": word["start"], "end": word["end"], "speaker": name, "words": [word]})

    def finish(self):
        if self.utterances:
            self.publish(self.utterances[-1])

    @staticmethod
    def publish(utterance):
        utterance["text"] = " ".join(word["text"] for word in utterance["words"])
        emit("data", payload={"utterance": {key: utterance[key] for key in ("start", "end", "speaker", "text")}})


# Output formats


def write_json(f, transcript, duration):
    speaking = {name: 0.0 for name in transcript.speakers.values()}
    for utterance in transcript.utterances:
        speaking[utterance["speaker"]] += utterance["end"] - utterance["start"]
    document = {
        "duration": round(duration, 2),
        "speakers": [{"id": name, "speaking_seconds": round(total, 2)} for name, total in speaking.items()],
        "utterances": [
            {key: utterance[key] for key in ("start", "end", "speaker", "text", "words")}
            for utterance in transcript.utterances
        ],
    }
    json.dump(document, f, ensure_ascii=False, indent=2)
    f.write("\n")


def write_vtt(f, transcript, duration):
    """WebVTT captions, each cue tagged with its speaker's voice."""
    f.write("WEBVTT\n")
    for utterance in transcript.utterances:
        for cue in caption_cues(utterance["words"]):
            text = " ".join(word["text"] for word in cue)
            text = text.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
            f.write(f"\n{timestamp(cue[0]['start'], millis=True)} --> {timestamp(cue[-1]['end'], millis=True)}\n")
            f.write(f"<v {speaker_name(utterance)}>{text}\n")


def caption_cues(words, max_characters=84):
    """Splits an utterance into caption-sized cues of up to two lines.

    A cue ends at the end of a sentence once it has been on screen for 2
    seconds, or at a comma after 5, so captions follow the speech without
    flickering.
    """
    cue = []
    for word in words:
        if cue and len(" ".join(w["text"] for w in cue + [word])) > max_characters:
            yield cue
            cue = []
        cue.append(word)
        seconds = word["end"] - cue[0]["start"]
        if (word["text"].endswith((".", "?", "!")) and seconds >= 2) or (word["text"].endswith(",") and seconds >= 5):
            yield cue
            cue = []
    if cue:
        yield cue


def write_text(f, transcript, duration):
    for utterance in transcript.utterances:
        f.write(f"[{timestamp(utterance['start'])}] {speaker_name(utterance)}: {utterance['text']}\n")


def speaker_name(utterance):
    """How captions and plain text name a speaker: S2 is "Speaker 2"."""
    return "Speaker " + utterance["speaker"].removeprefix("S")


# Helpers


def timestamp(seconds, millis=False):
    """Seconds as hours:minutes:seconds, e.g. 01:02:03, or 01:02:03.450 with millis."""
    minutes, ms = divmod(round(seconds * 1000), 60_000)
    hours, minutes = divmod(minutes, 60)
    text = f"{hours:02d}:{minutes:02d}:{ms // 1000:02d}"
    return f"{text}.{ms % 1000:03d}" if millis else text


def seconds_since(start):
    return round(time.monotonic() - start, 2)


def empty_device_cache(device):
    if device == "cuda":
        torch.cuda.empty_cache()
    elif device == "mps":
        torch.mps.empty_cache()


def out_of_memory(error):
    # CUDA raises OutOfMemoryError; Apple GPUs raise a RuntimeError saying so.
    return isinstance(error, (torch.OutOfMemoryError, MemoryError)) or "out of memory" in str(error).lower()


if __name__ == "__main__":
    try:
        sys.exit(main(json.load(sys.stdin)))
    except JobError as error:
        emit("error", message=str(error), retryable=error.retryable)
    except Exception as error:
        # The traceback goes to stderr, whose tail the runtime keeps for the
        # error report. The service gets a sentence.
        traceback.print_exc()
        if out_of_memory(error):
            # Worth another attempt: on a bigger machine, or on this one
            # when other programs have freed some memory.
            reason = str(error).split("\n")[0] or type(error).__name__
            emit("error", message=f"ran out of memory: {reason}", retryable=True)
        else:
            emit("error", message=f"transcription failed: {type(error).__name__}: {error}", retryable=False)
    sys.exit(1)
