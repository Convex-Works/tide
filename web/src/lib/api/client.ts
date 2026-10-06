import {
  AuthLogoutPath,
  DevTokenPath,
  KickPath,
  LobbyApprovePath,
  LobbyDenyPath,
  LobbyWaitPath,
  MachinePath,
  MachinesPath,
  MeetingEndPath,
  MePath,
  MutePath,
  PairingConfirmPath,
  PairingDenyPath,
  PairingPath,
  RecordingDownloadPath,
  RecordingPath,
  RecordingStartPath,
  RecordingStopPath,
  RecordingTranscriptDownloadPath,
  RecordingTranscriptPath,
  RoomJoinPath,
  RoomLobbyPath,
  RoomPath,
  RoomRecordingsPath,
  RoomsPath,
  type CreateRoomRequest,
  type ErrorResponse,
  type JoinRequest,
  type JoinResponse,
  type LobbyAdmittedSSE,
  type LobbyDeniedSSE,
  type LobbyPendingSSE,
  type LobbyWaitingSSE,
  type MachineInfo,
  type MachinesResponse,
  type Me,
  type PairingInfo,
  type PublicRoomInfo,
  type RecordingInfo,
  type RecordingStartRequest,
  type RoomInfo,
  type TokenResponse,
  type TranscriptFormatText,
  type TranscriptFormatVTT,
  type TranscriptInfo,
  type UpdateRoomRequest
} from './types.gen';

const csrfHeaders = { 'X-Tide-Csrf': '1' };

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

export class AuthRequiredError extends ApiError {
  constructor(message = 'Sign in to continue.') {
    super(message, 401);
    this.name = 'AuthRequiredError';
  }
}

/**
 * What to tell the user about a failed call: the server's own message when
 * it sent one, else `fallback`. Anything that isn't an ApiError comes from
 * the browser ("Failed to fetch", a JSON parse error) and says nothing a
 * user can act on.
 */
export function errorMessage(cause: unknown, fallback: string): string {
  return cause instanceof ApiError ? cause.message : fallback;
}

function pathWith(path: string, parameter: string, value: string): string {
  return path.replace(`{${parameter}}`, encodeURIComponent(value));
}

function participantActionPath(path: string, slug: string, identity: string): string {
  return pathWith(pathWith(path, 'slug', slug), 'identity', identity);
}

async function responseError(response: Response): Promise<Error> {
  const body = (await response.json().catch(() => null)) as ErrorResponse | null;
  if (response.status === 401) return new AuthRequiredError(body?.error);
  return new ApiError(body?.error ?? 'The request failed. Try again.', response.status);
}

async function requestJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, init);
  if (!response.ok) throw await responseError(response);
  return (await response.json()) as T;
}

async function requestEmpty(path: string, init: RequestInit): Promise<void> {
  const response = await fetch(path, init);
  if (!response.ok) throw await responseError(response);
}

function jsonRequest(method: string, body: unknown): RequestInit {
  return {
    method,
    headers: { ...csrfHeaders, 'Content-Type': 'application/json' },
    body: JSON.stringify(body)
  };
}

/**
 * Who this browser is, and what the deployment offers them. Without sign-in
 * (ARCHITECTURE.md §4.1) the server answers a browser with no session by
 * issuing an anonymous one, so it never throws AuthRequiredError there; it
 * may refuse with 429 instead (see rateLimited).
 */
export function me(): Promise<Me> {
  return requestJSON<Me>(MePath);
}

/** What to say when the server refused under a per-client limit (§15). */
export const rateLimitedMessage = 'Too many requests from this network. Try again in a minute.';

/** Whether the server refused under a per-client rate limit. */
export function rateLimited(cause: unknown): boolean {
  return cause instanceof ApiError && cause.status === 429;
}

/**
 * Creates a room. Both fields are optional: a blank name becomes the slug,
 * a missing slug is generated, and a taken one is a 409 (ARCHITECTURE.md §5).
 */
export async function createRoom(request: CreateRoomRequest): Promise<RoomInfo> {
  // The New dialog can't be dismissed while it creates, so a stalled request
  // must end on its own rather than hold the user in it.
  const timeout = new AbortController();
  const timer = setTimeout(() => timeout.abort(), createRoomTimeoutMs);
  try {
    return await requestJSON<RoomInfo>(RoomsPath, {
      ...jsonRequest('POST', request),
      signal: timeout.signal
    });
  } catch (cause) {
    if (timeout.signal.aborted) {
      throw new ApiError(
        'The server took too long to answer. Check your rooms before trying again.',
        0
      );
    }
    throw cause;
  } finally {
    clearTimeout(timer);
  }
}

const createRoomTimeoutMs = 15_000;

export function listRooms(): Promise<RoomInfo[]> {
  return requestJSON<RoomInfo[]>(RoomsPath);
}

export function roomInfo(slug: string): Promise<PublicRoomInfo> {
  return requestJSON<PublicRoomInfo>(pathWith(RoomPath, 'slug', slug));
}

export function updateRoom(slug: string, patch: UpdateRoomRequest): Promise<RoomInfo> {
  return requestJSON<RoomInfo>(pathWith(RoomPath, 'slug', slug), jsonRequest('PATCH', patch));
}

export function deleteRoom(slug: string): Promise<void> {
  return requestEmpty(pathWith(RoomPath, 'slug', slug), {
    method: 'DELETE',
    headers: csrfHeaders
  });
}

export function kick(slug: string, identity: string): Promise<void> {
  return requestEmpty(participantActionPath(KickPath, slug, identity), {
    method: 'POST',
    headers: csrfHeaders
  });
}

