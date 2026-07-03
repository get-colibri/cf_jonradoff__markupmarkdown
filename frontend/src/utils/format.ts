/** Short human date: "Jun 4", adding the year only when it isn't the
 * current one ("Jun 4, 2025"). Month names beat numeric forms —
 * "6/4/2026" reads as April 6 in half the world. */
export function formatShortDate(date: Date): string {
  const opts: Intl.DateTimeFormatOptions = { month: "short", day: "numeric" };
  if (date.getFullYear() !== new Date().getFullYear()) {
    opts.year = "numeric";
  }
  return date.toLocaleDateString(undefined, opts);
}

/** Full local timestamp for tooltips: "June 4, 2026, 2:41 PM EDT".
 * Always renders in the browser's local timezone. */
export function formatExact(iso: string): string {
  const date = new Date(iso);
  if (isNaN(date.getTime())) return iso;
  return date.toLocaleString(undefined, {
    month: "long",
    day: "numeric",
    year: "numeric",
    hour: "numeric",
    minute: "2-digit",
    timeZoneName: "short",
  });
}

export function formatRelative(iso: string): string {
  const date = new Date(iso);
  const now = Date.now();
  const diffSec = (now - date.getTime()) / 1000;
  // Future timestamps (e.g. token expiresAt). Render as "in Xd / Xh".
  if (diffSec < 0) {
    const abs = -diffSec;
    if (abs < 60) return "in a moment";
    if (abs < 3600) return `in ${Math.floor(abs / 60)}m`;
    if (abs < 86400) return `in ${Math.floor(abs / 3600)}h`;
    if (abs < 86400 * 30) return `in ${Math.floor(abs / 86400)}d`;
    return `on ${formatShortDate(date)}`;
  }
  if (diffSec < 60) return "just now";
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`;
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`;
  if (diffSec < 86400 * 7) return `${Math.floor(diffSec / 86400)}d ago`;
  return formatShortDate(date);
}

export function initials(name: string): string {
  const parts = name.trim().split(/\s+/);
  if (parts.length === 0 || !parts[0]) return "?";
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
}

const COLORS = [
  "#ef4444", "#f59e0b", "#10b981", "#3b82f6",
  "#8b5cf6", "#ec4899", "#14b8a6", "#f97316",
];

export function colorFor(name: string): string {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return COLORS[h % COLORS.length];
}
