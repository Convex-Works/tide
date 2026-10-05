import { expect, test } from '@playwright/test';
import { buildIcs, escapeText, foldLine, icsDateTime } from '../src/lib/ics';
import { defaultTimes, localInstant, resolveTimes } from '../src/lib/schedule';
import { normalizeSlug, slugError, suggestSlug } from '../src/lib/slug';

// Unit tests for the browser-side calendar invite and slug helpers. They
// import the modules directly and open no page; Playwright is the only test
// runner the web package has.

const bytes = (value: string) => Buffer.byteLength(value, 'utf8');
const unfold = (folded: string) => folded.replace(/\r\n /g, '');

/** Runs `body` with the process in another time zone (Node rereads TZ). */
function inZone<T>(zone: string, body: () => T): T {
  const before = process.env.TZ;
  process.env.TZ = zone;
  try {
    return body();
  } finally {
    if (before === undefined) delete process.env.TZ;
    else process.env.TZ = before;
  }
}

test.describe('ics', () => {
  test('escapes TEXT values', () => {
    expect(escapeText('a\\b;c,d')).toBe('a\\\\b\\;c\\,d');
    expect(escapeText('one\ntwo\r\nthree\rfour')).toBe('one\\ntwo\\nthree\\nfour');
    // The backslash is escaped first, so an escape it adds isn't doubled.
    expect(escapeText('\\,')).toBe('\\\\\\,');
  });

  test('folds at 75 octets and unfolds to the original', () => {
    const line = `DESCRIPTION:${'x'.repeat(200)}`;
    const physical = foldLine(line).split('\r\n');
    expect(bytes(physical[0])).toBe(75);
    for (const part of physical.slice(1)) {
      expect(part.startsWith(' ')).toBe(true);
      expect(bytes(part)).toBeLessThanOrEqual(75);
    }
    expect(unfold(foldLine(line))).toBe(line);
    expect(foldLine('SUMMARY:short')).toBe('SUMMARY:short');
  });

  test('never splits a multi-byte character when folding', () => {
    // 8 ASCII octets, then two-octet é: 33 of them make 74, a 34th would be 77.
    const accented = `SUMMARY:${'é'.repeat(60)}`;
    const first = foldLine(accented).split('\r\n')[0];
    expect(bytes(first)).toBe(74);

    const mixed = `SUMMARY:${'日本語🎉é'.repeat(30)}`;
    const folded = foldLine(mixed);
    const decoder = new TextDecoder('utf-8', { fatal: true });
    for (const part of folded.split('\r\n')) {
      expect(bytes(part)).toBeLessThanOrEqual(75);
      // A lone surrogate half would not survive a strict round trip.
      expect(decoder.decode(new TextEncoder().encode(part))).toBe(part);
      expect(part).not.toMatch(
        /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/
      );
    }
    expect(unfold(folded)).toBe(mixed);
  });

  test('writes times in UTC whatever the local zone', () => {
    expect(icsDateTime(new Date(Date.UTC(2026, 0, 2, 3, 4, 5)))).toBe('20260102T030405Z');
    const athens = inZone('Europe/Athens', () => icsDateTime(localInstant('2026-10-05', '10:30')!));
    expect(athens).toBe('20261005T073000Z');
    const newYork = inZone('America/New_York', () =>
      icsDateTime(localInstant('2026-12-31', '21:15')!)
    );
    expect(newYork).toBe('20270101T021500Z');
  });

  test('builds one VEVENT with CRLF line endings', () => {
    const url = 'https://meet.example.com/m/abc-defg-hij';
    const text = buildIcs({
      slug: 'abc-defg-hij',
      name: 'Retro; Q4, team\\ops',
      url,
      host: 'meet.example.com',
      start: new Date(Date.UTC(2026, 9, 5, 7, 30)),
      end: new Date(Date.UTC(2026, 9, 5, 8, 0)),
      stamp: new Date(Date.UTC(2026, 9, 4, 12, 0))
    });
    expect(text.endsWith('\r\n')).toBe(true);
    expect(text.replace(/\r\n/g, '')).not.toMatch(/[\r\n]/);
    const lines = unfold(text).split('\r\n');
    expect(lines.slice(0, 2)).toEqual(['BEGIN:VCALENDAR', 'VERSION:2.0']);
    expect(lines).toContain('PRODID:-//tide//EN');
    expect(lines).toContain('METHOD:PUBLISH');
    expect(lines.filter((line) => line === 'BEGIN:VEVENT')).toHaveLength(1);
    expect(lines).toContain('UID:abc-defg-hij-20261005T073000Z@meet.example.com');
    expect(lines).toContain('DTSTAMP:20261004T120000Z');
    expect(lines).toContain('DTSTART:20261005T073000Z');
    expect(lines).toContain('DTEND:20261005T080000Z');
    expect(lines).toContain('SUMMARY:Retro\\; Q4\\, team\\\\ops');
    expect(lines).toContain(`URL:${url}`);
    expect(lines).toContain(`LOCATION:${url}`);
    expect(lines).toContain(`DESCRIPTION:Join the meeting: ${url}`);
    expect(lines.at(-2)).toBe('END:VCALENDAR');
  });
});

