<!--
  tide's meeting stage, recreated with the app's own tokens, type and icons:
  the hairline, the clock and room name, four tiles with name labels, and the
  control bar. The active speaker moves around the room; under reduced motion
  it stays put. Nothing here is interactive.
-->
<script lang="ts">
  import '@fontsource-variable/inter';
  import CaretUp from 'phosphor-svelte/lib/CaretUp';
  import ChatTeardropText from 'phosphor-svelte/lib/ChatTeardropText';
  import Info from 'phosphor-svelte/lib/Info';
  import Microphone from 'phosphor-svelte/lib/Microphone';
  import MicrophoneSlash from 'phosphor-svelte/lib/MicrophoneSlash';
  import PhoneDisconnect from 'phosphor-svelte/lib/PhoneDisconnect';
  import Record from 'phosphor-svelte/lib/Record';
  import Screencast from 'phosphor-svelte/lib/Screencast';
  import UserRectangle from 'phosphor-svelte/lib/UserRectangle';
  import UsersThree from 'phosphor-svelte/lib/UsersThree';
  import VideoCamera from 'phosphor-svelte/lib/VideoCamera';

  const people = [
    { name: 'Daniel', photo: '/meeting/daniel.jpg' },
    { name: 'Maya', photo: '/meeting/maya.jpg' },
    { name: 'Marcus', photo: '/meeting/marcus.jpg', muted: true },
    { name: 'Nikos', photo: '/meeting/nikos.jpg', you: true }
  ];
  const speakers = [1, 0, 3];

  let turn = $state(0);
  let clock = $state('');

  $effect(() => {
    const format = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' });
    const tick = () => (clock = format.format(new Date()));
    tick();
    const minutes = setInterval(tick, 10_000);
    const still = matchMedia('(prefers-reduced-motion: reduce)').matches;
    const talk = still ? undefined : setInterval(() => (turn = (turn + 1) % speakers.length), 3400);
    return () => {
      clearInterval(minutes);
      clearInterval(talk);
    };
  });
</script>

<div
  class="stage"
  role="img"
  aria-label="A tide meeting with four people on the stage and the meeting controls below them"
>
  <div class="hairline"></div>
  <div class="top" aria-hidden="true">
    <span class="clock">{clock}</span>
    <span class="rule"></span>
    <span class="room">Weekly sync</span>
    <Info size={15} />
  </div>

  <div class="grid" aria-hidden="true">
    {#each people as person, index (person.name)}
      <div class="tile" class:speaking={speakers[turn] === index}>
        <img src={person.photo} alt="" />
        <span class="label">
          <span>{person.name}</span>
          {#if person.you}<span class="you">(You)</span>{/if}
          {#if person.muted}<MicrophoneSlash size={14} />{/if}
        </span>
      </div>
    {/each}
  </div>

  <div class="bar" aria-hidden="true">
    <span class="control"><Microphone size={16} /></span>
    <span class="caret"><CaretUp size={10} weight="bold" /></span>
    <span class="control"><VideoCamera size={16} /></span>
    <span class="caret"><CaretUp size={10} weight="bold" /></span>
    <span class="control"><Screencast size={16} /></span>
    <span class="control"><Record size={16} /></span>
    <span class="separator"></span>
    <span class="control"><UserRectangle size={16} /></span>
    <span class="control people"><UsersThree size={16} /><span class="badge">4</span></span>
    <span class="control"><ChatTeardropText size={16} /></span>
    <span class="separator"></span>
    <span class="control leave"><PhoneDisconnect size={16} /></span>
  </div>
</div>

<style>
  /* The app's dark meeting tokens (web/src/app.css). */
  .stage {
    --stage: #0f0f0e;
    --panel: #161615;
    --panel-2: #1e1e1c;
    --border-d: #2a2a27;
    --text: #edede6;
    --text-2: #8f8f85;
    --accent: #2320e6;
    --accent-d: #5b58ff;
    --rec: #e5484d;

    position: relative;
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 0 12px 12px;
    overflow: hidden;
    color: var(--text);
    background: var(--stage);
    border: 1px solid var(--border-d);
    border-radius: 10px;
    box-shadow:
      0 0 0 1px rgb(0 0 0 / 0.4),
      0 24px 60px -24px rgb(0 0 0 / 0.8);
    font-family: 'Inter Variable', ui-sans-serif, system-ui, sans-serif;
    font-size: 13px;
  }

  .hairline {
    height: 2px;
    margin: 0 -12px;
    background: var(--accent);
  }

  .top {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 22px;
    color: var(--text);
  }

  .clock {
    min-width: 2.6em;
    font-size: 12.5px;
    font-variant-numeric: tabular-nums;
  }

  .rule {
    width: 1px;
    height: 12px;
    background: var(--border-d);
  }

  .room {
    font-weight: 500;
  }

  .top :global(svg) {
    color: var(--text-2);
  }

  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
  }

  .tile {
    position: relative;
    aspect-ratio: 16 / 9;
    overflow: hidden;
    background: var(--panel-2);
    border: 1px solid var(--border-d);
    border-radius: 8px;
    transition: border-color 120ms ease-out;
  }

  .tile.speaking {
    border-color: var(--accent-d);
  }

  .tile img {
    width: 100%;
    height: 100%;
    object-fit: cover;
    border-radius: 7px;
  }

  .label {
    position: absolute;
    bottom: 6px;
    left: 6px;
    display: flex;
    align-items: center;
    gap: 5px;
    padding: 1px 6px;
    color: var(--text-2);
    font-size: 12px;
    background: color-mix(in srgb, var(--stage) 78%, transparent);
    border: 1px solid var(--border-d);
    border-radius: 4px;
    transition: color 120ms ease-out;
  }

  .speaking .label {
    color: var(--text);
  }

  .you {
    color: var(--text-2);
  }

  .bar {
    display: flex;
    align-items: center;
    align-self: center;
    gap: 6px;
    margin-top: 2px;
    padding: 8px;
    background: var(--panel);
    border: 1px solid var(--border-d);
    border-radius: 6px;
  }

  .control {
    position: relative;
    display: grid;
    width: 28px;
    height: 28px;
    place-items: center;
    border-radius: 4px;
  }

  .caret {
    display: grid;
    width: 12px;
    margin-left: -4px;
    place-items: center;
    color: var(--text-2);
  }

  .separator {
    width: 1px;
    height: 16px;
    background: var(--border-d);
  }

  .badge {
    position: absolute;
    top: -3px;
    right: -3px;
    display: grid;
    min-width: 14px;
    height: 14px;
    padding: 0 3px;
    place-items: center;
    color: #fff;
    font-size: 9px;
    font-weight: 600;
    background: var(--accent-d);
    border-radius: 7px;
  }

  .leave {
    color: var(--rec);
  }

  @media (max-width: 480px) {
    .stage {
      gap: 8px;
      padding: 0 8px 8px;
    }

    .hairline {
      margin: 0 -8px;
    }

    .grid {
      gap: 6px;
    }

    .label {
      font-size: 11px;
    }

    .bar {
      gap: 2px;
      padding: 6px;
    }

    .caret {
      display: none;
    }
  }
</style>
