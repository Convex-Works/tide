<script lang="ts">
  import { DotsThreeVertical, Eye, EyeSlash, MicrophoneSlash, UserMinus } from 'phosphor-svelte';
  import type { ParticipantView } from './media';

  let {
    participant,
    open = false,
    videoHidden = false,
    canManage = false,
    onopenchange = () => undefined,
    ontogglevideo = () => undefined,
    onmute = () => undefined,
    onremove = () => undefined
  }: {
    participant: ParticipantView;
    open?: boolean;
    videoHidden?: boolean;
    canManage?: boolean;
    onopenchange?: (open: boolean) => void;
    ontogglevideo?: () => void | Promise<void>;
    onmute?: () => void | Promise<void>;
    onremove?: () => void | Promise<void>;
  } = $props();

  let trigger: HTMLButtonElement;
  let busyAction = $state<'video' | 'mute' | 'remove' | ''>('');
  let confirmRemove = $state(false);
  let error = $state('');

  $effect(() => {
    if (open) return;
    confirmRemove = false;
    error = '';
  });

  function close(focusTrigger = false): void {
    onopenchange(false);
    if (focusTrigger) trigger?.focus();
  }

  function toggleMenu(): void {
    onopenchange(!open);
  }

  async function run(
    action: 'video' | 'mute' | 'remove',
    callback: () => void | Promise<void>,
    fallback: string
  ): Promise<void> {
    if (busyAction) return;
    busyAction = action;
    error = '';
    try {
      await callback();
      close();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : fallback;
    } finally {
      busyAction = '';
    }
  }

  function remove(): void {
    if (!confirmRemove) {
      confirmRemove = true;
      return;
    }
    void run('remove', onremove, 'Could not remove this person.');
  }
</script>

<svelte:window
  onclick={() => {
    if (open) close();
  }}
  onkeydown={(event) => {
    if (open && event.key === 'Escape') close(true);
  }}
/>

<!-- The propagation shield keeps this menu open while its own controls run. -->
<!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
<div class="participant-actions" onclick={(event) => event.stopPropagation()}>
  <button
    bind:this={trigger}
    class="menu-trigger"
    class:active={open}
    type="button"
    aria-label={`Actions for ${participant.name}`}
    aria-haspopup="menu"
    aria-expanded={open}
    title={`Actions for ${participant.name}`}
    onclick={toggleMenu}
  >
    <DotsThreeVertical size={16} weight="bold" aria-hidden="true" />
  </button>

  {#if open}
    <div class="participant-menu" role="menu" aria-label={`${participant.name} actions`}>
      <button
        type="button"
        role="menuitem"
        disabled={Boolean(busyAction)}
        onclick={() =>
          void run(
            'video',
            ontogglevideo,
            videoHidden ? 'Could not show this video.' : 'Could not hide this video.'
          )}
      >
        {#if videoHidden}
          <Eye size={16} weight="regular" aria-hidden="true" />
          <span>Show video</span>
        {:else}
          <EyeSlash size={16} weight="regular" aria-hidden="true" />
          <span>Hide video</span>
        {/if}
      </button>

      {#if canManage}
        <div class="separator" aria-hidden="true"></div>
        <button
          type="button"
          role="menuitem"
          disabled={Boolean(busyAction) || participant.micMuted}
          onclick={() => void run('mute', onmute, 'Could not mute this person.')}
        >
          <MicrophoneSlash size={16} weight="regular" aria-hidden="true" />
          <span>{participant.micMuted ? 'Muted' : 'Mute'}</span>
        </button>
        <button
          class="danger"
          class:confirm={confirmRemove}
          type="button"
          role="menuitem"
          disabled={Boolean(busyAction)}
          onclick={remove}
        >
          <UserMinus size={16} weight="regular" aria-hidden="true" />
          <span>{confirmRemove ? 'Remove?' : 'Remove'}</span>
        </button>
      {/if}

      {#if error}<p class="error" role="alert">{error}</p>{/if}
    </div>
  {/if}
</div>

<style>
  .participant-actions {
    position: absolute;
    z-index: 4;
    top: 6px;
    right: 6px;
  }

  button {
    color: var(--text-2);
    background: color-mix(in srgb, var(--stage) 82%, transparent);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-control);
  }

  button:hover:not(:disabled),
  button:focus-visible,
  button.active {
    color: var(--text);
    background: var(--panel);
  }

  button:focus-visible {
    outline-color: var(--accent-d);
  }

  button:disabled {
    cursor: default;
    opacity: 0.45;
  }

  .menu-trigger {
    display: grid;
    width: 28px;
    height: 28px;
    padding: 0;
    place-items: center;
    opacity: 0.78;
    transition:
      color var(--motion-fast),
      background var(--motion-fast),
      opacity var(--motion-fast);
  }

  .menu-trigger:hover,
  .menu-trigger:focus-visible,
  .menu-trigger.active {
    opacity: 1;
  }

  .participant-menu {
    position: absolute;
    top: 32px;
    right: 0;
    display: grid;
    width: 160px;
    padding: 4px;
    gap: 2px;
    color: var(--text);
    background: var(--panel);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-card);
  }

  .participant-menu button {
    display: flex;
    width: 100%;
    min-height: 28px;
    padding: 4px 7px;
    align-items: center;
    gap: 7px;
    color: var(--text-2);
    font-size: 12px;
    text-align: left;
    background: transparent;
    border-color: transparent;
  }

  .participant-menu button:hover:not(:disabled),
  .participant-menu button:focus-visible {
    color: var(--text);
    background: var(--panel-2);
  }

  .participant-menu button.danger:hover:not(:disabled),
  .participant-menu button.danger:focus-visible,
  .participant-menu button.confirm {
    color: var(--rec);
  }

  .separator {
    height: 1px;
    margin: 2px 3px;
    background: var(--border-d);
  }

  .error {
    margin: 3px 5px 2px;
    color: var(--rec);
    font-size: 11px;
    line-height: 15px;
  }

  @media (prefers-reduced-motion: reduce) {
    .menu-trigger {
      transition: none;
    }
  }
</style>
