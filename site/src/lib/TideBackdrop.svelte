<!--
  The hero's background, as a pixel display: a lattice of dots whose columns
  fan out toward the bottom as if the plane were folding away, and two pairs
  of tide streams that follow those bent columns and curl to the right. The
  streams are ordered-dither pixels with a fine vertical stripe; surges run
  down them while they sway. One ink colour at low alpha on the white.

  It is drawn into a buffer of 2×2-pixel cells and scaled up without
  smoothing. Still under prefers-reduced-motion; paused off screen.
-->
<script lang="ts">
  let canvas = $state<HTMLCanvasElement>();

  const cell = 2;
  // 4×4 Bayer thresholds for ordered dithering.
  const bayer = [0, 8, 2, 10, 12, 4, 14, 6, 3, 11, 1, 9, 15, 7, 13, 5].map((v) => (v + 0.5) / 16);

  // Ink (#171717) at a given alpha, as one little-endian RGBA word.
  const ink = (alpha: number) => ((Math.round(alpha * 255) << 24) | 0x171717) >>> 0;
  // The lattice brightens toward the bottom, where the plane comes closer.
  const dots = Array.from({ length: 8 }, (_, i) => ink(0.07 + i * 0.016));
  const stream = ink(0.24);

  const pairs = [
    { at: 0.37, gap: 0.035, phase: 0 },
    { at: 0.66, gap: 0.04, phase: 2.1 }
  ];

  $effect(() => {
    const el = canvas;
    const ctx = el?.getContext('2d');
    if (!el || !ctx) return;

    const still = matchMedia('(prefers-reduced-motion: reduce)').matches;
    let w = 0;
    let h = 0;
    let image: ImageData | null = null;
    let pixels = new Uint32Array(0);
    let frame = 0;
    let visible = true;
    let last = 0;

    // The fold: columns spread away from the centre toward the bottom.
    function fan(x: number, y: number, t: number): number {
      const k = 1 + 0.06 * Math.sin(t * 0.15);
      return w / 2 + (x - w / 2) * (1 + k * (y / h) ** 2.2);
    }

    function draw(t: number) {
      if (!image) return;
      pixels.fill(0);

      // The lattice: one cell per dot, two near the bottom where it is closer.
      const sx = 14;
      const sy = 11;
      for (let gy = sy; gy < h; gy += sy) {
        const tall = gy > h * 0.7 ? 2 : 1;
        const dot = dots[Math.min(7, Math.floor((gy / h) * 8))]!;
        for (let gx = -w; gx < w * 2; gx += sx) {
          const x = Math.round(fan(gx, gy, t));
          if (x < 0 || x >= w) continue;
          for (let k = 0; k < tall && gy + k < h; k++) pixels[(gy + k) * w + x] = dot;
        }
      }

      // The streams, dithered so their edges thin out into the lattice. On a
      // phone they start lower, under the headline rather than through it.
      const start = w * cell < 768 ? 0.5 : 0.18;
      for (const pair of pairs) {
        const sway = Math.sin(t * 0.11 + pair.phase) * w * 0.012;
        for (const offset of [0, pair.gap]) {
          const x0 = (pair.at + offset) * w + sway;
          for (let y = Math.round(h * start); y < h; y++) {
            const v = y / h;
            // Bend right toward the bottom, on top of the fold.
            const curl = Math.max(0, v - 0.45) ** 2 * w * 0.4;
            const centre = fan(x0, y, t) + curl;
            const half = 2 + v * 6;
            const reach = Math.ceil(half * 2.2);
            const rise = Math.min(1, (v - start) / 0.3);
            const surge = 0.55 + 0.45 * Math.sin(y * 0.07 - t * 1.1 + pair.phase + offset * 40);
            const row = y * w;
            const threshold = (y & 3) * 4;
            for (
              let x = Math.max(0, Math.floor(centre - reach));
              x < Math.min(w, centre + reach);
              x++
            ) {
              const d = (x - centre) / half;
              const stripe = x & 1 ? 0.6 : 1;
              const level = Math.exp(-d * d) * rise * surge * stripe;
              if (level > bayer[threshold + (x & 3)]!) pixels[row + x] = stream;
            }
          }
        }
      }

      ctx!.putImageData(image, 0, 0);
    }

    function resize() {
      const box = el!.getBoundingClientRect();
      w = Math.max(1, Math.ceil(box.width / cell));
      h = Math.max(1, Math.ceil(box.height / cell));
      el!.width = w;
      el!.height = h;
      image = ctx!.createImageData(w, h);
      pixels = new Uint32Array(image.data.buffer);
      draw(last / 1000);
    }

    function tick(now: number) {
      frame = requestAnimationFrame(tick);
      if (!visible || now - last < 42) return;
      last = now;
      draw(now / 1000);
    }

    const sizes = new ResizeObserver(resize);
    sizes.observe(el);
    const sight = new IntersectionObserver(([entry]) => (visible = entry?.isIntersecting ?? true));
    sight.observe(el);
    if (!still) frame = requestAnimationFrame(tick);

    return () => {
      cancelAnimationFrame(frame);
      sizes.disconnect();
      sight.disconnect();
    };
  });
</script>

<canvas
  bind:this={canvas}
  class="pointer-events-none absolute inset-0 h-full w-full [image-rendering:pixelated]"
  aria-hidden="true"
></canvas>
