// Shared audio-level engine for the per-tile waveforms. One lazy
// AudioContext, one analyser per metered track, one rAF loop sampling every
// registered meter — N tiles never cost N animation loops. The RMS +
// exponential smoothing matches the pre-join mic meter so levels feel the
// same across the app.

interface Meter {
  source: MediaStreamAudioSourceNode;
  analyser: AnalyserNode;
  samples: Uint8Array<ArrayBuffer>;
  level: number;
  onLevel: (level: number) => void;
}

let context: AudioContext | undefined;
let frame = 0;
const meters = new Set<Meter>();

function sample(): void {
  for (const meter of meters) {
    meter.analyser.getByteTimeDomainData(meter.samples);
    let sum = 0;
    for (const value of meter.samples) {
      const normalized = (value - 128) / 128;
      sum += normalized * normalized;
    }
    const rms = Math.sqrt(sum / meter.samples.length);
    meter.level = Math.min(1, meter.level * 0.72 + rms * 4 * 0.28);
    meter.onLevel(meter.level);
  }
  frame = requestAnimationFrame(sample);
}

export function createMeter(track: MediaStreamTrack, onLevel: (level: number) => void): () => void {
  if (!context) context = new AudioContext();
  // A user gesture has always happened by the time a meter exists (joining
  // the room), but resume defensively in case the context started suspended.
  if (context.state === 'suspended') void context.resume().catch(() => undefined);

  const analyser = context.createAnalyser();
  analyser.fftSize = 256;
  analyser.smoothingTimeConstant = 0.7;
  const source = context.createMediaStreamSource(new MediaStream([track]));
  source.connect(analyser);

  const meter: Meter = {
    source,
    analyser,
    samples: new Uint8Array(analyser.fftSize),
    level: 0,
    onLevel
  };
  meters.add(meter);
  if (meters.size === 1) frame = requestAnimationFrame(sample);

  return () => {
    if (!meters.delete(meter)) return;
    meter.source.disconnect();
    meter.analyser.disconnect();
    if (meters.size === 0) {
      cancelAnimationFrame(frame);
      frame = 0;
      if (context) {
        void context.close().catch(() => undefined);
        context = undefined;
      }
    }
  };
}
