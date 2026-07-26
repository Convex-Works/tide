import type { Action } from 'svelte/action';
import type { Track } from 'livekit-client';
import type { MediaTrackView } from './media';

export type MediaAttachment = Track | MediaTrackView;

function attachmentTrack(attachment: MediaAttachment): Track | undefined {
  return 'publicationSid' in attachment ? attachment.track : attachment;
}

function publicationSid(attachment: MediaAttachment): string {
  return 'publicationSid' in attachment
    ? attachment.publicationSid
    : (attachment.sid ?? attachment.mediaStreamTrack.id);
}

function source(attachment: MediaAttachment): string {
  return 'publicationSid' in attachment ? attachment.source : attachment.source;
}

function isAttached(node: HTMLMediaElement, track: Track): boolean {
  const stream = node.srcObject;
  return (
    stream instanceof MediaStream &&
    stream.getTracks().includes(track.mediaStreamTrack) &&
    track.attachedElements.includes(node)
  );
}

function testAttachmentDelay(): number {
  if (import.meta.env.VITE_KLISI_TEST !== 'true' || typeof window === 'undefined') return 0;
  return (
    (
      window as Window & {
        __klisiMediaTest?: { attachmentDelayMs?: number };
      }
    ).__klisiMediaTest?.attachmentDelayMs ?? 0
  );
}

/**
 * Owns exactly one DOM media node. Reconciliation supplies a fresh publication
 * view, which also makes the unchanged-track path verify and repair srcObject.
 */
export const attachMediaTrack: Action<HTMLMediaElement, MediaAttachment> = (
  node,
  initialAttachment
) => {
  let attachment = initialAttachment;
  let track = attachmentTrack(attachment);
  let timer: ReturnType<typeof setTimeout> | undefined;
  let destroyed = false;

  const attach = (nextAttachment: MediaAttachment, force = false): void => {
    attachment = nextAttachment;
    const nextTrack = attachmentTrack(nextAttachment);
    const sid = publicationSid(nextAttachment);
    node.dataset.publicationSid = sid;
    node.dataset.trackSource = source(nextAttachment);

    const apply = (): void => {
      if (destroyed) return;
      if (track && track !== nextTrack) track.detach(node);
      track = nextTrack;
      if (track && (force || !isAttached(node, track))) track.attach(node);
    };

    if (timer) clearTimeout(timer);
    const delay = testAttachmentDelay();
    if (delay > 0) timer = setTimeout(apply, delay);
    else apply();
  };

  attach(initialAttachment, true);

  return {
    update(nextAttachment) {
      attach(nextAttachment);
    },
    destroy() {
      destroyed = true;
      if (timer) clearTimeout(timer);
      track?.detach(node);
      delete node.dataset.publicationSid;
      delete node.dataset.trackSource;
    }
  };
};