export function muteParticipant(slug: string, identity: string): Promise<void> {
  return requestEmpty(participantActionPath(MutePath, slug, identity), {
    method: 'POST',
    headers: csrfHeaders
  });
}

export function endMeeting(slug: string): Promise<void> {
  return requestEmpty(pathWith(MeetingEndPath, 'slug', slug), {
    method: 'POST',
    headers: csrfHeaders
  });
}

export function startRecording(
  slug: string,
  options: RecordingStartRequest
): Promise<RecordingInfo> {
  return requestJSON<RecordingInfo>(
    pathWith(RecordingStartPath, 'slug', slug),
    jsonRequest('POST', options)
  );
}

export function stopRecording(slug: string): Promise<RecordingInfo> {
  return requestJSON<RecordingInfo>(pathWith(RecordingStopPath, 'slug', slug), {
    method: 'POST',
    headers: csrfHeaders
  });
}

export function listRecordings(slug: string): Promise<RecordingInfo[]> {
  return requestJSON<RecordingInfo[]>(pathWith(RoomRecordingsPath, 'slug', slug));
}

export function deleteRecording(id: string): Promise<void> {
  return requestEmpty(pathWith(RecordingPath, 'id', id), {
    method: 'DELETE',
    headers: csrfHeaders
  });
}

export function recordingDownloadURL(id: string): string {
  return pathWith(RecordingDownloadPath, 'id', id);
}

export function requestTranscript(id: string): Promise<TranscriptInfo> {
  return requestJSON<TranscriptInfo>(pathWith(RecordingTranscriptPath, 'id', id), {
    method: 'POST',
    headers: csrfHeaders
  });
}

export type TranscriptFormat = typeof TranscriptFormatText | typeof TranscriptFormatVTT;

export function transcriptDownloadURL(id: string, format: TranscriptFormat): string {
  const query = new URLSearchParams({ format });
  return `${pathWith(RecordingTranscriptDownloadPath, 'id', id)}?${query}`;
}

export function listMachines(): Promise<MachinesResponse> {
  return requestJSON<MachinesResponse>(MachinesPath);
}

export function removeMachine(id: string): Promise<void> {
  return requestEmpty(pathWith(MachinePath, 'id', id), {
    method: 'DELETE',
    headers: csrfHeaders
  });
}

export function pairing(code: string): Promise<PairingInfo> {
  return requestJSON<PairingInfo>(pathWith(PairingPath, 'code', code));
}

export function confirmPairing(code: string): Promise<MachineInfo> {
  return requestJSON<MachineInfo>(pathWith(PairingConfirmPath, 'code', code), {
    method: 'POST',
    headers: csrfHeaders
  });
}

export function denyPairing(code: string): Promise<void> {
  return requestEmpty(pathWith(PairingDenyPath, 'code', code), {
    method: 'POST',
    headers: csrfHeaders
  });
}

export function joinRoom(slug: string, name: string): Promise<JoinResponse> {
  const body: JoinRequest = { name };
  return requestJSON<JoinResponse>(pathWith(RoomJoinPath, 'slug', slug), jsonRequest('POST', body));
}

export function approveLobby(id: string): Promise<void> {
  return requestEmpty(pathWith(LobbyApprovePath, 'id', id), {
    method: 'POST',
    headers: csrfHeaders
  });
}

export function denyLobby(id: string): Promise<void> {
  return requestEmpty(pathWith(LobbyDenyPath, 'id', id), {
    method: 'POST',
    headers: csrfHeaders
  });
}

export function logout(): Promise<void> {
  return requestEmpty(AuthLogoutPath, { method: 'POST', headers: csrfHeaders });
}

export interface LobbyWaitHandlers {
  waiting?: (event: LobbyWaitingSSE) => void;
  admitted: (event: LobbyAdmittedSSE) => void;
  denied: (event: LobbyDeniedSSE) => void;
  expired?: (event: LobbyDeniedSSE) => void;
  error?: () => void;
}

export function lobbyWait(id: string, handlers: LobbyWaitHandlers): () => void {
  const source = new EventSource(pathWith(LobbyWaitPath, 'id', id));
  source.addEventListener('waiting', (event) => {
    handlers.waiting?.(JSON.parse((event as MessageEvent<string>).data) as LobbyWaitingSSE);
  });
  source.addEventListener('admitted', (event) => {
    handlers.admitted(JSON.parse((event as MessageEvent<string>).data) as LobbyAdmittedSSE);
  });
  source.addEventListener('denied', (event) => {
    handlers.denied(JSON.parse((event as MessageEvent<string>).data) as LobbyDeniedSSE);
  });
  source.addEventListener('expired', (event) => {
    handlers.expired?.(JSON.parse((event as MessageEvent<string>).data) as LobbyDeniedSSE);
  });
  source.onerror = () => {
    // EventSource retries transient failures itself. CLOSED means the server
    // refused the stream permanently — e.g. the request expired while this
    // tab was suspended (404) — and no event will ever arrive.
    if (source.readyState === EventSource.CLOSED) handlers.error?.();
  };
  return () => source.close();
}

export function roomLobby(slug: string, onPending: (event: LobbyPendingSSE) => void): () => void {
  const source = new EventSource(pathWith(RoomLobbyPath, 'slug', slug));
  source.addEventListener('pending', (event) => {
    onPending(JSON.parse((event as MessageEvent<string>).data) as LobbyPendingSSE);
  });
  return () => source.close();
}

export async function getDevToken(room: string, name: string): Promise<TokenResponse> {
  const query = new URLSearchParams({ room, name });
  return requestJSON<TokenResponse>(`${DevTokenPath}?${query}`);
}
