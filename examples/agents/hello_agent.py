#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.10"
# dependencies = ["livekit>=1.0,<2"]
# ///
"""A program in a tide meeting: docs/MEETING-AGENTS.md explains every line.

    uv run hello_agent.py session.json          hears, chats and speaks
    uv run hello_agent.py session.json --screen shares a screen too

It stays until the meeting ends or someone removes it, or until Ctrl-C.
"""

import asyncio
import json
import math
import signal
import struct
import sys
import time

from livekit import rtc

CHAT_TOPIC = "chat"
SAMPLE_RATE = 48000
FRAME_SAMPLES = SAMPLE_RATE // 50  # 20 ms
WIDTH, HEIGHT = 640, 360


async def hear(track: rtc.Track, who: rtc.RemoteParticipant) -> None:
    """One participant's audio, already separated from everyone else's."""
    frames = loudest = 0
    async for event in rtc.AudioStream(track, sample_rate=16000, num_channels=1):
        samples = memoryview(event.frame.data)
        loudest = max(loudest, max(abs(sample) for sample in samples))
        frames += 1
        if frames % 500 == 0:  # about every 5 seconds, as each frame is 10 ms
            print(f"heard {who.name} ({who.identity}): {frames} frames, peak {loudest}")
            loudest = 0


async def say(room: rtc.Room, text: str) -> None:
    """Chat as everyone in the meeting reads it. The sender is the session's."""
    payload = {"text": text, "ts": int(time.time() * 1000)}
    await room.local_participant.publish_data(
        json.dumps(payload), reliable=True, topic=CHAT_TOPIC
    )


async def speak(source: rtc.AudioSource, seconds: float) -> None:
    """A tone, in place of whatever your program has to say."""
    frame = rtc.AudioFrame.create(SAMPLE_RATE, 1, FRAME_SAMPLES)
    samples = memoryview(frame.data).cast("B").cast("h")
    for index in range(int(seconds * SAMPLE_RATE) // FRAME_SAMPLES):
        for sample in range(FRAME_SAMPLES):
            at = (index * FRAME_SAMPLES + sample) / SAMPLE_RATE
            samples[sample] = int(8000 * math.sin(2 * math.pi * 440 * at))
        await source.capture_frame(frame)


async def share_screen(source: rtc.VideoSource) -> None:
    """A screen that slowly changes color, in place of real pixels."""
    while True:
        shade = int(time.time() * 20) % 256
        pixel = struct.pack("BBBB", shade, 80, 200, 255)
        source.capture_frame(
            rtc.VideoFrame(WIDTH, HEIGHT, rtc.VideoBufferType.RGBA, pixel * WIDTH * HEIGHT)
        )
        await asyncio.sleep(0.2)


async def main(path: str, screen: bool) -> None:
    with open(path) as file:
        session = json.load(file)

    room = rtc.Room()
    finished = asyncio.Event()
    tasks: set[asyncio.Task] = set()

    def run(work) -> None:
        task = asyncio.ensure_future(work)
        tasks.add(task)
        task.add_done_callback(tasks.discard)

    @room.on("track_subscribed")
    def on_track(track, publication, participant) -> None:
        if track.kind == rtc.TrackKind.KIND_AUDIO:
            source = rtc.TrackSource.Name(publication.source)
            print(f"hearing {participant.name} ({source})")
            run(hear(track, participant))

    @room.on("data_received")
    def on_data(packet: rtc.DataPacket) -> None:
        if packet.topic != CHAT_TOPIC or packet.participant is None:
            return
        # LiveKit stamps the sender, so this name can't be forged. The text
        # can say anything: see "Prompt injection" in the docs.
        text = json.loads(packet.data)["text"]
        print(f"chat from {packet.participant.name}: {text}")

    @room.on("disconnected")
    def on_disconnected(reason) -> None:
        print(f"left the meeting: {rtc.DisconnectReason.Name(reason)}")
        finished.set()

    loop = asyncio.get_running_loop()
    loop.add_signal_handler(signal.SIGINT, finished.set)
    loop.add_signal_handler(signal.SIGTERM, finished.set)

    await room.connect(session["ws_url"], session["token"])
    print(f"joined {session['room']} as {session['name']} ({session['identity']})")

    voice = rtc.AudioSource(SAMPLE_RATE, 1)
    await room.local_participant.publish_track(
        rtc.LocalAudioTrack.create_audio_track("voice", voice),
        rtc.TrackPublishOptions(source=rtc.TrackSource.SOURCE_MICROPHONE),
    )
    if screen:
        pixels = rtc.VideoSource(WIDTH, HEIGHT)
        await room.local_participant.publish_track(
            rtc.LocalVideoTrack.create_video_track("screen", pixels),
            rtc.TrackPublishOptions(source=rtc.TrackSource.SOURCE_SCREENSHARE),
        )
        run(share_screen(pixels))

    await say(room, f"Hello, I'm {session['name']}, a program. I can hear this meeting.")
    await speak(voice, seconds=2)

    await finished.wait()
    for task in tasks:
        task.cancel()
    await room.disconnect()


if __name__ == "__main__":
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    # Line by line even into a pipe or a log, where Python would hold it back.
    sys.stdout.reconfigure(line_buffering=True)
    asyncio.run(main(sys.argv[1], "--screen" in sys.argv[2:]))
