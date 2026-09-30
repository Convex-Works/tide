import { expect, type Browser, type BrowserContext, type Page } from '@playwright/test';
import {
  probe,
  publishReplacementCamera,
  publishReplacementMicrophone,
  publishSyntheticScreen,
  setPublicationMuted,
  unpublishSynthetic,
  type MediaProbeSnapshot
} from '../media-helpers';
import { MEMBER, OWNER, sessionFor, type TestUser } from './session';

/**
 * A meeting with many actors over one browser and many contexts. One context
 * per participant costs a few hundred MB; one browser per participant costs
 * far more, and the mesh scenarios need five or more at once.
 *
 * Actors join through the real PreJoin form with capture off, then publish
 * synthetic canvas/oscillator tracks. The sealed stack serves plain http on a
 * non-localhost origin, so it is not a secure context and getUserMedia does
 * not exist — PreJoin would silently fall back to camera-off (PreJoin.svelte
 * flips camEnabled on capture failure) and the actor would publish nothing.
 * Synthetic tracks take the identical publishTrack -> SFU path, which is the
 * path these scenarios are about. Covering local capture as well needs the
 * stack served over TLS; see docs/ARCHITECTURE.md.
 */

export type ActorRole = 'owner' | 'member' | 'guest';

export interface LedgerEntry {
  at: number;
  event: string;
  identity?: string;
  publicationSid?: string;
  detail?: string;
}

export interface HandlerFault {
  at: number;
  event: string;
  message: string;
}

interface MediaTestBridge {
  __klisiRoom: {
    state: string;
    localParticipant: {
      identity: string;
      name: string;
      trackPublications: Map<string, { trackSid: string; source: string }>;
    };
  };
  __klisiMediaTest: {
    ledger(): LedgerEntry[];
    clearLedger(): void;
    uncaughtErrors(): string[];
    handlerFaults(): HandlerFault[];
    failNextParticipantEntered(): void;
    failNextReconcile(): void;
    deafen(): void;
    snapshot(): Promise<MediaProbeSnapshot>;
  };
}

export interface JoinOptions {
  /** Display name, also the label mesh assertions report against. */
  name: string;
  as?: ActorRole;
  camera?: boolean;
  microphone?: boolean;
}

const csrf = { 'X-Klisi-Csrf': '1' };

export class MeetingActor {
  /** LiveKit identity, resolved once the room reports connected. */
  identity = '';
  /** Publication sids by source, for mute and unpublish operations. */
  readonly publications = new Map<string, string>();

  constructor(
    readonly meeting: Meeting,
    readonly context: BrowserContext,
    readonly page: Page,
    readonly name: string,
    readonly role: ActorRole,
    private readonly camera: boolean,
    private readonly microphone: boolean
  ) {}

  /** Sources this actor is publishing, as mesh expectations read them. */
  get sources(): Set<string> {
    return new Set(this.publications.keys());
  }

  // Playwright serializes the callback and invokes it with the evaluate
  // argument, so the callback must reach for `window` itself and may not
  // close over anything. The casts are type-only and erased before the
  // source crosses into the browser.
  private bridge<T>(read: () => T): Promise<T> {
    return this.page.evaluate(read);
  }

  /**
   * Drives the real PreJoin form. Capture is forced off by polling the toggle
   * state rather than clicking blind: PreJoin also flips these off by itself
   * when capture fails, so a blind click can race and re-enable them.
   */
  async enterFromPreJoin(): Promise<void> {
    await this.page.goto(`/m/${this.meeting.slug}`);
    const nameField = this.page.locator('input[name="name"]');
    await expect(nameField).toBeVisible({ timeout: 30_000 });
    await nameField.fill(this.name);

    // Read the toggle state from aria-pressed rather than matching label
    // wording, and settle it before submitting: when capture is unavailable
    // PreJoin turns these off by itself, so a blind click can re-enable them.
    for (const control of ['camera', 'microphone']) {
      const button = this.page.locator(`button[aria-pressed][aria-label*="${control}" i]`).first();
      await expect(button).toBeVisible({ timeout: 15_000 });
      if ((await button.getAttribute('aria-pressed')) === 'true') await button.click();
      await expect(button).toHaveAttribute('aria-pressed', 'false', { timeout: 10_000 });
    }

    await this.page.getByRole('button', { name: 'Join meeting' }).click();
  }

  async waitUntilConnected(timeout = 45_000): Promise<void> {
    await expect(this.page.getByRole('button', { name: 'Share screen' })).toBeVisible({ timeout });
    await expect
      .poll(
        () =>
          this.bridge(() => (window as unknown as MediaTestBridge).__klisiRoom?.state ?? 'missing'),
        { timeout }
      )
      .toBe('connected');
    this.identity = await this.bridge(
      () => (window as unknown as MediaTestBridge).__klisiRoom.localParticipant.identity
    );
  }

  /** Publishes the sources this actor was created with. */
  async publishJoinMedia(): Promise<void> {
    if (this.camera) this.publications.set('camera', await publishReplacementCamera(this.page));
    if (this.microphone) {
      this.publications.set('microphone', await publishReplacementMicrophone(this.page));
    }
    await this.waitUntilPublishing();
  }