test.describe('meeting times', () => {
  test('default to the next half hour, 30 minutes long', () => {
    const at = (iso: string) => defaultTimes(new Date(iso));
    expect(at('2026-10-05T10:12:30')).toEqual({ date: '2026-10-05', start: '10:30', end: '11:00' });
    expect(at('2026-10-05T10:30:00')).toEqual({ date: '2026-10-05', start: '11:00', end: '11:30' });
    expect(at('2026-10-05T23:45:00')).toEqual({ date: '2026-10-06', start: '00:00', end: '00:30' });
    // The end stays on the start's day.
    expect(at('2026-10-05T23:10:00')).toEqual({ date: '2026-10-05', start: '23:30', end: '23:59' });
  });

  test('end 30 minutes later on the clock across a DST change', () => {
    inZone('America/New_York', () => {
      // Fall back: 01:30 EDT plus 30 elapsed minutes reads 01:00 EST.
      const fallBack = defaultTimes(new Date('2026-11-01T01:10:00-04:00'));
      expect(fallBack).toEqual({ date: '2026-11-01', start: '01:30', end: '02:00' });
      expect('error' in resolveTimes(fallBack)).toBe(false);
      // Spring forward: 02:00 doesn't exist, so the next half hour is 03:00.
      const springForward = defaultTimes(new Date('2026-03-08T01:40:00-05:00'));
      expect(springForward).toEqual({ date: '2026-03-08', start: '03:00', end: '03:30' });
      expect('error' in resolveTimes(springForward)).toBe(false);
    });
  });

  test('end after they start', () => {
    expect(resolveTimes({ date: '2026-10-05', start: '10:30', end: '10:30' })).toEqual({
      error: 'End the meeting after it starts.'
    });
    expect(resolveTimes({ date: '', start: '10:30', end: '11:00' })).toHaveProperty('error');
    const ok = resolveTimes({ date: '2026-10-05', start: '10:30', end: '11:00' });
    expect('error' in ok).toBe(false);
  });
});

test.describe('slugs', () => {
  test('suggestions follow the server format', () => {
    const seen = new Set<string>();
    for (let index = 0; index < 500; index++) {
      const slug = suggestSlug();
      expect(slug).toMatch(/^[a-z]{3}-[a-z]{4}-[a-z]{3}$/);
      expect(slugError(slug)).toBeUndefined();
      seen.add(slug);
    }
    expect(seen.size).toBe(500);
  });

  test('suggestions redraw bytes that would bias the letters', () => {
    // 234 = 9 × 26: bytes from 234 up would favour a–v, so they are skipped.
    const draws = [[255, 234, 0, 1, 2, 233, 3, 4, 5, 6, 7, 25, 26, 240, 250, 251], [8]];
    let call = 0;
    const slug = suggestSlug((target) => {
      target.fill(0);
      target.set(draws[call++]);
      return target;
    });
    expect(slug).toBe('abc-zdef-ghz');
    expect(call).toBe(1);

    let calls = 0;
    const starved = suggestSlug((target) => {
      target.fill(calls++ === 0 ? 255 : 1);
      return target;
    });
    expect(starved).toBe('bbb-bbbb-bbb');
    expect(calls).toBe(2);
  });

  test('rules mirror the server', () => {
    expect(normalizeSlug('  Weekly-Sync ')).toBe('weekly-sync');
    for (const bad of ['ab', 'a--b', '-abc', 'abc-', 'ab_c', 'Abc', 'a'.repeat(65)]) {
      expect(slugError(bad), bad).toBeDefined();
    }
    for (const good of ['abc', 'a1-b2', 'a'.repeat(64)]) {
      expect(slugError(good), good).toBeUndefined();
    }
  });
});
