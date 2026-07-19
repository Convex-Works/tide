import {
  AuthLogoutPath,
  DevTokenPath,
  KickPath,
  LobbyApprovePath,
  LobbyDenyPath,
  LobbyWaitPath,
  MePath,
  MutePath,
  RecordingDownloadPath,
  RecordingPath,
  RecordingStartPath,
  RecordingStopPath,
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
  type Me,
  type PublicRoomInfo,
  type RecordingInfo,
  type RoomInfo,
  type TokenResponse,
  type UpdateRoomRequest
} from './types.gen';

const csrfHeaders = { 'X-Klisi-Csrf': '1' };

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

export function me(): Promise<Me> {
  return requestJSON<Me>(MePath);
}

export function createRoom(name: string): Promise<RoomInfo> {
  const body: CreateRoomRequest = { name };
  return requestJSON<RoomInfo>(RoomsPath, jsonRequest('POST', body));
}

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

export function startRecording(slug: string): Promise<RecordingInfo> {
  return requestJSON<RecordingInfo>(pathWith(RecordingStartPath, 'slug', slug), {
    method: 'POST',
    headers: csrfHeaders
  });
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
