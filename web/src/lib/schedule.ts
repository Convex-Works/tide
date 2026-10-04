// The meeting time a calendar invite carries, as the viewer's local date and
// times (the values of <input type="date"> and <input type="time">).

export interface MeetingTimes {
  /** YYYY-MM-DD */
  date: string;
  /** HH:MM */
  start: string;
  /** HH:MM, same day as start */
  end: string;
}

const halfHour = 30 * 60_000;

function pad(value: number): string {
  return String(value).padStart(2, '0');
}

function localDate(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

function localTime(date: Date): string {
  return `${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

/** The next half hour strictly after `now`, 30 minutes long. */
export function defaultTimes(now = new Date()): MeetingTimes {
  const start = new Date(now);
  start.setSeconds(0, 0);
  start.setMinutes(start.getMinutes() < 30 ? 30 : 60);
  const end = new Date(start.getTime() + halfHour);
  // Ending past midnight would need a second date; stop at 23:59 instead.
  const endTime = localDate(end) === localDate(start) ? localTime(end) : '23:59';
  return { date: localDate(start), start: localTime(start), end: endTime };
}

/** A local date and time as an instant, or undefined when either is malformed. */
export function localInstant(date: string, time: string): Date | undefined {
  const dateParts = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date);
  const timeParts = /^(\d{2}):(\d{2})$/.exec(time);
  if (!dateParts || !timeParts) return undefined;
  const [year, month, day] = dateParts.slice(1).map(Number);
  const [hours, minutes] = timeParts.slice(1).map(Number);
  const instant = new Date(year, month - 1, day, hours, minutes);
  return Number.isNaN(instant.getTime()) ? undefined : instant;
}

export type ResolvedTimes = { start: Date; end: Date } | { error: string };

/** The times as instants, or what is wrong with them. */
export function resolveTimes(times: MeetingTimes): ResolvedTimes {
  const start = localInstant(times.date, times.start);
  const end = localInstant(times.date, times.end);
  if (!start || !end) return { error: 'Pick a date, a start time and an end time.' };
  if (end <= start) return { error: 'End the meeting after it starts.' };
  return { start, end };
}
