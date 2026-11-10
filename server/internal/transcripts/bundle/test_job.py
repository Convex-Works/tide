# /// script
# requires-python = ">=3.12,<3.13"
# dependencies = ["numpy==2.5.3"]
# ///
"""Tests of job.py with its models replaced: no weights, no torch.

moil ignores this file, so it isn't part of the bundle or its hash. It runs
the real main(), writers and events over a scripted meeting, and stands in
for the two things that need models: what the diarizer heard and what the
speech model wrote. `make bundle-check` runs it:

    uv run --script server/internal/transcripts/bundle/test_job.py
"""

import html
import importlib.util
import io
import json
import re
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import numpy as np

JOB = Path(__file__).with_name("job.py")
FRAME_SECONDS = 0.01
SPEAKERS = 8  # columns in the diarizer's output

ALL_OUTPUTS = ["transcript.json", "transcript.vtt", "transcript.txt"]


def load_job(path=JOB):
    """Imports a job.py with the modules it can't do without replaced. It
    takes over stdout as it loads, so that's put back."""
    stdout = sys.stdout
    sys.dont_write_bytecode = True
    stubs = {name: mock.MagicMock() for name in ("av", "torch", "transformers")}
    try:
        with mock.patch.dict(sys.modules, stubs):
            spec = importlib.util.spec_from_file_location("job", path)
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)
    finally:
        sys.stdout = stdout
    return module


job = load_job()


def said(talk):
    """Words for a talk script: one every half second of each stretch."""
    words = []
    for speaker, start, end in talk:
        for at in np.arange(start, end - 0.3, 0.5):
            words.append({"start": float(at), "end": float(at) + 0.3, "text": f"s{speaker}."})
    return sorted(words, key=lambda word: word["start"])


class Run:
    """A finished job: its events, by type, and the files it wrote."""

    def __init__(self, events, files):
        self.events = events
        self.files = files
        results = [event for event in events if event["type"] == "result"]
        self.meta = results[0]["meta"] if results else None
        self.data = [event["payload"]["utterance"] for event in events if event["type"] == "data"]
        self.warnings = [event["message"] for event in events if event.get("level") == "warn"]

    @property
    def json(self):
        return json.loads(self.files["transcript.json"])

    @property
    def text(self):
        return self.files["transcript.txt"]

    @property
    def vtt(self):
        return self.files["transcript.vtt"]

    def speakers_in_text(self):
        return re.findall(r"^\[\d\d:\d\d:\d\d\] (.*): s\d\.", self.text, re.M)


def run(talk, timeline=None, words=None, params=None, seconds=120, module=None, extra_inputs=()):
    """Runs a job over a scripted meeting and returns what it did.

    talk is [(diarizer speaker, start, end)]. timeline is the speakers.json
    input: a value to write as JSON, bytes to write as they are, or None for
    no such input.
    """
    module = module or job
    activity = np.full((round(seconds / FRAME_SECONDS), SPEAKERS), 0.01, dtype=np.float32)
    for speaker, start, end in talk:
        activity[round(start / FRAME_SECONDS) : round(end / FRAME_SECONDS), speaker] = 0.9
    words = said(talk) if words is None else words

    def transcribe(model, processor, audio, chunks):
        return [word for word in words if any(start <= word["start"] < end for start, end in chunks)]

    with tempfile.TemporaryDirectory() as directory:
        directory = Path(directory)
        recording = directory / "audio.ogg"
        recording.write_bytes(b"not read: decoding is replaced")
        inputs = {"audio.ogg": str(recording)}
        for name in extra_inputs:
            (directory / name).write_bytes(b"")
            inputs[name] = str(directory / name)
        if timeline is not None:
            path = directory / "speakers.json"
            path.write_bytes(timeline if isinstance(timeline, bytes) else json.dumps(timeline).encode())
            inputs["speakers.json"] = str(path)
        output_dir = directory / "out"
        output_dir.mkdir()
        events = io.StringIO()
        replaced = {
            "EVENTS": events,
            "decode": lambda path: np.zeros(seconds * module.SAMPLE_RATE, dtype=np.float32),
            "diarize": lambda audio, model_dir, device: (activity, FRAME_SECONDS),
            "load_asr": lambda model_dir, device: (None, None),
            "transcribe": transcribe,
        }
        with mock.patch.multiple(module, **replaced):
            module.main(
                {
                    "params": params,
                    "inputs": inputs,
                    "outputs": ALL_OUTPUTS,
                    "output_dir": str(output_dir),
                    "assets_dir": str(directory / "assets"),
                }
            )
        files = {path.name: path.read_text(encoding="utf-8") for path in output_dir.iterdir()}
    return Run([json.loads(line) for line in events.getvalue().splitlines()], files)


