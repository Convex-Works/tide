// EXPERIMENT (do not merge): logs every call into the browser's media stack,
// so the last console line before a WebKit web process deadlocks names the
// JavaScript call that led into it.
export function installMediaTrace(): void {
  const log = (what: string) => console.info(`[media-trace] ${performance.now().toFixed(1)} ${what}`);
  const wrap = <T extends object>(proto: T, name: string, label: string) => {
    const original = (proto as Record<string, unknown>)[name];
    if (typeof original !== 'function') return;
    (proto as Record<string, unknown>)[name] = function (this: unknown, ...args: unknown[]) {
      log(`${label} begin`);
      try {
        const result = (original as (...a: unknown[]) => unknown).apply(this, args);
        log(`${label} returned`);
        return result;
      } catch (error) {
        log(`${label} threw ${String(error)}`);
        throw error;
      }
    };
  };

  const NativeAudioContext = window.AudioContext;
  if (NativeAudioContext) {
    let contexts = 0;
    window.AudioContext = class extends NativeAudioContext {
      constructor(options?: AudioContextOptions) {
        const id = ++contexts;
        log(`AudioContext#${id} construct begin`);
        super(options);
        log(`AudioContext#${id} construct returned`);
      }
    } as typeof AudioContext;
    wrap(NativeAudioContext.prototype, 'close', 'AudioContext.close');
    wrap(NativeAudioContext.prototype, 'suspend', 'AudioContext.suspend');
    wrap(NativeAudioContext.prototype, 'resume', 'AudioContext.resume');
    wrap(NativeAudioContext.prototype, 'createMediaStreamSource', 'AudioContext.createMediaStreamSource');
    wrap(NativeAudioContext.prototype, 'createMediaStreamDestination', 'AudioContext.createMediaStreamDestination');
  }
  wrap(AudioNode.prototype, 'disconnect', 'AudioNode.disconnect');
  wrap(MediaStreamTrack.prototype, 'stop', 'MediaStreamTrack.stop');
  wrap(MediaStreamTrack.prototype, 'clone', 'MediaStreamTrack.clone');
  wrap(HTMLMediaElement.prototype, 'play', 'HTMLMediaElement.play');
  wrap(HTMLMediaElement.prototype, 'pause', 'HTMLMediaElement.pause');
  wrap(HTMLMediaElement.prototype, 'load', 'HTMLMediaElement.load');
  wrap(RTCRtpSender.prototype, 'replaceTrack', 'RTCRtpSender.replaceTrack');
  wrap(RTCPeerConnection.prototype, 'close', 'RTCPeerConnection.close');
  wrap(RTCPeerConnection.prototype, 'setRemoteDescription', 'RTCPeerConnection.setRemoteDescription');
  wrap(RTCPeerConnection.prototype, 'setLocalDescription', 'RTCPeerConnection.setLocalDescription');
  wrap(RTCPeerConnection.prototype, 'addTransceiver', 'RTCPeerConnection.addTransceiver');
  wrap(RTCPeerConnection.prototype, 'removeTrack', 'RTCPeerConnection.removeTrack');

  for (const [proto, property, label] of [
    [HTMLMediaElement.prototype, 'srcObject', 'HTMLMediaElement.srcObject='],
    [HTMLMediaElement.prototype, 'muted', 'HTMLMediaElement.muted='],
    [HTMLMediaElement.prototype, 'volume', 'HTMLMediaElement.volume=']
  ] as const) {
    const descriptor = Object.getOwnPropertyDescriptor(proto, property);
    if (!descriptor?.set) continue;
    const setter = descriptor.set;
    Object.defineProperty(proto, property, {
      ...descriptor,
      set(this: HTMLMediaElement, value: unknown) {
        const tag = `${label} on <${this.tagName.toLowerCase()} ${this.dataset.publicationSid ?? ''}> ${
          value instanceof MediaStream ? `stream(${value.getTracks().map((t) => t.kind).join(',')})` : String(value)
        }`;
        log(`${tag} begin`);
        setter.call(this, value);
        log(`${tag} returned`);
      }
    });
  }
}