  /** What this actor is really sending, as opposed to what it was asked to. */
  async publishedSources(): Promise<string[]> {
    return this.bridge(() =>
      [
        ...(
          window as unknown as MediaTestBridge
        ).__klisiRoom.localParticipant.trackPublications.values()
      ]
        .map((publication) => publication.source)
        .sort()
    );
  }

  /** Capture-environment facts, reported when a publish never appears. */
  async captureDiagnostics(): Promise<Record<string, unknown>> {
    return this.bridge(async () => ({
      secureContext: window.isSecureContext,
      hasMediaDevices: typeof navigator.mediaDevices !== 'undefined'
    }));
  }

  /**
   * A participant that silently published nothing looks identical to a peer
   * that failed to subscribe, so fail here with the capture environment
   * attached rather than later inside a mesh assertion.
   */
  async waitUntilPublishing(timeout = 20_000): Promise<void> {
    const wanted = [...this.sources].sort();
    if (wanted.length === 0) return;
    try {
      await expect.poll(() => this.publishedSources(), { timeout }).toEqual(wanted);
    } catch {
      const actual = await this.publishedSources();
      const diagnostics = await this.captureDiagnostics();
      throw new Error(
        `${this.name} published [${actual.join(', ')}] but was asked for ` +
          `[${wanted.join(', ')}]. Capture environment: ${JSON.stringify(diagnostics)}`
      );
    }
  }

  async isWaitingInLobby(): Promise<boolean> {
    return this.page
      .getByText('Waiting for the host to let you in.')
      .isVisible()
      .catch(() => false);
  }

  /** Reload and rejoin, the way a wedged participant recovers today. */
  async reload(): Promise<void> {
    this.publications.clear();
    await this.page.reload();
    await this.enterFromPreJoin();
    await this.waitUntilConnected();
    await this.publishJoinMedia();
  }

  async startScreenShare(withAudio = true): Promise<void> {
    const published = await publishSyntheticScreen(this.page, withAudio);
    this.publications.set('screen_share', published.screenSid);
    if (published.audioSid) this.publications.set('screen_share_audio', published.audioSid);
    await this.waitUntilPublishing();
  }

  async stopScreenShare(): Promise<void> {
    const sids = ['screen_share', 'screen_share_audio']
      .map((source) => this.publications.get(source))
      .filter((sid): sid is string => sid !== undefined);
    await unpublishSynthetic(this.page, sids);
    this.publications.delete('screen_share');
    this.publications.delete('screen_share_audio');
  }

  /**
   * Mutes at the publication, which is what the SFU and every receiver see.
   * The ControlBar buttons drive setMicrophoneEnabled, which owns its own
   * capture track and cannot mute a synthetic publication.
   */
  async setMuted(source: 'camera' | 'microphone', muted: boolean): Promise<void> {
    const sid = this.publications.get(source);
    if (!sid) throw new Error(`${this.name} has no ${source} publication to mute.`);
    await setPublicationMuted(this.page, sid, muted);
  }

  async probe(): Promise<MediaProbeSnapshot> {
    return probe(this.page);
  }

  async ledger(): Promise<LedgerEntry[]> {
    return this.bridge(() => (window as unknown as MediaTestBridge).__klisiMediaTest.ledger());
  }

  async clearLedger(): Promise<void> {
    await this.bridge(() => (window as unknown as MediaTestBridge).__klisiMediaTest.clearLedger());
  }

  async uncaughtErrors(): Promise<string[]> {
    return this.bridge(() =>
      (window as unknown as MediaTestBridge).__klisiMediaTest.uncaughtErrors()
    );
  }

  /**
   * Listener exceptions klisi caught. A guard that swallowed instead of
   * recording would leave both this and uncaughtErrors() empty, which is the
   * same bug one step further from view — so the fault scenarios assert it.
   */
  async handlerFaults(): Promise<HandlerFault[]> {
    return this.bridge(() =>
      (window as unknown as MediaTestBridge).__klisiMediaTest.handlerFaults()
    );
  }

  /** Reconcile counters: total ticks, and the subset that republished. */
  async projection(): Promise<{ ticks: number; revisions: number }> {
    return (await this.probe()).projection;
  }

  /** Arms a one-shot throw inside klisi's ParticipantConnected handler. */
  async failNextParticipantEntered(): Promise<void> {
    await this.bridge(() =>
      (window as unknown as MediaTestBridge).__klisiMediaTest.failNextParticipantEntered()
    );
  }

  /** Arms a one-shot throw inside the projection klisi runs from handlers. */
  async failNextReconcile(): Promise<void> {
    await this.bridge(() =>
      (window as unknown as MediaTestBridge).__klisiMediaTest.failNextReconcile()
    );
  }

  /**
   * Silences every RoomEvent listener klisi registered, leaving the SFU
   * connection and LiveKit's own state untouched. Models the general case
   * behind every media incident here: an event that never arrives.
   */
  async deafen(): Promise<void> {
    await this.bridge(() => (window as unknown as MediaTestBridge).__klisiMediaTest.deafen());
  }

