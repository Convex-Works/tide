<!--
  The hero's background: two bands of water a shade darker than the stone,
  drifting slowly in opposite directions while the whole waterline rises and
  falls, like a tide. Each wave's period divides the 1440-unit tile, so the
  drift loops without a seam. Still under prefers-reduced-motion.
-->
<script lang="ts">
  const width = 1440;
  const height = 240;

  // A sine wave across two tiles, as smooth quadratic segments.
  function wave(base: number, amplitude: number, period: number): string {
    let d = `M0 ${base}`;
    for (let x = 0; x < width * 2; x += period) {
      d += ` Q${x + period / 4} ${base - amplitude} ${x + period / 2} ${base}`;
      d += ` T${x + period} ${base}`;
    }
    return `${d} V${height + 40} H0 Z`;
  }

  const back = wave(96, 9, 720);
  const front = wave(150, 7, 480);
</script>

<svg
  class="pointer-events-none absolute inset-x-0 bottom-0 h-[42%] w-full"
  viewBox="0 0 {width} {height}"
  preserveAspectRatio="none"
  aria-hidden="true"
>
  <g class="tide">
    <g class="drift slow"><path d={back} fill="#c2c2b9" /></g>
    <g class="drift fast"><path d={front} fill="#bdbdb4" /></g>
  </g>
</svg>

<style>
  @media (prefers-reduced-motion: no-preference) {
    .tide {
      animation: tide 18s ease-in-out infinite alternate;
    }

    .drift.slow {
      animation: drift 90s linear infinite;
    }

    .drift.fast {
      animation: drift 60s linear infinite reverse;
    }
  }

  @keyframes tide {
    from {
      transform: translateY(14px);
    }
    to {
      transform: translateY(-14px);
    }
  }

  @keyframes drift {
    to {
      transform: translateX(-1440px);
    }
  }
</style>
