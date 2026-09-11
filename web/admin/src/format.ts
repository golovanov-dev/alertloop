// Formatting helpers for timestamps and identifiers.

// dateTime renders "YYYY-MM-DD HH:MM:SS" in the viewer's timezone, from an
// ISO/RFC3339 or "... UTC" string. It is what every list shows.
//
// The date is always there. Lists hold up to retention_days of events, and a
// time of day alone cannot tell today's 14:32 from last Tuesday's; a retry can
// be scheduled for tomorrow. The zone is not: columns using this are headed
// "(local)", and a zone name on every row would be noise. The format is fixed
// rather than the browser's locale, so rows line up and sort by eye. It is the
// same format as the built-in pages at /events, but not the same zone: those
// show UTC, this shows the viewer's own — each column header says which.
export function dateTime(ts: string): string {
  const d = parseDate(ts);
  if (!d) return ts;
  const p = (n: number) => String(n).padStart(2, "0");
  return (
    `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ` +
    `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
  );
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
  return dateTime(ts);
}
