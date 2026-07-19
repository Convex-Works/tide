import { DevTokenPath, type ErrorResponse, type TokenResponse } from './types.gen';

export async function getDevToken(room: string, name: string): Promise<TokenResponse> {
  const query = new URLSearchParams({ room, name });
  const response = await fetch(`${DevTokenPath}?${query}`);
  if (!response.ok) {
    const body = (await response.json().catch(() => null)) as ErrorResponse | null;
    throw new Error(body?.error ?? 'Could not join the room. Try again.');
  }
  return (await response.json()) as TokenResponse;
}
