import { chromium, test, type Browser, type BrowserContext, type Page } from '@playwright/test';
import fc, { type AsyncCommand } from 'fast-check';
import {
  attachDiagnostics,
  captureBrowserDiagnostics,
  expectMediaInvariant,
  joinMediaTestRoom,
  probe,
  publishReplacementCamera,
  publishReplacementMicrophone,
  publishSyntheticScreen,
  resetSyntheticMedia,
  resubscribeSettleTimeout,
  setPublicationMuted,
  unpublishSynthetic
} from './media-helpers';
import { mediaRegressionSeeds } from './media-regression-seeds';

interface MediaModel {
  cameraPublished: boolean;
  cameraMuted: boolean;
  microphonePublished: boolean;
  microphoneMuted: boolean;
  screenPublished: boolean;
  view: 'grid' | 'speaker';
}

interface MediaReal {
  publisher: Page;
  receiver: Page;
  roomName: string;
  cameraSid: string;
  microphoneSid: string;
  screenSids: string[];
  trace: string[];
}

abstract class MediaCommand implements AsyncCommand<MediaModel, MediaReal> {
  abstract check(model: Readonly<MediaModel>): boolean;
  abstract run(model: MediaModel, real: MediaReal): Promise<void>;
  abstract toString(): string;

  /**
   * The invariant against a room that is already settled: this command changed
   * nothing about what is published, so media is flowing before it runs and the
   * 3 s default is generous.
   */
  protected async converged(real: MediaReal, timeout?: number): Promise<void> {
    real.trace.push(this.toString());
    await expectMediaInvariant(real.receiver, timeout);
  }

  /**
   * The invariant after a change to what is published. Publishing,
   * unpublishing and reconnecting all renegotiate, and RTP resumes some way
   * after the SDK reports `subscribed`, so these get the same window a
   * re-subscribe gets. It still fails if media never returns.
   *
   * Every such command must use this, because the invariant passes vacuously
   * while a publication is still settling: it only checks publications the
   * receiver has already subscribed to, so a publish that has not been
   * subscribed yet is not checked at all. A command that returns early on that
   * vacuous pass hands an unsettled room to the next one, which then fails on
   * the tight default for a renegotiation it did not cause. That is exactly how
   * run 328 failed: `toggle-screen-and-screen-audio` passed vacuously, and
   * `toggle-grid-speaker` — a pure UI switch — inherited the un-flowing screen
   * share and was blamed for it.
   */
  protected async renegotiated(real: MediaReal): Promise<void> {
    await this.converged(real, resubscribeSettleTimeout);
  }
}

class ToggleCamera extends MediaCommand {
  check(): boolean {
    return true;
  }
  async run(model: MediaModel, real: MediaReal): Promise<void> {
    if (!model.cameraPublished) {
      real.cameraSid = await publishReplacementCamera(real.publisher);
      model.cameraPublished = true;
      model.cameraMuted = false;
      await this.renegotiated(real);
      return;
    }
    model.cameraMuted = !model.cameraMuted;
    await setPublicationMuted(real.publisher, real.cameraSid, model.cameraMuted);
    await this.renegotiated(real);
  }
  toString(): string {
    return 'toggle-camera-mute';
  }
}

class ToggleMicrophone extends MediaCommand {
  check(): boolean {
    return true;
  }
  async run(model: MediaModel, real: MediaReal): Promise<void> {
    if (!model.microphonePublished) {
      real.microphoneSid = await publishReplacementMicrophone(real.publisher);
      model.microphonePublished = true;
      model.microphoneMuted = false;
      await this.renegotiated(real);
      return;
    }
    model.microphoneMuted = !model.microphoneMuted;
    await setPublicationMuted(real.publisher, real.microphoneSid, model.microphoneMuted);
    await this.renegotiated(real);
  }
  toString(): string {
    return 'toggle-microphone-mute';
  }
}

class ToggleCameraPublication extends MediaCommand {
  check(): boolean {
    return true;
  }
  async run(model: MediaModel, real: MediaReal): Promise<void> {
    if (model.cameraPublished) {
      await unpublishSynthetic(real.publisher, [real.cameraSid]);
      model.cameraPublished = false;
      model.cameraMuted = false;
    } else {
      real.cameraSid = await publishReplacementCamera(real.publisher);
      model.cameraPublished = true;
    }
    await this.renegotiated(real);
  }
  toString(): string {
    return 'toggle-camera-publication';
  }
}

