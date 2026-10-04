// Room slugs as the server reads them (ARCHITECTURE.md §5): 3–64 lowercase
// letters, numbers and single hyphens. Pure, so specs import it directly.

export const slugMinLength = 3;
export const slugMaxLength = 64;
export const slugPattern = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;
export const slugRules = 'Use 3–64 lowercase letters, numbers, and single hyphens.';

/** What the server does to a slug before checking it: trim and lowercase. */
export function normalizeSlug(value: string): string {
  return value.trim().toLowerCase();
}

/** Why a (normalized) slug breaks the rules, or undefined when it follows them. */
export function slugError(slug: string): string | undefined {
  if (slug.length < slugMinLength || slug.length > slugMaxLength) return slugRules;
  if (!slugPattern.test(slug)) return slugRules;
  return undefined;
}

const alphabet = 'abcdefghijklmnopqrstuvwxyz';
// The largest multiple of 26 a byte can hold: bytes at or above it are
// redrawn, so every letter is equally likely (no modulo bias).
const byteLimit = 256 - (256 % alphabet.length);

export type RandomFill = (bytes: Uint8Array) => Uint8Array;

const cryptoFill: RandomFill = (bytes) => crypto.getRandomValues(bytes);

/**
 * A default slug in the server's format: ten letters a–z drawn uniformly,
 * grouped 3-4-3 (`abc-defg-hij`). `fill` is the random source, for tests.
 */
export function suggestSlug(fill: RandomFill = cryptoFill): string {
  const letters: string[] = [];
  const bytes = new Uint8Array(16);
  while (letters.length < 10) {
    fill(bytes);
    for (const byte of bytes) {
      if (byte >= byteLimit) continue;
      letters.push(alphabet[byte % alphabet.length]);
      if (letters.length === 10) break;
    }
  }
  const word = letters.join('');
  return `${word.slice(0, 3)}-${word.slice(3, 7)}-${word.slice(7)}`;
}
