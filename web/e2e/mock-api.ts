import type { Page, Route } from '@playwright/test';
import { slugError, suggestSlug } from '../src/lib/slug';
import {
  MachineIdle,
  MachineOffline,
  MachinePaused,
  MachinePath,
  MachinesPath,
  MePath,
  PairingConfirmPath,
  PairingDenyPath,
  PairingPath,
  RecordingTranscriptDownloadPath,
  RecordingTranscriptPath,
  RoomJoinPath,
  RoomLobbyPath,
  RoomPath,
  RoomRecordingsPath,
  RoomsPath,
  TranscriptCompleted,
  TranscriptFailed,
  TranscriptWaiting,
  type CreateRoomRequest,
  type MachineInfo,
  type MachinesResponse,
  type Me,
  type JoinResponse,
  type PairingInfo,
  type PublicRoomInfo,
  type RecordingInfo,
  type RoomInfo,
  type TranscriptInfo
} from '../src/lib/api/types.gen';

// An in-memory klisi API for specs that run against the Vite dev server
// alone: every /api request is answered here, from `state`, and recorded in
// `calls`. Nothing reaches the proxy, so a request the mock doesn't know is
// a 501 the spec can see. It answers the way the server does
// (server/internal/machines, server/internal/transcripts), with its
// messages; a spec that needs a failure routes over it.

export interface ApiCall {
  method: string;
  path: string;
  search: string;
  csrf: string | undefined;
}

export interface ApiState {
  signedIn: boolean;
  me: Me;
  rooms: RoomInfo[];
  recordings: RecordingInfo[];
  machines: MachinesResponse;
  /** Machines waiting to pair, by their code as the moil app shows it (XXXX-XXXX). */
  pairings: Record<string, PairingInfo>;
}

export interface MockApi {
  state: ApiState;
  calls: ApiCall[];
  count(method: string, path: string): number;
}

// tygo writes a Go pointer without omitempty as an optional field, but the
// server sends null for it; this is that null, typed to fit.
export const wireNull = null as unknown as undefined;

/**
 * Where the mock's join answers send livekit-client. Nothing listens there:
 * a spec that goes past pre-join answers it with `fakeSfu` (fake-sfu.ts).
 */
export const fakeSfuURL = 'ws://sfu.klisi.test';

/** A join token for the fake SFU: unsigned, it only carries who joined. */
function fakeToken(identity: string, name: string): string {
  const part = (value: object) => Buffer.from(JSON.stringify(value)).toString('base64url');
  return `${part({ alg: 'none' })}.${part({ sub: identity, name })}.`;
}

export const now = Math.floor(Date.parse('2026-09-27T12:00:00Z') / 1000);

export const bundle = {
  name: 'transcribe',
  version: '0.1.0',
  hash: 'a00a65c166291e905da7546a67518a81cc27cb498f009c75b824f28a8adfffb2'
};

export function machine(overrides: Partial<MachineInfo> = {}): MachineInfo {
  return {
    id: 'm-studio',
    name: 'Studio Mac mini',
    os: 'macos',
    arch: 'aarch64',
    app_version: '0.4.2',
    paired_at: now - 86_400 * 3,
    last_seen_at: now - 30,
    state: MachineIdle,
    approved: true,
    ...overrides
  };
}

export function recording(
  id: string,
  transcript: TranscriptInfo | undefined,
  overrides: Partial<RecordingInfo> = {}
): RecordingInfo {
  return {
    id,
    room_slug: 'standup',
    egress_id: `EG_${id}`,
    status: 'completed',
    started_by: 'host',
    started_at: now - 86_400,
    audio_only: true,
    ended_at: now - 86_400 + 723,
    duration_s: 723,
    size_bytes: 11_200_000,
    transcript,
    ...overrides
  };
}

export function defaultState(): ApiState {
  return {
    signedIn: true,
    me: { sub: 'host', email: 'host@klisi.dev', name: 'Ada Host', transcripts: true },
    rooms: [
      {
        id: 'r-standup',
        slug: 'standup',
        name: 'Standup',
        lobby_enabled: true,
        created_at: now - 86_400 * 30,
        active: false,
        num_participants: 0,
        recording: false,
        last_active_at: now - 86_400
      }
    ],
    recordings: [],
    machines: {
      machines: [],
      bundle,
      moil_url: 'https://klisi.example.com/moil',
      app_url: 'https://git.convex.works/ConvexWorks/moil/releases/latest'
    },
    pairings: {}
  };
}

