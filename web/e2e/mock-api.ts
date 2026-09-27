import type { Page, Route } from '@playwright/test';
import {
  MachinePath,
  MachinesPath,
  MePath,
  PairingConfirmPath,
  PairingDenyPath,
  PairingPath,
  RecordingTranscriptPath,
  RoomRecordingsPath,
  RoomsPath,
  type MachineInfo,
  type MachinesResponse,
  type Me,
  type PairingInfo,
  type RecordingInfo,
  type RoomInfo,
  type TranscriptInfo
} from '../src/lib/api/types.gen';

// An in-memory klisi API for specs that run against the Vite dev server
// alone: every /api request is answered here, from `state`, and recorded in
// `calls`. Nothing reaches the proxy, so a request the mock doesn't know is
// a 501 the spec can see.

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
  pairings: Record<string, PairingInfo>;
  /** What POST /api/recordings/{id}/transcript answers; the row takes it. */
  requested: TranscriptInfo;
}

export interface MockApi {
  state: ApiState;
  calls: ApiCall[];
  count(method: string, path: string): number;
}

// tygo writes a Go pointer without omitempty as an optional field, but the
// server sends null for it; this is that null, typed to fit.
export const wireNull = null as unknown as undefined;

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
    state: 'idle',
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
    me: { sub: 'host', email: 'host@klisi.dev', name: 'Ada Host' },
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
    machines: { machines: [], bundle, moil_url: 'https://klisi.example.com/moil' },
    pairings: {},
    requested: { status: 'waiting', message: 'No machine is online.' }
  };
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

    if (!state.signedIn) return json(401, { error: 'Sign in to continue.' });
    const mutating = method !== 'GET';
    if (mutating && request.headers()['x-klisi-csrf'] !== '1') {
      return json(403, { error: 'Missing CSRF header.' });
    }

    if (method === 'GET' && path === MePath) return json(200, state.me);
    if (method === 'GET' && path === RoomsPath) return json(200, state.rooms);

    const slug = match(RoomRecordingsPath, path);
    if (method === 'GET' && slug) {
      return json(
        200,
        state.recordings.filter((item) => item.room_slug === slug[0])
      );
    }

    const transcriptFor = match(RecordingTranscriptPath, path);
    if (method === 'POST' && transcriptFor) {
      const target = state.recordings.find((item) => item.id === transcriptFor[0]);
      if (!target) return json(404, { error: 'Recording not found.' });
      target.transcript = { ...state.requested };
      return json(202, state.requested);
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
      const waiting = state.pairings[confirmCode[0]];
      if (!waiting) return json(404, { error: 'This pairing code expired.' });
      delete state.pairings[confirmCode[0]];
      const paired = machine({
        id: `m-${waiting.code.toLowerCase()}`,
        name: waiting.name,
        os: waiting.os,
        arch: waiting.arch,
        app_version: waiting.app_version,
        paired_at: now,
        last_seen_at: now,
        approved: false
      });
      state.machines.machines = [...state.machines.machines, paired];
      return json(201, paired);
    }

    const denyCode = match(PairingDenyPath, path);
    if (method === 'POST' && denyCode) {
      if (!state.pairings[denyCode[0]]) return json(404, { error: 'This pairing code expired.' });
      delete state.pairings[denyCode[0]];
      return empty(204);
    }

    const code = match(PairingPath, path);
    if (method === 'GET' && code) {
      const waiting = state.pairings[code[0]];
      return waiting ? json(200, waiting) : json(404, { error: 'This pairing code expired.' });
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