def timeline(**spans):
    return {"v": 1, "participants": [{"name": name, "spans": s} for name, s in spans.items()]}


def parse_vtt(vtt):
    """The cues of a WebVTT file as [(voice, text)], strictly: every cue is a
    timing line and one line of `<v voice>text`, and a line break, a stray
    tag or a "-->" anywhere else fails."""
    header, *blocks = vtt.split("\n\n")
    if header != "WEBVTT":
        raise AssertionError(f"bad header: {header!r}")
    entity = r"&(?:amp|lt|gt);"
    cues = []
    for block in blocks:
        timing, *payload = block.rstrip("\n").split("\n")
        if not re.fullmatch(r"\d\d:\d\d:\d\d\.\d{3} --> \d\d:\d\d:\d\d\.\d{3}", timing):
            raise AssertionError(f"bad timing line: {timing!r}")
        if len(payload) != 1 or "-->" in payload[0]:
            raise AssertionError(f"bad payload: {payload!r}")
        cue = re.fullmatch(rf"<v ((?:[^<>&\n]|{entity})+)>((?:[^<>&\n]|{entity})*)", payload[0])
        if not cue:
            raise AssertionError(f"bad cue: {payload[0]!r}")
        cues.append((html.unescape(cue[1]), html.unescape(cue[2])))
    return cues


ADA_THEN_BOB = [(0, 0, 10), (1, 10, 20), (0, 20, 30), (1, 30, 40)]


