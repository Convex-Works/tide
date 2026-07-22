// Tiles render each stream at its native aspect ratio, clamped to this band.
// Inside the band the frame is shown whole (object-fit: cover becomes a
// no-op because the box aspect equals the stream aspect); outside it, cover
// center-crops only the overflow beyond the nearest bound. The same clamp
// runs on the pre-join preview, the in-room tiles, and the egress composite,
// so the self-view always shows exactly what is broadcast.
export const MIN_ASPECT = 9 / 16;
export const MAX_ASPECT = 16 / 9;

export function clampAspect(width: number, height: number): number {
  if (!width || !height) return MAX_ASPECT;
  return Math.min(MAX_ASPECT, Math.max(MIN_ASPECT, width / height));
}

// Svelte action: reports the clamped aspect of a video element's current
// frame, re-firing on `resize` (dimension changes mid-stream — phone
// rotation, device switch) as well as `loadedmetadata`.
export function observeAspect(node: HTMLVideoElement, onAspect: (aspect: number) => void) {
  let notify = onAspect;
  const report = () => notify(clampAspect(node.videoWidth, node.videoHeight));
  node.addEventListener('loadedmetadata', report);
  node.addEventListener('resize', report);
  report();

  return {
    update(next: (aspect: number) => void) {
      notify = next;
    },
    destroy() {
      node.removeEventListener('loadedmetadata', report);
      node.removeEventListener('resize', report);
    }
  };
}
