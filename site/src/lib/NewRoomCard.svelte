<!--
  tide's "New room" dialog, recreated with the app's light-shell tokens: name,
  link with its readable random slug, calendar times, and the two actions.
  Decorative: nothing here is interactive.
-->
<script lang="ts">
  import '@fontsource-variable/inter';
  import '@fontsource-variable/geist-mono';
  import CalendarBlank from 'phosphor-svelte/lib/CalendarBlank';
  import Check from 'phosphor-svelte/lib/Check';
  import Clock from 'phosphor-svelte/lib/Clock';
  import Shuffle from 'phosphor-svelte/lib/Shuffle';
  import X from 'phosphor-svelte/lib/X';
</script>

<div class="card" aria-hidden="true">
  <div class="head">
    <span class="title">New room</span>
    <X size={16} />
  </div>
  <span class="label">Name</span>
  <span class="field">Weekly sync</span>
  <span class="label">Link</span>
  <span class="field link">
    <span class="slug"><span class="prefix">https://meet.example.com/m/</span>chw-pnih-sxi</span>
    <Shuffle size={16} />
  </span>
  <span class="check"
    ><span class="box"><Check size={10} weight="bold" /></span>Add to calendar</span
  >
  <div class="times">
    <span class="label">Date</span>
    <span class="label">Starts</span>
    <span class="label">Ends</span>
    <span class="field">10/05/2026 <CalendarBlank size={14} /></span>
    <span class="field">07:30 PM <Clock size={14} /></span>
    <span class="field">08:00 PM <Clock size={14} /></span>
  </div>
  <div class="actions">
    <span class="button">Create</span>
    <span class="button accent">Create &amp; join</span>
  </div>
</div>

<style>
  /* The app's light-shell tokens (web/src/app.css). */
  .card {
    --paper: #f5f5f0;
    --surface: #ecece6;
    --border: #d8d8ce;
    --ink: #171717;
    --ink-2: #6e6e64;
    --accent: #2320e6;

    display: grid;
    padding: 12px;
    color: var(--ink);
    background: var(--paper);
    border: 1px solid var(--border);
    border-radius: 6px;
    box-shadow: 0 8px 24px rgb(23 23 23 / 14%);
    font-family: 'Inter Variable', ui-sans-serif, system-ui, sans-serif;
    font-size: 13px;
    font-weight: 450;
    line-height: 20px;
  }

  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin: -4px 0 8px;
    color: var(--ink-2);
  }

  .title {
    color: var(--ink);
    font-size: 15px;
    font-weight: 550;
  }

  .label {
    margin: 4px 0 4px;
    color: var(--ink-2);
    font-size: 12px;
  }

  .field {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    height: 28px;
    min-width: 0;
    padding: 0 8px;
    overflow: hidden;
    white-space: nowrap;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 4px;
  }

  .field :global(svg) {
    flex: none;
    color: var(--ink-2);
  }

  .link {
    margin-bottom: 4px;
  }

  .slug {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    font-family: 'Geist Mono Variable', ui-monospace, monospace;
    font-size: 12px;
  }

  .prefix {
    color: var(--ink-2);
  }

  .check {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 8px 0 4px;
  }

  .box {
    display: grid;
    width: 14px;
    height: 14px;
    place-items: center;
    color: #fff;
    background: var(--accent);
    border-radius: 3px;
  }

  .times {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 112px 112px;
    column-gap: 8px;
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 12px;
  }

  .button {
    display: inline-flex;
    align-items: center;
    height: 28px;
    padding: 0 10px;
    font-size: 12px;
    font-weight: 550;
    background: var(--paper);
    border: 1px solid var(--border);
    border-radius: 4px;
  }

  .button.accent {
    color: #fff;
    background: var(--accent);
    border-color: var(--accent);
  }
</style>