class Matching(unittest.TestCase):
    def test_one_speaker_per_name(self):
        result = run(ADA_THEN_BOB, timeline(Ada=[[0, 10], [20, 30]], Bob=[[10, 20], [30, 40]]))
        self.assertEqual(set(result.speakers_in_text()), {"Ada", "Bob"})
        self.assertEqual({voice for voice, _ in parse_vtt(result.vtt)}, {"Ada", "Bob"})
        self.assertEqual(result.meta["speaker_map"], [{"id": "S1", "name": "Ada"}, {"id": "S2", "name": "Bob"}])
        self.assertEqual(result.meta["speakers"], 2)

    def test_two_speakers_on_one_name_are_numbered_by_who_spoke_first(self):
        # The diarizer's speaker 3 speaks first, so it's S1 and Ada (1).
        talk = [(3, 0, 10), (1, 10, 20), (5, 20, 30)]
        result = run(talk, timeline(Ada=[[0, 20]], Bob=[[20, 30]]))
        self.assertEqual(result.speakers_in_text(), ["Ada (1)", "Ada (2)", "Bob"])
        self.assertEqual([voice for voice, _ in parse_vtt(result.vtt)][:1], ["Ada (1)"])
        # Only the labels are numbered: everything else has the plain name.
        self.assertEqual(
            result.meta["speaker_map"],
            [{"id": "S1", "name": "Ada"}, {"id": "S2", "name": "Ada"}, {"id": "S3", "name": "Bob"}],
        )
        self.assertEqual(
            [(u["speaker"], u["name"]) for u in result.json["utterances"]],
            [("S1", "Ada"), ("S2", "Ada"), ("S3", "Bob")],
        )

    def test_a_named_speaker_without_words_doesnt_count(self):
        talk = [(0, 0, 10), (1, 10, 20)]
        result = run(talk, timeline(Ada=[[0, 20]]), words=said(talk[:1]))
        self.assertEqual(set(result.speakers_in_text()), {"Ada"})
        self.assertEqual(result.meta["speaker_map"], [{"id": "S1", "name": "Ada"}])
        self.assertEqual(result.meta["speakers"], 1)

    def test_a_name_spanning_two_connections_is_one_person(self):
        # Neither connection alone has the 2 seconds it takes.
        document = {
            "v": 1,
            "participants": [{"name": "Ada", "spans": [[0, 1.5]]}, {"name": "Ada", "spans": [[1.5, 3]]}],
        }
        result = run([(0, 0, 3)], document)
        self.assertEqual(result.meta["speaker_map"], [{"id": "S1", "name": "Ada"}])

    def test_names_that_are_the_same_once_cleaned_are_one_person(self):
        document = {
            "v": 1,
            "participants": [{"name": "Ada\u200f", "spans": [[0, 1.5]]}, {"name": " Ada", "spans": [[1.5, 3]]}],
        }
        result = run([(0, 0, 3)], document)
        self.assertEqual(result.meta["speaker_map"], [{"id": "S1", "name": "Ada"}])

    def test_time_on_two_connections_at_once_counts_once(self):
        # Counted twice, Ada would have 20 of 28 seconds instead of 10 of 18.
        document = {
            "v": 1,
            "participants": [
                {"name": "Ada", "spans": [[0, 10]]},
                {"name": "Ada", "spans": [[0, 10]]},
                {"name": "Bob", "spans": [[0, 8]]},
            ],
        }
        self.assertEqual(run([(0, 0, 10)], document).meta["speaker_map"], [])

    def test_a_speaker_covering_two_names_stays_unnamed(self):
        result = run([(0, 0, 20)], timeline(Ada=[[0, 10]], Bob=[[10, 20]]))
        self.assertEqual(set(result.speakers_in_text()), {"Speaker 1"})
        self.assertEqual(result.meta["speaker_map"], [])
        self.assertNotIn("name", result.json["utterances"][0])

    def test_share_threshold(self):
        at_least = run([(0, 0, 10)], timeline(Ada=[[0, 6]], Bob=[[6, 10]]))
        self.assertEqual(at_least.meta["speaker_map"], [{"id": "S1", "name": "Ada"}])
        below = run([(0, 0, 10)], timeline(Ada=[[0, 5.9]], Bob=[[5.9, 10]]))
        self.assertEqual(below.meta["speaker_map"], [])

    def test_seconds_threshold(self):
        at_least = run([(0, 0, 4)], timeline(Ada=[[0, 2]]))
        self.assertEqual(at_least.meta["speaker_map"], [{"id": "S1", "name": "Ada"}])
        below = run([(0, 0, 4)], timeline(Ada=[[0, 1.9]]))
        self.assertEqual(below.meta["speaker_map"], [])
        self.assertEqual(set(below.speakers_in_text()), {"Speaker 1"})

    def test_coverage_threshold(self):
        at_least = run([(0, 0, 20)], timeline(Ada=[[0, 5]]))
        self.assertEqual(at_least.meta["speaker_map"], [{"id": "S1", "name": "Ada"}])
        below = run([(0, 0, 20)], timeline(Ada=[[0, 4.9]]))
        self.assertEqual(below.meta["speaker_map"], [])

    def test_a_dropped_speakers_speech_counts_for_who_the_transcript_gives_it_to(self):
        # With max_speakers 1, Bob's words go to speaker 0, so the transcript
        # has them under that speaker, who can't be called Ada.
        talk = [(0, 0, 20), (1, 20, 40)]
        result = run(talk, timeline(Ada=[[0, 20]], Bob=[[20, 40]]), params={"max_speakers": 1})
        self.assertEqual(result.meta["speakers"], 1)
        self.assertEqual(result.meta["speaker_map"], [])

    def test_a_voice_the_timeline_never_saw_keeps_no_name_from_someone_talking_over_it(self):
        # A shared video talks for a minute; Ada says something over it.
        result = run([(0, 0, 60)], timeline(Ada=[[10, 13]]))
        self.assertEqual(result.meta["speaker_map"], [])
        self.assertEqual(set(result.speakers_in_text()), {"Speaker 1"})

    def test_extreme_times_clamp_to_the_recording(self):
        result = run([(0, 0, 10)], timeline(Ada=[[0, 10], [1.7e308, 1.7e308], [-1.7e308, -1.7e308]]))
        self.assertEqual(result.meta["speaker_map"], [{"id": "S1", "name": "Ada"}])

    def test_silence_in_a_span_doesnt_count(self):
        result = run([(0, 0, 1)], timeline(Ada=[[0, 60]]))
        self.assertEqual(result.meta["speaker_map"], [])

    def test_spans_outside_the_recording_are_harmless(self):
        result = run([(0, 0, 10)], timeline(Ada=[[-5, 3], [100, 1e308]], Bob=[[500, 600], [-9, -1]]))
        self.assertEqual(result.meta["speaker_map"], [{"id": "S1", "name": "Ada"}])

    def test_a_timeline_with_nobody_in_it_is_still_usable(self):
        result = run(ADA_THEN_BOB, {"v": 1, "participants": []})
        self.assertEqual(result.warnings, [])
        self.assertEqual(result.meta["speaker_map"], [])

    def test_a_silent_recording_with_a_timeline(self):
        result = run([], timeline(Ada=[[0, 10]]))
        self.assertEqual(result.meta["speaker_map"], [])
        self.assertEqual(result.meta["speakers"], 0)
        self.assertEqual(result.text, "")

    def test_extra_fields_are_ignored(self):
        document = timeline(Ada=[[0, 10]]) | {"recording": "r1"}
        self.assertEqual(run([(0, 0, 10)], document).meta["speaker_map"], [{"id": "S1", "name": "Ada"}])


