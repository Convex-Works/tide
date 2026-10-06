# /// script
# requires-python = ">=3.12"
# dependencies = []
# ///
"""transcribe-e2e: a stand-in for tide's transcribe bundle, for its end-to-end tests.

It keeps the real bundle's contract (server/internal/transcripts/bundle): one
input, the recording; each declared output written in the format its
extension names (.txt, .vtt or .json); progress events with messages; and a
result whose meta counts the speakers. Instead of transcribing, it writes
what proves it read the recording it was given: the file's SHA-256 and size.

It can be held mid-run, so that a test can do something while a machine is on
the job. It waits while a file exists: the one params.hold names, or else
$TRANSCRIBE_STUB_DIR/hold. When $TRANSCRIBE_STUB_DIR is set, the script also
appends each attempt it starts to attempts.log there, one JSON object a line:
{"job_id", "attempt", "pid"}. Nothing waits for more than a minute, even if
the tests never let it go.

    moil run . --input recording.ogg=meeting.ogg \\
        --output transcript.txt --output transcript.vtt
"""

import hashlib
import json
import os
import sys
import time

GIVE_UP_AFTER = 60  # seconds

SPEAKERS = 2


def emit(type, **fields):
    print(json.dumps({"v": 1, "type": type, **fields}), flush=True)


def progress(fraction, message):
    emit("progress", fraction=fraction, message=message)


class JobError(Exception):
    def __init__(self, message, retryable=False):
        super().__init__(message)
        self.retryable = retryable


def main(job):
    params = job["params"] or {}
    if not isinstance(params, dict):
        raise JobError("params must be a JSON object")
    witness = os.environ.get("TRANSCRIBE_STUB_DIR")
    if witness:
        with open(os.path.join(witness, "attempts.log"), "a") as f:
            f.write(json.dumps({"job_id": job["job_id"], "attempt": job["attempt"], "pid": os.getpid()}) + "\n")

    # Check what the job asks for first, as the real bundle does.
    name, path = pick_input(job["inputs"], params)
    writers = pick_writers(job["outputs"])

    progress(0, "decoding audio")
    digest, size = sha256(path)
    progress(0.05, "finding speakers")
    hold = params.get("hold") or (witness and os.path.join(witness, "hold"))
    if hold and os.path.exists(hold):
        progress(0.5, "waiting for the gate to open")
        wait_while(hold)

    lines = [
        f"This stands in for a transcript of {name}.",
        f"It read {size} bytes with SHA-256 {digest}.",
    ]
    progress(0.97, "writing the transcript")
    for output, write in writers.items():
        with open(os.path.join(job["output_dir"], output), "w", encoding="utf-8") as f:
            write(f, lines, name, digest, size)
    emit(
        "result",
        files=list(writers),
        meta={
            "speakers": SPEAKERS,
            "utterances": len(lines),
            "words": sum(len(line.split()) for line in lines),
            "input": {"name": name, "size": size, "sha256": digest},
        },
    )


def pick_input(inputs, params):
    """The recording's name and path: the input params names, or the only one."""
    if "input" in params:
        if params["input"] not in inputs:
            raise JobError(f"params.input names {params['input']!r}, which isn't one of the job's inputs")
        return params["input"], inputs[params["input"]]
    if len(inputs) != 1:
        raise JobError(f"the job has {len(inputs)} inputs: send just the recording, or name it in params.input")
    return next(iter(inputs.items()))


def pick_writers(outputs):
    formats = {".json": write_json, ".vtt": write_vtt, ".txt": write_text}
    writers = {}
    for name in outputs:
        extension = os.path.splitext(name)[1].lower()
        if extension not in formats:
            raise JobError(f"can't write {name}: outputs must end in .json, .vtt or .txt")
        writers[name] = formats[extension]
    return writers


def sha256(path):
    digest, size = hashlib.sha256(), 0
    with open(path, "rb") as f:
        while chunk := f.read(1 << 16):
            digest.update(chunk)
            size += len(chunk)
    return digest.hexdigest(), size


def wait_while(path):
    deadline = time.monotonic() + GIVE_UP_AFTER
    while os.path.exists(path):
        if time.monotonic() > deadline:
            raise JobError(f"gave up waiting for {path} to go")
        time.sleep(0.02)


# One utterance a line, two seconds each, alternating between the speakers.


def write_text(f, lines, *_):
    for i, line in enumerate(lines):
        f.write(f"[{clock(2 * i)}] Speaker {i % SPEAKERS + 1}: {line}\n")


def write_vtt(f, lines, *_):
    f.write("WEBVTT\n")
    for i, line in enumerate(lines):
        f.write(f"\n{clock(2 * i)}.000 --> {clock(2 * i + 2)}.000\n<v Speaker {i % SPEAKERS + 1}>{line}\n")


def write_json(f, lines, name, digest, size):
    utterances = [
        {"start": 2 * i, "end": 2 * i + 2, "speaker": f"S{i % SPEAKERS + 1}", "text": line}
        for i, line in enumerate(lines)
    ]
    speakers = [{"id": f"S{i + 1}"} for i in range(SPEAKERS)]
    json.dump({"input": {"name": name, "size": size, "sha256": digest}, "speakers": speakers, "utterances": utterances}, f)


def clock(seconds):
    return f"{seconds // 3600:02d}:{seconds // 60 % 60:02d}:{seconds % 60:02d}"


if __name__ == "__main__":
    try:
        main(json.load(sys.stdin))
        sys.exit(0)
    except JobError as error:
        emit("error", message=str(error), retryable=error.retryable)
    sys.exit(1)
