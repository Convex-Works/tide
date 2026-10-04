import { gunzipSync } from 'node:zlib';
import { expect, type Page, type WebSocketRoute } from '@playwright/test';
import {
  JoinRequest,
  SignalRequest,
  SignalResponse,
  WrappedJoinRequest,
  WrappedJoinRequest_Compression
} from '@livekit/protocol';
import { fakeSfuURL } from './mock-api';

// Just enough of a LiveKit server for the meeting page to reach its stage in
// the mock-API suite, without Docker. The signal socket is answered here, in
// Node, through Playwright's WebSocket routing; the client's peer connection
// is answered by a second RTCPeerConnection inside the same page, so ICE
// completes locally and livekit-client sees a really connected transport.
// Nobody else is in the room and nothing is forwarded: media behaviour
// belongs to the real-SFU suite (`make media`), not here.

/**
 * Chromium hides host candidates behind mDNS names that two peers in one page
 * can't resolve for each other; a spec using the fake SFU launches with this.
 */
export const fakeSfuLaunchArgs = ['--disable-features=WebRtcHideLocalIpsWithMdns'];

export interface FakeSfu {
  /** Pushes room metadata the way the server does, e.g. `{"recording":true}`. */
  setRoomMetadata(metadata: string): void;
}

/** A response as its constructor takes it: nested messages may be partial. */
type PartialSignalResponse = NonNullable<ConstructorParameters<typeof SignalResponse>[0]>;

type PeerWindow = Window & { __klisiFakeSfuPeer?: RTCPeerConnection };

const sfuHost = new URL(fakeSfuURL).host;

const unsupportedPath =
  'The fake SFU only speaks the v1 signal path (a join_request in the URL); ' +
  'livekit-client connected some other way.';

/**
 * The v1 signal path carries the join request, gzipped, in the URL. Undefined
 * when it doesn't.
 */
function joinRequestFrom(url: URL): JoinRequest | undefined {
  const encoded = url.searchParams.get('join_request');
  if (!encoded) return undefined;
  const wrapped = WrappedJoinRequest.fromBinary(Buffer.from(encoded, 'base64url'));
  const bytes =
    wrapped.compression === WrappedJoinRequest_Compression.GZIP
      ? gunzipSync(wrapped.joinRequest)
      : wrapped.joinRequest;
  return JoinRequest.fromBinary(new Uint8Array(bytes));
}

/** The mock API's tokens are unsigned; their payload says who joined. */
function participantFrom(url: URL): { identity: string; name: string } {
  const token = url.searchParams.get('access_token') ?? '';
  const payload = JSON.parse(
    Buffer.from(token.split('.')[1] ?? '', 'base64url').toString() || '{}'
  ) as { sub?: string; name?: string };
  return { identity: payload.sub ?? 'local', name: payload.name ?? 'local' };
}

/** Answers an offer from the page's own peer, with all its candidates in the SDP. */
function answerInPage(page: Page, offer: string): Promise<string> {
  return page.evaluate(async (sdp) => {
    const peerWindow = window as PeerWindow;
    // Renegotiation offers land on the same connection.
    const peer = (peerWindow.__klisiFakeSfuPeer ??= new RTCPeerConnection());
    await peer.setRemoteDescription({ type: 'offer', sdp });
    await peer.setLocalDescription(await peer.createAnswer());
    await new Promise<void>((resolve) => {
      if (peer.iceGatheringState === 'complete') return resolve();
      peer.addEventListener('icegatheringstatechange', () => {
        if (peer.iceGatheringState === 'complete') resolve();
      });
      // Host candidates come at once; don't wait on interfaces that never finish.
      setTimeout(resolve, 1_000);
    });
    return peer.localDescription?.sdp ?? '';
  }, offer);
}

function addCandidateInPage(page: Page, candidateInit: string): Promise<void> {
  return page.evaluate(async (init) => {
    const peer = (window as PeerWindow).__klisiFakeSfuPeer;
    const candidate = JSON.parse(init) as RTCIceCandidateInit;
    if (peer && candidate.candidate) await peer.addIceCandidate(candidate).catch(() => undefined);
  }, candidateInit);
}

export async function fakeSfu(page: Page): Promise<FakeSfu> {
  const sockets = new Set<WebSocketRoute>();
  let roomMetadata = '';
  // livekit-client parses text frames as protobuf JSON; binary frames sent
  // before it sets binaryType would arrive as Blobs it can't read.
  const send = (ws: WebSocketRoute, message: PartialSignalResponse['message']) =>
    ws.send(new SignalResponse({ message }).toJsonString());

  function serve(ws: WebSocketRoute): void {
    const url = new URL(ws.url());
    const request = joinRequestFrom(url);
    if (!request) {
      // A throw here would vanish inside Playwright's routing and leave the
      // spec waiting for a stage that never comes. Record the failure on the
      // running test instead and close the page so it stops at once.
      expect.soft(request, unsupportedPath).toBeDefined();
      void ws.close({ code: 1011, reason: 'unsupported signal path' });
      void page.close();
      return;
    }
    const { identity, name } = participantFrom(url);
    sockets.add(ws);
    ws.onClose(() => sockets.delete(ws));

    // Offers and candidates are handled in arrival order.
    let queue = Promise.resolve();
    const later = (work: () => Promise<void>) => {
      queue = queue.then(work).catch((cause: unknown) => {
        // A page that navigated away mid-answer is not a failure of the spec.
        if (!page.isClosed()) console.error('fake SFU:', cause);
      });
    };
    const answer = (offer: { sdp: string; id: number }) =>
      later(async () => {
        const sdp = await answerInPage(page, offer.sdp);
        send(ws, { case: 'answer', value: { type: 'answer', sdp, id: offer.id } });
      });

    ws.onMessage((message) => {
      if (typeof message === 'string') return;
      const signal = SignalRequest.fromBinary(new Uint8Array(message)).message;
      if (signal.case === 'offer') answer(signal.value);
      else if (signal.case === 'trickle')
        later(() => addCandidateInPage(page, signal.value.candidateInit));
      else if (signal.case === 'leave') void ws.close();
    });

    send(ws, {
      case: 'join',
      value: {
        room: { sid: 'RM_fake', name: 'fake', metadata: roomMetadata },
        participant: {
          sid: 'PA_local',
          identity,
          name,
          state: 1, // JOINED
          permission: { canSubscribe: true, canPublish: true, canPublishData: true }
        },
        serverVersion: '1.9.0',
        serverInfo: { version: '1.9.0', protocol: 16 },
        // No pings: nothing here times out.
        pingInterval: 0,
        pingTimeout: 0
      }
    });
    if (request.publisherOffer) answer(request.publisherOffer);
  }

  await page.routeWebSocket(
    (url) => url.host === sfuHost,
    (ws) => serve(ws)
  );
  // livekit-client asks /rtc/v1/validate why a socket failed; it says nothing failed.
  await page.route(
    (url) => url.host === sfuHost,
    (route) => route.fulfill({ status: 200, headers: { 'access-control-allow-origin': '*' } })
  );

  return {
    setRoomMetadata(metadata) {
      roomMetadata = metadata;
      for (const ws of sockets) {
        send(ws, {
          case: 'roomUpdate',
          value: { room: { sid: 'RM_fake', name: 'fake', metadata } }
        });
      }
    }
  };
}