  /** Drives LiveKit's own fault simulations (signal-reconnect, server-leave…). */
  async simulate(scenario: string): Promise<void> {
    await this.page.evaluate(
      (name) =>
        (
          window as unknown as {
            __klisiRoom: { simulateScenario(scenario: string): Promise<void> };
          }
        ).__klisiRoom.simulateScenario(name),
      scenario
    );
  }

  async close(): Promise<void> {
    await this.context.close();
  }
}

export interface MeetingOptions {
  name?: string;
  lobby?: boolean;
}

export class Meeting {
  readonly actors: MeetingActor[] = [];

  private constructor(
    readonly browser: Browser,
    readonly slug: string,
    readonly roomName: string
  ) {}

  static async open(browser: Browser, options: MeetingOptions = {}): Promise<Meeting> {
    const roomName = options.name ?? `media gate ${Date.now().toString(36)}`;
    const state = await sessionFor(browser, OWNER);
    const context = await browser.newContext({ storageState: state });
    try {
      const created = await context.request.post('/api/rooms', {
        headers: { ...csrf, 'Content-Type': 'application/json' },
        data: { name: roomName }
      });
      if (!created.ok()) {
        throw new Error(`Could not create room: ${created.status()} ${await created.text()}`);
      }
      const room = (await created.json()) as { slug: string; lobby_enabled: boolean };
      const wantLobby = options.lobby ?? false;
      if (room.lobby_enabled !== wantLobby) {
        const patched = await context.request.patch(`/api/rooms/${room.slug}`, {
          headers: { ...csrf, 'Content-Type': 'application/json' },
          data: { lobby_enabled: wantLobby }
        });
        if (!patched.ok()) throw new Error(`Could not set lobby_enabled: ${patched.status()}`);
      }
      return new Meeting(browser, room.slug, roomName);
    } finally {
      await context.close();
    }
  }

  // No capture permissions are granted: actors publish synthetic tracks and
  // never call getUserMedia. WebKit also rejects the "camera" permission name
  // outright, so asking for it would fail the whole project.
  private async contextFor(role: ActorRole): Promise<BrowserContext> {
    if (role === 'guest') return this.browser.newContext();
    const user: TestUser = role === 'owner' ? OWNER : MEMBER;
    const storageState = await sessionFor(this.browser, user);
    return this.browser.newContext({ storageState });
  }

  /** Creates an actor, connects it, and publishes its media. */
  async join(options: JoinOptions): Promise<MeetingActor> {
    const actor = await this.arrive(options);
    await actor.waitUntilConnected();
    await actor.publishJoinMedia();
    return actor;
  }

  /** Creates an actor and submits PreJoin without waiting for the SFU. */
  async arrive(options: JoinOptions): Promise<MeetingActor> {
    const role = options.as ?? 'guest';
    const context = await this.contextFor(role);
    const page = await context.newPage();
    const actor = new MeetingActor(
      this,
      context,
      page,
      options.name,
      role,
      options.camera ?? true,
      options.microphone ?? true
    );
    this.actors.push(actor);
    await actor.enterFromPreJoin();
    return actor;
  }

  /** Runs an authenticated request as the room owner. */
  private async asOwner<T>(run: (request: BrowserContext['request']) => Promise<T>): Promise<T> {
    const storageState = await sessionFor(this.browser, OWNER);
    const context = await this.browser.newContext({ storageState });
    try {
      return await run(context.request);
    } finally {
      await context.close();
    }
  }

  async startRecording(video = false): Promise<void> {
    await this.asOwner(async (request) => {
      const response = await request.post(`/api/rooms/${this.slug}/recording/start`, {
        headers: { ...csrf, 'Content-Type': 'application/json' },
        data: { video }
      });
      if (!response.ok()) {
        throw new Error(`Could not start recording: ${response.status()} ${await response.text()}`);
      }
    });
  }

  async stopRecording(): Promise<void> {
    await this.asOwner(async (request) => {
      const response = await request.post(`/api/rooms/${this.slug}/recording/stop`, {
        headers: csrf
      });
      if (!response.ok()) {
        throw new Error(`Could not stop recording: ${response.status()} ${await response.text()}`);
      }
    });
  }

  async recordings(): Promise<{ id: string; status: string; audio_only: boolean }[]> {
    return this.asOwner(async (request) => {
      const response = await request.get(`/api/rooms/${this.slug}/recordings`);
      if (!response.ok()) throw new Error(`Could not list recordings: ${response.status()}`);
      return (await response.json()) as { id: string; status: string; audio_only: boolean }[];
    });
  }

  async remove(actor: MeetingActor): Promise<void> {
    const index = this.actors.indexOf(actor);
    if (index >= 0) this.actors.splice(index, 1);
    await actor.close();
  }

  peersOf(actor: MeetingActor): MeetingActor[] {
    return this.actors.filter((candidate) => candidate !== actor && candidate.identity !== '');
  }

  async close(): Promise<void> {
    await Promise.all(this.actors.map((actor) => actor.close().catch(() => undefined)));
    this.actors.length = 0;
  }
}
