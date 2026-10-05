import type { Browser, BrowserContext } from '@playwright/test';

/**
 * Real OIDC sessions against the media stack's dex. The gate previously had
 * only /api/dev/token, which mints non-host tokens — so /m/[slug], the lobby,
 * host grants, moderation and recording were all unreachable from a test.
 */

export interface TestUser {
  email: string;
  password: string;
  name: string;
}

/** Owns rooms it creates, so `can_manage` is true and its token carries host. */
export const OWNER: TestUser = {
  email: 'host@tide.dev',
  password: 'tide-dev',
  name: 'Tide host'
};

/** An ordinary authenticated user: never a host, so it exercises the lobby. */
export const MEMBER: TestUser = {
  email: 'member@tide.dev',
  password: 'tide-dev',
  name: 'Tide member'
};

type StorageState = Awaited<ReturnType<BrowserContext['storageState']>>;

// Logging in is slow and identical every time. Do it once per user per run and
// hand every later context the resulting cookie.
const sessions = new Map<string, Promise<StorageState>>();

async function authenticate(browser: Browser, user: TestUser): Promise<StorageState> {
  const context = await browser.newContext();
  try {
    const page = await context.newPage();
    await page.goto('/api/auth/login');
    await page.locator('#login').fill(user.email);
    await page.locator('#password').fill(user.password);
    await page.locator('#submit-login').click();
    // dex has skipApprovalScreen, so the next stop is tide's callback.
    await page.waitForURL((url) => !url.pathname.startsWith('/dex'), { timeout: 30_000 });
    const me = await context.request.get('/api/me');
    if (!me.ok()) {
      throw new Error(`Sign-in for ${user.email} did not produce a session (${me.status()}).`);
    }
    return await context.storageState();
  } finally {
    await context.close();
  }
}

export function sessionFor(browser: Browser, user: TestUser): Promise<StorageState> {
  const existing = sessions.get(user.email);
  if (existing) return existing;
  const pending = authenticate(browser, user);
  sessions.set(user.email, pending);
  return pending;
}

/** Sessions are per-run; a new browser in the same run reuses the cookie. */
export function forgetSessions(): void {
  sessions.clear();
}
