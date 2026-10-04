<script lang="ts">
  import type { Snippet } from 'svelte';
  import { X } from 'phosphor-svelte';

  // A modal on the light shell (ARCHITECTURE.md §10): the native <dialog>
  // opened with showModal(), so the page behind is inert (focus stays
  // inside), Escape closes it and focus returns to the opener. 6px radius,
  // 1px border and the one elevation dialogs are allowed.
  let {
    open = $bindable(false),
    title,
    width = 400,
    dismissible = true,
    children
  }: {
    open?: boolean;
    title: string;
    /** Max width in px; the dialog shrinks to fit a phone. */
    width?: number;
    /**
     * False while the dialog must stay up, e.g. during a request: Escape,
     * the backdrop and the close button are ignored. The owner can still
     * close it by setting `open`.
     */
    dismissible?: boolean;
    children: Snippet;
  } = $props();

  const id = $props.id();
  const titleId = `${id}-title`;
  let dialog: HTMLDialogElement;
  let pressedBackdrop = false;

  $effect(() => {
    if (open && !dialog.open) {
      dialog.showModal();
      // Start on the first field rather than the close button.
      dialog.querySelector<HTMLElement>('[data-autofocus]')?.focus();
    } else if (!open && dialog.open) dialog.close();
  });

  // A click that both starts and ends on the backdrop closes the dialog; a
  // text selection dragged out of an input does not.
  function pointerdown(event: PointerEvent): void {
    pressedBackdrop = event.target === dialog;
  }

  function click(event: MouseEvent): void {
    if (pressedBackdrop && event.target === dialog && dismissible) open = false;
    pressedBackdrop = false;
  }

  // Escape is a close request: refused at the key and at the native cancel.
  // Chrome stops honouring preventDefault on cancel after repeated Escapes
  // without other input, so the keydown is the dependable guard.
  function keydown(event: KeyboardEvent): void {
    if (event.key === 'Escape' && !dismissible) event.preventDefault();
  }

  function cancel(event: Event): void {
    if (!dismissible) event.preventDefault();
  }

  function closed(): void {
    // Closed natively anyway while it must stay up: put it back.
    if (open && !dismissible) {
      dialog.showModal();
      return;
    }
    open = false;
  }
</script>

<dialog
  bind:this={dialog}
  aria-labelledby={titleId}
  style:--dialog-width={`${width}px`}
  onclose={closed}
  oncancel={cancel}
  onkeydown={keydown}
  onpointerdown={pointerdown}
  onclick={click}
>
  {#if open}
    <div class="panel">
      <header>
        <h2 id={titleId}>{title}</h2>
        <button
          type="button"
          class="close"
          aria-label="Close"
          disabled={!dismissible}
          onclick={() => (open = false)}
        >
          <X size={16} weight="regular" aria-hidden="true" />
        </button>
      </header>
      {@render children()}
    </div>
  {/if}
</dialog>

<style>
  dialog {
    width: min(var(--dialog-width), calc(100vw - 32px));
    max-height: calc(100dvh - 32px);
    padding: 0;
    overflow: auto;
    color: var(--ink);
    background: var(--paper);
    border: 1px solid var(--border);
    border-radius: var(--radius-card);
    box-shadow: 0 8px 24px rgb(23 23 23 / 14%);
  }

  dialog::backdrop {
    background: rgb(23 23 23 / 24%);
  }

  .panel {
    padding: 12px;
  }

  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin: -4px 0 12px;
  }

  h2 {
    margin: 0;
    font-size: 15px;
    line-height: 20px;
    font-weight: 550;
  }

  .close {
    display: grid;
    width: var(--control-height);
    height: var(--control-height);
    margin-right: -6px;
    padding: 0;
    place-items: center;
    color: var(--ink-2);
    background: transparent;
    border: 0;
    border-radius: var(--radius-control);
    transition:
      color var(--motion-fast),
      background var(--motion-fast);
  }

  .close:hover:enabled {
    color: var(--ink);
    background: var(--surface-2);
  }

  .close:disabled {
    cursor: default;
    opacity: 0.4;
  }
</style>