class Outputs(unittest.TestCase):
    def test_json_keeps_ids_and_gains_names(self):
        result = run(ADA_THEN_BOB[:2], timeline(Ada=[[0, 10]]))
        speakers = result.json["speakers"]
        self.assertEqual([s["id"] for s in speakers], ["S1", "S2"])
        self.assertEqual(speakers[0]["name"], "Ada")
        self.assertNotIn("name", speakers[1])
        self.assertEqual(result.json["utterances"][0]["speaker"], "S1")
        self.assertEqual(result.json["utterances"][0]["name"], "Ada")
        self.assertNotIn("name", result.json["utterances"][1])

    def test_unnamed_speakers_keep_their_labels_beside_named_ones(self):
        result = run(ADA_THEN_BOB[:2], timeline(Ada=[[0, 10]]))
        self.assertEqual(result.speakers_in_text(), ["Ada", "Speaker 2"])
        self.assertEqual({voice for voice, _ in parse_vtt(result.vtt)}, {"Ada", "Speaker 2"})

    def test_live_events_carry_names_from_the_first_utterance(self):
        result = run(ADA_THEN_BOB, timeline(Ada=[[0, 10], [20, 30]]))
        self.assertEqual(
            [(u["speaker"], u.get("name")) for u in result.data],
            [("S1", "Ada"), ("S2", None), ("S1", "Ada"), ("S2", None)],
        )
        self.assertEqual(set(result.data[0]), {"start", "end", "speaker", "name", "text"})
        self.assertEqual(set(result.data[1]), {"start", "end", "speaker", "text"})


class Unchanged(unittest.TestCase):
    """Without a timeline, what the bundle did before speaker names."""

    TALK = [(0, 0, 1), (1, 1, 2)]

    def test_files_and_events(self):
        result = run(self.TALK)
        self.assertEqual(
            result.vtt,
            "WEBVTT\n\n"
            "00:00:00.000 --> 00:00:00.800\n<v Speaker 1>s0. s0.\n\n"
            "00:00:01.000 --> 00:00:01.800\n<v Speaker 2>s1. s1.\n",
        )
        self.assertEqual(result.text, "[00:00:00] Speaker 1: s0. s0.\n[00:00:01] Speaker 2: s1. s1.\n")
        self.assertEqual(
            result.files["transcript.json"],
            json.dumps(
                {
                    "duration": 120.0,
                    "speakers": [
                        {"id": "S1", "speaking_seconds": 0.8},
                        {"id": "S2", "speaking_seconds": 0.8},
                    ],
                    "utterances": [
                        {
                            "start": 0.0,
                            "end": 0.8,
                            "speaker": "S1",
                            "text": "s0. s0.",
                            "words": [
                                {"start": 0.0, "end": 0.3, "text": "s0."},
                                {"start": 0.5, "end": 0.8, "text": "s0."},
                            ],
                        },
                        {
                            "start": 1.0,
                            "end": 1.8,
                            "speaker": "S2",
                            "text": "s1. s1.",
                            "words": [
                                {"start": 1.0, "end": 1.3, "text": "s1."},
                                {"start": 1.5, "end": 1.8, "text": "s1."},
                            ],
                        },
                    ],
                },
                indent=2,
            )
            + "\n",
        )
        self.assertEqual(
            result.data,
            [
                {"start": 0.0, "end": 0.8, "speaker": "S1", "text": "s0. s0."},
                {"start": 1.0, "end": 1.8, "speaker": "S2", "text": "s1. s1."},
            ],
        )
        self.assertEqual(result.warnings, [])
        self.assertEqual(set(result.meta), {"duration", "speakers", "utterances", "words", "device", "seconds"})
        self.assertEqual(result.meta["speakers"], 2)