/** The server's reason a pending transcript has no machine on it (transcripts/status.go). */
export function waitingMessage(machines: MachineInfo[]): string {
  const approved = machines.filter((item) => item.approved);
  const online = approved.filter((item) => item.state !== MachineOffline);
  if (machines.length === 0) return 'No machine is paired to transcribe it.';
  if (approved.length === 0) {
    return 'No paired machine has approved the transcriber yet. Approve it in the moil app.';
  }
  if (online.length === 0) return 'Waiting for a paired machine to come online.';
  if (online.some((item) => item.state === MachineIdle))
    return 'Waiting for a machine to start it.';
  if (online.every((item) => item.state === MachinePaused)) {
    return 'The paired machines are paused. Resume one in the moil app.';
  }
  return 'Waiting for a paired machine to finish its current job.';
}

/** A pairing code as the server reads it: any case, with or without the hyphen. */
function pairingCode(entered: string): string {
  const plain = entered.toUpperCase().replace(/[^A-Z0-9]/g, '');
  return plain.length === 8 ? `${plain.slice(0, 4)}-${plain.slice(4)}` : plain;
}

const unknownCode =
  'No machine is waiting with this code. Check the code, or start pairing again in the moil app.';

/**
 * POST /api/rooms as the server answers it (ARCHITECTURE.md §5): a blank
 * name becomes the slug, a missing slug is drawn, a given one is validated
 * and a taken one is a 409, never replaced.
 */
function createRoom(
  state: ApiState,
  request: CreateRoomRequest
): { status: number; body: RoomInfo | { error: string } } {
  const name = (request.name ?? '').trim();
  if (name.length > 100) {
    return { status: 400, body: { error: 'Room name must be between 1 and 100 characters.' } };
  }
  const taken = (slug: string) => state.rooms.some((room) => room.slug === slug);
  let slug: string;
  if (request.slug === undefined) {
    do slug = suggestSlug();
    while (taken(slug));
  } else {
    slug = request.slug.trim().toLowerCase();
    if (slugError(slug)) {
      return {
        status: 400,
        body: { error: 'Room slug may contain lowercase letters, numbers, and single hyphens.' }
      };
    }
    if (taken(slug)) return { status: 409, body: { error: 'That room link is already in use.' } };
  }
  const room: RoomInfo = {
    id: `r-${slug}`,
    slug,
    name: name || slug,
    lobby_enabled: true,
    created_at: now,
    active: false,
    num_participants: 0,
    recording: false,
    last_active_at: wireNull
  };
  state.rooms = [room, ...state.rooms];
  return { status: 201, body: room };
}

/** Matches a concrete path against a generated path constant like /api/x/{id}. */
function match(template: string, path: string): string[] | undefined {
  const pattern = new RegExp(`^${template.replace(/\{[a-z]+\}/g, '([^/]+)')}$`);
  const found = pattern.exec(path);
  return found ? found.slice(1).map(decodeURIComponent) : undefined;
}