class ToggleMicrophonePublication extends MediaCommand {
  check(): boolean {
    return true;
  }
  async run(model: MediaModel, real: MediaReal): Promise<void> {
    if (model.microphonePublished) {
      await unpublishSynthetic(real.publisher, [real.microphoneSid]);
      model.microphonePublished = false;
      model.microphoneMuted = false;
    } else {
      real.microphoneSid = await publishReplacementMicrophone(real.publisher);
      model.microphonePublished = true;
    }
    await this.renegotiated(real);
  }
  toString(): string {
    return 'toggle-microphone-publication';
  }
}

class ToggleScreen extends MediaCommand {
  check(): boolean {
    return true;
  }
  async run(model: MediaModel, real: MediaReal): Promise<void> {
    if (model.screenPublished) {
      await unpublishSynthetic(real.publisher, real.screenSids);
      real.screenSids = [];
    } else {
      const screen = await publishSyntheticScreen(real.publisher);
      real.screenSids = [screen.screenSid, screen.audioSid].filter((sid): sid is string =>
        Boolean(sid)
      );
    }
    model.screenPublished = !model.screenPublished;
    await this.renegotiated(real);
  }
  toString(): string {
    return 'toggle-screen-and-screen-audio';
  }
}

class ToggleLayout extends MediaCommand {
  check(): boolean {
    return true;
  }
  async run(model: MediaModel, real: MediaReal): Promise<void> {
    await real.receiver
      .getByRole('button', { name: model.view === 'grid' ? 'Speaker view' : 'Grid view' })
      .click();
    model.view = model.view === 'grid' ? 'speaker' : 'grid';
    await this.converged(real);
  }
  toString(): string {
    return 'toggle-grid-speaker';
  }
}

class SpeakerStorm extends MediaCommand {
  check(): boolean {
    return true;
  }
  async run(_model: MediaModel, real: MediaReal): Promise<void> {
    await real.receiver.evaluate(async () => {
      const room = (
        window as Window &
          typeof globalThis & {
            __klisiRoom: {
              remoteParticipants: Map<string, unknown>;
              emit(event: string, participants: unknown[]): void;
            };
          }
      ).__klisiRoom;
      const remote = [...room.remoteParticipants.values()][0];
      for (let index = 0; index < 12; index += 1) {
        room.emit('activeSpeakersChanged', index % 2 === 0 ? [remote] : []);
        await new Promise((resolve) => setTimeout(resolve, 20));
      }
    });
    await this.converged(real);
  }
  toString(): string {
    return 'active-speaker-storm';
  }
}

class RepairAttachment extends MediaCommand {
  check(): boolean {
    return true;
  }
  async run(model: MediaModel, real: MediaReal): Promise<void> {
    // Publishing to satisfy the precondition renegotiates; re-attaching a
    // track that is already flowing does not. Asserting tightly on the repair
    // is the point of this command, so only the publish widens the window.
    let published = false;
    if (!model.microphonePublished) {
      real.microphoneSid = await publishReplacementMicrophone(real.publisher);
      model.microphonePublished = true;
      model.microphoneMuted = false;
      published = true;
    }
    await real.receiver.evaluate((sid) => {
      const hook = (
        window as Window &
          typeof globalThis & {
            __klisiMediaTest: {
              clearSrcObject(publicationSid: string): boolean;
              reconcile(): void;
            };
          }
      ).__klisiMediaTest;
      hook.clearSrcObject(sid);
      hook.reconcile();
    }, real.microphoneSid);
    if (published) await this.renegotiated(real);
    else await this.converged(real);
  }
  toString(): string {
    return 'clear-srcObject-and-reconcile';
  }
}

