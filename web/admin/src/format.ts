// Formatting helpers for timestamps and identifiers.

// timeOfDay extracts a HH:MM:SS label from an ISO/RFC3339 or "... UTC" string.
export function timeOfDay(ts: string): string {
  const d = parseDate(ts);
  if (!d) return ts;
  return d.toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

// fullTime renders an absolute local timestamp.
export function fullTime(ts: string): string {
  const d = parseDate(ts);
  if (!d) return ts;
  return d.toLocaleString([], {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

function parseDate(ts: string): Date | null {
  if (!ts) return null;
  // Accept "2026-07-03 14:32:07 UTC" as well as RFC3339.
  let s = ts.trim();
  if (s.endsWith(" UTC")) s = s.slice(0, -4).replace(" ", "T") + "Z";
  const d = new Date(s);
  return isNaN(d.getTime()) ? null : d;
}

export function shortId(id: string): string {
  return id.length > 8 ? id.slice(0, 8) : id;
}

export function relToLabel(ts?: string | null): string {
  if (!ts) return "—";
  return timeOfDay(ts);
}