export async function mockApi(page: Page, state: ApiState = defaultState()): Promise<MockApi> {
  const calls: ApiCall[] = [];

  async function handle(route: Route): Promise<void> {
    const request = route.request();
    const url = new URL(request.url());
    const method = request.method();
    const path = url.pathname;
    calls.push({ method, path, search: url.search, csrf: request.headers()['x-klisi-csrf'] });

    const json = (status: number, body: unknown) =>
      route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    const empty = (status: number) => route.fulfill({ status, body: '' });

    const mutating = method !== 'GET';
    if (mutating && request.headers()['x-klisi-csrf'] !== '1') {
      return json(403, { error: 'Missing CSRF header.' });
    }

    // A meeting's public face answers signed-out guests too (lobby/handler.go).
    // Every mock room belongs to the signed-in user.
    const publicSlug = match(RoomPath, path);
    if (method === 'GET' && publicSlug) {
      const room = state.rooms.find((item) => item.slug === publicSlug[0]);
      if (!room) return json(404, { error: 'Room not found.' });
      const info: PublicRoomInfo = {
        slug: room.slug,
        name: room.name,
        lobby_enabled: room.lobby_enabled,
        can_manage: state.signedIn
      };
      return json(200, info);
    }
    const joinSlug = match(RoomJoinPath, path);
    if (method === 'POST' && joinSlug) {
      const room = state.rooms.find((item) => item.slug === joinSlug[0]);
      if (!room) return json(404, { error: 'Room not found.' });
      const name = String((request.postDataJSON() as { name?: unknown }).name ?? '').trim();
      const admitted = (identity: string, who: string): JoinResponse => ({
        status: 'admitted',
        token: fakeToken(identity, who),
        ws_url: fakeSfuURL
      });
      if (state.signedIn) return json(200, admitted(`host:${state.me.sub}:0001`, state.me.name));
      if (!room.lobby_enabled) return json(200, admitted('guest:0001', name));
      return json(200, { status: 'waiting', request_id: 'lr-0001' } satisfies JoinResponse);
    }

    if (!state.signedIn) return json(401, { error: 'Authentication required.' });

    // The host's lobby stream, with nobody waiting.
    if (method === 'GET' && match(RoomLobbyPath, path)) {
      return route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        body: 'retry: 60000\nevent: pending\ndata: {"requests":[]}\n\n'
      });
    }

    if (method === 'GET' && path === MePath) return json(200, state.me);
    if (method === 'GET' && path === RoomsPath) return json(200, state.rooms);
    if (method === 'POST' && path === RoomsPath) {
      const created = createRoom(state, request.postDataJSON() as CreateRoomRequest);
      return json(created.status, created.body);
    }

    const slug = match(RoomRecordingsPath, path);
    if (method === 'GET' && slug) {
      const listed = state.recordings.filter((item) => item.room_slug === slug[0]);
      // With transcripts off the server never fills a recording's transcript.
      return json(
        200,
        state.me.transcripts ? listed : listed.map((item) => ({ ...item, transcript: wireNull }))
      );
    }

    // With transcripts off (KLISI_TRANSCRIPTS) the server registers none of
    // these routes, so each is the API's catch-all 404, whatever the method.
    const transcriptRoutes = [
      MachinesPath,
      MachinePath,
      PairingPath,
      PairingConfirmPath,
      PairingDenyPath,
      RecordingTranscriptPath,
      RecordingTranscriptDownloadPath
    ];
    if (!state.me.transcripts && transcriptRoutes.some((template) => match(template, path))) {
      return json(404, { error: 'API route not found.' });
    }

    const transcriptFor = match(RecordingTranscriptPath, path);
    if (method === 'POST' && transcriptFor) {
      const target = state.recordings.find((item) => item.id === transcriptFor[0]);
      if (!target) return json(404, { error: 'Recording not found.' });
      if (target.status !== 'completed') {
        return json(409, { error: 'Only a completed recording can be transcribed.' });
      }
      const status = target.transcript?.status;
      if (status === TranscriptCompleted) {
        return json(409, { error: 'This recording already has a transcript.' });
      }
      // Pending: the request answers with where it is.
      if (target.transcript && status !== TranscriptFailed && status !== 'available') {
        return json(202, target.transcript);
      }
      const machines = state.machines.machines;
      if (machines.length === 0) {
        return json(409, { error: 'Pair a machine at /machines to transcribe recordings.' });
      }
      target.transcript = { status: TranscriptWaiting, message: waitingMessage(machines) };
      return json(202, target.transcript);
    }

    if (method === 'GET' && path === MachinesPath) return json(200, state.machines);

    const machineID = match(MachinePath, path);
    if (method === 'DELETE' && machineID) {
      const before = state.machines.machines.length;
      state.machines.machines = state.machines.machines.filter((item) => item.id !== machineID[0]);
      return state.machines.machines.length < before
        ? empty(204)
        : json(404, { error: 'Machine not found.' });
    }

    const confirmCode = match(PairingConfirmPath, path);
    if (method === 'POST' && confirmCode) {
      const code = pairingCode(confirmCode[0]);
      const waiting = state.pairings[code];
      if (!waiting) return json(404, { error: unknownCode });
      delete state.pairings[code];
      // Just paired: it hasn't connected, so it can't have approved anything.
      const paired = machine({
        id: `m-${code.toLowerCase()}`,
        name: waiting.name,
        os: waiting.os,
        arch: waiting.arch,
        app_version: waiting.app_version,
        paired_at: now,
        last_seen_at: wireNull,
        state: MachineOffline,
        approved: false
      });
      state.machines.machines = [...state.machines.machines, paired];
      return json(201, paired);
    }

    const denyCode = match(PairingDenyPath, path);
    if (method === 'POST' && denyCode) {
      const code = pairingCode(denyCode[0]);
      if (!state.pairings[code]) return json(404, { error: unknownCode });
      delete state.pairings[code];
      return empty(204);
    }

    const lookup = match(PairingPath, path);
    if (method === 'GET' && lookup) {
      const waiting = state.pairings[pairingCode(lookup[0])];
      return waiting ? json(200, waiting) : json(404, { error: unknownCode });
    }

    return json(501, { error: `The mock API has no ${method} ${path}.` });
  }

  // A predicate, not a glob: Vite serves src/lib/api/*.ts too.
  await page.route((url) => url.pathname.startsWith('/api/'), handle);
  return {
    state,
    calls,
    count: (method, path) =>
      calls.filter((call) => call.method === method && call.path === path).length
  };
}