class DelayedAttachment extends MediaCommand {
  check(): boolean {
    return true;
  }
  async run(model: MediaModel, real: MediaReal): Promise<void> {
    // As in clear-srcObject-and-reconcile: only the precondition publish
    // renegotiates, and the delayed re-attachment is what this asserts tightly.
    let published = false;
    if (!model.microphonePublished) {
      real.microphoneSid = await publishReplacementMicrophone(real.publisher);
      model.microphonePublished = true;
      model.microphoneMuted = false;
      published = true;
    }
    await real.receiver.evaluate(async (sid) => {
      const hook = (
        window as Window &
          typeof globalThis & {
            __klisiMediaTest: {
              attachmentDelayMs: number;
              clearSrcObject(publicationSid: string): boolean;
              reconcile(): void;
            };
          }
      ).__klisiMediaTest;
      hook.attachmentDelayMs = 80;
      hook.clearSrcObject(sid);
      hook.reconcile();
      await new Promise((resolve) => setTimeout(resolve, 160));
      hook.attachmentDelayMs = 0;
      hook.reconcile();
    }, real.microphoneSid);
    if (published) await this.renegotiated(real);
    else await this.converged(real);
  }
  toString(): string {
    return 'delay-attachment';
  }
}

class SubscriptionFailure extends MediaCommand {
  check(): boolean {
    return true;
  }
  async run(model: MediaModel, real: MediaReal): Promise<void> {
    if (!model.cameraPublished) {
      real.cameraSid = await publishReplacementCamera(real.publisher);
      model.cameraPublished = true;
      model.cameraMuted = false;
    }
    // Two independent insults in one command: a failure record that describes
    // nothing (the publication is subscribed), and a subscription dropped with
    // no event to announce it. Both must be re-derived away by the tick.
    await real.receiver.evaluate((sid) => {
      const hook = (
        window as Window &
          typeof globalThis & {
            __klisiMediaTest: {
              injectSubscriptionFailure(publicationSid: string): void;
              unsubscribeBehindBack(publicationSid: string): boolean;
            };
          }
      ).__klisiMediaTest;
      hook.injectSubscriptionFailure(sid);
      hook.unsubscribeBehindBack(sid);
    }, real.cameraSid);
    await real.receiver.waitForFunction(
      (sid) =>
        (
          window as Window &
            typeof globalThis & {
              __klisiMediaTest: {
                snapshot(): Promise<{
                  participants: {
                    publications: Record<
                      string,
                      { publicationSid: string; subscribed: boolean; desired: boolean }
                    >;
                  }[];
                  subscriptionFailures: Record<string, unknown>;
                }>;
              };
            }
        ).__klisiMediaTest
          .snapshot()
          .then((snapshot) => {
            const camera = snapshot.participants
              .flatMap((participant) => Object.values(participant.publications))
              .find((publication) => publication.publicationSid === sid);
            return (
              snapshot.subscriptionFailures[sid] === undefined &&
              camera?.subscribed === true &&
              camera.desired
            );
          }),
      real.cameraSid,
      { timeout: 20_000 }
    );
    await this.renegotiated(real);
  }
  toString(): string {
    return 'subscription-failure-converges';
  }
}

class SignalReconnect extends MediaCommand {
  check(): boolean {
    return true;
  }
  async run(_model: MediaModel, real: MediaReal): Promise<void> {
    await real.receiver.evaluate(() =>
      (
        window as Window &
          typeof globalThis & {
            __klisiRoom: { simulateScenario(scenario: 'signal-reconnect'): Promise<void> };
          }
      ).__klisiRoom.simulateScenario('signal-reconnect')
    );
    await real.receiver.waitForFunction(
      () =>
        (window as Window & typeof globalThis & { __klisiRoom: { state: string } }).__klisiRoom
          .state === 'connected',
      undefined,
      { timeout: 30_000 }
    );
    await this.renegotiated(real);
  }
  toString(): string {
    return 'signal-reconnect';
  }
}

class FullReconnect extends MediaCommand {
  check(): boolean {
    return true;
  }
  async run(_model: MediaModel, real: MediaReal): Promise<void> {
    await real.receiver.evaluate(() =>
      (
        window as Window &
          typeof globalThis & {
            __klisiRoom: { simulateScenario(scenario: 'full-reconnect'): Promise<void> };
          }
      ).__klisiRoom.simulateScenario('full-reconnect')
    );
    await real.receiver.waitForFunction(
      () =>
        (window as Window & typeof globalThis & { __klisiRoom: { state: string } }).__klisiRoom
          .state === 'connected',
      undefined,
      { timeout: 30_000 }
    );
    await this.renegotiated(real);
  }
  toString(): string {
    return 'full-reconnect';
  }
}

