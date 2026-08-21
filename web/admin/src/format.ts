// Formatting helpers for timestamps and identifiers.

// timeOfDay extracts a HH:MM:SS label, in the viewer's timezone, from an
// ISO/RFC3339 or "... UTC" string. Table columns using it are headed
// "(local)": a zone on every row would be noise.
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

// fullTime renders an absolute timestamp in the viewer's own timezone. The API
// serves UTC; each viewer should read times in their zone, so the zone name is
// shown to make clear which one that is — the built-in pages at /events label
// theirs as UTC, and the two must not be silently mistaken for each other.
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
    timeZoneName: "short",
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
