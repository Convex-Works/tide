<script lang="ts">
  import { onDestroy, tick } from 'svelte';
  import { Popover } from 'bits-ui';
  import { Check, Copy, Info, Robot } from 'phosphor-svelte';
  import { agentSession } from '#lib/api/client.js';

  let {
    url,
    slug = '',
    isOwner = false
  }: {
    url: string;
    slug?: string;
    /** Offers an agent session; the server checks for itself (ARCHITECTURE.md §4). */
    isOwner?: boolean;
  } = $props();

  let open = $state(false);
  let copied = $state(false);
  let copyFailed = $state(false);
  let link = $state<HTMLElement>();
  let copiedTimer: ReturnType<typeof setTimeout> | undefined;
  let sessionBusy = $state(false);
  let sessionCopied = $state(false);
  let sessionError = $state('');
  // Only on screen when the clipboard refused it, to be copied by hand.
  let sessionText = $state('');
  let session = $state<HTMLElement>();
  let sessionTimer: ReturnType<typeof setTimeout> | undefined;

  /** A selection copy of what an element shows, which stays selected if it fails. */
  function copySelection(element: HTMLElement | undefined): void {
    const selection = window.getSelection();
    if (!element || !selection) throw new Error('No clipboard.');
    selection.selectAllChildren(element);
    if (!document.execCommand('copy')) throw new Error('No clipboard.');
  }

  /** Clipboard API where the page is a secure context, a selection copy where it isn't. */
  async function writeClipboard(text: string): Promise<void> {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      // Plain-http deployments have no navigator.clipboard.
    }
    copySelection(link);
  }

  /** The way a program without moil gets its session.json (docs/MEETING-AGENTS.md). */
  async function copySession(): Promise<void> {
    clearTimeout(sessionTimer);
    sessionBusy = true;
    sessionCopied = false;
    sessionError = '';
    sessionText = '';
    try {
      const text = JSON.stringify(await agentSession(slug), null, 2);
      try {
        await navigator.clipboard.writeText(text);
      } catch {
        sessionText = text;
        await tick();
        copySelection(session);
        sessionText = '';
      }
      sessionCopied = true;
      sessionTimer = setTimeout(() => (sessionCopied = false), 1_500);
    } catch (cause) {
      sessionError = sessionText
        ? "Couldn't copy. The session is selected; copy it by hand."
        : cause instanceof Error
          ? cause.message
          : 'Could not start an agent session. Try again.';
    } finally {
      sessionBusy = false;
    }
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
    clearTimeout(sessionTimer);
    sessionCopied = false;
    sessionError = '';
    sessionText = '';
  });

  onDestroy(() => {
    clearTimeout(copiedTimer);
    clearTimeout(sessionTimer);
  });
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
            <p class="details-label">Meeting link</p>
            <span class="link" bind:this={link}>{url}</span>
            <button type="button" class="copy" class:done={copied} onclick={() => void copy()}>
              {#if copied}
                <Check size={16} weight="regular" aria-hidden="true" />
                Copied
              {:else}
                <Copy size={16} weight="regular" aria-hidden="true" />
                Copy link
              {/if}
            </button>
            <span class="visually-hidden" role="status">
              {copied ? 'Link copied' : sessionCopied ? 'Agent session copied' : ''}
            </span>
            {#if copyFailed}
              <p class="details-error">Couldn't copy. The link is selected; copy it by hand.</p>
            {/if}
            {#if isOwner}
              <p class="details-label agent-label">Agent</p>
              <button
                type="button"
                class="copy"
                class:done={sessionCopied}
                disabled={sessionBusy}
                onclick={() => void copySession()}
              >
                {#if sessionCopied}
                  <Check size={16} weight="regular" aria-hidden="true" />
                  Copied
                {:else}
                  <Robot size={16} weight="regular" aria-hidden="true" />
                  Copy agent session
                {/if}
              </button>
              <p class="details-hint">
                A program can connect with it for 10 minutes. Everyone sees it.
              </p>
              {#if sessionText}<pre class="session" bind:this={session}>{sessionText}</pre>{/if}
              {#if sessionError}<p class="details-error" role="alert">{sessionError}</p>{/if}
            {/if}
          </div>
        </div>
      {/if}
    {/snippet}
  </Popover.Content>
</Popover.Root>

<style>
  .details-trigger {
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
  .details-trigger[aria-expanded='true'] {
    color: var(--text);
    background: var(--panel-2);
    border-color: var(--border-d);
  }

  .copy {
    display: inline-flex;
    justify-self: start;
    align-items: center;
    gap: 6px;
    height: var(--control-height);
    padding: 0 10px;
    color: var(--text);
    font: inherit;
    background: var(--panel-2);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-control);
    transition:
      background var(--motion-fast),
      border-color var(--motion-fast);
  }

  .copy:hover {
    border-color: var(--text-2);
  }

  .copy.done {
    color: var(--ok);
  }

  .copy:disabled {
    cursor: default;
    opacity: 0.45;
  }

  .agent-label {
    padding-top: 8px;
    border-top: 1px solid var(--border-d);
  }

  .session {
    max-height: 96px;
    margin: 0;
    overflow: auto;
    color: var(--text);
    font-size: 11px;
    line-height: 16px;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    user-select: all;
  }

  .details {
    /* bits-ui lifts its floating wrapper to the content's z-index: above
       tiles, side panels (15) and the control bar (20). */
    position: relative;
    z-index: 30;
    display: grid;
    gap: 8px;
    width: min(300px, calc(100vw - 24px));
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

  .details-label {
    margin: 0;
    color: var(--text-2);
    font-size: 12px;
    line-height: 16px;
  }

  /* The whole link, wrapped where it must: a link cut off by an ellipsis
     can't be read or checked before it is shared. */
  .link {
    color: var(--text);
    overflow-wrap: anywhere;
    user-select: all;
  }

  .details-hint,
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