class ReplaceCamera extends MediaCommand {
  check(): boolean {
    return true;
  }
  async run(model: MediaModel, real: MediaReal): Promise<void> {
    const previous = model.cameraPublished ? real.cameraSid : undefined;
    real.cameraSid = await publishReplacementCamera(real.publisher);
    if (previous) await unpublishSynthetic(real.publisher, [previous]);
    model.cameraPublished = true;
    model.cameraMuted = false;
    await this.renegotiated(real);
  }
  toString(): string {
    return 'replace-camera-track';
  }
}

class ConcurrentJoinMediaChange extends MediaCommand {
  check(): boolean {
    return true;
  }
  async run(model: MediaModel, real: MediaReal): Promise<void> {
    const browser = real.receiver.context().browser();
    if (!browser) throw new Error('Receiver browser is unavailable.');
    const context = await browser.newContext({
      baseURL: process.env.KLISI_MEDIA_BASE_URL ?? 'http://klisi:8080'
    });
    try {
      const page = await context.newPage();
      const join = joinMediaTestRoom(page, real.roomName, `concurrent-${real.trace.length}`);
      const mediaChange = model.screenPublished
        ? unpublishSynthetic(real.publisher, real.screenSids).then(() => {
            real.screenSids = [];
          })
        : publishSyntheticScreen(real.publisher).then((screen) => {
            real.screenSids = [screen.screenSid, screen.audioSid].filter((sid): sid is string =>
              Boolean(sid)
            );
          });
      await Promise.all([join, mediaChange]);
      model.screenPublished = !model.screenPublished;
      await expectMediaInvariant(page);
    } finally {
      await context.close();
    }
    await this.renegotiated(real);
  }
  toString(): string {
    return 'concurrent-join-and-screen-change';
  }
}

class SequentialJoinLeave extends MediaCommand {
  check(): boolean {
    return true;
  }
  async run(_model: MediaModel, real: MediaReal): Promise<void> {
    const browser = real.receiver.context().browser();
    if (!browser) throw new Error('Receiver browser is unavailable.');
    const context = await browser.newContext({
      baseURL: process.env.KLISI_MEDIA_BASE_URL ?? 'http://klisi:8080'
    });
    const page = await context.newPage();
    await joinMediaTestRoom(page, real.roomName, `transient-${real.trace.length}`);
    await expectMediaInvariant(page);
    await context.close();
    await this.renegotiated(real);
  }
  toString(): string {
    return 'sequential-join-leave';
  }
}

const commandArbitraries = [
  fc.constant(new ToggleCamera()),
  fc.constant(new ToggleMicrophone()),
  fc.constant(new ToggleCameraPublication()),
  fc.constant(new ToggleMicrophonePublication()),
  fc.constant(new ToggleScreen()),
  fc.constant(new ToggleLayout()),
  fc.constant(new SpeakerStorm()),
  fc.constant(new RepairAttachment()),
  fc.constant(new DelayedAttachment()),
  fc.constant(new SubscriptionFailure()),
  fc.constant(new SignalReconnect()),
  fc.constant(new FullReconnect()),
  fc.constant(new ReplaceCamera()),
  fc.constant(new SequentialJoinLeave()),
  fc.constant(new ConcurrentJoinMediaChange())
];
const commandArbitrary = fc.oneof(...commandArbitraries);

// A lifecycle only gets interesting after enough operations have piled state
// on each other, so every run executes at least this many. It is the
// arbitrary's own minimum rather than a deterministic tail appended after
// generation: padding is invisible to fast-check, so it cannot shrink it and
// does not report it. Run 328 shrank to the one-command counterexample
// `[subscription-failure-converges]` while the assertion that actually failed
// was `toggle-grid-speaker`, five appended commands later — the counterexample
// named a command that had passed. Shrinking now reduces which commands run,
// never how many, and what it reports is what ran.
const minimumOperations = 50;

