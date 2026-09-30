<script lang="ts">
  import type { RoomState } from '$lib/rtc/room.svelte';

  let { rtc }: { rtc: RoomState } = $props();

  let draft = $state('');
  let error = $state('');
  let messageList: HTMLDivElement;

  $effect(() => {
    rtc.chatRevision;
    queueMicrotask(() => {
      if (messageList) messageList.scrollTop = messageList.scrollHeight;
    });
  });

  async function send(): Promise<void> {
    const text = draft.trim();
    if (!text) return;
    error = '';
    try {
      await rtc.sendChat(text);
      draft = '';
    } catch {
      error = 'Message not sent. Try again.';
    }
  }

  function keydown(event: KeyboardEvent): void {
    if (event.key !== 'Enter' || event.shiftKey) return;
    event.preventDefault();
    void send();
  }

  function submit(event: SubmitEvent): void {
    event.preventDefault();
    void send();
  }
</script>

<aside class="chat-panel" aria-label="Chat">
  <header>
    <h2>Chat</h2>
  </header>

  <div class="messages" bind:this={messageList} aria-live="polite">
    {#if rtc.chat.length === 0}
      <p class="empty">Messages appear here for this meeting only.</p>
    {:else}
      {#each rtc.chat as message, index (`${message.ts}-${index}`)}
        <article class:mine={message.mine} title={new Date(message.ts).toLocaleString()}>
          <div class="name">{message.mine ? 'You' : message.from}</div>
          <p>{message.text}</p>
        </article>
      {/each}
    {/if}
  </div>

  <form onsubmit={submit}>
    <input
      bind:value={draft}
      name="chat-message"
      aria-label="Chat message"
      autocomplete="off"
      maxlength="2000"
      placeholder="Message"
      onkeydown={keydown}
    />
    <button type="submit" disabled={!draft.trim()} aria-label="Send message">Send</button>
  </form>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
</aside>

<style>
  .chat-panel {
    position: fixed;
    z-index: 15;
    top: 50px;
    right: 12px;
    bottom: 78px;
    display: grid;
    width: min(300px, calc(100vw - 24px));
    padding: 12px;
    grid-template-rows: auto minmax(0, 1fr) auto auto;
    gap: 8px;
    color: var(--text);
    background: var(--panel);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-card);
    animation: panel-in var(--motion-fast);
  }

  header,
  form {
    display: flex;
    align-items: center;
  }

  header {
    justify-content: space-between;
  }

  h2,
  p {
    margin: 0;
  }

  h2 {
    color: var(--text-2);
    font-size: 12px;
    font-weight: 550;
  }

  .empty {
    color: var(--text-2);
  }

  .name {
    font-size: 11px;
  }

  .messages {
    display: flex;
    min-height: 0;
    flex-direction: column;
    gap: 8px;
    overflow-y: auto;
    overscroll-behavior: contain;
  }

  .empty {
    margin: auto 0;
    padding: 12px;
    font-size: 12px;
    text-align: center;
  }

  article {
    max-width: 92%;
    padding: 6px 8px;
    align-self: flex-start;
    background: var(--panel-2);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-card);
  }

  article.mine {
    align-self: flex-end;
    border-color: color-mix(in srgb, var(--accent-d) 36%, var(--border-d));
  }

  .name {
    margin-bottom: 1px;
    overflow: hidden;
    color: var(--text-2);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  article p {
    overflow-wrap: anywhere;
    color: var(--text);
    font-size: 13px;
    line-height: 19px;
    white-space: pre-wrap;
  }

  form {
    gap: 4px;
  }

  input {
    min-width: 0;
    height: var(--control-height);
    padding: 3px 7px;
    flex: 1;
    color: var(--text);
    background: var(--panel-2);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-control);
  }

  input::placeholder {
    color: var(--text-2);
  }

  input:focus-visible,
  button:focus-visible {
    outline-color: var(--accent-d);
  }

  button {
    height: var(--control-height);
    padding: 3px 8px;
    color: white;
    background: var(--accent-d);
    border: 1px solid var(--accent-d);
    border-radius: var(--radius-control);
  }

  button:disabled {
    cursor: default;
    opacity: 0.45;
  }

  .error {
    color: var(--rec);
    font-size: 11px;
  }

  @keyframes panel-in {
    from {
      opacity: 0;
      transform: translateX(6px);
    }
  }
</style>
