// Calendar invites built in the browser (ARCHITECTURE.md §5): one RFC 5545
// VEVENT per file, times in UTC, nothing stored on the server. Pure apart
// from downloadIcs, so specs import it directly.

export interface CalendarEvent {
  /** The room's slug; names the file and starts the UID. */
  slug: string;
  /** The room's human name: the event's SUMMARY. */
  name: string;
  /** The meeting URL: URL, LOCATION and the description's link. */
  url: string;
  /** Host of the klisi install, the UID's domain part. */
  host: string;
  start: Date;
  end: Date;
  /** When the file was made (DTSTAMP); now by default. */
  stamp?: Date;
}

const crlf = '\r\n';
const maxOctets = 75;

function pad(value: number, width = 2): string {
  return String(value).padStart(width, '0');
}

/** A UTC DATE-TIME as RFC 5545 writes it: `YYYYMMDDTHHMMSSZ`. */
export function icsDateTime(date: Date): string {
  return (
    `${pad(date.getUTCFullYear(), 4)}${pad(date.getUTCMonth() + 1)}${pad(date.getUTCDate())}` +
    `T${pad(date.getUTCHours())}${pad(date.getUTCMinutes())}${pad(date.getUTCSeconds())}Z`
  );
}

/** Escapes a TEXT value (RFC 5545 §3.3.11): backslash, semicolon, comma, newline. */
export function escapeText(value: string): string {
  return value
    .replace(/\\/g, '\\\\')
    .replace(/;/g, '\\;')
    .replace(/,/g, '\\,')
    .replace(/\r\n|\r|\n/g, '\\n');
}

function utf8Length(codePoint: number): number {
  if (codePoint < 0x80) return 1;
  if (codePoint < 0x800) return 2;
  if (codePoint < 0x10000) return 3;
  return 4;
}

/**
 * Folds one content line (RFC 5545 §3.1): no physical line longer than 75
 * octets of UTF-8, continuations starting with a space, and never a split
 * inside a character.
 */
export function foldLine(line: string): string {
  const lines: string[] = [];
  let current = '';
  let octets = 0;
  for (const character of line) {
    const size = utf8Length(character.codePointAt(0) ?? 0);
    if (octets + size > maxOctets) {
      lines.push(current);
      current = ' ';
      octets = 1;
    }
    current += character;
    octets += size;
  }
  lines.push(current);
  return lines.join(crlf);
}

/** The whole .ics file for one meeting, CRLF line endings throughout. */
export function buildIcs(event: CalendarEvent): string {
  const start = icsDateTime(event.start);
  const lines = [
    'BEGIN:VCALENDAR',
    'VERSION:2.0',
    'PRODID:-//klisi//EN',
    'CALSCALE:GREGORIAN',
    'METHOD:PUBLISH',
    'BEGIN:VEVENT',
    `UID:${event.slug}-${start}@${event.host}`,
    `DTSTAMP:${icsDateTime(event.stamp ?? new Date())}`,
    `DTSTART:${start}`,
    `DTEND:${icsDateTime(event.end)}`,
    `SUMMARY:${escapeText(event.name)}`,
    `URL:${event.url}`,
    `LOCATION:${escapeText(event.url)}`,
    `DESCRIPTION:${escapeText(`Join the meeting: ${event.url}`)}`,
    'END:VEVENT',
    'END:VCALENDAR'
  ];
  return lines.map(foldLine).join(crlf) + crlf;
}

/** Saves `text` as `filename` through a temporary object URL. */
export function downloadIcs(filename: string, text: string): void {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/calendar;charset=utf-8' }));
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  link.style.display = 'none';
  document.body.append(link);
  link.click();
  link.remove();
  // Some browsers start reading the blob only after click() returns.
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
}