class Malformed(unittest.TestCase):
    CASES = {
        "not JSON": b"{nope",
        "not UTF-8": b'\xff\xfe{"v": 1}',
        "empty": b"",
        "too deep": b"[" * 200_000,
        "not an object": [],
        "no v": {"participants": []},
        "unknown v": {"v": 2, "participants": []},
        "v as a string": {"v": "1", "participants": []},
        "v as true": {"v": True, "participants": []},
        "no participants": {"v": 1},
        "participants not a list": {"v": 1, "participants": {}},
        "participant not an object": {"v": 1, "participants": ["Ada"]},
        "no name": {"v": 1, "participants": [{"spans": []}]},
        "name not a string": {"v": 1, "participants": [{"name": 7, "spans": []}]},
        "no spans": {"v": 1, "participants": [{"name": "Ada"}]},
        "spans not a list": {"v": 1, "participants": [{"name": "Ada", "spans": 3}]},
        "span not a list": {"v": 1, "participants": [{"name": "Ada", "spans": [3]}]},
        "span too short": {"v": 1, "participants": [{"name": "Ada", "spans": [[3]]}]},
        "span too long": {"v": 1, "participants": [{"name": "Ada", "spans": [[3, 4, 5]]}]},
        "span of strings": {"v": 1, "participants": [{"name": "Ada", "spans": [["0", "5"]]}]},
        "span of booleans": {"v": 1, "participants": [{"name": "Ada", "spans": [[False, True]]}]},
        "span backwards": {"v": 1, "participants": [{"name": "Ada", "spans": [[5, 0]]}]},
        "NaN": b'{"v": 1, "participants": [{"name": "Ada", "spans": [[NaN, 5]]}]}',
        "infinity": b'{"v": 1, "participants": [{"name": "Ada", "spans": [[0, Infinity]]}]}',
        "overflow": b'{"v": 1, "participants": [{"name": "Ada", "spans": [[0, 1e999]]}]}',
        "huge integer": b'{"v": 1, "participants": [{"name": "Ada", "spans": [[0, 1' + b"0" * 400 + b"]]}]}",
        "one bad entry among good ones": {
            "v": 1,
            "participants": [{"name": "Ada", "spans": [[0, 10]]}, {"name": "Bob", "spans": "nope"}],
        },
    }

    def test_costs_the_names_and_nothing_else(self):
        expected = run(ADA_THEN_BOB)
        for name, document in self.CASES.items():
            with self.subTest(name):
                result = run(ADA_THEN_BOB, document)
                self.assertEqual(len(result.warnings), 1, result.warnings)
                self.assertIn("speakers.json", result.warnings[0])
                self.assertEqual(result.files, expected.files)
                self.assertEqual(result.data, expected.data)
                self.assertNotIn("speaker_map", result.meta)
                self.assertEqual(result.meta["speakers"], 2)

    def test_a_timeline_that_cant_be_read_doesnt_say_where_it_is(self):
        logged = io.StringIO()
        with mock.patch.object(job, "EVENTS", logged):
            self.assertIsNone(job.load_timeline({"speakers.json": "/private/home/no-such-file"}))
        self.assertIn("warn", logged.getvalue())
        self.assertNotIn("/private/home", logged.getvalue())