test('seeded model-based media lifecycle fuzzing', async ({ page }, testInfo) => {
  test.setTimeout(900_000);
  const seedInput = process.env.FC_SEED?.trim();
  const configuredSeed = seedInput ? Number(seedInput) : Number.NaN;
  const seeds = Number.isSafeInteger(configuredSeed) ? [configuredSeed] : mediaRegressionSeeds;
  const maxCommands = Math.min(
    200,
    Math.max(minimumOperations, Number(process.env.FC_COMMANDS) || 75)
  );
  const numRuns = Math.max(1, Number(process.env.FC_RUNS) || 1);
  const roomName = `media-fuzz-${testInfo.project.name}-${Date.now()}`;
  const receiverConsoles: string[][] = [captureBrowserDiagnostics(page)];
  let publisherBrowser: Browser | undefined;
  let publisherConsole: string[] = [];
  let receiver = page;
  let replacementReceiverContext: BrowserContext | undefined;
  let receiverAttempts = 0;
  let finalReceiverProbe: unknown = { state: 'not-started' };
  const traces: Record<number, string[]> = {};

  try {
    publisherBrowser = await chromium.launch({
      args: ['--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream']
    });
    const publisherContext = await publisherBrowser.newContext({
      baseURL: process.env.KLISI_MEDIA_BASE_URL ?? 'http://klisi:8080',
      permissions: ['camera', 'microphone']
    });
    const publisher = await publisherContext.newPage();
    publisherConsole = captureBrowserDiagnostics(publisher);
    await joinMediaTestRoom(publisher, roomName, 'fuzz-publisher');

    for (const seed of seeds) {
      console.log(`KLISI_MEDIA_FUZZ seed=${seed} maxCommands=${maxCommands} runs=${numRuns}`);
      await fc.assert(
        fc.asyncProperty(
          fc.array(commandArbitrary, { minLength: minimumOperations, maxLength: maxCommands }),
          async (commands) => {
            const trace: string[] = [];
            traces[seed] = trace;
            const synthetic = await resetSyntheticMedia(publisher);
            receiverAttempts += 1;
            if (receiverAttempts > 1) {
              try {
                await receiver.goto('about:blank', {
                  waitUntil: 'commit',
                  timeout: 5_000
                });
              } catch {
                await replacementReceiverContext?.close().catch(() => undefined);
                const receiverBrowser = page.context().browser();
                if (!receiverBrowser?.isConnected()) {
                  throw new Error(
                    'Receiver browser crashed and could not be restarted for shrink.'
                  );
                }
                replacementReceiverContext = await receiverBrowser.newContext({
                  baseURL: process.env.KLISI_MEDIA_BASE_URL ?? 'http://klisi:8080'
                });
                receiver = await replacementReceiverContext.newPage();
                receiverConsoles.push(captureBrowserDiagnostics(receiver));
              }
            }
            await joinMediaTestRoom(
              receiver,
              roomName,
              `${testInfo.project.name}-fuzz-receiver-${receiverAttempts}`
            );
            await expectMediaInvariant(receiver);
            await fc.asyncModelRun(
              () => ({
                model: {
                  cameraPublished: true,
                  cameraMuted: false,
                  microphonePublished: true,
                  microphoneMuted: false,
                  screenPublished: false,
                  view: 'grid' as const
                },
                real: {
                  publisher,
                  receiver,
                  roomName,
                  cameraSid: synthetic.cameraSid,
                  microphoneSid: synthetic.microphoneSid,
                  screenSids: [],
                  trace
                }
              }),
              commands
            );
            finalReceiverProbe = await probe(receiver);
          }
        ),
        { seed, numRuns, verbose: 2 }
      );
      console.log(
        `KLISI_MEDIA_FUZZ_RESULT seed=${seed} operations=${traces[seed]?.length ?? 0} trace=${JSON.stringify(traces[seed] ?? [])}`
      );
    }
  } finally {
    await attachDiagnostics(testInfo, 'fuzz-seeds-and-traces.json', {
      configuredSeed: Number.isSafeInteger(configuredSeed) ? configuredSeed : undefined,
      maxCommands,
      numRuns,
      traces
    });
    await attachDiagnostics(
      testInfo,
      'receiver-console.json',
      receiverConsoles.flatMap((messages, index) =>
        messages.map((message) => `[receiver-${index + 1}] ${message}`)
      )
    );
    await attachDiagnostics(testInfo, 'publisher-console.json', publisherConsole);
    finalReceiverProbe = await probe(receiver).catch((error) => ({ error: String(error) }));
    await attachDiagnostics(testInfo, 'rtc-final.json', finalReceiverProbe);
    await replacementReceiverContext?.close().catch(() => undefined);
    await publisherBrowser?.close();
  }
});
