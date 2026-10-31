// The rejoin grant a guest admitted through the lobby is handed, kept per
// room for this tab so that leaving, a lost connection or a reload returns
// them to the meeting without the host admitting them again. The server
// decides whether it still holds; storage that is unavailable just means
// the lobby.

function key(slug: string): string {
  return `tide:rejoin:${slug}`;
}

export function readRejoinGrant(slug: string): string | undefined {
  try {
    return sessionStorage.getItem(key(slug)) ?? undefined;
  } catch {
    return undefined;
  }
}

export function storeRejoinGrant(slug: string, grant: string): void {
  try {
    sessionStorage.setItem(key(slug), grant);
  } catch {
    // Without storage the guest goes through the lobby next time.
  }
}

export function clearRejoinGrant(slug: string): void {
  try {
    sessionStorage.removeItem(key(slug));
  } catch {
    // Nothing stored that could be used.
  }
}
