// Shared display formatters for the dashboard and meeting detail views.

/** Long relative form, e.g. "4 days ago" — used for recording timestamps. */
export function relativeDate(timestamp: number): string {
  const seconds = Math.round(timestamp - Date.now() / 1000);
  const formatter = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
  if (Math.abs(seconds) < 60) return formatter.format(seconds, 'second');
  const minutes = Math.round(seconds / 60);
  if (Math.abs(minutes) < 60) return formatter.format(minutes, 'minute');
  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 24) return formatter.format(hours, 'hour');
  return formatter.format(Math.round(hours / 24), 'day');
}

/** Date and time for accessible names, e.g. "Sep 26, 2026, 2:00 PM". */
export function dateTimeLabel(timestamp: number): string {
  return new Date(timestamp * 1000).toLocaleString(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short'
  });
}

/** Compact past form for dense chips, e.g. "just now", "4d ago", "3mo ago". */
export function compactAgo(timestamp: number): string {
  const seconds = Math.max(0, Math.floor(Date.now() / 1000 - timestamp));
  if (seconds < 45) return 'just now';
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  if (days < 7) return `${days}d ago`;
  const weeks = Math.round(days / 7);
  if (weeks < 5) return `${weeks}w ago`;
  const months = Math.round(days / 30);
  if (months < 12) return `${months}mo ago`;
  return `${Math.round(days / 365)}y ago`;
}

/** Recording length as m:ss, or "—" when unknown. */
export function durationLabel(seconds?: number | null): string {
  if (seconds == null) return '—';
  const minutes = Math.floor(seconds / 60);
  const remainder = seconds % 60;
  return `${minutes}:${remainder.toString().padStart(2, '0')}`;
}

/** File size as KB/MB, or "—" when unknown. */
export function sizeLabel(bytes?: number | null): string {
  if (bytes == null) return '—';
  if (bytes < 1_000_000) return `${Math.max(1, Math.round(bytes / 1_000))} KB`;
  return `${(bytes / 1_000_000).toFixed(1)} MB`;
}

const systemNames: Record<string, string> = { macos: 'macOS', linux: 'Linux', windows: 'Windows' };

/** A machine's OS by its usual name, e.g. "macOS". */
export function osLabel(os: string): string {
  return systemNames[os] ?? os;
}

/** A machine's OS and architecture as moil reports them, e.g. "macOS · aarch64". */
export function systemLabel(os: string, arch: string): string {
  return [osLabel(os), arch].filter(Boolean).join(' · ');
}

/** The first 12 hex characters of a bundle hash — what the moil app shows. */
export function shortHash(hash: string): string {
  return hash.slice(0, 12);
}