class HostileNames(unittest.TestCase):
    def test_a_name_cant_break_out_of_the_voice_tag(self):
        name = "\u202eEve\u2066 --> <b>&amp;\n\n00:01.000 --> 00:02.000\n<v Ada>hi</v>\u2069"
        result = run([(0, 0, 10)], timeline(**{name: [[0, 10]]}))
        cleaned = "Eve --> <b>&amp; 00:01.000 --> 00:02.000 <v Ada>hi</v>"
        self.assertEqual(result.meta["speaker_map"], [{"id": "S1", "name": cleaned}])
        self.assertEqual(result.json["speakers"][0]["name"], cleaned)
        cues = parse_vtt(result.vtt)
        self.assertEqual({voice for voice, _ in cues}, {cleaned})
        self.assertEqual(" ".join(text for _, text in cues), result.json["utterances"][0]["text"])
        self.assertEqual(len(result.text.splitlines()), len(result.json["utterances"]))

    def test_every_troublesome_character_on_its_own(self):
        for name in ("a-->b", "a>b", "a<b", "a&b", "a--&gt;b", "<v Bob>", "</v>", "Ada (1)"):
            with self.subTest(name):
                cues = parse_vtt(run([(0, 0, 10)], timeline(**{name: [[0, 10]]})).vtt)
                self.assertEqual({voice for voice, _ in cues}, {name})

    def test_a_numbered_label_is_escaped_too(self):
        result = run([(0, 0, 10), (1, 10, 20)], timeline(**{"<x>": [[0, 20]]}))
        self.assertEqual({voice for voice, _ in parse_vtt(result.vtt)}, {"<x> (1)", "<x> (2)"})

    def test_cleaning(self):
        cases = {
            "Ada Lovelace": "Ada Lovelace",
            "  Ada \t\n Lovelace  ": "Ada Lovelace",
            "A\x00d\x1b[31ma\x7f": "Ad[31ma",
            "\u202aA\u202bd\u202c\u202da\u202e\u2066\u2067\u2068\u2069\u200e\u200f\u061c": "Ada",
            "Ada\u2028Lovelace\u0085x": "Ada Lovelace x",
            "\ufeffAda\u00ad": "Ada",
            "Ad\ud800a": "Ada",
            "Zoe\u0308": "Zo\u00eb",
            "\u0639\u0644\u06cc\u200c\u0631\u0636\u0627": "\u0639\u0644\u06cc\u200c\u0631\u0636\u0627",
            "\U0001f469\u200d\U0001f4bb": "\U0001f469\u200d\U0001f4bb",
            "\u200d Ada \u200c": "Ada",
            "": "",
            " \t\n": "",
            "\u202e\u200f": "",
            "\u200d": "",
            "x" * 100: "x" * 64,
            "x" * 63 + "\u00a0y": "x" * 63,
        }
        for name, cleaned in cases.items():
            with self.subTest(name):
                self.assertEqual(job.clean_name(name), cleaned)

    def test_a_lone_surrogate_doesnt_stop_the_files_being_written(self):
        document = b'{"v": 1, "participants": [{"name": "Ada\\ud83d", "spans": [[0, 10]]}]}'
        result = run([(0, 0, 10)], document)
        self.assertEqual(result.json["speakers"][0]["name"], "Ada")

    def test_a_name_empty_once_cleaned_is_dropped(self):
        result = run([(0, 0, 10)], timeline(**{"\u202e\n\u200f": [[0, 10]]}))
        self.assertEqual(result.meta["speaker_map"], [])
        self.assertEqual(set(result.speakers_in_text()), {"Speaker 1"})
        self.assertEqual(result.warnings, [])

    def test_a_nameless_guests_speech_doesnt_go_to_someone_else(self):
        # The guest is speaker 0, but Ada talks over them for 3 seconds. Only
        # the guest's own spans stop speaker 0 being called Ada.
        document = {
            "v": 1,
            "participants": [{"name": "\u200f", "spans": [[0, 30]]}, {"name": "Ada", "spans": [[0, 3], [30, 40]]}],
        }
        result = run([(0, 0, 30), (1, 30, 40)], document)
        self.assertEqual(result.meta["speaker_map"], [{"id": "S2", "name": "Ada"}])


class PickInput(unittest.TestCase):
    def test_the_timeline_isnt_counted(self):
        inputs = {"audio.ogg": "/a", "speakers.json": "/s"}
        self.assertEqual(job.pick_input(inputs, {}), "/a")
        self.assertEqual(job.pick_input({"speakers.json": "/s", "audio.ogg": "/a"}, {}), "/a")

    def test_params_input_still_picks(self):
        inputs = {"a.ogg": "/a", "b.ogg": "/b", "speakers.json": "/s"}
        self.assertEqual(job.pick_input(inputs, {"input": "b.ogg"}), "/b")

    def test_two_recordings_still_need_params_input(self):
        with self.assertRaisesRegex(job.JobError, "the job has 2 inputs"):
            job.pick_input({"a.ogg": "/a", "b.ogg": "/b", "speakers.json": "/s"}, {})

    def test_a_timeline_is_no_recording(self):
        with self.assertRaisesRegex(job.JobError, "the job has 0 inputs"):
            job.pick_input({"speakers.json": "/s"}, {})
        with self.assertRaisesRegex(job.JobError, "not a recording"):
            job.pick_input({"a.ogg": "/a", "speakers.json": "/s"}, {"input": "speakers.json"})

    def test_a_job_with_params_input_and_a_timeline_gets_names(self):
        result = run([(0, 0, 10)], timeline(Ada=[[0, 10]]), params={"input": "audio.ogg"}, extra_inputs=["other.ogg"])
        self.assertEqual(result.meta["speaker_map"], [{"id": "S1", "name": "Ada"}])


class Environment(unittest.TestCase):
    def test_this_runs_on_the_numpy_the_bundle_pins(self):
        self.assertIn(f'"numpy=={np.__version__}"', JOB.read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
