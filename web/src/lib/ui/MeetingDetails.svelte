<script lang="ts">
  import { onDestroy } from 'svelte';
  import { Popover } from 'bits-ui';
  import { Check, Copy, Info } from 'phosphor-svelte';

  let { name, url }: { name: string; url: string } = $props();

  let open = $state(false);
  let copied = $state(false);
  let copyFailed = $state(false);
  let link = $state<HTMLElement>();
  let copiedTimer: ReturnType<typeof setTimeout> | undefined;

  /** Clipboard API where the page is a secure context, a selection copy where it isn't. */
  async function writeClipboard(text: string): Promise<void> {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      // Plain-http deployments have no navigator.clipboard.
    }
    const selection = window.getSelection();
    if (!link || !selection) throw new Error('No clipboard.');
    selection.selectAllChildren(link);
    if (!document.execCommand('copy')) throw new Error('No clipboard.');
  }

  async function copy(): Promise<void> {
    clearTimeout(copiedTimer);
    copyFailed = false;
    try {
      await writeClipboard(url);
      copied = true;
    } catch {
      // Leave the link selected so it can be copied by hand.
      copied = false;
      copyFailed = true;
    }
    copiedTimer = setTimeout(() => {
      copied = false;
      copyFailed = false;
    }, 1_500);
  }

  $effect(() => {
    if (open) return;
    clearTimeout(copiedTimer);
    copied = false;
    copyFailed = false;
  });

  onDestroy(() => clearTimeout(copiedTimer));
</script>

<Popover.Root bind:open>
  <Popover.Trigger>
    {#snippet child({ props })}
      <button
        {...props}
        type="button"
        class="details-trigger"
        aria-label="Meeting details"
        title="Meeting details"
      >
        <Info size={16} weight="regular" aria-hidden="true" />
      </button>
    {/snippet}
  </Popover.Trigger>
  <Popover.Content side="bottom" align="start" sideOffset={6} collisionPadding={12}>
    {#snippet child({ wrapperProps, props, open: shown })}
      {#if shown}
        <div {...wrapperProps}>
          <div {...props} class="details" role="dialog" aria-label="Meeting details">
            <p class="details-name">{name}</p>
            <div class="details-link">
              <span class="link mono" bind:this={link}>{url}</span>
              <button
                type="button"
                class="copy"
                class:done={copied}
                aria-label="Copy meeting link"
                title="Copy meeting link"
                onclick={() => void copy()}
              >
                {#if copied}
                  <Check size={16} weight="regular" aria-hidden="true" />
                {:else}
                  <Copy size={16} weight="regular" aria-hidden="true" />
                {/if}
              </button>
            </div>
            <span class="visually-hidden" role="status">{copied ? 'Link copied' : ''}</span>
            {#if copyFailed}
              <p class="details-error">Couldn't copy. The link is selected; copy it by hand.</p>
            {/if}
          </div>
        </div>
      {/if}
    {/snippet}
  </Popover.Content>
</Popover.Root>

<style>
  .details-trigger,
  .copy {
    display: grid;
    flex: none;
    width: var(--control-height);
    height: var(--control-height);
    padding: 0;
    place-items: center;
    color: var(--text-2);
    background: transparent;
    border: 1px solid transparent;
    border-radius: var(--radius-control);
    transition:
      color var(--motion-fast),
      background var(--motion-fast),
      border-color var(--motion-fast);
  }

  .details-trigger:hover,
  .details-trigger[aria-expanded='true'],
  .copy:hover {
    color: var(--text);
    background: var(--panel-2);
    border-color: var(--border-d);
  }

  .copy.done {
    color: var(--ok);
  }

  .details {
    /* bits-ui lifts its floating wrapper to the content's z-index: above
       tiles, side panels (15) and the control bar (20). */
    position: relative;
    z-index: 30;
    display: grid;
    gap: 8px;
    width: min(320px, calc(100vw - 24px));
    padding: 12px;
    color: var(--text);
    background: var(--panel);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-card);
    box-shadow: 0 8px 24px rgb(0 0 0 / 0.45);
  }

  .details:focus {
    outline: none;
  }

  .details-name {
    margin: 0;
    overflow-wrap: anywhere;
    font-weight: 550;
  }

  .details-link {
    display: flex;
    align-items: center;
    gap: 4px;
    padding-left: 8px;
    background: var(--stage);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-control);
  }

  .link {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    color: var(--text);
    font-size: 12px;
    white-space: nowrap;
    text-overflow: ellipsis;
    user-select: all;
  }

  .details-error {
    margin: 0;
    color: var(--text-2);
    font-size: 11px;
    line-height: 16px;
  }

  .visually-hidden {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }
</style>
